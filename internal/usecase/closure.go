package usecase

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
)

// Closure is use case 2: the map of ground to keep out of, drawn from located
// hypocentres, against the ground the events really made dangerous.
//
// The other cases ask whether one decision had what it needed in time. This
// one asks something the same run answers differently: taking every location
// the mine had at a moment, and the zone each one draws, does the closed
// ground cover the dangerous ground — and how much of the mine does it shut
// that nothing endangered? Both matter, and they trade against each other: a
// wider allowance for location error misses less and closes more.
//
// It is measured over the tunnels, in metre-seconds, because that is what a
// closure costs: so many metres of drift shut for so many seconds. The mine is
// sampled every StepMeters, each sample standing for the stretch of tunnel
// around it, and a stretch is dangerous when the event's true ground motion
// reaches Level there, closed when some location the mine had says it does.
//
// The map is a union, as an operator would read it — a neighbour's zone can
// close the ground of an event nobody has located yet, which is exactly the
// effect case 2 was written to look for.
type Closure struct{ p ClosureParams }

// ClosureParams is what the measure runs with.
type ClosureParams struct {
	// Level is the ground motion that closes ground, both in the truth and in
	// what a location claims. Moderate by default, the level intent protects
	// at.
	Level string `json:"level"`

	// WindowSeconds is how long an event's ground stays dangerous, and its
	// zone closed, after it happened. The same assumption the other cases
	// make, and the same sweep axis.
	WindowSeconds float64 `json:"window_seconds"`

	// ExtraAllowanceMeters widens every closed zone beyond what the run
	// recorded. A recorded zone already carries the mine's own allowance for
	// how far a location may be out; this asks what a more cautious mine
	// would have missed, and closed, instead.
	ExtraAllowanceMeters float64 `json:"extra_allowance_meters"`

	// StepMeters is how finely the tunnels are sampled. Finer is slower and
	// no more true than the zones themselves are.
	StepMeters float64 `json:"step_meters"`

	level hazard.Level
}

// DefaultClosure is the measure's starting point.
func DefaultClosure() ClosureParams {
	return ClosureParams{Level: hazard.Moderate.String(), WindowSeconds: 1800, StepMeters: 25}
}

// NewClosure reads parameters over the defaults; nil or empty takes them all.
func NewClosure(params json.RawMessage) (Closure, error) {
	p := DefaultClosure()
	if err := decode("closure", params, &p); err != nil {
		return Closure{}, err
	}
	var problems []string
	level, ok := levelNamed(p.Level)
	if !ok {
		problems = append(problems, fmt.Sprintf("level %q is not one of %s", p.Level, strings.Join(levelNames(), ", ")))
	}
	p.level = level
	if p.WindowSeconds <= 0 {
		problems = append(problems, fmt.Sprintf("window_seconds must be > 0, got %v: ground closed for "+
			"no time at all is no map", p.WindowSeconds))
	}
	if p.StepMeters <= 0 {
		problems = append(problems, fmt.Sprintf("step_meters must be > 0, got %v", p.StepMeters))
	}
	if p.ExtraAllowanceMeters < 0 {
		problems = append(problems, fmt.Sprintf("extra_allowance_meters must be >= 0, got %v: a zone "+
			"narrower than the one the mine drew is not a map it could have had", p.ExtraAllowanceMeters))
	}
	if len(problems) > 0 {
		return Closure{}, fmt.Errorf("parameters of closure: %s", strings.Join(problems, "; "))
	}
	return Closure{p}, nil
}

// Params are the parameters it runs with, defaults filled in.
func (c Closure) Params() any { return c.p }

// ClosureMap is how the mine's map did: the whole run taken together, and
// each event that made ground dangerous.
type ClosureMap struct {
	Summary ClosureSummary
	Events  []ClosureEvent
}

