package intent

import (
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
	"github.com/casperlundberg/simlab-api/internal/orchestrator"
	"github.com/casperlundberg/simlab-api/internal/seismic"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// What an event was judged from.
const (
	BasisLocation = "location"
	BasisSensors  = "sensors"
	BasisTruth    = "truth"

	// basisOff is intent switched off: every job keeps its submitted priority.
	basisOff = "off"
)

// Sighting is what intent knows about an event at a moment: where it is taken
// to be, how large, and how far out that may be.
type Sighting struct {
	At          domain.Point
	Magnitude   float64
	Uncertainty float64
	Basis       string
}

// Planner is the mine's intent: every cycle it judges each event with work
// outstanding, and says which queued jobs should move.
//
// It keeps its own record of what it has asked for, as a real mine would have
// to: an orchestrator reports what finished, not what priority everything
// holds. So an update is sent only when what is wanted for a job changes, and
// a job that is already running when it does simply finishes.
//
// Not safe for concurrent use; a run plans from one goroutine.
type Planner struct {
	jobs      []workload.Job
	events    []eventWork
	entities  []domain.Entity
	catalogue *workload.Catalogue
	settings  domain.IntentSettings

	// truths is the simulator's ground truth, one per event. Read only under
	// KnowledgeTruth, the oracle arm, and by nothing else here.
	truths []Sighting

	requested []request
	// altered is how many jobs of open events are asked to be somewhere other
	// than as submitted, so that intent switched off with nothing to undo
	// costs nothing.
	altered int

	next   int
	open   []int
	judged []judgement
}

type eventWork struct {
	origin time.Duration
	first  domain.JobID
	picks  int

	// triggers is where the detecting sensors are, first arrival first. The
	// arrival order is what a real system receives before any pick has been
	// processed.
	triggers []domain.Point
}

type request struct {
	priority domain.Priority
	exempt   bool

	// moved is whether intent has ever asked for the job to be somewhere other
	// than as submitted, which is what makes a job back there restored.
	moved bool
}

// displaces reports whether a request leaves a job anywhere other than it was
// submitted: at another priority, or exempt from cloud burst.
func (r request) displaces(submitted domain.Priority) bool {
	return r.priority != submitted || r.exempt
}

type judgement struct {
	state, basis string
}

// Transition is a change of intent about one event, by its index.
type Transition struct {
	Event int
	domain.IntentTransition
}

// Plan is one cycle's intent.
type Plan struct {
	Updates     []orchestrator.PriorityUpdate
	Transitions []Transition
}

// New prepares a planner for a workload, with nothing asked for yet.
func New(w workload.Workload, catalogue *workload.Catalogue, settings domain.IntentSettings) *Planner {
	sensors := map[string]domain.Point{}
	for _, sensor := range w.Layout.Sensors {
		sensors[sensor.ID] = sensor.At
	}

	p := &Planner{
		jobs:      w.Jobs,
		entities:  w.Entities,
		catalogue: catalogue,
		settings:  settings,
		events:    make([]eventWork, len(w.Events)),
		truths:    make([]Sighting, len(w.Events)),
		requested: make([]request, len(w.Jobs)),
		judged:    make([]judgement, len(w.Events)),
	}
	for i, event := range w.Events {
		triggers := make([]domain.Point, 0, len(event.Picks))
		for _, pick := range event.Picks {
			if at, ok := sensors[pick.SensorID]; ok {
				triggers = append(triggers, at)
			}
		}
		p.events[i] = eventWork{origin: event.Origin, first: event.FirstJob, picks: len(event.Picks), triggers: triggers}
		p.truths[i] = Sighting{At: event.Truth, Magnitude: event.Magnitude, Basis: BasisTruth}
	}
	for i, job := range w.Jobs {
		p.requested[i] = request{priority: job.Priority}
	}
	return p
}

// Configure changes the settings the next plan is made under.
func (p *Planner) Configure(settings domain.IntentSettings) { p.settings = settings }

// Plan judges every event with work outstanding at a moment, and returns the
// updates that would put its jobs where intent wants them, and every event
// whose judgement changed.
func (p *Planner) Plan(at time.Duration) Plan {
	for p.next < len(p.events) && p.events[p.next].origin <= at {
		p.open = append(p.open, p.next)
		p.next++
	}

	var plan Plan
	off := p.settings.Mode == domain.IntentOff
	var paths []Path
	if !off {
		paths = Paths(p.entities, at, p.settings)
	}

	open := p.open[:0]
	for _, i := range p.open {
		if p.catalogue.Remaining(i) == 0 {
			p.close(i)
			continue
		}
		open = append(open, i)
		if off && p.altered == 0 {
			// Nothing to undo and nothing to do; but a judgement still on
			// record has to say intent let go of it.
			p.judge(&plan, i, at, domain.IntentTransition{At: at, State: domain.EventKept, Basis: basisOff})
			continue
		}

		transition := domain.IntentTransition{At: at, State: domain.EventKept, Basis: basisOff}
		if !off {
			transition = p.assess(i, at, paths)
		}
		p.judge(&plan, i, at, transition)
		p.request(&plan, i, at, transition.State)
	}
	p.open = open
	return plan
}

// assess judges one event against the protected paths.
func (p *Planner) assess(i int, at time.Duration, paths []Path) domain.IntentTransition {
	sighting, ok := p.sight(i)
	if !ok {
		return domain.IntentTransition{At: at, State: domain.EventUnknown}
	}
	out := domain.IntentTransition{At: at, Basis: sighting.Basis}
	entity, distance, found := Nearest(paths, sighting.At)
	if found {
		out.Entity, out.Distance = entity, distance
	}

	s := p.settings
	if promote, reaches := p.reach(sighting, s.PromoteLevel); s.Mode.Promotes() && found && reaches && distance <= promote {
		out.State, out.Reach = domain.EventPromoted, promote
		return out
	}
	protect, reaches := p.reach(sighting, s.ProtectLevel)
	out.Reach = protect
	if s.Mode.Decays() && (!found || !reaches || distance > protect) {
		out.State = domain.EventDecayed
		return out
	}
	out.State = domain.EventKept
	return out
}

// sight is what intent knows about event i under the knowledge it is set to.
func (p *Planner) sight(i int) (Sighting, bool) {
	s := p.settings
	if s.Knowledge == domain.KnowledgeTruth {
		return p.truths[i], true
	}

	if location := p.catalogue.Location(i); location != nil {
		magnitude := s.PreLocationMagnitude
		if location.Magnitude != nil {
			magnitude = *location.Magnitude
		}
		return Sighting{At: location.At, Magnitude: magnitude, Uncertainty: s.LocationUncertainty, Basis: BasisLocation}, true
	}

	triggers := p.events[i].triggers
	if !s.PreLocation || len(triggers) == 0 {
		return Sighting{}, false
	}
	// The event is nearer the first sensor to trigger than the others that
	// follow it, so it is within about the distance to the fourth of them —
	// the fourth because four are what a location would need.
	spread := triggers[0].DistanceTo(triggers[min(seismic.MinimumPicks, len(triggers))-1])
	return Sighting{
		At: triggers[0], Magnitude: s.PreLocationMagnitude,
		Uncertainty: max(s.LocationUncertainty, spread), Basis: BasisSensors,
	}, true
}

// reach is how far a level of ground motion extends from a sighting, widened
// by the margin. ok is false for a level the event cannot reach at all.
func (p *Planner) reach(sighting Sighting, level string) (float64, bool) {
	zones := hazard.Default.Zones(sighting.Magnitude, sighting.Uncertainty)
	for _, l := range hazard.Levels {
		if l.String() == level {
			radius, ok := zones[l]
			if !ok {
				return 0, false
			}
			return radius + p.settings.Margin, true
		}
	}
	return 0, false
}

// judge records a change of judgement about event i. A first judgement that
// says nothing — unknown, or intent off — is not a change worth recording.
func (p *Planner) judge(plan *Plan, i int, at time.Duration, transition domain.IntentTransition) {
	last := p.judged[i]
	now := judgement{state: transition.State, basis: transition.Basis}
	if now == last {
		return
	}
	if last == (judgement{}) && (now.state == domain.EventUnknown || now.basis == basisOff) {
		return
	}
	p.judged[i] = now
	plan.Transitions = append(plan.Transitions, Transition{Event: i, IntentTransition: transition})
}

// request asks for event i's unfinished jobs to be where the state wants them.
func (p *Planner) request(plan *Plan, i int, at time.Duration, state string) {
	s := p.settings
	event := p.events[i]
	for k := 0; k < event.picks; k++ {
		id := event.first + domain.JobID(k)
		if p.catalogue.Finished(id) {
			continue
		}
		submitted := p.jobs[id].Priority
		have := p.requested[id]
		want := request{priority: submitted, moved: have.moved}
		var class domain.IntentClass
		switch {
		case state == domain.EventDecayed && s.DecayTo < submitted:
			want.priority, class = s.DecayTo, domain.ClassDecayed
		case state == domain.EventPromoted && s.PromoteTo > submitted:
			want.priority, class = s.PromoteTo, domain.ClassPromoted
		case !s.Restore && s.Mode != domain.IntentOff && have.priority < submitted:
			// Decay is final: work stays decayed whatever comes near its
			// event. Switching intent off still puts it back.
			want.priority, class = have.priority, domain.ClassDecayed
		case have.moved:
			class = domain.ClassRestored
		}
		want.moved = want.moved || want.priority != submitted
		want.exempt = class != "" && s.Exempts(class)

		if want == have {
			continue
		}
		switch before, after := have.displaces(submitted), want.displaces(submitted); {
		case !before && after:
			p.altered++
		case before && !after:
			p.altered--
		}
		p.requested[id] = want
		plan.Updates = append(plan.Updates, orchestrator.PriorityUpdate{
			At: at, JobID: id, Priority: want.priority, Reason: state,
			RestartDeadline: s.DeadlineFrom == domain.DeadlineFromChange,
			BurstExempt:     want.exempt,
		})
	}
}

// close forgets an event whose work is all done.
func (p *Planner) close(i int) {
	event := p.events[i]
	for k := 0; k < event.picks; k++ {
		id := event.first + domain.JobID(k)
		if p.requested[id].displaces(p.jobs[id].Priority) {
			p.altered--
		}
	}
}
