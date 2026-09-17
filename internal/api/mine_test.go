package api_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func completedRun(t *testing.T, f *fixture) string {
	t.Helper()
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 30, "time_compression": 1000000,
		"settings": map[string]any{"local_executor_cap": 20, "cloud_executor_cap": 40},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/runs = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	runID, _ := decodeBody(t, resp)["id"].(string)
	waitForRun(t, f, runID, domain.StatusCompleted)
	return runID
}

func TestARunsVirtualMineCanBeReadBack(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)

	resp := f.do(t, http.MethodGet, "/api/runs/"+runID+"/layout", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET layout = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	layout, _ := decodeBody(t, resp)["layout"].(map[string]any)
	if sensors, _ := layout["sensors"].([]any); len(sensors) != 20 {
		t.Errorf("the layout has %d sensors, the mine has 20", len(sensors))
	}

	body := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/seismicity", nil))
	events, _ := body["events"].([]any)
	if len(events) == 0 {
		t.Fatal("a completed run has no seismic events")
	}
	for _, raw := range events {
		event, _ := raw.(map[string]any)
		if event["processed_at_seconds"] == nil {
			t.Fatalf("event %v was never processed in a completed run", event["sequence"])
		}
		sensors, _ := event["sensors"].([]any)
		if len(sensors) >= 4 && event["located"] == nil {
			t.Fatalf("event %v had %d picks processed and no location", event["sequence"], len(sensors))
		}
		if _, ok := event["truth"].(map[string]any); !ok {
			t.Fatalf("event %v does not say where it happened", event["sequence"])
		}
	}
}

func TestSeismicityCanBeFetchedFromWhereAClientLeftOff(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)

	first := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/seismicity?limit=2", nil))
	if events, _ := first["events"].([]any); len(events) != 2 {
		t.Fatalf("limit=2 returned %d events", len(events))
	}
	next, _ := first["next"].(float64)
	if next != 2 {
		t.Fatalf("next = %v, want 2", first["next"])
	}

	second := decodeBody(t, f.do(t, http.MethodGet,
		"/api/runs/"+runID+"/seismicity?from="+strconv.Itoa(int(next)), nil))
	events, _ := second["events"].([]any)
	if len(events) == 0 {
		t.Fatal("resuming returned nothing")
	}
	if entry, _ := events[0].(map[string]any); entry["sequence"] != 3.0 {
		t.Errorf("resumed at sequence %v, want 3", entry["sequence"])
	}
}

// Every run recorded before mines were modelled has no layout, and neither
// does a live run. The answer has to say that, rather than a bare 404 that
// reads as a broken link.
func TestARunWithNoRecordedMineSaysWhy(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)
	if err := f.store.SaveRun(context.Background(), domain.Run{
		ID: "old-run", TargetID: "old-run", ScenarioID: "burst", Mode: domain.ModeSimulation,
		Status: domain.StatusCompleted, TimeCompression: 600, DecisionInterval: 15 * time.Second,
	}, nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	resp := f.do(t, http.MethodGet, "/api/runs/old-run/layout", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET layout = %d, want 404", resp.StatusCode)
	}
	if message, _ := decodeBody(t, resp)["error"].(string); !strings.Contains(message, "before mines were modelled") {
		t.Errorf("error = %q, want it to explain why there is no mine", message)
	}
}

