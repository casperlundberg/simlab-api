package pipeline

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/seismic"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// drainWindow is how far back the observed pick-completion rate looks. Long
// enough not to be noise, short enough that a burst shows up while it is still
// happening.
const drainWindow = 5 * time.Minute

// Stage is one step of the workflow: what it is worth, and what it costs.
type Stage struct {
	Priority domain.Priority

	// Seconds is the nominal execution time. Actual durations are spread
	// around it, because a fleet sized for the mean of a long-tailed
	// distribution is undersized for most of the work in the tail.
	Seconds float64
}

// Sweep is the associate stage. It has a cost model rather than a duration
// because a sweep's cost was measured to follow what it emits: 1.126 s plus
// 0.080 s per locate, fitted over 241,742 real sweeps.
//
// This is what stops a policy that sweeps rarely looking free. Sweeping half as
// often does not halve the associate work — it makes each sweep emit twice as
// much, and cost accordingly.
type Sweep struct {
	Priority domain.Priority

	// Seconds is what an empty sweep costs.
	Seconds float64

	// PerLocate is added for each locate the sweep submits.
	PerLocate float64
}

// Cost is what a sweep emitting this many locates occupies an executor for.
func (s Sweep) Cost(locates int) float64 {
	return s.Seconds + s.PerLocate*float64(locates)
}

// Plan is how a mine runs its pipeline: when to sweep, how far back a sweep
// looks, and what each stage is worth and costs.
type Plan struct {
	Trigger Trigger

	// Window is how far back a sweep groups picks. The real associate reads a
	// 30-minute sliding window, so an event older than that is finished with
	// however few of its picks ever arrived.
	Window time.Duration

	Associate Sweep
	Locate    Stage
}

// Event is one seismic event as the workflow sees it: when it happened, and
// which generated jobs are its picks.
//
// Picks are generated with the workload rather than submitted here, because
// they are what a scenario's seed has always produced. The workflow observes
// them finishing and submits the two stages that follow.
type Event struct {
	Origin   time.Duration
	FirstJob domain.JobID
	Picks    int
}

// Step is what the workflow did in one interval.
type Step struct {
	// Submit is the work the mine is handing to the orchestrator: the locates
	// that finishing sweeps produced, and a sweep if one was decided.
	Submit []workload.Job

	// Decision is what the trigger decided and why, whether or not it swept.
	// The reason is recorded: a sweep nobody can explain is not decision
	// support.
	Decision Decision

	// Located are the events whose locate finished in this interval. An event
	// is located when its locate *completes* — the work is not done when it is
	// queued.
	Located []int

	// Picks are the finished jobs that were picks, which is what the mine's
	// catalogue records as processed. Separated here because the workflow is
	// what knows which stage a job belonged to.
	Picks []domain.JobID
}

// issued is a job this workflow submitted, and what it is for.
type issued struct {
	stage workload.Stage

	// events is what a finishing sweep will ask to be located, or the single
	// event a locate is for.
	events []int
}

// state is what the workflow knows about one event.
type state struct {
	// processed is how many of the event's picks have finished.
	processed int

	// unswept is how many processed picks no sweep has grouped yet. A sweep
	// takes them, so an event with nothing new is not located again — without
	// which every event in the window would be re-located on every sweep,
	// forever.
	unswept int

	located bool
}

// Pipeline is the mine's workflow in flight.
//
// It owns the dependency between the stages — a locate needs an associate, an
// associate needs picks — and nothing else. The queue is told what to run and
// knows nothing of the order; the autoscaler is told how deep the queue is and
// knows nothing of either. That is what keeps dependency support out of the
// orchestrator's capability set, which matters because Kueue, ColonyOS and
// Celery disagree about dependencies far more than about priority.
//
// Not safe for concurrent use; a run advances it from one goroutine.
type Pipeline struct {
	plan   Plan
	events []Event
	state  []state
	random *rand.Rand

	// firstJob indexes events by their first pick, so a finishing pick is
	// attributed without scanning every event.
	firstJob []domain.JobID

	next   domain.JobID
	issued map[domain.JobID]issued
	done   map[domain.JobID]bool

	lastSweep time.Duration
	swept     bool

	// completions are recent pick finishes, which is where the drain rate a
	// trigger predicts from comes from. Measured, never assumed: it already
	// reflects whatever the autoscaler has done about capacity.
	completions []time.Duration
}

// New starts a workflow over a set of events. nextJob is the first identity it
// may issue, which must be past every generated job, and random spreads the
// durations of the work it submits.
func New(plan Plan, events []Event, nextJob domain.JobID, random *rand.Rand) *Pipeline {
	ordered := append([]Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].FirstJob < ordered[j].FirstJob })

	firstJob := make([]domain.JobID, len(ordered))
	for i, event := range ordered {
		firstJob[i] = event.FirstJob
	}
	return &Pipeline{
		plan:     plan,
		events:   ordered,
		state:    make([]state, len(ordered)),
		random:   random,
		firstJob: firstJob,
		next:     nextJob,
		issued:   map[domain.JobID]issued{},
		done:     map[domain.JobID]bool{},
	}
}

