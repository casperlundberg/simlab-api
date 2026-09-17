// Package astest is a stand-in for the autoscaler service.
//
// It speaks the real contract from the autoscaler's openapi.yaml — the same
// paths, the same JSON, the same status codes — and it scales for real, with a
// simple but honest policy and a fleet that honours coldstart on the cycle's
// own clock. That matters for the run engine's tests: a fake that always
// replied "maintain" would let a broken engine pass, because the queue would
// behave the same either way.
//
// It is not a substitute for testing against the real service. The
// cross-repository check does that; this is what makes the unit tests fast.
package astest

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Server is a fake autoscaler.
type Server struct {
	mu sync.Mutex
	t  *testing.T

	Token string

	targets map[string]*target

	// Cycles records every decision request, so a test can assert on what the
	// run engine actually asked for.
	Cycles []CycleCall

	// FailCyclesAfter makes cycles fail once this many have succeeded, for
	// testing how a run handles the autoscaler going away mid-run. Zero means
	// never.
	FailCyclesAfter int

	// Build is what /v1/version reports. Nil makes the endpoint absent, as it
	// is on an autoscaler built before it existed.
	Build map[string]any
}

// CycleCall is one recorded decision request.
type CycleCall struct {
	TargetID   string
	At         time.Time
	TotalDepth int
	Executors  int

	// ExemptDepth is the part of TotalDepth sent as exempt from cloud burst.
	ExemptDepth int
}

type target struct {
	id       string
	kind     string
	mode     string
	config   map[string]string
	settings map[string]any
	version  int64

	// fleet is one ready-at instant per executor, exactly as the real
	// simulation adapter models it.
	local []time.Time
	cloud []time.Time

	// lastDecision is what /status reports, for testing a live run.
	lastDecision map[string]any
}

// New builds a fake with no targets.
func New(t *testing.T, token string) *Server {
	return &Server{t: t, Token: token, targets: map[string]*target{}, Build: map[string]any{
		"version": "1.0.0-fake", "commit": "0000000000000000000000000000000000000fake",
		"modified": false, "go_version": "go-fake", "platform": "fake/fake",
	}}
}

// Start runs it and returns the base URL.
func (s *Server) Start() string {
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	s.t.Cleanup(server.Close)
	return server.URL
}

// TargetExists reports whether a target is registered, so a test can check
// that a run cleaned up after itself.
func (s *Server) TargetExists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.targets[id]
	return ok
}

// SettingsOf returns a target's current settings document.
func (s *Server) SettingsOf(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.targets[id]; ok {
		return t.settings
	}
	return nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if s.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.Token {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "a bearer token is required"})
		return
	}

	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")

	switch {
	case path == "v1/version" && r.Method == http.MethodGet && s.Build != nil:
		writeJSON(w, http.StatusOK, s.Build)
	case path == "v1/platforms" && r.Method == http.MethodGet:
		s.servePlatforms(w)
	case path == "v1/targets" && r.Method == http.MethodGet:
		s.serveList(w)
	case path == "v1/targets" && r.Method == http.MethodPost:
		s.serveCreate(w, r)
	case len(parts) == 3 && parts[1] == "targets" && r.Method == http.MethodGet:
		s.serveGet(w, parts[2])
	case len(parts) == 3 && parts[1] == "targets" && r.Method == http.MethodDelete:
		s.serveDelete(w, parts[2])
	case len(parts) == 4 && parts[3] == "settings" && r.Method == http.MethodGet:
		s.serveGetSettings(w, parts[2])
	case len(parts) == 4 && parts[3] == "settings" && r.Method == http.MethodPatch:
		s.servePatchSettings(w, r, parts[2])
	case len(parts) == 4 && parts[3] == "cycle" && r.Method == http.MethodPost:
		s.serveCycle(w, r, parts[2])
	case len(parts) == 4 && parts[3] == "status" && r.Method == http.MethodGet:
		s.serveStatus(w, parts[2])
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such endpoint: " + path})
	}
}

