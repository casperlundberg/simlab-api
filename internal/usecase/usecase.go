// Package usecase asks, of a recorded run, whether the information the mine's
// decisions needed arrived before those decisions stopped being useful.
//
// Faster is not the claim worth making; fast enough is. A use case — turning
// a vehicle back before it drives into an event's zone, say — lists from the
// world as it really was every decision a mine would have had to make: whom it
// was for, what information it waited on, when it could first be made and when
// it stopped being useful. Scoring then reads, from what the mine had and when,
// whether each decision had what it needed in time.
//
// The two halves read different things, and the types keep them apart. A Case
// reads the World — the simulator's truth — to know which decisions there
// were, never to make them. A Need reads the Record — when the mine had each
// event located — and nothing else. Both are pure functions of what a run
// stored, so a case can be scored on any recorded run, including runs made
// before the case existed, and a report of it reproduces exactly.
//
// A new case is a type implementing Case and one line in the registry; a new
// kind of information, a type implementing Need. Nothing else changes.
package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// World is how a run really was.
type World struct {
	Events  []Event
	Units   []domain.Entity
	Tunnels []domain.Tunnel
}

// Event is one event as it really happened.
type Event struct {
	Origin    time.Duration
	At        domain.Point
	Magnitude float64
}

// Record is what the mine had, and when: for each event, the moment it was
// first located and the moment every one of its picks had been processed, and
// the locations it had then — where it took the event to be, and how far each
// level of ground motion reached from there, allowance for error included.
// Nil for an event the mine never had.
type Record struct {
	Located   []*time.Duration
	Processed []*time.Duration
	First     []*domain.Location
	Final     []*domain.Location
}

// FromRun builds the world and the record from what a run stored. Event i is
// the event of sequence i+1.
func FromRun(events []domain.SeismicEvent, units []domain.Entity, tunnels []domain.Tunnel) (World, Record) {
	w := World{Units: units, Tunnels: tunnels, Events: make([]Event, len(events))}
	r := Record{
		Located: make([]*time.Duration, len(events)), Processed: make([]*time.Duration, len(events)),
		First: make([]*domain.Location, len(events)), Final: make([]*domain.Location, len(events)),
	}
	for i, e := range events {
		magnitude := 0.0
		if e.Magnitude != nil {
			magnitude = *e.Magnitude
		}
		w.Events[i] = Event{Origin: e.Origin, At: e.Truth, Magnitude: magnitude}
		r.Located[i], r.Processed[i] = e.LocatedAt, e.ProcessedAt
		r.First[i], r.Final[i] = e.Located, e.Final
	}
	return w, r
}

// Need is the information a decision waits on.
type Need interface {
	// Name says what it is, for a reader of the outcomes.
	Name() string
	// MetAt is when the mine had it, if it ever did.
	MetAt(r Record) (time.Duration, bool)
}

// FirstLocation is an event's first location: somewhere to point at.
type FirstLocation struct{ Event int }

func (FirstLocation) Name() string { return "first-location" }

func (n FirstLocation) MetAt(r Record) (time.Duration, bool) { return at(r.Located, n.Event) }

// FinalLocation is an event's location from every pick that detected it.
type FinalLocation struct{ Event int }

func (FinalLocation) Name() string { return "final-location" }

func (n FinalLocation) MetAt(r Record) (time.Duration, bool) { return at(r.Processed, n.Event) }

// Warning is a location that tells the mine what the decision is about: the
// first of the event's locations whose own zone at the level — widened by the
// allowance for how far the location may be out — reaches the point that
// matters, where a unit would enter the zone or where it stands. A location
// too far out to reach it says nothing about this unit, however early it came.
type Warning struct {
	Event int
	At    domain.Point
	Level string
}

func (Warning) Name() string { return "warning" }

func (n Warning) MetAt(r Record) (time.Duration, bool) {
	for _, candidate := range []struct {
		when     []*time.Duration
		location []*domain.Location
	}{{r.Located, r.First}, {r.Processed, r.Final}} {
		when, ok := at(candidate.when, n.Event)
		if !ok || n.Event >= len(candidate.location) || candidate.location[n.Event] == nil {
			continue
		}
		loc := candidate.location[n.Event]
		if reach, ok := loc.Zones[n.Level]; ok && loc.At.DistanceTo(n.At) <= reach {
			return when, true
		}
	}
	return 0, false
}

func at(times []*time.Duration, i int) (time.Duration, bool) {
	if i < 0 || i >= len(times) || times[i] == nil {
		return 0, false
	}
	return *times[i], true
}

// Opportunity is one decision a mine would have had to make: for whom, about
// which event, waiting on what, and the time it had — from Opens, when it
// could first be made, to Closes, after which it no longer helps.
type Opportunity struct {
	Case   string
	Event  int
	Entity string
	Need   Need
	Opens  time.Duration
	Closes time.Duration
}

// Case is one use case.
type Case interface {
	// Kind is the name it is registered under.
	Kind() string
	// Params are the parameters it runs with, defaults filled in, so a
	// result can say exactly what was asked.
	Params() any
	// Opportunities is every decision the world gave it, in event and then
	// unit order.
	Opportunities(w World) []Opportunity
}

