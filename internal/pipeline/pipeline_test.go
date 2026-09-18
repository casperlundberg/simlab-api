package pipeline_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/pipeline"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// The measured pipeline, from platform-experiments/docs/workflow-inference.md.
func plan(trigger pipeline.Trigger) pipeline.Plan {
	return pipeline.Plan{
		Trigger:   trigger,
		Window:    30 * time.Minute,
		Associate: pipeline.Sweep{Priority: domain.PriorityAssociate, Seconds: 1.126, PerLocate: 0.080},
		Locate:    pipeline.Stage{Priority: domain.PriorityLocate, Seconds: 47.7},
	}
}

// oneEvent is an event at the origin whose picks are jobs 0..picks-1.
func oneEvent(picks int) []pipeline.Event {
	return []pipeline.Event{{Origin: 0, FirstJob: 0, Picks: picks}}
}

func newPipeline(t *testing.T, p pipeline.Plan, events []pipeline.Event) *pipeline.Pipeline {
	t.Helper()
	return pipeline.New(p, events, domain.JobID(1000), rand.New(rand.NewPCG(1, 2)))
}

// always sweeps whenever anything is ready, which isolates what a sweep does
// from when one happens.
type always struct{}

func (always) Name() string { return "always" }
func (always) Decide(_ time.Duration, o pipeline.Observation) pipeline.Decision {
	if o.PicksReady == 0 {
		return pipeline.Decision{Reason: "nothing ready"}
	}
	return pipeline.Decision{Associate: true, Reason: "always"}
}

// advance is Advance with the error checked, which every test but the two
// about refusal wants.
func advance(t *testing.T, p *pipeline.Pipeline, at time.Duration,
	finished []domain.JobID, valued func(int) bool) pipeline.Step {
	t.Helper()
	step, err := p.Advance(at, finished, valued)
	if err != nil {
		t.Fatalf("Advance(%s, %v): %v", at, finished, err)
	}
	return step
}

func picks(first domain.JobID, n int) []domain.JobID {
	out := make([]domain.JobID, n)
	for i := range out {
		out[i] = first + domain.JobID(i)
	}
	return out
}

func TestASweepIsNotRunBeforeAnyPickHasBeenProcessed(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	step := advance(t, p, time.Second, nil, nil)

	if len(step.Submit) != 0 {
		t.Errorf("submitted %d jobs with no pick processed: %+v", len(step.Submit), step.Submit)
	}
}

// Four picks is what a location needs: three coordinates and an origin time.
func TestASweepWaitsUntilAnEventHasEnoughPicksToLocate(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	step := advance(t, p, time.Second, picks(0, 3), nil)

	if len(step.Submit) != 0 {
		t.Fatalf("swept with only 3 picks processed, one short of a location: %+v", step.Submit)
	}
}

func TestASweepIsSubmittedOnceAnEventCanBeLocated(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	step := advance(t, p, time.Second, picks(0, 4), nil)

	if len(step.Submit) != 1 || step.Submit[0].Stage != workload.StageAssociate {
		t.Fatalf("Submit = %+v, want one associate", step.Submit)
	}
	if got := step.Submit[0].Priority; got != domain.PriorityAssociate {
		t.Errorf("associate priority = %d, want %d", got, domain.PriorityAssociate)
	}
}

// The sweep's cost follows what it emits — 1.126 s plus 0.080 s per locate,
// fitted over 241,742 real sweeps. A flat cost would make a policy that sweeps
// rarely, and so emits more per sweep, look free.
func TestASweepCostsMoreWhenItHasMoreToLocate(t *testing.T) {
	events := make([]pipeline.Event, 10)
	for i := range events {
		events[i] = pipeline.Event{Origin: 0, FirstJob: domain.JobID(i * 4), Picks: 4}
	}
	one := newPipeline(t, plan(always{}), oneEvent(4))
	ten := newPipeline(t, plan(always{}), events)

	small := advance(t, one, time.Second, picks(0, 4), nil).Submit[0]
	large := advance(t, ten, time.Second, picks(0, 40), nil).Submit[0]

	if want := 1.126 + 0.080; !near(small.Seconds, want) {
		t.Errorf("a sweep emitting 1 locate costs %.4fs, want %.4fs", small.Seconds, want)
	}
	if want := 1.126 + 0.080*10; !near(large.Seconds, want) {
		t.Errorf("a sweep emitting 10 locates costs %.4fs, want %.4fs", large.Seconds, want)
	}
}

