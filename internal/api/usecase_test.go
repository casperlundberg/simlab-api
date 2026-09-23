package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/usecase"
)

type useCaseBody struct {
	Kind     string            `json:"kind"`
	Params   json.RawMessage   `json:"params"`
	Summary  json.RawMessage   `json:"summary"`
	Outcomes []json.RawMessage `json:"outcomes"`
}

// A use case's score is a pure function of what a run stored, so the endpoint
// has to give exactly what scoring the stored run directly gives.
func TestARunIsScoredForAUseCaseFromWhatItRecorded(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)
	ctx := context.Background()
	events, err := f.store.SeismicEvents(ctx, runID, 0, 20000)
	if err != nil {
		t.Fatalf("SeismicEvents() = %v", err)
	}
	entities, _ := f.store.Entities(ctx, runID)
	layout, _ := f.store.RunLayout(ctx, runID)
	world, record := usecase.FromRun(events, entities, layout.Tunnels)

	for _, kind := range usecase.Kinds() {
		params := `{"level":"moderate","window_seconds":3600}`
		resp := f.do(t, http.MethodPost, "/api/runs/"+runID+"/use-cases",
			map[string]any{"kind": kind, "params": json.RawMessage(params)})
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: POST use-cases = %d: %s", kind, resp.StatusCode, body)
		}
		var got useCaseBody
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		resp.Body.Close()

		c, err := usecase.New(kind, json.RawMessage(params))
		if err != nil {
			t.Fatalf("New() = %v", err)
		}
		outcomes := usecase.Score(c.Opportunities(world), record)
		wantSummary, _ := json.Marshal(usecase.Summarise(outcomes))
		wantParams, _ := json.Marshal(c.Params())
		if got.Kind != kind || string(got.Summary) != string(wantSummary) || string(got.Params) != string(wantParams) {
			t.Errorf("%s: served %s with %s, want %s with %s", kind, got.Summary, got.Params, wantSummary, wantParams)
		}
		if len(got.Outcomes) != len(outcomes) {
			t.Errorf("%s: %d outcomes served, %d scored", kind, len(got.Outcomes), len(outcomes))
		}
	}
}

