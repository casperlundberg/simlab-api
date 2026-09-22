package intent

import (
	"math"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
	"github.com/casperlundberg/simlab-api/internal/observe"
	"github.com/casperlundberg/simlab-api/internal/orchestrator"
	"github.com/casperlundberg/simlab-api/internal/seismic"
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
	priorities []domain.Priority
	events     []eventWork
	views      observe.Views
	catalogue  Catalogue
	settings   domain.IntentSettings

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

	// generation counts settings changes, and speed is the fastest anything
	// protected moves; together they are what makes skipping a judgement safe.
	generation int
	speed      float64

	// alwaysJudge turns the skipping off, for the test that holds it to
	// deciding exactly what judging everything every cycle decides.
	alwaysJudge bool
}

type eventWork struct {
	origin time.Duration
	first  domain.JobID
	picks  int

	// triggers is where the detecting sensors are, first arrival first. The
	// arrival order is what a real system receives before any pick has been
	// processed.
	triggers []domain.Point

	// struck is who was nearest the event when it happened, measured from
	// where intent last took it to be. Kept so it is measured once per
	// position rather than every cycle.
	struck struck
}

type struck struct {
	measured bool
	from     domain.Point
	entity   string
	distance float64
	found    bool
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

	// at is when it was made, and slack how far the nearest protected path
	// was from the boundary that decided it, in metres. Nothing protected can
	// have crossed that boundary until it has had time to cover the slack, so
	// until then the judgement stands without measuring anything.
	at    time.Duration
	slack float64

	// from is the position the event was judged at, so a new location — or a
	// first one — is judged afresh however much slack the last one had.
	from domain.Point

	// asked is the state the event's jobs were last asked to follow, and
	// under which generation of the settings. Both unchanged means what is
	// wanted for every one of its jobs is unchanged.
	asked      string
	generation int
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

// Catalogue is what the mine has of its events so far: how many of an event's
// picks are still to be processed, the location it has for it, and whether a
// job has finished. The planner orders work from this and never from the
// events themselves.
type Catalogue interface {
	Remaining(event int) int
	Location(event int) *domain.Location
	Finished(id domain.JobID) bool
}

// New prepares a planner, with nothing asked for yet.
//
// Work is the processing as the mine sees it before any is done; the catalogue
// is what it learns as it goes; views are what it may know of where people and
// vehicles are. Oracle is the one piece of truth it holds — where each event
// really was, and how large — read only under knowledge "truth", the arm that
// bounds what ordering by location could achieve. Which implementations these
// are, and where they come from, is decided where the run is wired.
func New(work domain.Work, catalogue Catalogue, oracle []Sighting, settings domain.IntentSettings, views observe.Views) *Planner {
	p := &Planner{
		priorities: work.Priorities,
		views:      views,
		catalogue:  catalogue,
		settings:   settings,
		generation: 1,
		// The faster of the two, so skipping stays sound whichever knowledge
		// the settings switch to mid-run.
		speed:     math.Max(views.Mine.Speed(), views.Oracle.Speed()),
		events:    make([]eventWork, len(work.Events)),
		truths:    oracle,
		requested: make([]request, len(work.Priorities)),
		judged:    make([]judgement, len(work.Events)),
	}
	for i, event := range work.Events {
		p.events[i] = eventWork{origin: event.Origin, first: event.FirstJob, picks: event.Picks, triggers: event.Triggers}
	}
	for i, priority := range work.Priorities {
		p.requested[i] = request{priority: priority}
	}
	return p
}

// Configure changes the settings the next plan is made under.
func (p *Planner) Configure(settings domain.IntentSettings) {
	// Who counts as protected may have changed, and with it who was nearest
	// each event when it happened. Every judgement and every request is made
	// again under the new settings.
	for i := range p.events {
		p.events[i].struck = struck{}
		p.judged[i].slack = 0
	}
	p.generation++
	p.settings = settings
}

// Valued reports whether the mine currently believes event i matters enough to
// wait for: it has been promoted, or it has been judged and kept rather than
// relaxed.
//
// It is the mine's own estimate, and before an event has a location the mine
// has usually judged nothing — so under estimates this is empty early, which is
// exactly when a sweep policy would most like to know. That is a finding about
// what a sparse array can support, not a gap to paper over: the oracle arm is
// where the same question gets a truthful answer.
func (p *Planner) Valued(i int) bool {
	if i < 0 || i >= len(p.judged) {
		return false
	}
	switch p.judged[i].state {
	case domain.EventPromoted, domain.EventKept:
		return true
	default:
		return false
	}
}

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
	var paths []observe.Path
	if !off {
		paths = p.whereabouts().Reach(at, p.settings.Lookahead, p.settings.Protects)
	}

	open := p.open[:0]
	for _, i := range p.open {
		if p.catalogue.Remaining(i) == 0 {
			p.close(i)
			continue
		}
		open = append(open, i)
		if !off && p.settled(i, at) {
			continue
		}
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

// settled reports whether event i's judgement cannot have changed since it was
// made, and neither can what its jobs were asked for.
//
// Two things could change it: the mine's sighting of the event, which is
// checked against the position it was judged from; and something protected
// moving across the boundary that decided it. Nothing protected moves faster
// than the quickest vehicle, so until the time since the judgement is enough
// to cover the slack at that speed, nothing can have crossed. Every judgement
// this skips is one that would have come out the same.
func (p *Planner) settled(i int, at time.Duration) bool {
	last := p.judged[i]
	if p.alwaysJudge || last.slack <= 0 || last.asked == "" || last.generation != p.generation {
		return false
	}
	if p.speed <= 0 {
		// Nothing moves: only a new sighting can change anything.
		return p.sightingAt(i) == last.from
	}
	if (at-last.at).Seconds()*p.speed >= last.slack {
		return false
	}
	return p.sightingAt(i) == last.from
}

// sightingAt is where intent currently takes event i to be, or the zero point
// when it has nothing to go on.
func (p *Planner) sightingAt(i int) domain.Point {
	sighting, ok := p.sight(i)
	if !ok {
		return domain.Point{}
	}
	return sighting.At
}

// whereabouts is the view the current knowledge decides from.
func (p *Planner) whereabouts() observe.Whereabouts {
	return p.views.For(p.settings.Knowledge)
}

// assess judges one event against the protected paths.
func (p *Planner) assess(i int, at time.Duration, paths []observe.Path) domain.IntentTransition {
	sighting, ok := p.sight(i)
	if !ok {
		return domain.IntentTransition{At: at, State: domain.EventUnknown}
	}
	out := domain.IntentTransition{At: at, Basis: sighting.Basis}
	judged := &p.judged[i]
	judged.at, judged.from, judged.slack = at, sighting.At, math.Inf(1)
	entity, distance, found := observe.Nearest(paths, sighting.At)
	// Where people were when the event happened counts as much as where they
	// are going: an event that shook someone is worth locating after they
	// have walked away, since its location is where to look for them.
	if hit := p.strikeOf(i, sighting.At); hit.found && (!found || hit.distance < distance) {
		entity, distance, found = hit.entity, hit.distance, true
	}
	if found {
		out.Entity, out.Distance = entity, distance
	}

	s := p.settings
	// The slack is the distance to whichever boundary the judgement turned
	// on; with nothing protected anywhere, no boundary can be crossed.
	if promote, reaches := p.reach(sighting, s.PromoteLevel); s.Mode.Promotes() && reaches {
		if found {
			judged.slack = math.Min(judged.slack, math.Abs(distance-promote))
		}
		if found && distance <= promote {
			out.State, out.Reach = domain.EventPromoted, promote
			return out
		}
	}
	protect, reaches := p.reach(sighting, s.ProtectLevel)
	out.Reach = protect
	if found && reaches {
		judged.slack = math.Min(judged.slack, math.Abs(distance-protect))
	}
	if s.Mode.Decays() && (!found || !reaches || distance > protect) {
		out.State = domain.EventDecayed
		return out
	}
	out.State = domain.EventKept
	return out
}

// strikeOf is the protected entity nearest event i, taken to be at from, at
// the moment the event happened.
func (p *Planner) strikeOf(i int, from domain.Point) struck {
	event := &p.events[i]
	if event.struck.measured && event.struck.from == from {
		return event.struck
	}
	hit := struck{measured: true, from: from, distance: math.Inf(1)}
	for _, unit := range p.whereabouts().Positions(event.origin, p.settings.Protects) {
		if d := unit.At.DistanceTo(from); d < hit.distance {
			hit.entity, hit.distance, hit.found = unit.Entity, d, true
		}
	}
	event.struck = hit
	return hit
}

// sight is what intent knows about event i under the knowledge it is set to.
func (p *Planner) sight(i int) (Sighting, bool) {
	s := p.settings
	if s.Knowledge == domain.KnowledgeTruth {
		// The oracle knows every event it was given the truth of, and nothing
		// else: one it was not given is unknown, never guessed at.
		if i >= len(p.truths) {
			return Sighting{}, false
		}
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
	p.judged[i].state, p.judged[i].basis = transition.State, transition.Basis
	if transition.State == last.state && transition.Basis == last.basis {
		return
	}
	if last.state == "" && (transition.State == domain.EventUnknown || transition.Basis == basisOff) {
		return
	}
	plan.Transitions = append(plan.Transitions, Transition{Event: i, IntentTransition: transition})
}

// request asks for event i's unfinished jobs to be where the state wants them.
func (p *Planner) request(plan *Plan, i int, at time.Duration, state string) {
	s := p.settings
	event := p.events[i]
	// What each of an event's jobs should be at follows from its state and the
	// settings alone, so asking again for the same pair can move nothing.
	if p.judged[i].asked == state && p.judged[i].generation == p.generation {
		return
	}
	p.judged[i].asked, p.judged[i].generation = state, p.generation
	for k := 0; k < event.picks; k++ {
		id := event.first + domain.JobID(k)
		if p.catalogue.Finished(id) {
			continue
		}
		submitted := p.priorities[id]
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
		case have.moved && s.Mode != domain.IntentOff:
			// Intent switched off puts work back as it arrived, exemption
			// included: nothing it did stands.
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
		if p.requested[id].displaces(p.priorities[id]) {
			p.altered--
		}
	}
}