// Every one of the 555,153 locates in the extract was submitted inside a
// sweep's own execution. So a locate appears when its sweep completes, not when
// the sweep is decided.
func TestLocatesAreSubmittedByTheSweepThatFinishes(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	sweep := advance(t, p, time.Second, picks(0, 4), nil).Submit[0]
	before := advance(t, p, 2*time.Second, nil, nil)
	after := advance(t, p, 3*time.Second, []domain.JobID{sweep.ID}, nil)

	if got := stage(before.Submit, workload.StageLocate); got != 0 {
		t.Errorf("%d locates appeared before the sweep finished", got)
	}
	if got := stage(after.Submit, workload.StageLocate); got != 1 {
		t.Fatalf("the finished sweep submitted %d locates, want 1", got)
	}
}

func TestAnEventIsLocatedWhenItsLocateFinishesAndNotBefore(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	sweep := advance(t, p, time.Second, picks(0, 4), nil).Submit[0]
	emitted := advance(t, p, 2*time.Second, []domain.JobID{sweep.ID}, nil)
	locate := emitted.Submit[0]

	if len(emitted.Located) != 0 {
		t.Errorf("located before the locate ran: %+v", emitted.Located)
	}
	done := advance(t, p, 3*time.Second, []domain.JobID{locate.ID}, nil)
	if len(done.Located) != 1 || done.Located[0] != 0 {
		t.Errorf("Located = %+v, want event 0", done.Located)
	}
}

// The sweep groups what is ready; an event with nothing new since its last
// locate is not worth locating again. Without this the pipeline would re-locate
// every event in the window on every sweep, forever.
func TestAnEventIsNotLocatedAgainUntilMorePicksJoinIt(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	first := advance(t, p, time.Second, picks(0, 4), nil).Submit[0]
	advance(t, p, 2*time.Second, []domain.JobID{first.ID}, nil)

	idle := advance(t, p, 3*time.Second, nil, nil)

	if len(idle.Submit) != 0 {
		t.Errorf("swept again with nothing new: %+v", idle.Submit)
	}
}

func TestAnEventIsLocatedAgainAsLaterPicksJoinIt(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	first := advance(t, p, time.Second, picks(0, 4), nil).Submit[0]
	advance(t, p, 2*time.Second, []domain.JobID{first.ID}, nil)

	again := advance(t, p, 3*time.Second, picks(4, 2), nil)

	if len(again.Submit) != 1 || again.Submit[0].Stage != workload.StageAssociate {
		t.Fatalf("Submit = %+v, want a second sweep once more picks joined", again.Submit)
	}
}

// A 30-minute sliding window is what the real associate reads. An event that
// has fallen out of it is finished with, however few of its picks arrived.
func TestAnEventOlderThanTheWindowIsNoLongerSwept(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	step := advance(t, p, 31*time.Minute, picks(0, 4), nil)

	if len(step.Submit) != 0 {
		t.Errorf("swept an event 31 minutes past a 30-minute window: %+v", step.Submit)
	}
}

func TestTheTriggerSeesWhatIsReadyAndWhatIsStillInFlight(t *testing.T) {
	var seen pipeline.Observation
	spy := spyTrigger{onDecide: func(o pipeline.Observation) { seen = o }}
	p := newPipeline(t, plan(&spy), oneEvent(10))

	advance(t, p, time.Second, picks(0, 4), nil)

	if seen.PicksReady != 4 {
		t.Errorf("PicksReady = %d, want the 4 processed picks", seen.PicksReady)
	}
	if seen.PicksPending != 6 {
		t.Errorf("PicksPending = %d, want the 6 still in flight", seen.PicksPending)
	}
}

