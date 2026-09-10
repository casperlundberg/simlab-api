package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func validMine() domain.Mine {
	return domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 48, BackgroundRate: 12}
}

func validScenario() domain.Scenario {
	return domain.Scenario{
		ID: "large-event", MineID: "storhall", Name: "Large seismic event",
		Duration:   6 * time.Hour,
		JobSeconds: 20,
		PriorityMix: map[domain.Priority]float64{
			domain.PriorityAssociate: 0.2,
			domain.PriorityLocate:    0.3,
			domain.PriorityPick:      0.5,
		},
		Bursts: []domain.Burst{{At: time.Hour, Magnitude: 40, AftershockDecay: 3 * time.Hour}},
		Seed:   42,
	}
}

func validRun() domain.Run {
	return domain.Run{
		ID: "run-1", TargetID: "storhall", Mode: domain.ModeSimulation,
		ScenarioID: "large-event", TimeCompression: 600,
		DecisionInterval: 15 * time.Second,
	}
}

func TestAWellFormedMineIsAccepted(t *testing.T) {
	if err := validMine().Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestAMineWithoutSensorsIsRefused(t *testing.T) {
	mine := validMine()
	mine.Sensors = 0

	if err := mine.Validate(); err == nil || !strings.Contains(err.Error(), "sensors") {
		t.Errorf("Validate() = %v, want a complaint about sensors", err)
	}
}

func TestAWellFormedScenarioIsAccepted(t *testing.T) {
	if err := validScenario().Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestAScenarioReportsEveryProblemAtOnce(t *testing.T) {
	scenario := validScenario()
	scenario.Duration = 0
	scenario.JobSeconds = 0
	scenario.PriorityMix = nil

	err := scenario.Validate()
	if err == nil {
		t.Fatal("Validate() = nil")
	}
	for _, want := range []string{"duration", "job_seconds", "priority_mix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() = %q, want it to mention %q", err, want)
		}
	}
}

// A burst outside the scenario window simply never happens, which looks
// exactly like a scenario that was mis-specified and produces a run whose
// results mean nothing.
func TestABurstOutsideTheScenarioIsRefused(t *testing.T) {
	scenario := validScenario()
	scenario.Bursts = []domain.Burst{{At: 10 * time.Hour, Magnitude: 5}}

	if err := scenario.Validate(); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("Validate() = %v, want the burst flagged as outside the scenario", err)
	}
}

func TestAPriorityMixOfAllZeroesIsRefused(t *testing.T) {
	scenario := validScenario()
	scenario.PriorityMix = map[domain.Priority]float64{domain.PriorityPick: 0}

	if err := scenario.Validate(); err == nil || !strings.Contains(err.Error(), "zero") {
		t.Errorf("Validate() = %v, want the all-zero mix refused", err)
	}
}

// Map iteration is randomised, and a scenario has to generate the same jobs
// every time or comparing two runs proves nothing.
func TestSortedPrioritiesIsDescendingAndStable(t *testing.T) {
	got := validScenario().SortedPriorities()

	want := []domain.Priority{domain.PriorityAssociate, domain.PriorityLocate, domain.PriorityPick}
	if len(got) != len(want) {
		t.Fatalf("SortedPriorities() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortedPriorities() = %v, want %v", got, want)
		}
	}
}

func TestAWellFormedRunIsAccepted(t *testing.T) {
	if err := validRun().Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestASimulationRunWithoutAScenarioIsRefused(t *testing.T) {
	run := validRun()
	run.ScenarioID = ""

	if err := run.Validate(); err == nil || !strings.Contains(err.Error(), "scenario_id") {
		t.Errorf("Validate() = %v, want a complaint about the missing scenario", err)
	}
}

// A live run watches what is really happening, so there is nothing to replay
// and nothing to compress.
func TestALiveRunNeedsNoScenario(t *testing.T) {
	run := domain.Run{
		ID: "run-2", TargetID: "storhall", Mode: domain.ModeLive,
		DecisionInterval: 15 * time.Second,
	}

	if err := run.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestAnUnknownRunModeIsRefused(t *testing.T) {
	run := validRun()
	run.Mode = "hybrid"

	if err := run.Validate(); err == nil || !strings.Contains(err.Error(), "hybrid") {
		t.Errorf("Validate() = %v, want the unknown mode named", err)
	}
}

func TestTerminalStatuses(t *testing.T) {
	for status, want := range map[domain.RunStatus]bool{
		domain.StatusPending:   false,
		domain.StatusRunning:   false,
		domain.StatusCompleted: true,
		domain.StatusFailed:    true,
		domain.StatusCancelled: true,
	} {
		if got := status.Terminal(); got != want {
			t.Errorf("%q.Terminal() = %v, want %v", status, got, want)
		}
	}
}

// A client has to be able to send back what it was given. Accepting
// `duration_seconds` and returning nanoseconds means it cannot, and that
// asymmetry is what an end-to-end run surfaced.
func TestAScenarioRoundTripsThroughItsOwnWireFormat(t *testing.T) {
	original := validScenario()

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatalf("Unmarshal to map = %v", err)
	}
	if got, want := generic["duration_seconds"], 6*3600.0; got != want {
		t.Errorf("duration_seconds = %v, want %v", got, want)
	}
	bursts, _ := generic["bursts"].([]any)
	if len(bursts) != 1 {
		t.Fatalf("bursts = %#v, want one", generic["bursts"])
	}
	if burst, _ := bursts[0].(map[string]any); burst["at_seconds"] != 3600.0 {
		t.Errorf("burst at_seconds = %v, want 3600", burst["at_seconds"])
	}

	var back domain.Scenario
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal back = %v", err)
	}
	if back.Duration != original.Duration || back.JobSeconds != original.JobSeconds ||
		back.Seed != original.Seed {
		t.Errorf("round trip = %+v, want %+v", back, original)
	}
	if len(back.Bursts) != 1 || back.Bursts[0].At != time.Hour ||
		back.Bursts[0].AftershockDecay != 3*time.Hour {
		t.Errorf("bursts round trip = %+v", back.Bursts)
	}
	if back.PriorityMix[domain.PriorityPick] != original.PriorityMix[domain.PriorityPick] {
		t.Errorf("priority mix round trip = %v", back.PriorityMix)
	}
}

func TestARunRoundTripsWithItsIntervalInSeconds(t *testing.T) {
	original := validRun()

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatalf("Unmarshal to map = %v", err)
	}
	if got, want := generic["decision_interval_seconds"], 15.0; got != want {
		t.Errorf("decision_interval_seconds = %v, want %v", got, want)
	}

	var back domain.Run
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal back = %v", err)
	}
	if back.DecisionInterval != original.DecisionInterval {
		t.Errorf("DecisionInterval round trip = %v, want %v",
			back.DecisionInterval, original.DecisionInterval)
	}
}

func TestAPriorityMixKeyThatIsNotALevelIsRefused(t *testing.T) {
	err := json.Unmarshal([]byte(`{"priority_mix":{"urgent":1}}`), &domain.Scenario{})
	if err == nil || !strings.Contains(err.Error(), "urgent") {
		t.Errorf("Unmarshal() = %v, want the bad key named", err)
	}
}
