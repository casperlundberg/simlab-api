package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/api"
	"github.com/casperlundberg/simlab-api/internal/autoscaler"
	"github.com/casperlundberg/simlab-api/internal/autoscaler/astest"
	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/events"
	"github.com/casperlundberg/simlab-api/internal/run"
	"github.com/casperlundberg/simlab-api/internal/runner"
	"github.com/casperlundberg/simlab-api/internal/store"
)

type fixture struct {
	server  *httptest.Server
	store   *store.Store
	fake    *astest.Server
	manager *runner.Manager
	hub     *events.Hub
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	url := os.Getenv("SIMLAB_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SIMLAB_TEST_DATABASE_URL is not set; skipping the API tests")
	}

	db, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("store.Open() = %v", err)
	}
	t.Cleanup(db.Close)
	reset(t, db)

	fake := astest.New(t, "")
	client, err := autoscaler.New(fake.Start(), "", 10*time.Second)
	if err != nil {
		t.Fatalf("autoscaler.New() = %v", err)
	}

	hub := events.New()
	engine := run.New(client, db, hub).
		WithClock(func(time.Duration) {}, func() time.Time { return time.Now().UTC() })
	manager := runner.New(db, engine, nil)
	t.Cleanup(manager.Shutdown)

	handler := api.New(api.Options{
		Store: db, Manager: manager, Autoscaler: client, Hub: hub,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &fixture{server: server, store: db, fake: fake, manager: manager, hub: hub}
}

func reset(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()

	runs, err := db.Runs(ctx, "", 500)
	if err != nil {
		t.Fatalf("Runs() = %v", err)
	}
	for _, stored := range runs {
		_ = db.DeleteRun(ctx, stored.ID)
	}
	mines, err := db.Mines(ctx)
	if err != nil {
		t.Fatalf("Mines() = %v", err)
	}
	for _, mine := range mines {
		_ = db.DeleteMine(ctx, mine.ID)
	}
}

func (f *fixture) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return body
}