func (s *Server) servePlatforms(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"platforms": []any{
		map[string]any{
			"kind": "simulation", "summary": "Decides for real, provisions nothing.",
			"sees_workload": false,
			"config": []any{
				map[string]any{"name": "local_coldstart_seconds", "label": "Local coldstart (seconds)",
					"default": "120", "required": false},
			},
		},
		map[string]any{
			"kind": "kubernetes", "summary": "Scales one Deployment per tier.",
			"sees_workload": false,
			"credentials": []any{
				map[string]any{"name": "bearer_token", "label": "Bearer token", "required": false},
			},
		},
	}})
}

func (s *Server) serveList(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()

	targets := []any{}
	for _, t := range s.targets {
		targets = append(targets, t.snapshot())
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

func (s *Server) serveCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target struct {
			ID     string            `json:"id"`
			Name   string            `json:"name"`
			Kind   string            `json:"kind"`
			Mode   string            `json:"mode"`
			Config map[string]string `json:"config"`
		} `json:"target"`
		Settings map[string]any `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Target.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed target"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.targets[body.Target.ID]; exists {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a target with this id already exists"})
		return
	}

	settings := defaultSettings()
	for key, value := range body.Settings {
		settings[key] = value
	}
	s.targets[body.Target.ID] = &target{
		id: body.Target.ID, kind: body.Target.Kind, mode: body.Target.Mode,
		config: body.Target.Config, settings: settings, version: 1,
	}
	writeJSON(w, http.StatusCreated, s.targets[body.Target.ID].snapshot())
}

func (s *Server) serveGet(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.targets[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such target: " + id})
		return
	}
	writeJSON(w, http.StatusOK, t.snapshot())
}

func (s *Server) serveDelete(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.targets[id]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such target: " + id})
		return
	}
	delete(s.targets, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serveGetSettings(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.targets[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such target: " + id})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": t.version, "settings": t.settings})
}

func (s *Server) servePatchSettings(w http.ResponseWriter, r *http.Request, id string) {
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed settings"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.targets[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such target: " + id})
		return
	}
	for key, value := range patch {
		t.settings[key] = value
	}
	t.version++
	writeJSON(w, http.StatusOK, map[string]any{"version": t.version, "settings": t.settings})
}

func (s *Server) serveCycle(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		At       time.Time `json:"at"`
		Workload *struct {
			Queues map[string]struct {
				Depth               int     `json:"depth"`
				OldestJobAgeSeconds float64 `json:"oldest_job_age_seconds"`
				ArrivalRate         float64 `json:"arrival_rate_per_second"`
			} `json:"queues"`
			BurstExempt map[string]struct {
				Depth int `json:"depth"`
			} `json:"burst_exempt"`
			ExecutorThroughput float64 `json:"executor_throughput_per_second"`
		} `json:"workload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed cycle request"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.targets[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such target: " + id})
		return
	}
	if body.Workload == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "target " + id + " cannot see its own queue, and no workload was supplied",
		})
		return
	}
	if s.FailCyclesAfter > 0 && len(s.Cycles) >= s.FailCyclesAfter {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the autoscaler is unwell"})
		return
	}

	now := body.At
	if now.IsZero() {
		now = time.Now().UTC()
	}

	depth, exempt := 0, 0
	for _, level := range body.Workload.Queues {
		depth += level.Depth
	}
	for _, level := range body.Workload.BurstExempt {
		exempt += level.Depth
	}
	counted := depth
	depth += exempt

	localReady, localPending := split(t.local, now)
	cloudReady, cloudPending := split(t.cloud, now)
	previous := map[string]int{
		"local": localReady + localPending,
		"cloud": cloudReady + cloudPending,
	}

	// A simple but real policy: enough executors to clear what is waiting
	// within one deadline's worth of time, local first, then cloud.
	throughput := body.Workload.ExecutorThroughput
	if throughput <= 0 {
		throughput = 1.0 / 20
	}
	need := func(jobs int) int {
		n := int(math.Ceil(float64(jobs) * throughput / math.Max(throughput*60, 1e-9)))
		if jobs > 0 && n < 1 {
			n = 1
		}
		return n
	}
	needed := need(depth)

	localCap := intSetting(t.settings, "local_executor_cap", 10)
	cloudCap := intSetting(t.settings, "cloud_executor_cap", 20)

	// Exempt work may use local capacity but buys no cloud, as the real
	// engine's does.
	planLocal := min(needed, localCap)
	planCloud := min(max(need(counted)-planLocal, 0), cloudCap)

	t.local = resize(t.local, planLocal, now, coldstart(t.config, "local_coldstart_seconds"))
	t.cloud = resize(t.cloud, planCloud, now, coldstart(t.config, "cloud_coldstart_seconds"))

	s.Cycles = append(s.Cycles, CycleCall{
		TargetID: id, At: now, TotalDepth: depth, Executors: localReady + cloudReady, ExemptDepth: exempt,
	})

	action := "maintain"
	switch {
	case planLocal+planCloud > previous["local"]+previous["cloud"]:
		action = "scale_up"
	case planLocal+planCloud < previous["local"]+previous["cloud"]:
		action = "scale_down"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"decision": map[string]any{
			"at":               now,
			"previous":         map[string]any{"local_executors": previous["local"], "cloud_executors": previous["cloud"]},
			"plan":             map[string]any{"local_executors": planLocal, "cloud_executors": planCloud},
			"action":           action,
			"reason":           "fake policy: " + itoa(depth) + " jobs waiting",
			"projection":       map[string]any{"breach_expected": depth > 500, "peak_queue_depth": depth},
			"settings_version": t.version,
			"constrained":      needed > localCap+cloudCap,
		},
		"observation": map[string]any{
			"capacity": map[string]any{
				"local_ready": localReady, "cloud_ready": cloudReady,
				"local_pending": localPending, "cloud_pending": cloudPending,
			},
		},
		"applied": map[string]any{
			"applied": map[string]any{"local_executors": planLocal, "cloud_executors": planCloud},
			"changed": action != "maintain",
		},
	})
}