// Outcome is how one decision went.
type Outcome struct {
	Opportunity
	// MetAt is when the mine had what it needed, nil if it never did.
	MetAt  *time.Duration
	InTime bool
}

// Score judges every decision against what the mine had and when. A decision
// is in time when its need was met no later than it closed.
func Score(opportunities []Opportunity, r Record) []Outcome {
	out := make([]Outcome, len(opportunities))
	for i, o := range opportunities {
		out[i] = Outcome{Opportunity: o}
		if met, ok := o.Need.MetAt(r); ok {
			m := met
			out[i].MetAt = &m
			out[i].InTime = met <= o.Closes
		}
	}
	return out
}

// MarshalJSON renders an outcome with its event by sequence, as the rest of
// the API names events, and its times in seconds.
func (o Outcome) MarshalJSON() ([]byte, error) {
	var met *float64
	if o.MetAt != nil {
		s := o.MetAt.Seconds()
		met = &s
	}
	return json.Marshal(struct {
		Event         int      `json:"event"`
		Entity        string   `json:"entity,omitempty"`
		Need          string   `json:"need"`
		OpensSeconds  float64  `json:"opens_seconds"`
		ClosesSeconds float64  `json:"closes_seconds"`
		MetAtSeconds  *float64 `json:"met_at_seconds"`
		InTime        bool     `json:"in_time"`
	}{o.Event + 1, o.Entity, o.Need.Name(), o.Opens.Seconds(), o.Closes.Seconds(), met, o.InTime})
}

// Summary is how a run's decisions went, taken together.
type Summary struct {
	Opportunities int
	// InTime is how many had what they needed no later than they closed.
	InTime int
	// Never is how many never had it at all.
	Never int
	// Unwinnable is how many closed no later than they opened: nothing could
	// have been in time for them, however fast the processing.
	Unwinnable int
	// InTimeShare is InTime over Opportunities; not a number when there were
	// none, which is not the same as none or all of them.
	InTimeShare float64
	// Latency is how long, in seconds, a met need took from when its decision
	// opened; Slack how long, in seconds, an in-time decision had to spare.
	// Not a number when there is nothing to take them over.
	LatencyP50, LatencyP95, SlackP50 float64
}

// Summarise takes a run's outcomes together.
func Summarise(outcomes []Outcome) Summary {
	s := Summary{Opportunities: len(outcomes), InTimeShare: math.NaN()}
	var latency, slack []float64
	for _, o := range outcomes {
		if o.Closes <= o.Opens {
			s.Unwinnable++
		}
		if o.MetAt == nil {
			s.Never++
			continue
		}
		latency = append(latency, (*o.MetAt - o.Opens).Seconds())
		if o.InTime {
			s.InTime++
			slack = append(slack, (o.Closes - *o.MetAt).Seconds())
		}
	}
	if s.Opportunities > 0 {
		s.InTimeShare = float64(s.InTime) / float64(s.Opportunities)
	}
	s.LatencyP50, s.LatencyP95 = percentile(latency, 0.5), percentile(latency, 0.95)
	s.SlackP50 = percentile(slack, 0.5)
	return s
}

// percentile is the nearest-rank percentile, the definition the sweep reports
// use, so a figure here and one there mean the same.
func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[max(0, int(math.Ceil(q*float64(len(sorted))))-1)]
}

// MarshalJSON renders what is not a number as null.
func (s Summary) MarshalJSON() ([]byte, error) {
	num := func(v float64) *float64 {
		if math.IsNaN(v) {
			return nil
		}
		return &v
	}
	return json.Marshal(struct {
		Opportunities     int      `json:"opportunities"`
		InTime            int      `json:"in_time"`
		Never             int      `json:"never"`
		Unwinnable        int      `json:"unwinnable"`
		InTimeShare       *float64 `json:"in_time_share"`
		LatencyP50Seconds *float64 `json:"latency_p50_seconds"`
		LatencyP95Seconds *float64 `json:"latency_p95_seconds"`
		SlackP50Seconds   *float64 `json:"slack_p50_seconds"`
	}{s.Opportunities, s.InTime, s.Never, s.Unwinnable, num(s.InTimeShare),
		num(s.LatencyP50), num(s.LatencyP95), num(s.SlackP50)})
}

// registry is every case this build has, by kind. Looked up, never iterated
// for a decision: Kinds sorts.
var registry = map[string]func(params json.RawMessage) (Case, error){
	"turn-back": newTurnBack,
	"way-out":   newWayOut,
	"reroute":   newReroute,
}

// Kinds is every registered case, sorted.
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for kind := range registry {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// New makes a case of a kind from its parameters, as JSON; nil or empty takes
// every default.
func New(kind string, params json.RawMessage) (Case, error) {
	build, ok := registry[kind]
	if !ok {
		return nil, fmt.Errorf("no use case %q: this build has %s", kind, strings.Join(Kinds(), ", "))
	}
	return build(params)
}

// decode reads parameters over defaults already in into, refusing a field the
// case does not have — a misspelt parameter silently taking its default would
// report a result for something nobody asked.
func decode(kind string, params json.RawMessage, into any) error {
	if len(bytes.TrimSpace(params)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("parameters of %s: %w", kind, err)
	}
	return nil
}