// JustInTime waits only for the picks the mine values, so the pipeline has to
// tell it which pending picks those are. The mine's own estimate, never truth.
func TestTheTriggerIsToldHowManyPendingPicksTheMineValues(t *testing.T) {
	var seen pipeline.Observation
	spy := spyTrigger{onDecide: func(o pipeline.Observation) { seen = o }}
	events := []pipeline.Event{
		{Origin: 0, FirstJob: 0, Picks: 10},
		{Origin: 0, FirstJob: 10, Picks: 10},
	}
	p := newPipeline(t, plan(&spy), events)

	// Four of event 0's picks are done; the mine values event 1, all of whose
	// picks are still in flight.
	advance(t, p, time.Second, picks(0, 4), func(event int) bool { return event == 1 })

	if seen.HighValuePending != 10 {
		t.Errorf("HighValuePending = %d, want event 1's 10 pending picks", seen.HighValuePending)
	}
}

// An event whose origin has not been reached has submitted no picks, so its
// picks are not pending and cannot be waited for.
func TestPicksAreOnlyPendingOnceTheirEventHasHappened(t *testing.T) {
	var seen pipeline.Observation
	spy := spyTrigger{onDecide: func(o pipeline.Observation) { seen = o }}
	events := []pipeline.Event{
		{Origin: 0, FirstJob: 0, Picks: 6},
		{Origin: time.Hour, FirstJob: 6, Picks: 4},
	}
	p := newPipeline(t, plan(&spy), events)

	advance(t, p, time.Minute, picks(0, 4), nil)

	if seen.PicksPending != 2 {
		t.Errorf("PicksPending = %d, want only the 2 outstanding from the event that happened", seen.PicksPending)
	}
}

func TestEveryJobTheWorkflowSubmitsHasItsOwnIdentity(t *testing.T) {
	events := make([]pipeline.Event, 20)
	for i := range events {
		events[i] = pipeline.Event{
			Origin: time.Duration(i) * time.Second, FirstJob: domain.JobID(i * 6), Picks: 6,
		}
	}
	p := newPipeline(t, plan(always{}), events)

	seen := map[domain.JobID]bool{}
	pending := picks(0, 120)
	for cycle := 1; cycle < 60; cycle++ {
		step := advance(t, p, time.Duration(cycle)*10*time.Second, pending, nil)
		pending = nil
		for _, job := range step.Submit {
			if seen[job.ID] {
				t.Fatalf("job id %d was issued twice", job.ID)
			}
			if job.ID < 1000 {
				t.Fatalf("job id %d collides with the generated picks", job.ID)
			}
			seen[job.ID] = true
			pending = append(pending, job.ID)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the workflow submitted nothing at all")
	}
}

// A job reported finished twice, or one nobody submitted, means the run and the
// workflow disagree about what happened — which would quietly corrupt every
// count that follows.
func TestAJobNobodySubmittedIsRefused(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(4))

	if _, err := p.Advance(time.Second, []domain.JobID{9999}, nil); err == nil {
		t.Error("a job the workflow never heard of was accepted as finished")
	}
}

func TestAJobReportedFinishedTwiceIsRefused(t *testing.T) {
	p := newPipeline(t, plan(always{}), oneEvent(8))

	if _, err := p.Advance(time.Second, picks(0, 4), nil); err != nil {
		t.Fatalf("first report: %v", err)
	}
	if _, err := p.Advance(2*time.Second, picks(0, 1), nil); err == nil {
		t.Error("the same pick was accepted as finished twice")
	}
}

type spyTrigger struct {
	onDecide func(pipeline.Observation)
}

func (s *spyTrigger) Name() string { return "spy" }

func (s *spyTrigger) Decide(_ time.Duration, o pipeline.Observation) pipeline.Decision {
	s.onDecide(o)
	if o.PicksReady == 0 {
		return pipeline.Decision{Reason: "nothing ready"}
	}
	return pipeline.Decision{Associate: true, Reason: "spy"}
}

func stage(jobs []workload.Job, want workload.Stage) int {
	n := 0
	for _, job := range jobs {
		if job.Stage == want {
			n++
		}
	}
	return n
}

func near(got, want float64) bool { return got-want < 1e-6 && want-got < 1e-6 }