// LiveDecision sets what a target reports as its last decision, so a live run
// can be tested against a target that is scaling without Simlab driving it.
func (s *Server) LiveDecision(id string, decision map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.targets[id]; ok {
		t.lastDecision = decision
	}
}

func (s *Server) serveStatus(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.targets[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such target: " + id})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": t.id, "kind": t.kind, "mode": t.mode,
		"settings_version": t.version,
		"last_decision":    t.lastDecision,
	})
}

func (t *target) snapshot() map[string]any {
	return map[string]any{
		"target": map[string]any{
			"id": t.id, "kind": t.kind, "mode": t.mode, "config": t.config,
		},
		"settings": map[string]any{"version": t.version, "settings": t.settings},
	}
}

func defaultSettings() map[string]any {
	return map[string]any{
		"decision_interval_seconds":    15.0,
		"local_executor_cap":           10.0,
		"cloud_executor_cap":           20.0,
		"deadline_seconds_by_priority": map[string]any{"100": 60.0, "25": 43200.0},
		"default_deadline_seconds":     86400.0,
	}
}

func resize(tier []time.Time, want int, now time.Time, coldstart time.Duration) []time.Time {
	if want < 0 {
		want = 0
	}
	for len(tier) < want {
		tier = append(tier, now.Add(coldstart))
	}
	if want < len(tier) {
		tier = tier[:want]
	}
	return tier
}

func split(tier []time.Time, now time.Time) (ready, pending int) {
	for _, readyAt := range tier {
		if !readyAt.After(now) {
			ready++
		} else {
			pending++
		}
	}
	return ready, pending
}

func coldstart(config map[string]string, key string) time.Duration {
	raw, ok := config[key]
	if !ok {
		return 0
	}
	seconds := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0
		}
		seconds = seconds*10 + int(c-'0')
	}
	return time.Duration(seconds) * time.Second
}

func intSetting(settings map[string]any, key string, fallback int) int {
	if value, ok := settings[key].(float64); ok {
		return int(value)
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
