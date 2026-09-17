// Package api is Simlab's HTTP surface.
//
// It is also the only thing that talks to the autoscaler. The browser never
// does: keeping that relationship on this side is what keeps the autoscaler's
// API token, and through it every platform credential the autoscaler holds,
// off the client entirely.
package api

import (
	"context"
	"crypto/subtle"
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

// guarded wraps a handler in the bearer-token check.
func (s *server) guarded(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" && !s.authorised(r) {
			writeError(w, http.StatusUnauthorized, "a bearer token is required")
			return
		}
		next(w, r)
	})
}

func (s *server) authorised(r *http.Request) bool {
	const prefix = "Bearer "

	if header := r.Header.Get("Authorization"); len(header) > len(prefix) && header[:len(prefix)] == prefix {
		return s.matches(header[len(prefix):])
	}

	// A token in the query string, accepted on reads only.
	//
	// The event streams are consumed by EventSource, which cannot set request
	// headers — there is no header to put a bearer token in. The alternatives
	// are a cookie, which brings CSRF with it, or leaving the streams open,
	// which is what this change exists to stop.
	//
	// Restricted to GET so that a URL, which is the thing that ends up in
	// access logs, browser history and referrers, can never authorise a write.
	// Treat any log recording query strings as holding credentials.
	if r.Method == http.MethodGet {
		if token := r.URL.Query().Get("token"); token != "" {
			return s.matches(token)
		}
	}
	return false
}

// matches compares in constant time, so a caller cannot learn the token one
// byte at a time from how long the comparison takes.
func (s *server) matches(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.Token)) == 1
}

// Options is everything the HTTP layer needs.
type Options struct {
	Store      *store.Store
	Manager    *runner.Manager
	Autoscaler *autoscaler.Client
	Hub        *events.Hub
	Logger     *slog.Logger

	// Token, when set, is required as a bearer token on every endpoint except
	// the probes.
	//
	// This service is the one that gets published. simlab-web's nginx proxies
	// /api straight here, so without a token anyone who can reach the ingress
	// can drive it — and since this service holds the autoscaler's token and
	// proxies to it, that reaches an authenticated autoscaler, and through it
	// whatever infrastructure a target points at.
	//
	// Empty leaves the API open, which is what a development install with no
	// token configured needs. The umbrella chart refuses to render an
	// arrangement where the autoscaler is guarded and this is not.
	Token string

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
	Public  bool
	Handler func(*server) http.HandlerFunc
}

var routes = []route{
	// Probes are unauthenticated. A kubelet cannot carry a credential, and
	// requiring one would have the pod killed for being unauthenticated rather
	// than unhealthy. Everything else is guarded.
	{http.MethodGet, "/healthz", true, func(s *server) http.HandlerFunc { return s.health }},
	{http.MethodGet, "/readyz", true, func(s *server) http.HandlerFunc { return s.ready }},

	{http.MethodGet, "/api/mines", false, func(s *server) http.HandlerFunc { return s.listMines }},
	{http.MethodPost, "/api/mines", false, func(s *server) http.HandlerFunc { return s.saveMine }},
	{http.MethodGet, "/api/mines/{id}", false, func(s *server) http.HandlerFunc { return s.getMine }},
	{http.MethodPut, "/api/mines/{id}", false, func(s *server) http.HandlerFunc { return s.saveMine }},
	{http.MethodDelete, "/api/mines/{id}", false, func(s *server) http.HandlerFunc { return s.deleteMine }},

	{http.MethodGet, "/api/scenarios", false, func(s *server) http.HandlerFunc { return s.listScenarios }},
	{http.MethodPost, "/api/scenarios", false, func(s *server) http.HandlerFunc { return s.saveScenario }},
	{http.MethodGet, "/api/scenarios/{id}", false, func(s *server) http.HandlerFunc { return s.getScenario }},
	{http.MethodPut, "/api/scenarios/{id}", false, func(s *server) http.HandlerFunc { return s.saveScenario }},
	{http.MethodDelete, "/api/scenarios/{id}", false, func(s *server) http.HandlerFunc { return s.deleteScenario }},

	{http.MethodGet, "/api/runs", false, func(s *server) http.HandlerFunc { return s.listRuns }},
	{http.MethodPost, "/api/runs", false, func(s *server) http.HandlerFunc { return s.createRun }},
	{http.MethodGet, "/api/runs/{id}", false, func(s *server) http.HandlerFunc { return s.getRun }},
	{http.MethodDelete, "/api/runs/{id}", false, func(s *server) http.HandlerFunc { return s.deleteRun }},
	{http.MethodPost, "/api/runs/{id}/cancel", false, func(s *server) http.HandlerFunc { return s.cancelRun }},
	{http.MethodGet, "/api/runs/{id}/cycles", false, func(s *server) http.HandlerFunc { return s.runCycles }},
	{http.MethodGet, "/api/runs/{id}/metrics", false, func(s *server) http.HandlerFunc { return s.runMetrics }},
	{http.MethodGet, "/api/runs/{id}/layout", false, func(s *server) http.HandlerFunc { return s.runLayout }},
	{http.MethodGet, "/api/runs/{id}/seismicity", false, func(s *server) http.HandlerFunc { return s.runSeismicity }},
	{http.MethodGet, "/api/runs/{id}/events", false, func(s *server) http.HandlerFunc { return s.runEvents }},
	{http.MethodGet, "/api/events", false, func(s *server) http.HandlerFunc { return s.allEvents }},

	{http.MethodGet, "/api/platforms", false, func(s *server) http.HandlerFunc { return s.listPlatforms }},
	{http.MethodGet, "/api/targets", false, func(s *server) http.HandlerFunc { return s.listTargets }},
	{http.MethodPost, "/api/targets", false, func(s *server) http.HandlerFunc { return s.createTarget }},
	{http.MethodGet, "/api/targets/{id}", false, func(s *server) http.HandlerFunc { return s.getTarget }},
	{http.MethodDelete, "/api/targets/{id}", false, func(s *server) http.HandlerFunc { return s.deleteTarget }},
	{http.MethodGet, "/api/targets/{id}/settings", false, func(s *server) http.HandlerFunc { return s.getTargetSettings }},
	{http.MethodPatch, "/api/targets/{id}/settings", false, func(s *server) http.HandlerFunc { return s.patchTargetSettings }},
	{http.MethodGet, "/api/targets/{id}/status", false, func(s *server) http.HandlerFunc { return s.getTargetStatus }},
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
		handler := r.Handler(s)
		if r.Public {
			mux.Handle(r.Method+" "+r.Path, handler)
			continue
		}
		mux.Handle(r.Method+" "+r.Path, s.guarded(handler))
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

func (s *server) saveScenario(w http.ResponseWriter, r *http.Request) {
	// Decoded straight into the domain type, which now converts both
	// directions. A separate request struct only ever covered one, and that is
	// how a response ended up rendering nanoseconds for a field the request
	// took in seconds.
	var scenario domain.Scenario
	if err := decode(r, &scenario); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if id := r.PathValue("id"); id != "" {
		scenario.ID = id
	}
	if scenario.ID == "" {
		id, err := newID("scn")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		scenario.ID = id
	}
	if err := s.checkGeometry(r.Context(), scenario); err != nil {
		writeStoreError(w, err)
		return
	}
	if scenario.Seed == 0 {
		// A scenario without a seed is not reproducible, and a caller who did
		// not think about it should still get a run that can be repeated.
		scenario.Seed = time.Now().UnixNano()
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

func (s *server) deleteScenario(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteScenario(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
