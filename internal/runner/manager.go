// Package runner owns runs that are in flight: starting them, stopping them,
// and knowing which are still going.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/run"
	"github.com/casperlundberg/simlab-api/internal/store"
)

// ErrAlreadyRunning is returned when a run is asked to start twice.
var ErrAlreadyRunning = errors.New("this run is already in flight")

// Manager starts runs and keeps track of them.
type Manager struct {
	store  *store.Store
	engine *run.Engine
	log    *slog.Logger

	mu     sync.Mutex
	active map[string]context.CancelFunc

	// finished lets tests and shutdown wait for in-flight runs.
	wg sync.WaitGroup
}

// New builds a manager.
func New(store *store.Store, engine *run.Engine, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{store: store, engine: engine, log: log, active: map[string]context.CancelFunc{}}
}

// Start executes a run in the background and returns as soon as it is under
// way.
//
// Background because a run takes minutes and an HTTP request should not. The
// context deliberately does not come from the request: a run must survive the
// browser tab that started it being closed, which is the normal way somebody
// launches one and then goes to look at something else.
func (m *Manager) Start(spec run.Spec) error {
	m.mu.Lock()
	if _, running := m.active[spec.Run.ID]; running {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, spec.Run.ID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.active[spec.Run.ID] = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.active, spec.Run.ID)
			m.mu.Unlock()
			cancel()
		}()

		metrics, err := m.engine.Execute(ctx, spec)
		if err != nil {
			// Already recorded against the run by the engine; logged here so
			// it is visible without querying the database.
			m.log.Warn("run ended early", "run", spec.Run.ID, "error", err)
			return
		}
		m.log.Info("run completed", "run", spec.Run.ID,
			"cycles", metrics.Cycles, "breaches", metrics.SLABreaches,
			"cloud_executor_seconds", metrics.CloudExecutorSeconds)
	}()
	return nil
}

// Cancel stops a run. Whatever it recorded before stopping stands.
func (m *Manager) Cancel(runID string) error {
	m.mu.Lock()
	cancel, running := m.active[runID]
	m.mu.Unlock()

	if !running {
		return fmt.Errorf("run %q is not in flight", runID)
	}
	cancel()
	return nil
}

// Active is the runs currently in flight.
func (m *Manager) Active() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]string, 0, len(m.active))
	for id := range m.active {
		out = append(out, id)
	}
	return out
}

// IsActive reports whether one run is in flight.
func (m *Manager) IsActive(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, running := m.active[runID]
	return running
}

// Shutdown stops every run and waits for them to unwind.
//
// Stopping rather than abandoning matters: a run that is killed mid-cycle
// leaves an ephemeral autoscaler target behind, and those accumulate silently.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	for _, cancel := range m.active {
		cancel()
	}
	m.mu.Unlock()

	m.wg.Wait()
}

// Prepare assembles the spec for a run from what is stored.
func (m *Manager) Prepare(ctx context.Context, runID string) (run.Spec, error) {
	stored, err := m.store.Run(ctx, runID)
	if err != nil {
		return run.Spec{}, err
	}

	settings, err := m.store.RunSettings(ctx, runID)
	if err != nil {
		return run.Spec{}, err
	}

	spec := run.Spec{Run: stored, Settings: json.RawMessage(settings)}
	if stored.Mode != domain.ModeSimulation {
		return spec, nil
	}

	scenario, err := m.store.Scenario(ctx, stored.ScenarioID)
	if err != nil {
		return run.Spec{}, fmt.Errorf("run %q refers to a scenario that is gone: %w", runID, err)
	}
	mine, err := m.store.Mine(ctx, scenario.MineID)
	if err != nil {
		return run.Spec{}, fmt.Errorf("scenario %q refers to a mine that is gone: %w",
			scenario.ID, err)
	}

	spec.Scenario = scenario
	spec.Mine = mine
	return spec, nil
}