// Advance moves the workflow to an instant: it records what finished, decides
// whether to sweep, and returns the work to submit.
//
// valued reports whether the mine currently believes an event matters. It is
// the mine's own estimate — never ground truth — and nil means it values
// nothing in particular.
func (p *Pipeline) Advance(at time.Duration, finished []domain.JobID,
	valued func(event int) bool) (Step, error) {
	step := Step{Located: []int{}}

	for _, id := range finished {
		if p.done[id] {
			return Step{}, fmt.Errorf("job %d was reported finished twice", id)
		}
		p.done[id] = true

		switch job, ok := p.issued[id]; {
		case ok && job.stage == workload.StageAssociate:
			// A sweep submits its locates while it runs: every one of the
			// 555,153 locates in the extract was submitted inside a sweep's
			// own execution.
			step.Submit = append(step.Submit, p.locates(at, job.events)...)
		case ok && job.stage == workload.StageLocate:
			for _, event := range job.events {
				if !p.state[event].located {
					p.state[event].located = true
					step.Located = append(step.Located, event)
				}
			}
		default:
			event, err := p.pick(id)
			if err != nil {
				return Step{}, err
			}
			p.state[event].processed++
			p.state[event].unswept++
			p.completions = append(p.completions, at)
			step.Picks = append(step.Picks, id)
		}
	}

	step.Decision = p.plan.Trigger.Decide(at, p.observe(at, valued))
	if step.Decision.Associate {
		if sweep, ok := p.sweep(at); ok {
			step.Submit = append(step.Submit, sweep)
		}
	}
	return step, nil
}

// pick attributes a finished job to the event whose pick it is.
func (p *Pipeline) pick(id domain.JobID) (int, error) {
	i := sort.Search(len(p.firstJob), func(i int) bool { return p.firstJob[i] > id }) - 1
	if i < 0 || int(id) >= int(p.events[i].FirstJob)+p.events[i].Picks {
		return 0, fmt.Errorf("job %d was reported finished, but it is neither a pick of any "+
			"event nor work this pipeline submitted", id)
	}
	return i, nil
}

// sweep decides what an associate sweep would group now, and submits it.
//
// The candidates are taken here rather than when the sweep finishes, because
// the sweep's cost depends on how much it has to do — and because a second
// sweep decided while the first is still running must not emit the same
// locates again.
func (p *Pipeline) sweep(at time.Duration) (workload.Job, bool) {
	candidates := p.candidates(at)
	if len(candidates) == 0 {
		// The trigger wanted a sweep, but there is nothing a sweep could
		// group. Running it would cost an executor and produce nothing.
		return workload.Job{}, false
	}
	for _, event := range candidates {
		p.state[event].unswept = 0
	}
	p.lastSweep, p.swept = at, true

	return p.submit(workload.Job{
		SubmittedAt: at,
		Priority:    p.plan.Associate.Priority,
		Seconds:     p.plan.Associate.Cost(len(candidates)),
		Event:       workload.NoEvent,
		Stage:       workload.StageAssociate,
	}, candidates), true
}

// candidates are the events a sweep would locate: in the window, with enough
// picks to solve a location, and with something new since their last locate.
func (p *Pipeline) candidates(at time.Duration) []int {
	var out []int
	for i, event := range p.events {
		if !p.inWindow(at, event) {
			continue
		}
		if p.state[i].unswept > 0 && p.state[i].processed >= seismic.MinimumPicks {
			out = append(out, i)
		}
	}
	return out
}

func (p *Pipeline) inWindow(at time.Duration, event Event) bool {
	return event.Origin <= at && at-event.Origin <= p.plan.Window
}

// locates is the work a finishing sweep submits: one locate per event it
// grouped.
func (p *Pipeline) locates(at time.Duration, events []int) []workload.Job {
	out := make([]workload.Job, 0, len(events))
	for _, event := range events {
		out = append(out, p.submit(workload.Job{
			SubmittedAt: at,
			Priority:    p.plan.Locate.Priority,
			Seconds:     spread(p.plan.Locate.Seconds, p.random),
			Event:       event,
			Stage:       workload.StageLocate,
		}, []int{event}))
	}
	return out
}

// submit gives a job its identity and records what it is for.
func (p *Pipeline) submit(job workload.Job, events []int) workload.Job {
	job.ID = p.next
	p.next++
	p.issued[job.ID] = issued{stage: job.Stage, events: events}
	return job
}

// observe is what the mine can see about its own pipeline right now.
func (p *Pipeline) observe(at time.Duration, valued func(int) bool) Observation {
	o := Observation{SinceLast: at - p.lastSweep}
	if !p.swept {
		// Nothing has swept yet, so the wait is the whole run so far. A
		// trigger that has never fired should not be told it just did.
		o.SinceLast = at
	}

	for i, event := range p.events {
		if event.Origin > at {
			// Not yet happened: its picks are not in flight and cannot be
			// waited for.
			continue
		}
		pending := event.Picks - p.state[i].processed
		o.PicksPending += pending
		if valued != nil && valued(i) {
			o.HighValuePending += pending
		}
		if p.inWindow(at, event) {
			o.PicksReady += p.state[i].unswept
		}
	}
	o.DrainPerSecond = p.drain(at)
	return o
}

// drain is the observed rate picks are completing at, over the recent window.
func (p *Pipeline) drain(at time.Duration) float64 {
	cutoff := at - drainWindow
	drop := 0
	for drop < len(p.completions) && p.completions[drop] < cutoff {
		drop++
	}
	p.completions = p.completions[drop:]

	over := min(at, drainWindow).Seconds()
	if over <= 0 || len(p.completions) == 0 {
		return 0
	}
	return float64(len(p.completions)) / over
}

// spread draws a duration around a nominal figure, log-normally: processing
// time is bounded below by zero and has a long right tail, and the jobs in that
// tail are the ones that hold an executor while the queue behind them ages.
func spread(nominal float64, random *rand.Rand) float64 {
	const sigma = 0.35
	// Shifting by -sigma^2/2 keeps the mean at the nominal value rather than
	// letting the tail drag it upward.
	return nominal * math.Exp(sigma*random.NormFloat64()-sigma*sigma/2)
}