// ClosureSummary is the map over the whole run.
type ClosureSummary struct {
	// GroundMeters is how much tunnel there is, and DurationSeconds how long
	// the map is read over: from the first event to the end of the last
	// event's window. The shares are against these.
	GroundMeters, DurationSeconds float64

	// HazardMeterSeconds is tunnel that was truly dangerous, ClosedMeterSeconds
	// tunnel the map shut, MissedMeterSeconds dangerous tunnel left open and
	// FalseMeterSeconds tunnel shut that nothing endangered.
	HazardMeterSeconds, ClosedMeterSeconds, MissedMeterSeconds, FalseMeterSeconds float64

	// CoveredShare is the dangerous tunnel the map shut, FalseShare the shut
	// tunnel nothing endangered. Not a number when there was none of it.
	CoveredShare, FalseShare float64

	// MeanClosedShare and PeakClosedShare are how much of the mine the map
	// shuts: on average over the run, and at its worst moment.
	MeanClosedShare, PeakClosedShare float64

	// Events is how many events made tunnel dangerous, Complete how many had
	// every dangerous metre of theirs shut at one moment within their window,
	// and NeverComplete the rest.
	Events, Complete, NeverComplete int

	// CompleteP50 and CompleteP95 are how long that took from the event, in
	// seconds, over the events where it happened at all.
	CompleteP50, CompleteP95 float64
}

// ClosureEvent is how the map did by one event.
type ClosureEvent struct {
	// Event is its index; the JSON names it by sequence, as the API does.
	Event int
	// HazardMeters is the tunnel its ground motion made dangerous.
	HazardMeters float64
	// CompleteAfter is how long after it happened the map first covered every
	// one of those metres — by any location, not only its own. Nil if the map
	// never did, within the window.
	CompleteAfter *time.Duration
}

// span is a stretch of time a sample of tunnel was dangerous, or closed.
type span struct{ from, until time.Duration }

// placement is one sample of tunnel: where it is, and the metres of drift it
// stands for.
type placement struct {
	at     domain.Point
	metres float64
}

// Map reads the run's closure map. A pure function of what the run stored, so
// any recorded run can be asked, including one recorded before this existed.
func (c Closure) Map(w World, r Record) ClosureMap {
	samples := sampled(w.Tunnels, c.p.StepMeters)
	out := ClosureMap{Events: []ClosureEvent{}}
	for _, s := range samples {
		out.Summary.GroundMeters += s.metres
	}

	window := time.Duration(c.p.WindowSeconds * float64(time.Second))
	dangerous := make([][]span, len(samples)) // when each sample was truly dangerous
	shut := make([][]span, len(samples))      // when the map had it closed
	inZone := make([][]int, len(w.Events))    // the samples each event endangered
	var last time.Duration

	for i, e := range w.Events {
		end := e.Origin + window
		if end > last {
			last = end
		}
		if radius, ok := trueRadius(c.p.level, e); ok {
			for j, s := range samples {
				if s.at.DistanceTo(e.At) <= radius {
					dangerous[j] = append(dangerous[j], span{e.Origin, end})
					inZone[i] = append(inZone[i], j)
				}
			}
		}
		for _, drawn := range c.closures(i, e.Origin, end, r) {
			for j, s := range samples {
				if s.at.DistanceTo(drawn.at) <= drawn.reach {
					shut[j] = append(shut[j], span{drawn.from, drawn.until})
				}
			}
		}
	}
	out.Summary.DurationSeconds = last.Seconds()

	var switches []struct {
		at     time.Duration
		metres float64
	}
	for j, s := range samples {
		dangerous[j], shut[j] = merged(dangerous[j]), merged(shut[j])
		hazardous, closed := length(dangerous[j]), length(shut[j])
		both := intersection(dangerous[j], shut[j])
		out.Summary.HazardMeterSeconds += s.metres * hazardous.Seconds()
		out.Summary.ClosedMeterSeconds += s.metres * closed.Seconds()
		out.Summary.MissedMeterSeconds += s.metres * (hazardous - both).Seconds()
		out.Summary.FalseMeterSeconds += s.metres * (closed - both).Seconds()
		for _, sp := range shut[j] {
			switches = append(switches,
				struct {
					at     time.Duration
					metres float64
				}{sp.from, s.metres},
				struct {
					at     time.Duration
					metres float64
				}{sp.until, -s.metres})
		}
	}

	var complete []float64
	for i, e := range w.Events {
		if len(inZone[i]) == 0 {
			continue
		}
		event := ClosureEvent{Event: i}
		for _, j := range inZone[i] {
			event.HazardMeters += samples[j].metres
		}
		if after, ok := c.completeAfter(inZone[i], shut, e.Origin, e.Origin+window); ok {
			event.CompleteAfter = &after
			complete = append(complete, after.Seconds())
			out.Summary.Complete++
		} else {
			out.Summary.NeverComplete++
		}
		out.Summary.Events++
		out.Events = append(out.Events, event)
	}

	out.Summary.CoveredShare = share(out.Summary.HazardMeterSeconds-out.Summary.MissedMeterSeconds,
		out.Summary.HazardMeterSeconds)
	out.Summary.FalseShare = share(out.Summary.FalseMeterSeconds, out.Summary.ClosedMeterSeconds)
	out.Summary.MeanClosedShare = share(out.Summary.ClosedMeterSeconds,
		out.Summary.GroundMeters*out.Summary.DurationSeconds)
	out.Summary.PeakClosedShare = share(peak(switches), out.Summary.GroundMeters)
	out.Summary.CompleteP50, out.Summary.CompleteP95 = percentile(complete, 0.5), percentile(complete, 0.95)
	return out
}

