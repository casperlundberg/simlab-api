// Package api is Simlab's HTTP surface.
//
// It is also the only thing that talks to the autoscaler. The browser never
// does: keeping that relationship on this side is what keeps the autoscaler's
// API token, and through it every platform credential the autoscaler holds,
// off the client entirely.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/casperlundberg/simlab-api/internal/autoscaler"
	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/events"
	"github.com/casperlundberg/simlab-api/internal/runner"
	"github.com/casperlundberg/simlab-api/internal/store"
)

const maxBodyBytes = 1 << 20

// Options is everything the HTTP layer needs.
type Options struct {
	Store      *store.Store
	Manager    *runner.Manager
	Autoscaler *autoscaler.Client
	Hub        *events.Hub
	Logger     *slog.Logger

	// Static, when set, is a directory of built frontend assets served for any
	// path the API does not claim. It lets one container serve both, which is
	// what makes a single-namespace deployment straightforward.
	Static string
}

type server struct {
	Options
}

// route is one endpoint, in a table so the OpenAPI document can be checked
// against what is actually served.
type route struct {
	Method  string
	Path    string
	Handler func(*server) http.HandlerFunc
}

var routes = []route{
	{http.MethodGet, "/healthz", func(s *server) http.HandlerFunc { return s.health }},
	{http.MethodGet, "/readyz", func(s *server) http.HandlerFunc { return s.ready }},

	{http.MethodGet, "/api/mines", func(s *server) http.HandlerFunc { return s.listMines }},
	{http.MethodPost, "/api/mines", func(s *server) http.HandlerFunc { return s.saveMine }},
	{http.MethodGet, "/api/mines/{id}", func(s *server) http.HandlerFunc { return s.getMine }},
	{http.MethodPut, "/api/mines/{id}", func(s *server) http.HandlerFunc { return s.saveMine }},
	{http.MethodDelete, "/api/mines/{id}", func(s *server) http.HandlerFunc { return s.deleteMine }},

	{http.MethodGet, "/api/scenarios", func(s *server) http.HandlerFunc { return s.listScenarios }},
	{http.MethodPost, "/api/scenarios", func(s *server) http.HandlerFunc { return s.saveScenario }},
	{http.MethodGet, "/api/scenarios/{id}", func(s *server) http.HandlerFunc { return s.getScenario }},
	{http.MethodPut, "/api/scenarios/{id}", func(s *server) http.HandlerFunc { return s.saveScenario }},
	{http.MethodDelete, "/api/scenarios/{id}", func(s *server) http.HandlerFunc { return s.deleteScenario }},

	{http.MethodGet, "/api/runs", func(s *server) http.HandlerFunc { return s.listRuns }},
	{http.MethodPost, "/api/runs", func(s *server) http.HandlerFunc { return s.createRun }},
	{http.MethodGet, "/api/runs/{id}", func(s *server) http.HandlerFunc { return s.getRun }},
	{http.MethodDelete, "/api/runs/{id}", func(s *server) http.HandlerFunc { return s.deleteRun }},
	{http.MethodPost, "/api/runs/{id}/cancel", func(s *server) http.HandlerFunc { return s.cancelRun }},
	{http.MethodGet, "/api/runs/{id}/cycles", func(s *server) http.HandlerFunc { return s.runCycles }},
	{http.MethodGet, "/api/runs/{id}/metrics", func(s *server) http.HandlerFunc { return s.runMetrics }},
	{http.MethodGet, "/api/runs/{id}/events", func(s *server) http.HandlerFunc { return s.runEvents }},
	{http.MethodGet, "/api/events", func(s *server) http.HandlerFunc { return s.allEvents }},

	{http.MethodGet, "/api/platforms", func(s *server) http.HandlerFunc { return s.listPlatforms }},
	{http.MethodGet, "/api/targets", func(s *server) http.HandlerFunc { return s.listTargets }},
	{http.MethodPost, "/api/targets", func(s *server) http.HandlerFunc { return s.createTarget }},
	{http.MethodGet, "/api/targets/{id}", func(s *server) http.HandlerFunc { return s.getTarget }},
	{http.MethodDelete, "/api/targets/{id}", func(s *server) http.HandlerFunc { return s.deleteTarget }},
	{http.MethodGet, "/api/targets/{id}/settings", func(s *server) http.HandlerFunc { return s.getTargetSettings }},
	{http.MethodPatch, "/api/targets/{id}/settings", func(s *server) http.HandlerFunc { return s.patchTargetSettings }},
	{http.MethodGet, "/api/targets/{id}/status", func(s *server) http.HandlerFunc { return s.getTargetStatus }},
}

// Routes is every endpoint served, for the contract test.
func Routes() []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// New builds the HTTP handler.
func New(options Options) http.Handler {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	s := &server{Options: options}

	mux := http.NewServeMux()
	for _, r := range routes {
		mux.Handle(r.Method+" "+r.Path, r.Handler(s))
	}
	if options.Static != "" {
		mux.Handle("/", spaHandler(options.Static))
	}
	return mux
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ready reports on the two things this service cannot work without.
func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := s.Store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable", "database": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "active_runs": len(s.Manager.Active()),
	})
}

