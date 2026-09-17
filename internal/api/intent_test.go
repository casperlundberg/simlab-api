package api_test

import (
	"net/http"
	"testing"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func TestARunIsCreatedWithDecayOnlyIntentUnlessItSaysOtherwise(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 60, "time_compression": 1000000,
	})
	runID, _ := decodeBody(t, resp)["id"].(string)
	waitForRun(t, f, runID, domain.StatusCompleted)

	got := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/intent", nil))
	settings, _ := got["settings"].(map[string]any)
	if settings["mode"] != "decay" || settings["knowledge"] != "estimate" {
		t.Errorf("settings = %v, want decay only from estimates", settings)
	}
	if got["active"] != false || got["version"] != 1.0 {
		t.Errorf("active %v, version %v; want a finished run at version 1", got["active"], got["version"])
	}
	changes, _ := got["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %v, want the initial settings", got["changes"])
	}
	if first, _ := changes[0].(map[string]any); first["cycle"] != 1.0 || first["source"] != "initial" {
		t.Errorf("first change = %v", first)
	}

	metrics := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/metrics", nil))
	if _, present := metrics["sla_breaches_as_submitted"]; !present {
		t.Errorf("metrics = %v, want breaches as submitted", metrics)
	}
}

func TestARunCanBeCreatedWithIntentAndChangesPlannedForLater(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 60, "time_compression": 1000000,
		"intent":          map[string]any{"mode": "both", "burst_exempt": []string{"promoted"}},
		"intent_schedule": []map[string]any{{"cycle": 5, "settings": map[string]any{"mode": "off"}}},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/runs = %d: %v", resp.StatusCode, decodeBody(t, resp))
	}
	runID, _ := decodeBody(t, resp)["id"].(string)
	waitForRun(t, f, runID, domain.StatusCompleted)

	got := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/intent", nil))
	changes, _ := got["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want the initial settings and the planned change", got["changes"])
	}
	second, _ := changes[1].(map[string]any)
	if settings, _ := second["settings"].(map[string]any); second["cycle"] != 5.0 || second["source"] != "schedule" || settings["mode"] != "off" {
		t.Errorf("second change = %v, want off from cycle 5", second)
	}

	run := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID, nil))
	provenance, _ := run["provenance"].(map[string]any)
	if intent, _ := provenance["intent"].(map[string]any); intent == nil {
		t.Errorf("provenance = %v, want the intent the run was created with", provenance)
	}
}

func TestIntentThatCannotBeActedOnIsRefusedBeforeTheRunStarts(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	for name, body := range map[string]map[string]any{
		"an unknown mode": {"intent": map[string]any{"mode": "sideways"}},
		"a bad step":      {"intent_schedule": []map[string]any{{"cycle": 3, "settings": map[string]any{"lookahead_seconds": -5}}}},
		"a step at zero":  {"intent_schedule": []map[string]any{{"cycle": 0, "settings": map[string]any{}}}},
	} {
		body["mode"], body["scenario_id"] = "simulation", "burst"
		if resp := f.do(t, http.MethodPost, "/api/runs", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: POST /api/runs = %d, want 400", name, resp.StatusCode)
		}
	}

	live := map[string]any{"mode": "live", "target_id": "somewhere", "intent": map[string]any{"mode": "both"}}
	if resp := f.do(t, http.MethodPost, "/api/runs", live); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST a live run with intent = %d, want 400", resp.StatusCode)
	}
}

func TestIntentCanBeChangedOnlyWhileARunIsInFlight(t *testing.T) {
	f := newFixture(t)
	seedMineAndScenario(t, f)

	resp := f.do(t, http.MethodPost, "/api/runs", map[string]any{
		"mode": "simulation", "scenario_id": "burst",
		"decision_interval_seconds": 1, "time_compression": 1,
	})
	runID, _ := decodeBody(t, resp)["id"].(string)
	t.Cleanup(func() { _ = f.manager.Cancel(runID) })

	changed := f.do(t, http.MethodPatch, "/api/runs/"+runID+"/intent?expected_version=1", map[string]any{"mode": "both"})
	if changed.StatusCode != http.StatusOK {
		t.Fatalf("PATCH intent = %d: %v", changed.StatusCode, decodeBody(t, changed))
	}
	if body := decodeBody(t, changed); body["version"] != 2.0 {
		t.Errorf("PATCH intent = %v, want version 2", body)
	}

	if stale := f.do(t, http.MethodPatch, "/api/runs/"+runID+"/intent?expected_version=1", map[string]any{"mode": "off"}); stale.StatusCode != http.StatusConflict {
		t.Errorf("PATCH against version 1 at version 2 = %d, want 409", stale.StatusCode)
	}
	if bad := f.do(t, http.MethodPatch, "/api/runs/"+runID+"/intent", map[string]any{"mode": "sideways"}); bad.StatusCode != http.StatusBadRequest {
		t.Errorf("PATCH an unknown mode = %d, want 400", bad.StatusCode)
	}

	current := decodeBody(t, f.do(t, http.MethodGet, "/api/runs/"+runID+"/intent", nil))
	if settings, _ := current["settings"].(map[string]any); current["active"] != true || settings["mode"] != "both" {
		t.Errorf("GET intent = %v, want the live settings", current)
	}

	if err := f.manager.Cancel(runID); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	waitForRun(t, f, runID, domain.StatusCancelled)
	if late := f.do(t, http.MethodPatch, "/api/runs/"+runID+"/intent", map[string]any{"mode": "off"}); late.StatusCode != http.StatusConflict {
		t.Errorf("PATCH a finished run's intent = %d, want 409", late.StatusCode)
	}
}