// drawn is one zone the map had: from when the mine had the location until it
// had a better one or the window ended.
type drawn struct {
	from, until time.Duration
	at          domain.Point
	reach       float64
}

// closures is the zones an event contributed to the map: the one its first
// location drew, until its final location replaced it, and the final one's
// until the window ends. A location whose ground motion does not reach the
// level draws nothing.
func (c Closure) closures(i int, origin, end time.Duration, r Record) []drawn {
	var out []drawn
	add := func(from, until time.Duration, location *domain.Location) {
		if location == nil || from >= until {
			return
		}
		if reach, ok := location.Zones[c.p.Level]; ok {
			out = append(out, drawn{from: from, until: until, at: location.At,
				reach: reach + c.p.ExtraAllowanceMeters})
		}
	}
	first, hasFirst := at(r.Located, i)
	final, hasFinal := at(r.Processed, i)
	if hasFirst {
		until := end
		if hasFinal && final < until {
			until = final
		}
		add(max(first, origin), until, location(r.First, i))
	}
	if hasFinal {
		add(max(final, origin), end, location(r.Final, i))
	}
	return out
}

// completeAfter is how long after an event the map first had every one of its
// dangerous samples closed at once, within its window.
func (c Closure) completeAfter(samples []int, shut [][]span, origin, end time.Duration) (time.Duration, bool) {
	candidates := []time.Duration{origin}
	for _, j := range samples {
		for _, sp := range shut[j] {
			if sp.from > origin && sp.from < end {
				candidates = append(candidates, sp.from)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	for _, t := range candidates {
		all := true
		for _, j := range samples {
			if !covers(shut[j], t) {
				all = false
				break
			}
		}
		if all {
			return t - origin, true
		}
	}
	return 0, false
}

// sampled walks the tunnels and puts a sample every step metres, each standing
// for the stretch around it. Deterministic: the same layout always gives the
// same samples, in the same order.
func sampled(tunnels []domain.Tunnel, step float64) []placement {
	var out []placement
	for _, t := range tunnels {
		for i := 1; i < len(t.Path); i++ {
			a, b := t.Path[i-1], t.Path[i]
			length := a.DistanceTo(b)
			if length == 0 {
				continue
			}
			n := max(1, int(math.Round(length/step)))
			for k := 0; k < n; k++ {
				f := (float64(k) + 0.5) / float64(n)
				out = append(out, placement{
					at:     domain.Point{X: a.X + f*(b.X-a.X), Y: a.Y + f*(b.Y-a.Y), Z: a.Z + f*(b.Z-a.Z)},
					metres: length / float64(n),
				})
			}
		}
	}
	return out
}

// trueRadius is how far an event's true magnitude carries the level, with no
// allowance: the ground it really made dangerous.
func trueRadius(level hazard.Level, e Event) (float64, bool) {
	r, ok := hazard.Default.Zones(e.Magnitude, 0)[level]
	return r, ok
}

func levelNamed(name string) (hazard.Level, bool) {
	for _, l := range hazard.Levels {
		if l.String() == name {
			return l, true
		}
	}
	return hazard.None, false
}

func levelNames() []string {
	names := make([]string, len(hazard.Levels))
	for i, l := range hazard.Levels {
		names[i] = l.String()
	}
	return names
}

func location(locations []*domain.Location, i int) *domain.Location {
	if i < 0 || i >= len(locations) {
		return nil
	}
	return locations[i]
}

// merged is the spans in time order with overlaps joined.
func merged(spans []span) []span {
	if len(spans) < 2 {
		return spans
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	out := spans[:1]
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s.from <= last.until {
			if s.until > last.until {
				last.until = s.until
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// length is how long merged spans last in total.
func length(spans []span) time.Duration {
	var total time.Duration
	for _, s := range spans {
		total += s.until - s.from
	}
	return total
}

// intersection is how long merged spans overlap.
func intersection(a, b []span) time.Duration {
	var total time.Duration
	for i, j := 0, 0; i < len(a) && j < len(b); {
		from, until := max(a[i].from, b[j].from), min(a[i].until, b[j].until)
		if until > from {
			total += until - from
		}
		if a[i].until < b[j].until {
			i++
		} else {
			j++
		}
	}
	return total
}

// covers reports whether merged spans hold at a moment.
func covers(spans []span, at time.Duration) bool {
	for _, s := range spans {
		if s.from <= at && at < s.until {
			return true
		}
	}
	return false
}

// peak is the most metres closed at once, from every sample's closures as
// changes to the total.
func peak(switches []struct {
	at     time.Duration
	metres float64
}) float64 {
	sort.SliceStable(switches, func(i, j int) bool { return switches[i].at < switches[j].at })
	var now, most float64
	for _, s := range switches {
		now += s.metres
		if now > most {
			most = now
		}
	}
	return most
}

// share is a over b, and not a number when there is no b — which is not the
// same as none of it.
func share(a, b float64) float64 {
	if b <= 0 {
		return math.NaN()
	}
	return a / b
}

// MarshalJSON renders the summary with its times in seconds and what is not a
// number as null.
func (s ClosureSummary) MarshalJSON() ([]byte, error) {
	num := func(v float64) *float64 {
		if math.IsNaN(v) {
			return nil
		}
		return &v
	}
	return json.Marshal(struct {
		GroundMeters       float64  `json:"ground_meters"`
		DurationSeconds    float64  `json:"duration_seconds"`
		HazardMeterSeconds float64  `json:"hazard_meter_seconds"`
		ClosedMeterSeconds float64  `json:"closed_meter_seconds"`
		MissedMeterSeconds float64  `json:"missed_meter_seconds"`
		FalseMeterSeconds  float64  `json:"false_meter_seconds"`
		CoveredShare       *float64 `json:"covered_share"`
		FalseShare         *float64 `json:"false_share"`
		MeanClosedShare    *float64 `json:"mean_closed_share"`
		PeakClosedShare    *float64 `json:"peak_closed_share"`
		Events             int      `json:"events"`
		Complete           int      `json:"complete"`
		NeverComplete      int      `json:"never_complete"`
		CompleteP50Seconds *float64 `json:"complete_p50_seconds"`
		CompleteP95Seconds *float64 `json:"complete_p95_seconds"`
	}{s.GroundMeters, s.DurationSeconds, s.HazardMeterSeconds, s.ClosedMeterSeconds,
		s.MissedMeterSeconds, s.FalseMeterSeconds, num(s.CoveredShare), num(s.FalseShare),
		num(s.MeanClosedShare), num(s.PeakClosedShare), s.Events, s.Complete, s.NeverComplete,
		num(s.CompleteP50), num(s.CompleteP95)})
}

// MarshalJSON names the event by sequence, as the rest of the API does, and
// gives its time in seconds.
func (e ClosureEvent) MarshalJSON() ([]byte, error) {
	var after *float64
	if e.CompleteAfter != nil {
		s := e.CompleteAfter.Seconds()
		after = &s
	}
	return json.Marshal(struct {
		Event                int      `json:"event"`
		HazardMeters         float64  `json:"hazard_meters"`
		CompleteAfterSeconds *float64 `json:"complete_after_seconds"`
	}{e.Event + 1, e.HazardMeters, after})
}
