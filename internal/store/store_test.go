package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/store"
)

// These tests run against a real Postgres. Nothing here is worth testing
// against a fake: the value is in the schema constraints, the JSONB
// round-trips and the cascade behaviour, and a fake would assert none of them.
func open(t *testing.T) *store.Store {
	t.Helper()

	url := os.Getenv("SIMLAB_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SIMLAB_TEST_DATABASE_URL is not set; skipping the database tests")
	}

	s, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(s.Close)

	// Each test starts from a clean slate, so one test's rows cannot make
	// another pass or fail.
	for _, id := range []string{"storhall", "kvarnberg"} {
		_ = s.DeleteMine(context.Background(), id)
	}
	for _, run := range mustRuns(t, s) {
		_ = s.DeleteRun(context.Background(), run.ID)
	}
	return s
}

func mustRuns(t *testing.T, s *store.Store) []domain.Run {
	t.Helper()
	runs, err := s.Runs(context.Background(), "", 500)
	if err != nil {
		t.Fatalf("Runs() = %v", err)
	}
	return runs
}

func mine() domain.Mine {
	return domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 48, BackgroundRate: 12,
		Description: "Main production area"}
}

func scenario() domain.Scenario {
	return domain.Scenario{
		ID: "large-event", MineID: "storhall", Name: "Large seismic event",
		Duration: 6 * time.Hour, JobSeconds: 20, Seed: 42,
		PriorityMix: map[domain.Priority]float64{
			domain.PriorityAssociate: 0.2, domain.PriorityPick: 0.8,
		},
		Bursts: []domain.Burst{{At: time.Hour, Magnitude: 40, AftershockDecay: 3 * time.Hour}},
	}
}

func simulationRun() domain.Run {
	return domain.Run{
		ID: "run-1", Name: "Baseline", ScenarioID: "large-event", TargetID: "run-1",
		Mode: domain.ModeSimulation, Status: domain.StatusPending,
		SimulatedStart:   time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC),
		TimeCompression:  600,
		DecisionInterval: 15 * time.Second,
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	s := open(t)
	_ = s

	// Open runs them; opening again must be a no-op rather than a failure, or
	// every restart of the service would crash it.
	url := os.Getenv("SIMLAB_TEST_DATABASE_URL")
	second, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("second Open() = %v", err)
	}
	second.Close()
}

func TestAMineRoundTrips(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if err := s.SaveMine(ctx, mine()); err != nil {
		t.Fatalf("SaveMine() = %v", err)
	}

	got, err := s.Mine(ctx, "storhall")
	if err != nil {
		t.Fatalf("Mine() = %v", err)
	}
	if got.Name != "Storhall" || got.Sensors != 48 || got.BackgroundRate != 12 {
		t.Errorf("Mine() = %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set by the database")
	}
}

func TestSavingAMineTwiceUpdatesIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SaveMine(ctx, mine()); err != nil {
		t.Fatalf("SaveMine() = %v", err)
	}

	updated := mine()
	updated.Sensors = 96
	if err := s.SaveMine(ctx, updated); err != nil {
		t.Fatalf("second SaveMine() = %v", err)
	}

	got, _ := s.Mine(ctx, "storhall")
	if got.Sensors != 96 {
		t.Errorf("Sensors = %d, want the update applied", got.Sensors)
	}
}

func TestAnUnusableMineIsRefusedBeforeItReachesTheDatabase(t *testing.T) {
	s := open(t)
	bad := mine()
	bad.Sensors = 0

	if err := s.SaveMine(context.Background(), bad); err == nil {
		t.Error("SaveMine() = nil for a mine with no sensors")
	}
}

func TestAMissingRowIsRecognisableAsNotFound(t *testing.T) {
	s := open(t)

	_, err := s.Mine(context.Background(), "nobody")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Mine() = %v, want ErrNotFound", err)
	}
}

