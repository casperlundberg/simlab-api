package run_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/autoscaler"
	"github.com/casperlundberg/simlab-api/internal/autoscaler/astest"
	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/run"
)

var start = time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC)

// recorder collects what a run produced, so a test can read it back.
type recorder struct {
	mu       sync.Mutex
	cycles   []domain.Cycle
	metrics  []domain.Metrics
	statuses []domain.RunStatus
	failures []string

	failSaveCycleAt int

	layouts []domain.Layout

	// seismic is each event's latest record, by sequence, as a store that
	// upserts would hold it; seismicWrites is every write in order.
	seismic       map[int]domain.SeismicEvent
	seismicWrites [][]domain.SeismicEvent
}

func (r *recorder) SaveLayout(_ context.Context, _ string, layout domain.Layout) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.layouts = append(r.layouts, layout)
	return nil
}

func (r *recorder) SaveSeismicEvents(_ context.Context, _ string, events []domain.SeismicEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seismic == nil {
		r.seismic = map[int]domain.SeismicEvent{}
	}
	for _, event := range events {
		r.seismic[event.Sequence] = event
	}
	r.seismicWrites = append(r.seismicWrites, events)
	return nil
}

func (r *recorder) SaveCycle(_ context.Context, cycle domain.Cycle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cycles = append(r.cycles, cycle)
	if r.failSaveCycleAt > 0 && len(r.cycles) >= r.failSaveCycleAt {
		return context.DeadlineExceeded
	}
	return nil
}

func (r *recorder) SaveMetrics(_ context.Context, metrics domain.Metrics) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = append(r.metrics, metrics)
	return nil
}

func (r *recorder) SetStatus(_ context.Context, _ string, status domain.RunStatus, failure string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statuses = append(r.statuses, status)
	r.failures = append(r.failures, failure)
	return nil
}

func (r *recorder) lastStatus() domain.RunStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.statuses) == 0 {
		return ""
	}
	return r.statuses[len(r.statuses)-1]
}

type collector struct {
	mu     sync.Mutex
	events []run.Event
}

func (c *collector) Publish(event run.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *collector) typed(kind string) []run.Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []run.Event
	for _, event := range c.events {
		if event.Type == kind {
			out = append(out, event)
		}
	}
	return out
}

type harness struct {
	engine   *run.Engine
	fake     *astest.Server
	client   *autoscaler.Client
	recorder *recorder
	events   *collector
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	fake := astest.New(t, "")
	client, err := autoscaler.New(fake.Start(), "", 5*time.Second)
	if err != nil {
		t.Fatalf("autoscaler.New() = %v", err)
	}

	rec, events := &recorder{}, &collector{}
	// Tests never wait: pacing is injected, so a run that would take a minute
	// of wall clock takes none.
	engine := run.New(client, rec, events).
		WithClock(func(time.Duration) {}, func() time.Time { return start })

	return &harness{engine: engine, fake: fake, client: client, recorder: rec, events: events}
}

func mine() domain.Mine {
	return domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 20, BackgroundRate: 60}
}

func scenario() domain.Scenario {
	return domain.Scenario{
		ID: "burst", MineID: "storhall", Duration: time.Hour, JobSeconds: 20,
		PriorityMix: map[domain.Priority]float64{domain.PriorityAssociate: 1, domain.PriorityPick: 3},
		Bursts:      []domain.Burst{{At: 20 * time.Minute, Magnitude: 30, AftershockDecay: 10 * time.Minute}},
		Seed:        7,
	}
}

func simulationSpec() run.Spec {
	return run.Spec{
		Run: domain.Run{
			ID: "run-1", TargetID: "run-1", Mode: domain.ModeSimulation,
			ScenarioID: "burst", TimeCompression: 100000,
			DecisionInterval: 30 * time.Second, SimulatedStart: start,
		},
		Mine:     mine(),
		Scenario: scenario(),
		Settings: json.RawMessage(`{"local_executor_cap": 20, "cloud_executor_cap": 40}`),
	}
}