func seedMineAndScenario(t *testing.T, f *fixture) string {
	t.Helper()

	if resp := f.do(t, http.MethodPost, "/api/mines", map[string]any{
		"id": "storhall", "name": "Storhall", "sensors": 20,
		"background_rate_per_hour": 60,
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mines = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}

	resp := f.do(t, http.MethodPost, "/api/scenarios", map[string]any{
		"id": "burst", "mine_id": "storhall", "name": "Rock burst",
		"duration_seconds": 1800, "job_seconds": 20, "seed": 7,
		"priority_mix": map[string]float64{"100": 1, "25": 3},
		"bursts": []map[string]any{
			{"at_seconds": 600, "magnitude": 30, "aftershock_decay_seconds": 600},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/scenarios = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	return "burst"
}

func TestHealthAndReadinessReportTheDatabase(t *testing.T) {
	f := newFixture(t)

	if resp := f.do(t, http.MethodGet, "/healthz", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d", resp.StatusCode)
	}

	resp := f.do(t, http.MethodGet, "/readyz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz = %d", resp.StatusCode)
	}
	if _, ok := decodeBody(t, resp)["active_runs"]; !ok {
		t.Error("readiness does not report how many runs are in flight")
	}
}

func TestAMineRoundTripsThroughTheAPI(t *testing.T) {
	f := newFixture(t)

	resp := f.do(t, http.MethodPost, "/api/mines", map[string]any{
		"id": "storhall", "name": "Storhall", "sensors": 48,
		"background_rate_per_hour": 12,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mines = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}

	list := decodeBody(t, f.do(t, http.MethodGet, "/api/mines", nil))
	mines, _ := list["mines"].([]any)
	if len(mines) != 1 {
		t.Fatalf("mines = %#v, want one", list["mines"])
	}
}

func TestAMineWithNoSensorsIsRefusedWithAReason(t *testing.T) {
	f := newFixture(t)

	resp := f.do(t, http.MethodPost, "/api/mines", map[string]any{
		"id": "storhall", "name": "Storhall", "sensors": 0,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/mines = %d, want 400", resp.StatusCode)
	}
	if message, _ := decodeBody(t, resp)["error"].(string); !strings.Contains(message, "sensors") {
		t.Errorf("error = %q, want it to name the problem", message)
	}
}

func TestAScenarioTakesItsDurationsInSeconds(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodGet, "/api/scenarios/burst", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/scenarios/burst = %d", resp.StatusCode)
	}

	body := decodeBody(t, resp)
	bursts, _ := body["bursts"].([]any)
	if len(bursts) != 1 {
		t.Fatalf("bursts = %#v, want one", body["bursts"])
	}
}

// A scenario with no seed is not reproducible, and somebody who did not think
// about it should still get a run that can be repeated.
func TestAScenarioWithNoSeedIsGivenOne(t *testing.T) {
	f := newFixture(t)
	if resp := f.do(t, http.MethodPost, "/api/mines", map[string]any{
		"id": "storhall", "name": "Storhall", "sensors": 20, "background_rate_per_hour": 30,
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mines = %d", resp.StatusCode)
	}

	resp := f.do(t, http.MethodPost, "/api/scenarios", map[string]any{
		"mine_id": "storhall", "name": "Unseeded", "duration_seconds": 600,
		"job_seconds": 20, "priority_mix": map[string]float64{"100": 1},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/scenarios = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	if seed, _ := decodeBody(t, resp)["seed"].(float64); seed == 0 {
		t.Error("seed = 0; the scenario cannot be replayed identically")
	}
}

// The whole point of the app, end to end through HTTP.
func TestASimulationRunGoesFromRequestToResults(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"name": "Baseline", "mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 30, "time_compression": 1000000,
		"settings": map[string]any{"local_executor_cap": 20, "cloud_executor_cap": 40},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/runs = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	created := decodeBody(t, resp)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("the created run has no id: %#v", created)
	}

	waitForRun(t, f, runID, domain.StatusCompleted)

	metrics := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/metrics", nil))
	if submitted, _ := metrics["jobs_submitted"].(float64); submitted == 0 {
		t.Errorf("jobs_submitted = %v, want a real workload", metrics["jobs_submitted"])
	}
	if cycles, _ := metrics["cycles"].(float64); cycles == 0 {
		t.Error("the run produced no cycles")
	}

	timeline := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/cycles", nil))
	recorded, _ := timeline["cycles"].([]any)
	if len(recorded) == 0 {
		t.Fatal("no cycles were recorded")
	}
	first, _ := recorded[0].(map[string]any)
	if reason, _ := first["reason"].(string); reason == "" {
		t.Error("a recorded cycle has no reasoning attached")
	}
}

func TestARunCleansUpItsAutoscalerTarget(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 60, "time_compression": 1000000,
	})
	runID, _ := decodeBody(t, resp)["id"].(string)
	waitForRun(t, f, runID, domain.StatusCompleted)

	if f.fake.TargetExists(runID) {
		t.Error("the run's ephemeral target outlived it")
	}
}

func TestARunForAScenarioThatDoesNotExistIsRefusedImmediately(t *testing.T) {
	f := newFixture(t)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "nothing",
	})
	// Refused here, where the caller is looking, rather than inside a
	// background run whose status they would have to go and read.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /api/runs = %d, want 404", resp.StatusCode)
	}
}

func TestARunWithAnUnknownModeIsRefused(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "hybrid", "scenario_id": "burst",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /api/runs = %d, want 400", resp.StatusCode)
	}
}

func TestTheRunListSaysWhichRunsAreStillGoing(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 60, "time_compression": 1000000,
	})
	runID, _ := decodeBody(t, resp)["id"].(string)
	waitForRun(t, f, runID, domain.StatusCompleted)

	list := decodeBody(t, f.do(t, http.MethodGet, "/api/runs", nil))
	runs, _ := list["runs"].([]any)
	if len(runs) == 0 {
		t.Fatal("no runs were listed")
	}
	entry, _ := runs[0].(map[string]any)
	// A killed process leaves a run stuck at "running" in the table, so
	// whether it is really in flight is a live fact the list has to carry.
	if _, ok := entry["active"]; !ok {
		t.Error("the run listing does not say whether a run is in flight")
	}
}

func TestARunInFlightCannotBeDeleted(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	// A run that will not finish quickly, so it is still going when deleted.
	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 1, "time_compression": 1,
	})
	runID, _ := decodeBody(t, resp)["id"].(string)
	t.Cleanup(func() { _ = f.manager.Cancel(runID) })

	deadline := time.Now().Add(3 * time.Second)
	for !f.manager.IsActive(runID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if got := f.do(t, http.MethodDelete, "/api/runs/"+runID, nil); got.StatusCode != http.StatusConflict {
		t.Errorf("DELETE a running run = %d, want 409", got.StatusCode)
	}
}

func TestARunCanBeCancelled(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 1, "time_compression": 1,
	})
	runID, _ := decodeBody(t, resp)["id"].(string)

	deadline := time.Now().Add(3 * time.Second)
	for !f.manager.IsActive(runID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if got := f.do(t, http.MethodPost, "/api/runs/"+runID+"/cancel", nil); got.StatusCode != http.StatusAccepted {
		t.Fatalf("POST cancel = %d", got.StatusCode)
	}
	waitForRun(t, f, runID, domain.StatusCancelled)
}