func TestAStatedArrayAndScenarioGeometryRoundTripThroughTheAPI(t *testing.T) {
	f := newFixture(t)

	layout := map[string]any{
		"extent": map[string]any{
			"min": map[string]any{"x": 0, "y": 0, "z": -1000},
			"max": map[string]any{"x": 800, "y": 600, "z": -200},
		},
		"sensors": []map[string]any{
			{"id": "a", "at": map[string]any{"x": 100, "y": 100, "z": -300}},
			{"id": "b", "at": map[string]any{"x": 700, "y": 500, "z": -900}},
		},
	}
	resp := f.do(t, http.MethodPost, "/api/mines", map[string]any{
		"id": "storhall", "name": "Storhall", "sensors": 2, "background_rate_per_hour": 10, "layout": layout,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/mines = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	mine := decodeBody(t, f.do(t, http.MethodGet, "/api/mines/storhall", nil))
	stated, _ := mine["layout"].(map[string]any)
	if sensors, _ := stated["sensors"].([]any); len(sensors) != 2 {
		t.Fatalf("the stated layout did not come back: %v", mine)
	}

	resp = f.do(t, http.MethodPost, "/api/scenarios", map[string]any{
		"id": "burst", "mine_id": "storhall", "name": "Rock burst",
		"duration_seconds": 1800, "job_seconds": 20, "seed": 7, "pick_jitter_seconds": 0.003,
		"priority_mix": map[string]float64{"100": 1},
		"bursts": []map[string]any{{"at_seconds": 600, "magnitude": 30,
			"epicentre": map[string]any{"x": 400, "y": 300, "z": -600}}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/scenarios = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	scenario := decodeBody(t, f.do(t, http.MethodGet, "/api/scenarios/burst", nil))
	if scenario["pick_jitter_seconds"] != 0.003 {
		t.Errorf("pick_jitter_seconds = %v, want 0.003", scenario["pick_jitter_seconds"])
	}
	bursts, _ := scenario["bursts"].([]any)
	burst, _ := bursts[0].(map[string]any)
	if epicentre, _ := burst["epicentre"].(map[string]any); epicentre["z"] != -600.0 {
		t.Errorf("the burst's epicentre did not come back: %v", burst)
	}
}

// Found when the scenario is saved, where the person who typed the epicentre
// is looking, rather than as a failed run later.
func TestAnEpicentreOutsideTheMineIsRefusedWhenTheScenarioIsSaved(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/scenarios", map[string]any{
		"id": "bad", "mine_id": "storhall", "name": "Above ground",
		"duration_seconds": 1800, "job_seconds": 20, "seed": 7,
		"priority_mix": map[string]float64{"100": 1},
		"bursts": []map[string]any{{"at_seconds": 600, "magnitude": 30,
			"epicentre": map[string]any{"x": 400, "y": 300, "z": 250}}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /api/scenarios = %d, want 400", resp.StatusCode)
	}
	if message, _ := decodeBody(t, resp)["error"].(string); !strings.Contains(message, "burst 0") {
		t.Errorf("error = %q, want burst 0 named", message)
	}
}

func TestACycleCarriesTheQueueBySubmittedPriority(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)

	body := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/cycles", nil))
	cycles, _ := body["cycles"].([]any)
	if len(cycles) == 0 {
		t.Fatal("no cycles")
	}
	for _, raw := range cycles {
		cycle, _ := raw.(map[string]any)
		if _, ok := cycle["depth_by_submitted_priority"].(map[string]any); !ok {
			t.Fatalf("cycle %v has no depth_by_submitted_priority: %v",
				cycle["sequence"], cycle["depth_by_submitted_priority"])
		}
	}
}

func TestTheMinesPeopleAndVehiclesCanBeReadBack(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)

	body := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/entities", nil))
	entities, _ := body["entities"].([]any)
	if len(entities) == 0 {
		t.Fatal("a completed run has nobody underground")
	}
	first, _ := entities[0].(map[string]any)
	track, _ := first["track"].([]any)
	if len(track) < 2 {
		t.Fatalf("%v has a track of %d waypoints", first["id"], len(track))
	}
	if point, _ := track[0].([]any); len(point) != 4 {
		t.Errorf("a waypoint is %v, want [seconds, x, y, z]", track[0])
	}
}

func TestTheServiceSaysWhichBuildsItAndItsAutoscalerAre(t *testing.T) {
	f := newFixture(t)

	body := decodeBody(t, f.do(t, http.MethodGet, "/api/version", nil))
	simlab, _ := body["simlab_api"].(map[string]any)
	if _, ok := simlab["commit"]; !ok {
		t.Errorf("no simlab_api build: %v", body)
	}
	autoscaler, _ := body["autoscaler"].(map[string]any)
	if autoscaler["version"] != f.fake.Build["version"] {
		t.Errorf("autoscaler build = %v, want the fake's", body["autoscaler"])
	}
}

func TestARunServesItsProvenance(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)

	body := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID, nil))
	provenance, _ := body["provenance"].(map[string]any)
	scenario, _ := provenance["scenario"].(map[string]any)
	if scenario["seed"] != 7.0 {
		t.Errorf("provenance scenario = %v, want the seeded scenario the run replayed", provenance["scenario"])
	}
	run, _ := body["run"].(map[string]any)
	if _, ok := run["built_with"].(map[string]any); !ok {
		t.Errorf("run = %v, want built_with", run)
	}
}