// The scenario is the reproducibility contract, so everything that shapes the
// workload has to survive a round trip exactly.
func TestAScenarioRoundTripsIncludingItsBurstsAndMix(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SaveMine(ctx, mine()); err != nil {
		t.Fatalf("SaveMine() = %v", err)
	}
	if err := s.SaveScenario(ctx, scenario()); err != nil {
		t.Fatalf("SaveScenario() = %v", err)
	}

	got, err := s.Scenario(ctx, "large-event")
	if err != nil {
		t.Fatalf("Scenario() = %v", err)
	}

	if got.Duration != 6*time.Hour {
		t.Errorf("Duration = %v, want 6h", got.Duration)
	}
	if got.Seed != 42 {
		t.Errorf("Seed = %d, want 42 — without it the run is not reproducible", got.Seed)
	}
	if got.PriorityMix[domain.PriorityPick] != 0.8 {
		t.Errorf("PriorityMix = %v", got.PriorityMix)
	}
	if len(got.Bursts) != 1 {
		t.Fatalf("%d bursts, want 1", len(got.Bursts))
	}
	if got.Bursts[0].At != time.Hour || got.Bursts[0].Magnitude != 40 ||
		got.Bursts[0].AftershockDecay != 3*time.Hour {
		t.Errorf("Bursts[0] = %+v", got.Bursts[0])
	}
}

func TestDeletingAMineTakesItsScenariosWithIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SaveMine(ctx, mine()); err != nil {
		t.Fatalf("SaveMine() = %v", err)
	}
	if err := s.SaveScenario(ctx, scenario()); err != nil {
		t.Fatalf("SaveScenario() = %v", err)
	}

	if err := s.DeleteMine(ctx, "storhall"); err != nil {
		t.Fatalf("DeleteMine() = %v", err)
	}
	if _, err := s.Scenario(ctx, "large-event"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Scenario() after deleting its mine = %v, want ErrNotFound", err)
	}
}

func TestARunRoundTrips(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)

	if err := s.SaveRun(ctx, simulationRun(), json.RawMessage(`{"local_executor_cap": 20}`)); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	got, err := s.Run(ctx, "run-1")
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if got.Mode != domain.ModeSimulation || got.DecisionInterval != 15*time.Second {
		t.Errorf("Run() = %+v", got)
	}
	if got.TimeCompression != 600 {
		t.Errorf("TimeCompression = %v, want 600", got.TimeCompression)
	}

	settings, err := s.RunSettings(ctx, "run-1")
	if err != nil {
		t.Fatalf("RunSettings() = %v", err)
	}
	if len(settings) == 0 {
		t.Error("the settings the run was executed under were not stored")
	}
}

// A run that reaches a terminal status with no finish time makes a list of
// runs impossible to sort sensibly, so the two are set together.
func TestReachingATerminalStatusStampsTheFinishTime(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	if err := s.SetStatus(ctx, "run-1", domain.StatusRunning, ""); err != nil {
		t.Fatalf("SetStatus(running) = %v", err)
	}
	running, _ := s.Run(ctx, "run-1")
	if running.StartedAt.IsZero() {
		t.Error("StartedAt was not stamped when the run started")
	}
	if !running.FinishedAt.IsZero() {
		t.Error("FinishedAt was stamped while the run was still going")
	}

	if err := s.SetStatus(ctx, "run-1", domain.StatusCompleted, ""); err != nil {
		t.Fatalf("SetStatus(completed) = %v", err)
	}
	finished, _ := s.Run(ctx, "run-1")
	if finished.FinishedAt.IsZero() {
		t.Error("FinishedAt was not stamped when the run completed")
	}
}

func TestAFailedRunKeepsItsReason(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	if err := s.SetStatus(ctx, "run-1", domain.StatusFailed, "the autoscaler was unreachable"); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}

	got, _ := s.Run(ctx, "run-1")
	if got.Error != "the autoscaler was unreachable" {
		t.Errorf("Error = %q, want the failure preserved", got.Error)
	}
}