func (s *server) listMines(w http.ResponseWriter, r *http.Request) {
	mines, err := s.Store.Mines(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mines": mines})
}

func (s *server) getMine(w http.ResponseWriter, r *http.Request) {
	mine, err := s.Store.Mine(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mine)
}

func (s *server) saveMine(w http.ResponseWriter, r *http.Request) {
	var mine domain.Mine
	if err := decode(r, &mine); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if id := r.PathValue("id"); id != "" {
		// The path is the authority, so a body naming a different mine cannot
		// overwrite one somebody else is editing.
		mine.ID = id
	}

	if err := s.Store.SaveMine(r.Context(), mine); err != nil {
		writeStoreError(w, err)
		return
	}
	saved, err := s.Store.Mine(r.Context(), mine.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *server) deleteMine(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteMine(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listScenarios(w http.ResponseWriter, r *http.Request) {
	scenarios, err := s.Store.Scenarios(r.Context(), r.URL.Query().Get("mine_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenarios": scenarios})
}

func (s *server) getScenario(w http.ResponseWriter, r *http.Request) {
	scenario, err := s.Store.Scenario(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scenario)
}

// scenarioRequest is a scenario on the wire. Durations are seconds, matching
// the autoscaler's contract, because these are typed by hand.
type scenarioRequest struct {
	ID              string             `json:"id"`
	MineID          string             `json:"mine_id"`
	Name            string             `json:"name"`
	DurationSeconds float64            `json:"duration_seconds"`
	JobSeconds      float64            `json:"job_seconds"`
	Seed            int64              `json:"seed"`
	PriorityMix     map[string]float64 `json:"priority_mix"`
	Bursts          []burstRequest     `json:"bursts,omitempty"`
	Description     string             `json:"description,omitempty"`
}

type burstRequest struct {
	AtSeconds              float64 `json:"at_seconds"`
	Magnitude              float64 `json:"magnitude"`
	AftershockDecaySeconds float64 `json:"aftershock_decay_seconds"`
}

func (s *server) saveScenario(w http.ResponseWriter, r *http.Request) {
	var request scenarioRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if id := r.PathValue("id"); id != "" {
		request.ID = id
	}
	if request.ID == "" {
		id, err := newID("scn")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		request.ID = id
	}
	if request.Seed == 0 {
		// A scenario without a seed is not reproducible, and a caller who did
		// not think about it should still get a run that can be repeated.
		request.Seed = time.Now().UnixNano()
	}

	scenario, err := request.toDomain()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.SaveScenario(r.Context(), scenario); err != nil {
		writeStoreError(w, err)
		return
	}

	saved, err := s.Store.Scenario(r.Context(), scenario.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (r scenarioRequest) toDomain() (domain.Scenario, error) {
	mix := map[domain.Priority]float64{}
	for key, weight := range r.PriorityMix {
		priority, err := strconv.Atoi(key)
		if err != nil {
			return domain.Scenario{}, fmt.Errorf("priority_mix has the key %q, which is not "+
				"a priority level", key)
		}
		mix[domain.Priority(priority)] = weight
	}

	scenario := domain.Scenario{
		ID: r.ID, MineID: r.MineID, Name: r.Name,
		Duration:    seconds(r.DurationSeconds),
		JobSeconds:  r.JobSeconds,
		Seed:        r.Seed,
		PriorityMix: mix,
		Description: r.Description,
	}
	for _, burst := range r.Bursts {
		scenario.Bursts = append(scenario.Bursts, domain.Burst{
			At:              seconds(burst.AtSeconds),
			Magnitude:       burst.Magnitude,
			AftershockDecay: seconds(burst.AftershockDecaySeconds),
		})
	}
	return scenario, nil
}

func (s *server) deleteScenario(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteScenario(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func seconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
}

func decode(r *http.Request, into any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("request body: %w", err)
	}
	return nil
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, runner.ErrAlreadyRunning):
		writeError(w, http.StatusConflict, err.Error())
	case autoscaler.NotFound(err):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func parseInt64(raw string) (int64, error) {
	return strconv.ParseInt(raw, 10, 64)
}

// asAutoscalerError unwraps an autoscaler failure so its status can be passed
// through, rather than flattening every upstream refusal into one code.
func asAutoscalerError(err error) *autoscaler.Error {
	var apiErr *autoscaler.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return nil
}

// spaHandler serves the built frontend, falling back to index.html for any
// path that is not a file.
//
// The fallback is what makes client-side routing work: a browser asked to open
// /runs/abc123 directly requests that path from the server, and without this it
// would get a 404 instead of the app that knows how to render it.
func spaHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(root, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
}
