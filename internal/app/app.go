// Package app wires Simlab together and owns the process lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/casperlundberg/simlab-api/internal/api"
	"github.com/casperlundberg/simlab-api/internal/autoscaler"
	"github.com/casperlundberg/simlab-api/internal/events"
	"github.com/casperlundberg/simlab-api/internal/run"
	"github.com/casperlundberg/simlab-api/internal/runner"
	"github.com/casperlundberg/simlab-api/internal/store"
)

// Config is the service's own configuration.
type Config struct {
	Address     string
	DatabaseURL string

	// AutoscalerURL is the service Simlab drives. Without it there is nothing
	// to run a run against, so it is required rather than defaulted: a default
	// would produce a service that starts happily and fails every run.
	AutoscalerURL   string
	AutoscalerToken string

	// StaticDir, when set, is a directory of built frontend assets served
	// alongside the API, so one container can serve both.
	StaticDir string

	RequestTimeout time.Duration
	LogLevel       slog.Level
}

// LoadConfig reads the environment, refusing anything it cannot parse.
func LoadConfig(lookup func(string) string) (Config, error) {
	cfg := Config{
		Address:         valueOr(lookup, "SIMLAB_ADDRESS", ":8081"),
		DatabaseURL:     lookup("SIMLAB_DATABASE_URL"),
		AutoscalerURL:   lookup("SIMLAB_AUTOSCALER_URL"),
		AutoscalerToken: lookup("SIMLAB_AUTOSCALER_TOKEN"),
		StaticDir:       lookup("SIMLAB_STATIC_DIR"),
		RequestTimeout:  30 * time.Second,
		LogLevel:        slog.LevelInfo,
	}

	var missing []string
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		missing = append(missing, "SIMLAB_DATABASE_URL")
	}
	if strings.TrimSpace(cfg.AutoscalerURL) == "" {
		missing = append(missing, "SIMLAB_AUTOSCALER_URL")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("%s must be set: this service cannot do anything "+
			"without a database and an autoscaler to drive", strings.Join(missing, " and "))
	}

	if raw := lookup("SIMLAB_REQUEST_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("SIMLAB_REQUEST_TIMEOUT=%q is not a duration: %w", raw, err)
		}
		cfg.RequestTimeout = timeout
	}
	if raw := lookup("SIMLAB_LOG_LEVEL"); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(strings.ToUpper(raw))); err != nil {
			return Config{}, fmt.Errorf("SIMLAB_LOG_LEVEL=%q is not a level: %w", raw, err)
		}
	}
	return cfg, nil
}

// Run starts the service and blocks until its context is cancelled.
func Run(ctx context.Context, cfg Config) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	client, err := autoscaler.New(cfg.AutoscalerURL, cfg.AutoscalerToken, cfg.RequestTimeout)
	if err != nil {
		return err
	}
	if cfg.AutoscalerToken == "" {
		logger.Warn("no autoscaler token is set; this only works if the autoscaler is " +
			"itself unauthenticated")
	}

	hub := events.New()
	manager := runner.New(db, run.New(client, db, hub), logger)

	server := &http.Server{
		Addr: cfg.Address,
		Handler: api.New(api.Options{
			Store: db, Manager: manager, Autoscaler: client, Hub: hub,
			Logger: logger, Static: cfg.StaticDir,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: the event stream is a long-lived response by
		// design, and a write deadline would sever every watcher on a timer.
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("listening", "address", cfg.Address, "autoscaler", cfg.AutoscalerURL)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
		close(serverErrors)
	}()

	select {
	case err := <-serverErrors:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}

	// Runs are stopped rather than abandoned. A run killed mid-cycle leaves an
	// ephemeral autoscaler target behind, and those accumulate silently.
	manager.Shutdown()
	return nil
}

func valueOr(lookup func(string) string, key, fallback string) string {
	if value := lookup(key); value != "" {
		return value
	}
	return fallback
}