func TestCyclesComeBackInOrderWithTheirQueues(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	for sequence := 1; sequence <= 5; sequence++ {
		if err := s.SaveCycle(ctx, domain.Cycle{
			RunID: "run-1", Sequence: sequence,
			At: time.Date(2026, 9, 10, 6, 0, sequence*15, 0, time.UTC),
			Queues: map[domain.Priority]domain.QueueSnapshot{
				domain.PriorityAssociate: {Depth: sequence * 10, OldestJobAgeSeconds: 12.5, ArrivalRate: 2},
			},
			LocalReady: sequence, Action: "scale_up", PlanLocal: sequence + 1,
			Reason:          "P100 breaches in " + strconv.Itoa(sequence) + "s",
			SettingsVersion: 3, Completed: 4, Breached: 1,
		}); err != nil {
			t.Fatalf("SaveCycle(%d) = %v", sequence, err)
		}
	}

	cycles, err := s.Cycles(ctx, "run-1", 0, 0)
	if err != nil {
		t.Fatalf("Cycles() = %v", err)
	}
	if len(cycles) != 5 {
		t.Fatalf("%d cycles, want 5", len(cycles))
	}
	for i, cycle := range cycles {
		if cycle.Sequence != i+1 {
			t.Fatalf("cycle %d has sequence %d", i, cycle.Sequence)
		}
	}

	level := cycles[2].Queues[domain.PriorityAssociate]
	if level.Depth != 30 || level.OldestJobAgeSeconds != 12.5 {
		t.Errorf("queue snapshot = %+v, want depth 30 and age 12.5s", level)
	}
	if cycles[2].Reason == "" || cycles[2].SettingsVersion != 3 {
		t.Errorf("cycle = %+v, want its reasoning preserved", cycles[2])
	}
}

// Runs are streamed to a browser page by page, so the reader has to be able to
// ask for what it has not already seen.
func TestCyclesCanBeReadFromWhereAReaderLeftOff(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}
	for sequence := 1; sequence <= 10; sequence++ {
		if err := s.SaveCycle(ctx, domain.Cycle{RunID: "run-1", Sequence: sequence,
			At: time.Now().UTC()}); err != nil {
			t.Fatalf("SaveCycle() = %v", err)
		}
	}

	cycles, err := s.Cycles(ctx, "run-1", 7, 0)
	if err != nil {
		t.Fatalf("Cycles() = %v", err)
	}
	if len(cycles) != 3 || cycles[0].Sequence != 8 {
		t.Errorf("Cycles(from=7) returned %d cycles starting at %d, want 3 starting at 8",
			len(cycles), cycles[0].Sequence)
	}
}

// A retried run re-emits the same sequence numbers, and failing on the second
// attempt would make a retry impossible for the least interesting reason.
func TestSavingACycleTwiceReplacesIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	at := time.Now().UTC()
	if err := s.SaveCycle(ctx, domain.Cycle{RunID: "run-1", Sequence: 1, At: at, PlanLocal: 3}); err != nil {
		t.Fatalf("first SaveCycle() = %v", err)
	}
	if err := s.SaveCycle(ctx, domain.Cycle{RunID: "run-1", Sequence: 1, At: at, PlanLocal: 9}); err != nil {
		t.Fatalf("second SaveCycle() = %v", err)
	}

	cycles, _ := s.Cycles(ctx, "run-1", 0, 0)
	if len(cycles) != 1 || cycles[0].PlanLocal != 9 {
		t.Errorf("cycles = %+v, want one cycle with the replaced plan", cycles)
	}
}

func TestMetricsRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	want := domain.Metrics{
		RunID: "run-1", JobsSubmitted: 12000, JobsCompleted: 11800, SLABreaches: 42,
		BreachRate: 0.0035, MeanWaitSeconds: 14.2, P95WaitSeconds: 61.5, MaxWaitSeconds: 300,
		PeakQueueDepth: 1800, LocalExecutorSeconds: 90000, CloudExecutorSeconds: 12000,
		PeakLocalExecutors: 50, PeakCloudExecutors: 18, ScalingActions: 63, Cycles: 1440,
	}
	if err := s.SaveMetrics(ctx, want); err != nil {
		t.Fatalf("SaveMetrics() = %v", err)
	}

	got, err := s.Metrics(ctx, "run-1")
	if err != nil {
		t.Fatalf("Metrics() = %v", err)
	}
	if got != want {
		t.Errorf("Metrics() = %+v, want %+v", got, want)
	}
}

func TestDeletingARunTakesItsCyclesAndMetricsWithIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}
	if err := s.SaveCycle(ctx, domain.Cycle{RunID: "run-1", Sequence: 1, At: time.Now().UTC()}); err != nil {
		t.Fatalf("SaveCycle() = %v", err)
	}
	if err := s.SaveMetrics(ctx, domain.Metrics{RunID: "run-1"}); err != nil {
		t.Fatalf("SaveMetrics() = %v", err)
	}

	if err := s.DeleteRun(ctx, "run-1"); err != nil {
		t.Fatalf("DeleteRun() = %v", err)
	}
	if _, err := s.Metrics(ctx, "run-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Metrics() after DeleteRun = %v, want ErrNotFound", err)
	}
	cycles, _ := s.Cycles(ctx, "run-1", 0, 0)
	if len(cycles) != 0 {
		t.Errorf("%d cycles survived the run being deleted", len(cycles))
	}
}

// A completed run's results stay meaningful after its scenario is tidied away,
// and losing them would destroy the only record of what the autoscaler did.
func TestDeletingAScenarioDoesNotDestroyItsRuns(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}
	if err := s.SaveMetrics(ctx, domain.Metrics{RunID: "run-1", JobsSubmitted: 500}); err != nil {
		t.Fatalf("SaveMetrics() = %v", err)
	}

	if err := s.DeleteScenario(ctx, "large-event"); err != nil {
		t.Fatalf("DeleteScenario() = %v", err)
	}

	got, err := s.Run(ctx, "run-1")
	if err != nil {
		t.Fatalf("Run() after deleting its scenario = %v", err)
	}
	if got.ScenarioID != "" {
		t.Errorf("ScenarioID = %q, want it cleared", got.ScenarioID)
	}
	metrics, err := s.Metrics(ctx, "run-1")
	if err != nil || metrics.JobsSubmitted != 500 {
		t.Errorf("the run's results did not survive its scenario being deleted: %v", err)
	}
}

func TestRunsAreListedNewestFirstAndCanBeFilteredByStatus(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)

	for i, status := range []domain.RunStatus{domain.StatusCompleted, domain.StatusFailed} {
		run := simulationRun()
		run.ID = "run-" + strconv.Itoa(i+1)
		run.TargetID = run.ID
		run.Status = status
		if err := s.SaveRun(ctx, run, nil); err != nil {
			t.Fatalf("SaveRun() = %v", err)
		}
	}

	all, err := s.Runs(ctx, "", 0)
	if err != nil {
		t.Fatalf("Runs() = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("%d runs, want 2", len(all))
	}

	failed, err := s.Runs(ctx, domain.StatusFailed, 0)
	if err != nil {
		t.Fatalf("Runs(failed) = %v", err)
	}
	if len(failed) != 1 || failed[0].Status != domain.StatusFailed {
		t.Errorf("Runs(failed) = %+v", failed)
	}
}

func seed(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.SaveMine(ctx, mine()); err != nil {
		t.Fatalf("SaveMine() = %v", err)
	}
	if err := s.SaveScenario(ctx, scenario()); err != nil {
		t.Fatalf("SaveScenario() = %v", err)
	}
}
