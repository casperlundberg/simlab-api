package run_test

import (
	"context"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/run"
)

// A run of the mine's own workflow is the point of the whole exercise: the
// three stages really run, one feeding the next, against the real autoscaler.

func pipelineSpec(sweep domain.TriggerSpec) run.Spec {
	spec := simulationSpec()
	plan := domain.DefaultPipeline()
	plan.Sweep = sweep
	scenario := spec.Scenario
	scenario.Pipeline = &plan
	spec.Scenario = scenario
	return spec
}

func TestARunOfTheWorkflowSweepsAndLocates(t *testing.T) {
	h := newHarness(t)

	metrics, err := h.engine.Execute(context.Background(),
		pipelineSpec(domain.TriggerSpec{Kind: domain.TriggerFixed, Every: 10 * time.Second}))

	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if metrics.Sweeps == 0 {
		t.Error("the workflow never ran an associate sweep")
	}
	if metrics.Locates == 0 {
		t.Error("the workflow never submitted a locate")
	}
	// Every sweep submits at least one locate, or it would not have run.
	if metrics.Locates < metrics.Sweeps {
		t.Errorf("%d sweeps produced only %d locates", metrics.Sweeps, metrics.Locates)
	}
}

// The stages are work. A run that did not queue them would be measuring a
// pipeline whose associate and locate cost nothing.
func TestTheWorkflowsOwnStagesAreQueuedAsWork(t *testing.T) {
	h := newHarness(t)
	plain := simulationSpec()

	withWorkflow, err := h.engine.Execute(context.Background(),
		pipelineSpec(domain.TriggerSpec{Kind: domain.TriggerFixed, Every: 10 * time.Second}))
	if err != nil {
		t.Fatalf("Execute() with a workflow = %v", err)
	}

	h2 := newHarness(t)
	without, err := h2.engine.Execute(context.Background(), plain)
	if err != nil {
		t.Fatalf("Execute() without = %v", err)
	}

	extra := withWorkflow.Sweeps + withWorkflow.Locates
	if got := withWorkflow.JobsSubmitted - without.JobsSubmitted; got != extra {
		t.Errorf("the workflow added %d jobs, want its %d sweeps and locates", got, extra)
	}
}

// An event is located when its locate finishes. Having the picks is not having
// the answer, and the gap is what an operator waits.
func TestWithAWorkflowAnEventIsLocatedOnlyAfterItsLocateRuns(t *testing.T) {
	h := newHarness(t)

	if _, err := h.engine.Execute(context.Background(),
		pipelineSpec(domain.TriggerSpec{Kind: domain.TriggerFixed, Every: 10 * time.Second})); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	located := 0
	for _, event := range h.recorder.seismic {
		if event.LocatedAt == nil {
			continue
		}
		located++
		fourth := nthProcessed(event, 4)
		if fourth == nil {
			t.Errorf("event %d is located but fewer than four of its picks were processed", event.Sequence)
			continue
		}
		if *event.LocatedAt < *fourth {
			t.Errorf("event %d was located at %s, before its fourth pick was processed at %s",
				event.Sequence, *event.LocatedAt, *fourth)
		}
	}
	if located == 0 {
		t.Fatal("no event was located at all")
	}
}

// Sweeping rarely does not make the associate stage cheaper: each sweep emits
// more, and costs more. A trigger arm that looked free would be worthless as a
// comparison.
func TestSweepingLessOftenMakesFewerAndLargerSweeps(t *testing.T) {
	h := newHarness(t)
	often, err := h.engine.Execute(context.Background(),
		pipelineSpec(domain.TriggerSpec{Kind: domain.TriggerFixed, Every: 10 * time.Second}))
	if err != nil {
		t.Fatalf("Execute() at 10s = %v", err)
	}

	h2 := newHarness(t)
	rarely, err := h2.engine.Execute(context.Background(),
		pipelineSpec(domain.TriggerSpec{Kind: domain.TriggerFixed, Every: 5 * time.Minute}))
	if err != nil {
		t.Fatalf("Execute() at 5m = %v", err)
	}

	if rarely.Sweeps >= often.Sweeps {
		t.Errorf("sweeping every 5m ran %d sweeps, not fewer than %d at 10s",
			rarely.Sweeps, often.Sweeps)
	}
	perSweep := func(m domain.Metrics) float64 {
		if m.Sweeps == 0 {
			return 0
		}
		return float64(m.Locates) / float64(m.Sweeps)
	}
	if perSweep(rarely) <= perSweep(often) {
		t.Errorf("sweeping every 5m emitted %.1f locates a sweep, not more than %.1f at 10s",
			perSweep(rarely), perSweep(often))
	}
}

// nthProcessed is when the nth of an event's picks was processed.
func nthProcessed(event domain.SeismicEvent, n int) *time.Duration {
	var times []time.Duration
	for _, at := range event.PickProcessedAt {
		if at != nil {
			times = append(times, *at)
		}
	}
	if len(times) < n {
		return nil
	}
	for i := 1; i < len(times); i++ {
		for j := i; j > 0 && times[j] < times[j-1]; j-- {
			times[j], times[j-1] = times[j-1], times[j]
		}
	}
	return &times[n-1]
}