func TestASimulationRunDrivesTheRealDecisionPath(t *testing.T) {
	h := newHarness(t)

	metrics, err := h.engine.Execute(context.Background(), simulationSpec())
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	if metrics.Cycles == 0 {
		t.Fatal("the run produced no cycles")
	}
	if len(h.fake.Cycles) != metrics.Cycles {
		t.Errorf("the autoscaler saw %d cycles, the run recorded %d",
			len(h.fake.Cycles), metrics.Cycles)
	}
	if metrics.JobsSubmitted == 0 {
		t.Error("no jobs were submitted")
	}
	if metrics.JobsCompleted == 0 {
		t.Error("no jobs were completed, so nothing was ever actually served")
	}
}

// The property that makes the whole app worth having: each run creates its own
// target and removes it, so runs cannot inherit each other's fleets.
func TestARunCreatesItsOwnTargetAndCleansItUp(t *testing.T) {
	h := newHarness(t)

	if _, err := h.engine.Execute(context.Background(), simulationSpec()); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	if h.fake.TargetExists("run-1") {
		t.Error("the run's target outlived the run")
	}
}

func TestAFailedRunStillCleansUpItsTarget(t *testing.T) {
	h := newHarness(t)
	h.fake.FailCyclesAfter = 3

	if _, err := h.engine.Execute(context.Background(), simulationSpec()); err == nil {
		t.Fatal("Execute() = nil error, want the autoscaler's failure surfaced")
	}

	// A leftover target from every failed run would accumulate until somebody
	// noticed a registry full of them.
	if h.fake.TargetExists("run-1") {
		t.Error("a failed run left its target behind")
	}
	if h.recorder.lastStatus() != domain.StatusFailed {
		t.Errorf("status = %q, want failed", h.recorder.lastStatus())
	}
}

func TestTheRunsSettingsReachTheTarget(t *testing.T) {
	h := newHarness(t)
	spec := simulationSpec()
	spec.Settings = json.RawMessage(`{"local_executor_cap": 3, "cloud_executor_cap": 0}`)

	if _, err := h.engine.Execute(context.Background(), spec); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	// Capped at three local and no cloud, no cycle can have planned more.
	for _, cycle := range h.recorder.cycles {
		if cycle.PlanLocal > 3 || cycle.PlanCloud > 0 {
			t.Fatalf("cycle %d planned %d local and %d cloud, beyond the caps the run set",
				cycle.Sequence, cycle.PlanLocal, cycle.PlanCloud)
		}
	}
}

// Constraining capacity has to show up as missed SLAs, or the run is not
// measuring anything.
func TestATightCapProducesBreachesThatAGenerousOneDoesNot(t *testing.T) {
	tight := newHarness(t)
	tightSpec := simulationSpec()
	tightSpec.Settings = json.RawMessage(`{"local_executor_cap": 1, "cloud_executor_cap": 0}`)
	tightMetrics, err := tight.engine.Execute(context.Background(), tightSpec)
	if err != nil {
		t.Fatalf("tight Execute() = %v", err)
	}

	generous := newHarness(t)
	generousSpec := simulationSpec()
	generousSpec.Run.ID, generousSpec.Run.TargetID = "run-2", "run-2"
	generousSpec.Settings = json.RawMessage(`{"local_executor_cap": 200, "cloud_executor_cap": 400}`)
	generousMetrics, err := generous.engine.Execute(context.Background(), generousSpec)
	if err != nil {
		t.Fatalf("generous Execute() = %v", err)
	}

	if tightMetrics.SLABreaches <= generousMetrics.SLABreaches {
		t.Errorf("one executor produced %d breaches and 200 produced %d; the tighter "+
			"cap should be clearly worse", tightMetrics.SLABreaches, generousMetrics.SLABreaches)
	}
	// And the same workload, so the comparison is controlled.
	if tightMetrics.JobsSubmitted != generousMetrics.JobsSubmitted {
		t.Errorf("the two runs replayed different workloads: %d and %d jobs",
			tightMetrics.JobsSubmitted, generousMetrics.JobsSubmitted)
	}
}