// The client follows a run by asking for what it has not already seen.
func TestCyclesCanBeFetchedFromWhereAClientLeftOff(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 60, "time_compression": 1000000,
	})
	runID, _ := decodeBody(t, resp)["id"].(string)
	waitForRun(t, f, runID, domain.StatusCompleted)

	first := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/cycles?limit=3", nil))
	next, _ := first["next"].(float64)
	if next == 0 {
		t.Fatalf("next = %v, want a resume point", first["next"])
	}

	second := decodeBody(t, f.do(t, http.MethodGet,
		"/api/runs/"+runID+"/cycles?from="+strconv.Itoa(int(next)), nil))
	cycles, _ := second["cycles"].([]any)
	if len(cycles) == 0 {
		t.Fatal("resuming returned nothing")
	}
	entry, _ := cycles[0].(map[string]any)
	if sequence, _ := entry["sequence"].(float64); int(sequence) != int(next)+1 {
		t.Errorf("resumed at sequence %v, want %d", entry["sequence"], int(next)+1)
	}
}

// A watcher sees a run as it happens.
func TestTheEventStreamDeliversARunAsItHappens(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	req, err := http.NewRequest(http.MethodGet, f.server.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("building the stream request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream, err := f.server.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer stream.Body.Close()

	if got := stream.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	seen := make(chan string, 64)
	go func() {
		scanner := bufio.NewScanner(stream.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "event: ") {
				select {
				case seen <- strings.TrimPrefix(line, "event: "):
				default:
				}
			}
		}
	}()

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 60, "time_compression": 1000000,
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/runs = %d", resp.StatusCode)
	}

	deadline := time.After(15 * time.Second)
	wanted := map[string]bool{"status": false, "cycle": false, "metrics": false}
	for {
		select {
		case kind := <-seen:
			wanted[kind] = true
			if wanted["status"] && wanted["cycle"] && wanted["metrics"] {
				return
			}
		case <-deadline:
			t.Fatalf("the stream delivered %v, want status, cycle and metrics events", wanted)
		}
	}
}

// The browser never talks to the autoscaler; this service does, and passes
// its answers through.
func TestPlatformsAndTargetsAreServedFromTheAutoscaler(t *testing.T) {
	f := newFixture(t)

	platforms := decodeBody(t, f.do(t, http.MethodGet, "/api/platforms", nil))
	if list, _ := platforms["platforms"].([]any); len(list) == 0 {
		t.Error("no platforms were passed through")
	}

	if resp := f.do(t, http.MethodPost, "/api/targets", map[string]any{
		"target": map[string]any{"id": "storhall", "kind": "kubernetes", "mode": "autonomous"},
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/targets = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}

	targets := decodeBody(t, f.do(t, http.MethodGet, "/api/targets", nil))
	if list, _ := targets["targets"].([]any); len(list) != 1 {
		t.Errorf("targets = %#v, want the one just registered", targets["targets"])
	}
}

// The settings editor has to reach the live controller, or it is decoration.
func TestChangingATargetsSettingsReachesTheAutoscaler(t *testing.T) {
	f := newFixture(t)
	if resp := f.do(t, http.MethodPost, "/api/targets", map[string]any{
		"target": map[string]any{"id": "storhall", "kind": "kubernetes"},
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/targets = %d", resp.StatusCode)
	}

	resp := f.do(t, http.MethodPatch, "/api/targets/storhall/settings",
		map[string]any{"local_executor_cap": 64})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH settings = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}

	if got := f.fake.SettingsOf("storhall")["local_executor_cap"]; got != 64.0 {
		t.Errorf("the autoscaler has local_executor_cap = %v, want 64", got)
	}
}

func TestAMissingTargetIsPassedThroughAsNotFound(t *testing.T) {
	f := newFixture(t)

	if resp := f.do(t, http.MethodGet, "/api/targets/nobody", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/targets/nobody = %d, want 404", resp.StatusCode)
	}
}

func TestAMalformedBodyIs400(t *testing.T) {
	f := newFixture(t)

	req, _ := http.NewRequest(http.MethodPost, f.server.URL+"/api/mines", strings.NewReader("{nope"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST with a malformed body = %d, want 400", resp.StatusCode)
	}
}

func waitForRun(t *testing.T, f *fixture, runID string, want domain.RunStatus) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := f.store.Run(context.Background(), runID)
		if err == nil && stored.Status == want {
			return
		}
		if err == nil && stored.Status.Terminal() && stored.Status != want {
			t.Fatalf("run %q ended as %q, want %q: %s", runID, stored.Status, want, stored.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	stored, _ := f.store.Run(context.Background(), runID)
	t.Fatalf("run %q did not reach %q; it is %q", runID, want, stored.Status)
}
