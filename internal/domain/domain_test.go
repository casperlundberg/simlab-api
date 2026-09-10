package domain_test

import (
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