func TestTheCostSideIsMeasuredSeparatelyByTier(t *testing.T) {
	h := newHarness(t)

	metrics, err := h.engine.Execute(context.Background(), simulationSpec())
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	if metrics.LocalExecutorSeconds <= 0 {
		t.Error("no local executor time was recorded")
	}
	// Cloud is the tier that is actually billed, so it is kept apart.
	if metrics.CloudExecutorSeconds < 0 {
		t.Errorf("CloudExecutorSeconds = %v", metrics.CloudExecutorSeconds)
	}
	if metrics.PeakLocalExecutors == 0 {
		t.Error("PeakLocalExecutors = 0")
	}
}

func TestEveryCycleIsRecordedWithTheReasoningBehindIt(t *testing.T) {
	h := newHarness(t)

	if _, err := h.engine.Execute(context.Background(), simulationSpec()); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	for _, cycle := range h.recorder.cycles {
		if cycle.Reason == "" {
			t.Fatalf("cycle %d has no reason recorded", cycle.Sequence)
		}
		if cycle.SettingsVersion == 0 {
			t.Fatalf("cycle %d has no settings version, so it cannot be explained later",
				cycle.Sequence)
		}
	}
}

func TestCyclesAreNumberedInOrderAndCarryTheRunsClock(t *testing.T) {
	h := newHarness(t)

	if _, err := h.engine.Execute(context.Background(), simulationSpec()); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	for i, cycle := range h.recorder.cycles {
		if cycle.Sequence != i+1 {
			t.Fatalf("cycle %d has sequence %d", i, cycle.Sequence)
		}
		if cycle.At.Before(start) {
			t.Fatalf("cycle %d is at %v, before the run's start %v", i, cycle.At, start)
		}
	}
}

func TestAWatcherSeesTheRunAsItHappens(t *testing.T) {
	h := newHarness(t)

	if _, err := h.engine.Execute(context.Background(), simulationSpec()); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	if len(h.events.typed("cycle")) == 0 {
		t.Error("no cycle events were published")
	}
	if len(h.events.typed("metrics")) != 1 {
		t.Errorf("%d metrics events, want exactly one at the end", len(h.events.typed("metrics")))
	}
	statuses := h.events.typed("status")
	if len(statuses) < 2 {
		t.Fatalf("%d status events, want at least running and completed", len(statuses))
	}
	if statuses[0].Status != domain.StatusRunning {
		t.Errorf("first status = %q, want running", statuses[0].Status)
	}
	if statuses[len(statuses)-1].Status != domain.StatusCompleted {
		t.Errorf("last status = %q, want completed", statuses[len(statuses)-1].Status)
	}
}

// A stopped run keeps what it recorded. Discarding it would throw away the
// only evidence of whatever prompted somebody to stop it.
func TestACancelledRunIsMarkedCancelledNotFailed(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())

	cancelled := false
	h.engine.WithClock(func(time.Duration) {
		if !cancelled {
			cancelled = true
			cancel()
		}
	}, func() time.Time { return start })

	if _, err := h.engine.Execute(ctx, simulationSpec()); err == nil {
		t.Fatal("Execute() = nil error for a cancelled run")
	}
	if h.recorder.lastStatus() != domain.StatusCancelled {
		t.Errorf("status = %q, want cancelled", h.recorder.lastStatus())
	}
	if len(h.recorder.cycles) == 0 {
		t.Error("a cancelled run discarded what it had already recorded")
	}
}

func TestARunThatCannotBeRecordedFails(t *testing.T) {
	h := newHarness(t)
	h.recorder.failSaveCycleAt = 2

	if _, err := h.engine.Execute(context.Background(), simulationSpec()); err == nil {
		t.Fatal("Execute() = nil error when cycles could not be saved")
	}
	if h.recorder.lastStatus() != domain.StatusFailed {
		t.Errorf("status = %q, want failed", h.recorder.lastStatus())
	}
}

func TestAnInvalidRunIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	h := newHarness(t)
	spec := simulationSpec()
	spec.Run.DecisionInterval = 0

	if _, err := h.engine.Execute(context.Background(), spec); err == nil {
		t.Fatal("Execute() = nil error for a run with no decision interval")
	}
	if h.fake.TargetExists("run-1") {
		t.Error("a target was created for a run that was never valid")
	}
}

// A live run watches; it must not drive. Two controllers issuing cycles for
// one target would be two schedulers fighting over one fleet.
func TestALiveRunObservesWithoutDrivingTheTarget(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())

	if err := createLiveTarget(h); err != nil {
		t.Fatalf("preparing the live target: %v", err)
	}
	h.fake.LiveDecision("storhall", map[string]any{
		"at":     start.Format(time.RFC3339Nano),
		"action": "scale_up",
		"plan":   map[string]any{"local_executors": 6, "cloud_executors": 0},
		"reason": "P100 breaches in 20s",
	})

	observations := 0
	h.engine.WithClock(func(time.Duration) {
		observations++
		if observations >= 3 {
			cancel()
		}
	}, func() time.Time { return start })

	spec := run.Spec{Run: domain.Run{
		ID: "watch-1", TargetID: "storhall", Mode: domain.ModeLive,
		DecisionInterval: time.Second,
	}}
	if _, err := h.engine.Execute(ctx, spec); err == nil {
		t.Fatal("Execute() = nil error, want the cancellation reported")
	}

	if len(h.fake.Cycles) != 0 {
		t.Errorf("a live run issued %d cycles; it must only observe", len(h.fake.Cycles))
	}
	if len(h.recorder.cycles) != 1 {
		t.Fatalf("%d cycles recorded, want the one decision the target reported",
			len(h.recorder.cycles))
	}
	if h.recorder.cycles[0].Reason == "" {
		t.Error("the observed decision was recorded without its reasoning")
	}
}

func TestALiveRunRecordsEachDecisionOnlyOnce(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())

	if err := createLiveTarget(h); err != nil {
		t.Fatalf("preparing the live target: %v", err)
	}
	h.fake.LiveDecision("storhall", map[string]any{
		"at": start.Format(time.RFC3339Nano), "action": "maintain", "reason": "holding",
	})

	polls := 0
	h.engine.WithClock(func(time.Duration) {
		polls++
		if polls >= 5 {
			cancel()
		}
	}, func() time.Time { return start })

	spec := run.Spec{Run: domain.Run{
		ID: "watch-2", TargetID: "storhall", Mode: domain.ModeLive,
		DecisionInterval: time.Second,
	}}
	_, _ = h.engine.Execute(ctx, spec)

	if len(h.recorder.cycles) != 1 {
		t.Errorf("%d cycles recorded from one unchanging decision, want 1",
			len(h.recorder.cycles))
	}
}

func TestAnUnknownRunModeIsRefused(t *testing.T) {
	h := newHarness(t)
	spec := simulationSpec()
	spec.Run.Mode = "hybrid"

	if _, err := h.engine.Execute(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "hybrid") {
		t.Errorf("Execute() = %v, want the unknown mode named", err)
	}
}

// createLiveTarget registers a target the way an operator would, so a live run
// has something real to watch.
func createLiveTarget(h *harness) error {
	_, err := h.client.CreateTarget(context.Background(), autoscaler.Target{
		ID: "storhall", Kind: "kubernetes", Mode: "autonomous",
	}, nil)
	return err
}

// The virtual mine a run replayed is part of what it recorded. A 3D view built
// by regenerating it later would show whatever the scenario had been edited
// into since, not what the autoscaler was actually deciding against.
func TestASimulationRunRecordsTheMineItReplayed(t *testing.T) {
	h := newHarness(t)

	if _, err := h.engine.Execute(context.Background(), simulationSpec()); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	if len(h.recorder.layouts) != 1 {
		t.Fatalf("the layout was saved %d times, want once", len(h.recorder.layouts))
	}
	if got := len(h.recorder.layouts[0].Sensors); got != mine().Sensors {
		t.Errorf("the recorded array has %d sensors, the mine has %d", got, mine().Sensors)
	}
	if len(h.recorder.seismicWrites) == 0 || len(h.recorder.seismicWrites[0]) == 0 {
		t.Fatal("no seismic events were recorded before the run began")
	}
	for _, event := range h.recorder.seismicWrites[0] {
		if event.LocatedAt != nil || event.ProcessedAt != nil {
			t.Fatalf("event %d was recorded as located before anything ran", event.Sequence)
		}
	}
}

