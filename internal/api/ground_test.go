package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/observe"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

type groundBody struct {
	AtSeconds float64        `json:"at_seconds"`
	Knowledge string         `json:"knowledge"`
	Ground    []observe.Path `json:"ground"`
}

// What a page draws as protected ground has to be what the planner decided
// from, or a picture of a run misleads about why it went as it did. So the
// ground is served from the same views the run engine builds, and this test
// builds them again from what the run stored and requires the same answer.
func TestARunServesTheGroundItsPlannerProtects(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)
	ctx := context.Background()
	entities, err := f.store.Entities(ctx, runID)
	if err != nil {
		t.Fatalf("Entities() = %v", err)
	}
	layout, err := f.store.RunLayout(ctx, runID)
	if err != nil {
		t.Fatalf("RunLayout() = %v", err)
	}
	views := observe.ViewsOf(entities, layout.Tunnels, workload.WalkingSpeed)
	everyone := func(string) bool { return true }

	for _, knowledge := range []domain.IntentKnowledge{domain.KnowledgeEstimate, domain.KnowledgeTruth} {
		resp := f.do(t, http.MethodGet, "/api/runs/"+runID+"/ground?at_seconds=90&lookahead_seconds=300"+
			"&protect=person,crewed-vehicle,autonomous-vehicle&knowledge="+string(knowledge), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: GET ground = %d", knowledge, resp.StatusCode)
		}
		var got groundBody
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		resp.Body.Close()

		want := views.For(knowledge).Reach(90*time.Second, 300*time.Second, everyone)
		if len(want) == 0 {
			t.Fatalf("%s: the run protects no one at 90 s, so this proves nothing", knowledge)
		}
		if got.AtSeconds != 90 || got.Knowledge != string(knowledge) || !reflect.DeepEqual(got.Ground, want) {
			t.Errorf("%s: served %d paths at %v s under %q, want the %d the planner's view has",
				knowledge, len(got.Ground), got.AtSeconds, got.Knowledge, len(want))
		}
	}
}

func TestTheGroundLeavesOutWhoIsNotProtected(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)
	resp := f.do(t, http.MethodGet, "/api/runs/"+runID+"/ground?at_seconds=90&protect=autonomous-vehicle", nil)
	var got groundBody
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	for _, path := range got.Ground {
		if !strings.HasPrefix(path.Entity, "autonomous-vehicle") {
			t.Errorf("ground for %s, when only autonomous vehicles are protected", path.Entity)
		}
	}
}

func TestAGroundRequestThatCannotBeAnsweredSaysWhichParameterIsWrong(t *testing.T) {
	f := newFixture(t)
	runID := completedRun(t, f)
	for query, field := range map[string]string{
		"":                                    "at_seconds",
		"?at_seconds=soon":                    "at_seconds",
		"?at_seconds=-1":                      "at_seconds",
		"?at_seconds=1&lookahead_seconds=-5":  "lookahead_seconds",
		"?at_seconds=1&protect=person,miners": "miners",
		"?at_seconds=1&knowledge=psychic":     "knowledge",
	} {
		resp := f.do(t, http.MethodGet, "/api/runs/"+runID+"/ground"+query, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", query, resp.StatusCode)
			continue
		}
		if message, _ := decodeBody(t, resp)["error"].(string); !strings.Contains(message, field) {
			t.Errorf("%q: error %q does not name %s", query, message, field)
		}
	}
}

func TestTheGroundOfARunThatDoesNotExistIsNotFound(t *testing.T) {
	f := newFixture(t)
	resp := f.do(t, http.MethodGet, "/api/runs/no-such-run/ground?at_seconds=1", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}