func TestAUseCaseRequestThatCannotBeAnsweredSaysWhy(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)
	for body, want := range map[string]string{
		`{"kind":"fortune-telling"}`:                            "turn-back",
		`{"kind":"turn-back","params":{"level":"cataclysmic"}}`: "level",
		`{"kind":"turn-back","params":{"crystal_ball":1}}`:      "crystal_ball",
		`{"params":{}}`: "kind",
	} {
		resp := f.do(t, http.MethodPost, "/api/runs/"+runID+"/use-cases", json.RawMessage(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
			continue
		}
		if message, _ := decodeBody(t, resp)["error"].(string); !strings.Contains(message, want) {
			t.Errorf("%s: error %q does not mention %s", body, message, want)
		}
	}
	resp := f.do(t, http.MethodPost, "/api/runs/no-such-run/use-cases", json.RawMessage(`{"kind":"turn-back"}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a run that does not exist: status %d, want 404", resp.StatusCode)
	}
}

// The closure map is the same kind of pure function of a recorded run, and is
// served as what drawing it directly gives.
func TestARunsClosureMapIsDrawnFromWhatItRecorded(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)
	ctx := context.Background()
	events, err := f.store.SeismicEvents(ctx, runID, 0, 20000)
	if err != nil {
		t.Fatalf("SeismicEvents() = %v", err)
	}
	entities, _ := f.store.Entities(ctx, runID)
	layout, _ := f.store.RunLayout(ctx, runID)
	world, record := usecase.FromRun(events, entities, layout.Tunnels)

	params := `{"window_seconds":3600,"step_meters":50}`
	resp := f.do(t, http.MethodPost, "/api/runs/"+runID+"/closure",
		map[string]any{"params": json.RawMessage(params)})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST closure = %d: %s", resp.StatusCode, body)
	}
	var got struct {
		Params  json.RawMessage   `json:"params"`
		Summary json.RawMessage   `json:"summary"`
		Events  []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	resp.Body.Close()

	c, err := usecase.NewClosure(json.RawMessage(params))
	if err != nil {
		t.Fatalf("NewClosure() = %v", err)
	}
	want := c.Map(world, record)
	wantSummary, _ := json.Marshal(want.Summary)
	wantParams, _ := json.Marshal(c.Params())
	if string(got.Summary) != string(wantSummary) || string(got.Params) != string(wantParams) {
		t.Errorf("served %s with %s, want %s with %s", got.Summary, got.Params, wantSummary, wantParams)
	}
	if len(got.Events) != len(want.Events) {
		t.Errorf("%d events served, %d drawn", len(got.Events), len(want.Events))
	}
}

func TestAClosureRequestThatCannotBeAnsweredSaysWhy(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)
	for body, want := range map[string]string{
		`{"params":{"level":"cataclysmic"}}`: "level",
		`{"params":{"crystal_ball":1}}`:      "crystal_ball",
		`{"params":{"step_meters":0}}`:       "step_meters",
	} {
		resp := f.do(t, http.MethodPost, "/api/runs/"+runID+"/closure", json.RawMessage(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
			continue
		}
		if message, _ := decodeBody(t, resp)["error"].(string); !strings.Contains(message, want) {
			t.Errorf("%s: error %q does not mention %s", body, message, want)
		}
	}
	resp := f.do(t, http.MethodPost, "/api/runs/no-such-run/closure", json.RawMessage(`{}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a run that does not exist: status %d, want 404", resp.StatusCode)
	}
}

// The whole chain an experiment depends on: a scenario scripts encounters, the
// generator places them, the run records what produced each event, and a case
// asked about that activity scores the scripted decisions and nothing else.
func TestAScenariosScriptedEncountersReachTheDecisionsScoredForThem(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)
	if resp := f.do(t, http.MethodPost, "/api/scenarios", map[string]any{
		"id": "scripted", "mine_id": "storhall", "name": "Scripted",
		"duration_seconds": 1800, "job_seconds": 20, "seed": 11,
		"priority_mix": map[string]float64{"100": 1, "25": 3},
		"encounters":   map[string]any{"count": 8, "magnitude": 2.5, "lead_seconds": 120, "level": "high"},
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/scenarios = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "scripted",
		"decision_interval_seconds": 30, "time_compression": 1000000,
		"settings": map[string]any{"local_executor_cap": 20, "cloud_executor_cap": 40},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/runs = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	runID, _ := decodeBody(t, resp)["id"].(string)
	waitForRun(t, f, runID, domain.StatusCompleted)

	events, err := f.store.SeismicEvents(context.Background(), runID, 0, 20000)
	if err != nil {
		t.Fatalf("SeismicEvents() = %v", err)
	}
	scripted := 0
	for _, e := range events {
		if e.Activity == "encounter" {
			scripted++
		}
	}
	if scripted == 0 {
		t.Fatal("the run recorded no scripted encounter; the scenario asked for eight")
	}

	all := useCaseSummary(t, f, runID, `{"level":"high"}`)
	only := useCaseSummary(t, f, runID, `{"level":"high","activity":"encounter"}`)
	if only.Opportunities == 0 {
		t.Error("no decision was scored for the encounters that were scripted")
	}
	if only.Opportunities > all.Opportunities {
		t.Errorf("%d decisions about the encounters alone, %d about every event",
			only.Opportunities, all.Opportunities)
	}
}

func useCaseSummary(t *testing.T, f *fixture, runID, params string) usecase.Summary {
	t.Helper()
	resp := f.do(t, http.MethodPost, "/api/runs/"+runID+"/use-cases",
		map[string]any{"kind": "turn-back", "params": json.RawMessage(params)})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST use-cases %s = %d: %s", params, resp.StatusCode, body)
	}
	var got struct {
		Summary usecase.Summary `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	resp.Body.Close()
	return got.Summary
}