// Every event's picks are jobs, and a completed run has completed every job,
// so every event is processed by the end. The last interval matters most here:
// the loop ends on the interval that drains the queue, and a location it
// produced but never recorded would leave an event unlocated forever.
func TestByTheEndOfARunEveryEventHasBeenProcessed(t *testing.T) {
	h := newHarness(t)

	metrics, err := h.engine.Execute(context.Background(), simulationSpec())
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	picks := 0
	for _, event := range h.recorder.seismic {
		picks += len(event.Sensors)
		if event.ProcessedAt == nil {
			t.Fatalf("event %d (%d picks) was never processed", event.Sequence, len(event.Sensors))
		}
		if len(event.Sensors) >= 4 && event.LocatedAt == nil {
			t.Fatalf("event %d had %d picks processed and no location", event.Sequence, len(event.Sensors))
		}
		if event.LocatedAt != nil && (*event.LocatedAt < event.Origin || *event.ProcessedAt < *event.LocatedAt) {
			t.Fatalf("event %d: origin %v, located %v, processed %v — out of order",
				event.Sequence, event.Origin, *event.LocatedAt, *event.ProcessedAt)
		}
	}
	if picks != metrics.JobsSubmitted {
		t.Errorf("events account for %d picks, the run submitted %d jobs", picks, metrics.JobsSubmitted)
	}
}

// This is the link between the mine and the autoscaler: capacity is what
// decides how long an operator waits for a location.
func TestLessCapacityMeansLongerToLocateAnEvent(t *testing.T) {
	delays := func(settings string, id string) float64 {
		h := newHarness(t)
		spec := simulationSpec()
		spec.Run.ID, spec.Run.TargetID = id, id
		spec.Settings = json.RawMessage(settings)
		if _, err := h.engine.Execute(context.Background(), spec); err != nil {
			t.Fatalf("Execute() = %v", err)
		}
		total, n := 0.0, 0
		for _, event := range h.recorder.seismic {
			if event.LocatedAt != nil {
				total += (*event.LocatedAt - event.Origin).Seconds()
				n++
			}
		}
		if n == 0 {
			t.Fatalf("%s: nothing was located", id)
		}
		return total / float64(n)
	}

	tight := delays(`{"local_executor_cap": 2, "cloud_executor_cap": 0}`, "tight")
	generous := delays(`{"local_executor_cap": 200, "cloud_executor_cap": 400}`, "generous")
	if tight <= generous {
		t.Errorf("two executors located events in %.0f s on average and two hundred in %.0f s",
			tight, generous)
	}
}

// Nothing in a plain run changes a priority after submission, so the two
// counts of the queue agree level by level. Once something does, this is the
// test that should start needing an exception, and the charts are what show it.
func TestEachCycleCountsTheQueueBySubmittedPriorityToo(t *testing.T) {
	h := newHarness(t)

	if _, err := h.engine.Execute(context.Background(), simulationSpec()); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	waiting := false
	for _, cycle := range h.recorder.cycles {
		if cycle.SubmittedDepths == nil {
			t.Fatalf("cycle %d did not record the queue by submitted priority", cycle.Sequence)
		}
		for priority, level := range cycle.Queues {
			if cycle.SubmittedDepths[priority] != level.Depth {
				t.Fatalf("cycle %d: P%d holds %d now but %d were submitted at it, and nothing "+
					"reprioritises", cycle.Sequence, priority, level.Depth, cycle.SubmittedDepths[priority])
			}
			waiting = waiting || level.Depth > 0
		}
	}
	if !waiting {
		t.Error("no cycle had anything waiting, so this proves nothing")
	}
}
