// Package workload turns a mine and a scenario into the jobs a run replays.
//
// The model is small on purpose. It is not trying to be a seismology paper —
// it is trying to produce load with the shape that makes autoscaling hard: a
// steady baseline that shift patterns modulate, a burst that delivers an hour
// of work in minutes, and an aftershock tail that keeps the queue elevated
// long after the event itself is over.
package workload

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/activity"
	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
	"github.com/casperlundberg/simlab-api/internal/seismic"
)

// maxJobs bounds what a single scenario may produce.
//
// A scenario is specified by hand and can easily ask for hundreds of millions
// of jobs by accident. Refusing is far better than exhausting memory while
// somebody waits for a page to load.
const maxJobs = 5_000_000

// detectionProbability is the share of a mine's sensors that register a given
// event and therefore contribute a pick. It is the term that turns array size
// into work per event.
const detectionProbability = 0.6

// diurnalAmplitude is how much shift patterns swing the background rate. Real
// mines are busier on shift; the point here is only that the baseline is not
// perfectly flat, so a controller cannot get away with a single fixed number.
const diurnalAmplitude = 0.35

// omoriExponent is the p in Omori's law, the empirical rule that aftershock
// rate falls off as 1/t^p after a main shock. Values near 1 are what is
// observed in practice.
const omoriExponent = 1.1

// Rock is the velocity model every mine is simulated with: one uniform P-wave
// velocity for hard rock. seismic.Model says why a single velocity is enough
// for what is being studied.
var Rock = seismic.Model{PVelocitySeconds: 5800}

// defaultExtent is the rock a mine is modelled as when it does not state its
// own: the active volume of a large underground mine, 1.6 km by 1 km, between
// the -400 and -1400 levels. A wave crosses it in about a third of a second.
var defaultExtent = domain.Extent{
	Min: domain.Point{X: 0, Y: 0, Z: -1400},
	Max: domain.Point{X: 1600, Y: 1000, Z: -400},
}

// aftershockSpread is how far, as a standard deviation per axis in metres, a
// burst's events fall from its epicentre. A burst is one volume of rock
// failing, and the events that follow it come from around that volume rather
// than from anywhere in the mine.
const aftershockSpread = 60.0

// backgroundSpread and burstSpread are how far, as a standard deviation per
// axis in metres, background events and a burst's own epicentre fall from the
// workings. Mining-induced seismicity concentrates where mining changes the
// stress; re-entry practice restricts access 50 to 100 m around an event in
// open-stope mines (Vallejos & McKinnon 2009), which is the scale of volume a
// mine treats as affected.
const (
	backgroundSpread = 40.0
	burstSpread      = 20.0
)

// The streams a scenario's seed drives. Separate streams rather than one
// shared generator, because each is a dial someone will turn independently:
// jobs were generated before geometry existed and must not move because of it;
// where events happen must not move because pick noise was turned up; and a
// mine's sensors must not move because a different scenario was replayed on
// it.
const (
	jobStream       = 0x9E3779B97F4A7C15
	geometryStream  = 0xD1B54A32D192ED03
	noiseStream     = 0x8CB92BA72F3D8DD7
	layoutStream    = 0xA0761D6478BD642F
	magnitudeStream = 0xE7037ED1A0B428DB
)

// Magnitudes, as Nuttli mN, the scale the hazard scaling law is stated in.
//
// Background seismicity follows Gutenberg–Richter with b = 1 — ten times as
// many events for each unit smaller — between the smallest a dense mine array
// records and the largest a quiet shift produces. A burst's main shock is its
// first event, and its aftershocks follow the same law up to a gap below it:
// Vallejos and McKinnon found main shocks in mines exceed their largest
// aftershock by 1.5 ± 0.6 on average.
const (
	gutenbergRichterB    = 1.0
	smallestMagnitude    = -1.0
	largestBackground    = 1.5
	defaultMainMagnitude = 2.5
	aftershockGap        = 1.5

	// stationScatter is the standard deviation of one sensor's reading of an
	// event's magnitude. Averaged over four readings it is ±0.15, the scatter
	// the handbook's own magnitude relations carry.
	stationScatter = 0.3
)

// Stage is which step of the mine's workflow a job belongs to: a seismogram is
// picked, an associate sweep groups the picks that are ready, and each group is
// located.
//
// StagePick is the zero value deliberately. Every scenario recorded before the
// pipeline existed generates picks and nothing else, so those workloads keep
// their meaning — and their pinned job-stream digests — untouched.
type Stage uint8

const (
	StagePick Stage = iota
	StageAssociate
	StageLocate
)

func (s Stage) String() string {
	switch s {
	case StageAssociate:
		return "associate"
	case StageLocate:
		return "locate"
	default:
		return "pick"
	}
}

// Job is one unit of work arriving at the queue.
type Job struct {
	// ID is this job's identity within the run, so the mine can direct intent
	// at it after it has been submitted. Assigned by position once the jobs
	// are ordered, which makes it a function of the seed alone.
	ID domain.JobID

	// SubmittedAt is the offset from the start of the scenario.
	SubmittedAt time.Duration

	Priority domain.Priority

	// Seconds is how long this job occupies an executor.
	Seconds float64

	// Event is the index, into Workload.Events, of the event this job belongs
	// to. An associate sweep serves many events at once and carries NoEvent.
	Event int

	// Stage is which step of the workflow this job is.
	Stage Stage
}

// NoEvent is the Event of a job that belongs to no single event: an associate
// sweep groups whatever is ready, which is generally several.
const NoEvent = -1

// Workload is everything a scenario generates: the jobs a run replays, and the
// mine they came from.
type Workload struct {
	Jobs []Job

	// Layout is the sensor array the picks came from, stated by the mine or
	// derived for it.
	Layout domain.Layout

	// Model is the rock the arrivals travelled through.
	Model seismic.Model

	// Events are the detected events, in origin order. Each one's picks are a
	// consecutive run of jobs.
	Events []Event

	// Entities are the people and vehicles underground, and where they go.
	Entities []domain.Entity

	// Blasts are the blasts fired, in a scenario that says how its mine is
	// worked.
	Blasts []activity.BlastAt
}

// Event is one seismic event, and what the sensors made of it.
type Event struct {
	Origin time.Duration

	// Burst is the index of the scenario burst this belongs to, or nil for
	// background activity.
	Burst *int

	// Truth is where it really happened. Ground truth: known to the simulator
	// and not to the mine, so nothing that decides processing order or solves
	// for a location may read it.
	Truth domain.Point

	// Magnitude is how large it really was, in mN. Ground truth, like Truth;
	// the mine reads it only through each pick's own reading.
	Magnitude float64

	// Picks are the detections, first arrival first. Picks[k] is the pick that
	// job FirstJob+k processes.
	Picks    []seismic.Pick
	FirstJob domain.JobID

	// Activity is what produced it in a worked mine — "work", "blast" or
	// "background" — and empty in a scenario that does not say how its mine
	// is worked, or for a burst's events. Near is the face work and blasts
	// happen around; Blast the index in Workload.Blasts of the blast it
	// follows, -1 otherwise. Ground truth, like Truth.
	Activity string
	Near     domain.Point
	Blast    int
}

// Generate produces the jobs for one scenario, deterministically from its
// seed. It is Build without the mine, for callers that only replay work.
func Generate(mine domain.Mine, scenario domain.Scenario) ([]Job, error) {
	workload, err := Build(mine, scenario)
	if err != nil {
		return nil, err
	}
	return workload.Jobs, nil
}

// Build produces the jobs for one scenario and the mine they came from,
// deterministically from its seed.
//
// Determinism is the point. Two runs of the same scenario replay exactly the
// same jobs, so a difference in results can be attributed to the settings that
// changed rather than to the workload having been different. The generator
// uses explicitly seeded PCG sources rather than the package-level random
// number generator, which shares global state and would make one run's output
// depend on what else the process happened to be doing.
//
// Jobs arrive at an event's origin time rather than at each pick's arrival.
// The difference is under a second across the whole mine, far below any
// decision interval, and keeping it is what keeps every scenario recorded
// before geometry existed replaying the identical jobs.
func Build(mine domain.Mine, scenario domain.Scenario) (Workload, error) {
	if err := mine.Validate(); err != nil {
		return Workload{}, err
	}
	if err := scenario.Validate(); err != nil {
		return Workload{}, err
	}

	random := rand.New(rand.NewPCG(uint64(scenario.Seed), jobStream))
	geometry := rand.New(rand.NewPCG(uint64(scenario.Seed), geometryStream))
	noise := rand.New(rand.NewPCG(uint64(scenario.Seed), noiseStream))
	sizes := rand.New(rand.NewPCG(uint64(scenario.Seed), magnitudeStream))
	mainShockDrawn := make([]bool, len(scenario.Bursts))

	// A worked mine's events come from its working, which needs the plan of
	// the mine to know its faces; every other scenario draws them as it always
	// has, and derives the layout only once it knows the scenario is not too
	// large to replay.
	var (
		events     []time.Duration
		worked     []happening
		blasts     []activity.BlastAt
		plan       *activity.Plan
		layout     domain.Layout
		haveLayout bool
		err        error
	)
	if scenario.Activity == nil {
		events, err = seismicEvents(mine, scenario, random)
	} else {
		layout, haveLayout = layoutOf(mine), true
		working := rand.New(rand.NewPCG(uint64(scenario.Seed), activityStream))
		var made activity.Plan
		if made, err = planOf(scenario, layout, working); err == nil {
			plan = &made
			blasts = made.Blasts()
			worked = workedEvents(mine, scenario, made, working)
			for _, h := range worked {
				events = append(events, h.at)
			}
		}
	}
	if err != nil {
		return Workload{}, err
	}

	priorities, weights := priorityTable(scenario)

	// Each detecting sensor contributes one pick, so array size is what turns
	// an event into an amount of work. Checked before a layout is derived,
	// which costs time in the square of the sensor count.
	estimated := len(events) * int(math.Ceil(float64(mine.Sensors)*detectionProbability))
	if estimated > maxJobs {
		return Workload{}, fmt.Errorf("this scenario would generate about %d jobs, which is too "+
			"large to replay: reduce the duration, the burst magnitude, the sensor "+
			"count or the background rate", estimated)
	}

	if !haveLayout {
		layout = layoutOf(mine)
	}
	epicentres, err := burstEpicentres(scenario, layout, geometry)
	if err != nil {
		return Workload{}, err
	}

	out := Workload{
		Jobs:     make([]Job, 0, estimated),
		Layout:   layout,
		Model:    Rock,
		Entities: workforce(layout, scenario, plan),
		Blasts:   blasts,
	}
	for k, at := range events {
		picks := 0
		for sensor := 0; sensor < mine.Sensors; sensor++ {
			if random.Float64() < detectionProbability {
				picks++
			}
		}
		if picks == 0 {
			// An event nothing detected is an event that produced no work.
			continue
		}

		// The count above is the draw the job stream has always made. The
		// geometry only decides which sensors those are — the nearest — and
		// draws from streams of its own, so no job moves.
		var (
			source *int
			truth  domain.Point
			worker = activity.Source{Blast: -1}
		)
		if worked == nil {
			source = sourceOf(mine, scenario, at, geometry)
			truth = placeEvent(source, epicentres, layout, geometry)
		} else {
			source = worked[k].burst
			truth = placeWorked(worked[k], scenario.Activity.Spread, epicentres, layout, geometry)
			worker = worked[k].source
		}
		detecting := nearest(layout.Sensors, truth, picks)

		magnitude := magnitudeOf(source, scenario, mainShockDrawn, sizes)
		event := Event{
			Origin:    at,
			Burst:     source,
			Truth:     truth,
			Magnitude: magnitude,
			Picks:     Rock.Picks(seismic.Event{At: truth, Origin: at}, detecting, scenario.PickJitter, noise),
			FirstJob:  domain.JobID(len(out.Jobs)),
			Blast:     worker.Blast,
		}
		if worked != nil && source == nil {
			event.Activity, event.Near = worker.Kind.String(), worker.Near
		}
		for k := range event.Picks {
			event.Picks[k].Magnitude = magnitude + sizes.NormFloat64()*stationScatter
		}
		index := len(out.Events)
		out.Events = append(out.Events, event)

		for pick := 0; pick < picks; pick++ {
			// The draws are made either way, and only their meaning changes.
			// A scenario with a pipeline and one without then produce exactly
			// the same events, from the same seed, so a comparison between
			// them isolates the workflow rather than also moving the rock.
			priority := samplePriority(priorities, weights, random)
			seconds := sampleDuration(scenario.JobSeconds, random)
			if scenario.Pipeline != nil {
				priority = scenario.Pipeline.Pick.Priority
				// Rescaled rather than redrawn: a second draw would consume
				// the stream and move every later event.
				seconds = seconds / scenario.JobSeconds * scenario.Pipeline.Pick.Seconds
			}
			out.Jobs = append(out.Jobs, Job{
				SubmittedAt: at,
				Priority:    priority,
				Seconds:     seconds,
				Event:       index,
			})
			if len(out.Jobs) > maxJobs {
				return Workload{}, fmt.Errorf("this scenario generated more than %d jobs, which is "+
					"too large to replay", maxJobs)
			}
		}
	}

	if scenario.Encounters != nil {
		encountered(&out, mine, scenario)
	}

	// Identity by position, assigned once the whole list exists. Jobs are
	// appended in event order and never reordered here — scripted encounters
	// are merged back into that order before this — so this is a function of
	// the seed and nothing else, which is what lets two runs of one scenario
	// be compared job for job.
	for i := range out.Jobs {
		out.Jobs[i].ID = domain.JobID(i)
	}
	return out, nil
}

// Encounter marks an event that was scripted rather than drawn: what produced
// it, as `work` and `blast` say for a worked mine.
const Encounter = "encounter"

// encountered scripts a scenario's encounters into a workload: one event each,
// with the picks the array would have made of it and the jobs those picks are,
// merged back into the day in the order things happened.
//
// Drawn from a stream of its own, after the day it is scripted into, so the
// day is the day that seed always gave — the same events, in the same places,
// with the same jobs — and the encounters are additions to it.
func encountered(out *Workload, mine domain.Mine, scenario domain.Scenario) {
	spec := *scenario.Encounters
	random := rand.New(rand.NewPCG(uint64(scenario.Seed), encounterStream))
	priorities, weights := priorityTable(scenario)

	for _, e := range scripted(spec, out.Entities, out.Layout.Extent, scenario.Duration, random) {
		picks := 0
		for sensor := 0; sensor < mine.Sensors; sensor++ {
			if random.Float64() < detectionProbability {
				picks++
			}
		}
		if picks == 0 {
			continue // an event nothing detected is an event that produced no work
		}
		detecting := nearest(out.Layout.Sensors, e.truth, picks)
		event := Event{
			Origin:    e.at,
			Truth:     e.truth,
			Magnitude: spec.Magnitude,
			Picks:     Rock.Picks(seismic.Event{At: e.truth, Origin: e.at}, detecting, scenario.PickJitter, random),
			Activity:  Encounter,
			Blast:     -1,
		}
		for k := range event.Picks {
			event.Picks[k].Magnitude = spec.Magnitude + random.NormFloat64()*stationScatter
		}
		index := len(out.Events)
		out.Events = append(out.Events, event)
		for pick := 0; pick < picks; pick++ {
			priority := samplePriority(priorities, weights, random)
			seconds := sampleDuration(scenario.JobSeconds, random)
			if scenario.Pipeline != nil {
				priority = scenario.Pipeline.Pick.Priority
				seconds = seconds / scenario.JobSeconds * scenario.Pipeline.Pick.Seconds
			}
			out.Jobs = append(out.Jobs, Job{SubmittedAt: e.at, Priority: priority, Seconds: seconds, Event: index})
		}
	}
	inTimeOrder(out)
}

// inTimeOrder puts events back in the order they happened and jobs back in the
// order they were submitted, and points each event at its first job again.
//
// Everything the run engine and the catalogue read depends on both: the queue
// plays jobs as they arrive, and an event's picks are the jobs from FirstJob
// on. A stable sort leaves a workload already in order exactly as it was.
func inTimeOrder(out *Workload) {
	order := make([]int, len(out.Events))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return out.Events[order[a]].Origin < out.Events[order[b]].Origin })
	events := make([]Event, len(out.Events))
	position := make([]int, len(out.Events))
	for to, from := range order {
		events[to], position[from] = out.Events[from], to
	}
	out.Events = events
	for i := range out.Jobs {
		out.Jobs[i].Event = position[out.Jobs[i].Event]
	}
	sort.SliceStable(out.Jobs, func(a, b int) bool { return out.Jobs[a].SubmittedAt < out.Jobs[b].SubmittedAt })

	found := make([]bool, len(out.Events))
	for i, job := range out.Jobs {
		if !found[job.Event] {
			out.Events[job.Event].FirstJob, found[job.Event] = domain.JobID(i), true
		}
	}
}

// layoutOf is the mine's stated layout, or one derived from its id: a plan of
// tunnels, and the sensors installed in them.
//
// From the id rather than the scenario's seed, because neither the tunnels nor
// the sensors move between scenarios: two scenarios on one mine that drew
// them differently would be comparing two mines.
func layoutOf(mine domain.Mine) domain.Layout {
	if mine.Layout != nil {
		return *mine.Layout
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(mine.ID))
	random := rand.New(rand.NewPCG(hash.Sum64(), layoutStream))
	tunnels := mineplan.Tunnels(defaultExtent, random)
	return domain.Layout{
		Extent:  defaultExtent,
		Sensors: mineplan.Sensors(tunnels, mine.Sensors, random),
		Tunnels: tunnels,
	}
}

// CheckGeometry refuses a scenario whose bursts are stated to happen outside
// the mine it runs on. Build refuses it too; this is for saying so earlier,
// without generating a workload to find out.
func CheckGeometry(mine domain.Mine, scenario domain.Scenario) error {
	extent := defaultExtent
	if mine.Layout != nil {
		extent = mine.Layout.Extent
	}
	for i, burst := range scenario.Bursts {
		if err := epicentreProblem(i, burst, extent); err != nil {
			return err
		}
	}
	return nil
}

func epicentreProblem(i int, burst domain.Burst, extent domain.Extent) error {
	if burst.Epicentre == nil || extent.Contains(*burst.Epicentre) {
		return nil
	}
	return fmt.Errorf("burst %d has its epicentre at (%v, %v, %v), outside the mine, "+
		"which spans (%v, %v, %v) to (%v, %v, %v)", i,
		burst.Epicentre.X, burst.Epicentre.Y, burst.Epicentre.Z,
		extent.Min.X, extent.Min.Y, extent.Min.Z, extent.Max.X, extent.Max.Y, extent.Max.Z)
}

// burstEpicentres is where each burst happened: as stated, or drawn.
//
// A candidate is drawn for every burst whether or not one is stated, so that
// stating the epicentre of one burst moves that burst and nothing else.
func burstEpicentres(scenario domain.Scenario, layout domain.Layout, random *rand.Rand) ([]domain.Point, error) {
	extent := layout.Extent
	out := make([]domain.Point, len(scenario.Bursts))
	for i, burst := range scenario.Bursts {
		out[i] = mineplan.NearWorkings(layout.Tunnels, extent, burstSpread, random)
		if burst.Epicentre == nil {
			continue
		}
		if err := epicentreProblem(i, burst, extent); err != nil {
			return nil, err
		}
		out[i] = *burst.Epicentre
	}
	return out, nil
}

// sourceOf decides whether an event belongs to a burst or to background
// activity, in proportion to what each contributed to the rate at that
// instant. It is the same rate the event's time was drawn from, so an event in
// the first minute of a burst almost always belongs to it and one an hour into
// a quiet shift almost never does.
func sourceOf(mine domain.Mine, scenario domain.Scenario, at time.Duration, random *rand.Rand) *int {
	background := mine.BackgroundRate * diurnal(at)
	total := background
	for _, burst := range scenario.Bursts {
		total += burstRate(mine.BackgroundRate, burst, at)
	}

	draw := random.Float64() * total
	if draw < background || total <= 0 {
		return nil
	}
	draw -= background
	for i, burst := range scenario.Bursts {
		draw -= burstRate(mine.BackgroundRate, burst, at)
		if draw < 0 {
			index := i
			return &index
		}
	}
	// Rounding left the draw a hair past the last share; it belongs to the
	// last burst contributing anything.
	for i := len(scenario.Bursts) - 1; i >= 0; i-- {
		if burstRate(mine.BackgroundRate, scenario.Bursts[i], at) > 0 {
			index := i
			return &index
		}
	}
	return nil
}

// magnitudeOf is how large an event is: the burst's main shock for the first
// event a burst produces, and a Gutenberg–Richter draw otherwise.
func magnitudeOf(source *int, scenario domain.Scenario, mainShockDrawn []bool, random *rand.Rand) float64 {
	if source == nil {
		return gutenbergRichter(smallestMagnitude, largestBackground, random)
	}
	main := defaultMainMagnitude
	if stated := scenario.Bursts[*source].MainMagnitude; stated != nil {
		main = *stated
	}
	if !mainShockDrawn[*source] {
		mainShockDrawn[*source] = true
		return main
	}
	return gutenbergRichter(smallestMagnitude, main-aftershockGap, random)
}

// gutenbergRichter draws a magnitude between low and high with b-value
// gutenbergRichterB, by inverting the truncated distribution.
func gutenbergRichter(low, high float64, random *rand.Rand) float64 {
	if high <= low {
		return low
	}
	span := 1 - math.Pow(10, -gutenbergRichterB*(high-low))
	return low - math.Log10(1-random.Float64()*span)/gutenbergRichterB
}

// placeEvent is where in the rock an event happened: around its burst's
// epicentre, or for background activity somewhere around the workings.
func placeEvent(source *int, epicentres []domain.Point, layout domain.Layout, random *rand.Rand) domain.Point {
	extent := layout.Extent
	if source == nil {
		return mineplan.NearWorkings(layout.Tunnels, extent, backgroundSpread, random)
	}
	centre := epicentres[*source]
	return extent.Clamp(domain.Point{
		X: centre.X + random.NormFloat64()*aftershockSpread,
		Y: centre.Y + random.NormFloat64()*aftershockSpread,
		Z: centre.Z + random.NormFloat64()*aftershockSpread,
	})
}

// nearest is the count sensors closest to a point, closest first. Ties go to
// the sensor listed first, so the choice never depends on sort internals.
func nearest(sensors []domain.Sensor, to domain.Point, count int) []domain.Sensor {
	order := make([]int, len(sensors))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return sensors[order[a]].At.DistanceTo(to) < sensors[order[b]].At.DistanceTo(to)
	})
	if count > len(order) {
		count = len(order)
	}
	out := make([]domain.Sensor, count)
	for i := range out {
		out[i] = sensors[order[i]]
	}
	return out
}

// seismicEvents draws event times from a rate that varies over the scenario.
//
// The method is thinning: generate candidates at the highest rate the scenario
// ever reaches, then keep each one with probability rate(t)/peak. It is the
// standard way to sample a non-homogeneous Poisson process, and unlike
// stepping through time in fixed increments it neither loses events inside a
// burst nor spends most of its work on the quiet hours.
func seismicEvents(mine domain.Mine, scenario domain.Scenario, random *rand.Rand) ([]time.Duration, error) {
	peak := peakRate(mine, scenario)
	if peak <= 0 {
		// Nothing ever happens: a mine with no background activity and no
		// bursts is a valid, if dull, scenario.
		return nil, nil
	}
	perSecond := peak / 3600

	var events []time.Duration
	at := 0.0
	limit := scenario.Duration.Seconds()

	for {
		// Exponential inter-arrival at the peak rate.
		at += random.ExpFloat64() / perSecond
		if at >= limit {
			break
		}
		elapsed := time.Duration(at * float64(time.Second))
		if random.Float64() < rateAt(mine, scenario, elapsed)/peak {
			events = append(events, elapsed)
		}
		if len(events) > maxJobs {
			return nil, fmt.Errorf("this scenario generates more than %d seismic events, "+
				"which is too large to replay", maxJobs)
		}
	}
	return events, nil
}

// rateAt is the event rate, in events per hour, at one instant.
func rateAt(mine domain.Mine, scenario domain.Scenario, elapsed time.Duration) float64 {
	rate := mine.BackgroundRate * diurnal(elapsed)
	for _, burst := range scenario.Bursts {
		rate += burstRate(mine.BackgroundRate, burst, elapsed)
	}
	return rate
}

// diurnal modulates the background rate over a working day. Blasting and
// heavy extraction cluster on shift, and the baseline follows.
func diurnal(elapsed time.Duration) float64 {
	hours := elapsed.Hours()
	return 1 + diurnalAmplitude*math.Sin(2*math.Pi*(hours/24-0.25))
}

// burstRate is one event's contribution: a spike at the moment it happens,
// followed by an Omori-law aftershock tail.
func burstRate(background float64, burst domain.Burst, elapsed time.Duration) float64 {
	if elapsed < burst.At {
		return 0
	}
	// A burst on a mine with no background activity must still produce work,
	// so the magnitude is scaled against a floor rather than against zero.
	base := math.Max(background, 1)
	excess := base * (burst.Magnitude - 1)
	if excess <= 0 {
		return 0
	}

	since := elapsed - burst.At
	if burst.AftershockDecay <= 0 {
		// No aftershock sequence: the event is over almost at once. A minute
		// of elevated rate, so the flood is not a single instant nothing can
		// be sampled from.
		if since <= time.Minute {
			return excess
		}
		return 0
	}

	// Omori's law: the aftershock rate falls off as 1/t^p. The decay setting
	// is the timescale, so a three-hour decay still reads as clearly elevated
	// an hour later and close to background after a day.
	scaled := since.Seconds() / burst.AftershockDecay.Seconds()
	return excess / math.Pow(1+scaled*10, omoriExponent)
}

// peakRate is the highest rate the scenario reaches, which thinning needs as
// its envelope. It is computed by evaluating at the moments a peak can occur —
// each burst's own instant — plus the diurnal maximum, rather than by scanning
// the whole scenario.
func peakRate(mine domain.Mine, scenario domain.Scenario) float64 {
	peak := mine.BackgroundRate * (1 + diurnalAmplitude)
	for _, burst := range scenario.Bursts {
		if rate := rateAt(mine, scenario, burst.At); rate > peak {
			peak = rate
		}
	}
	return peak
}

// priorityTable turns the mix into a sorted, cumulative table so sampling is
// reproducible. Iterating the map directly would make the workload depend on
// Go's randomised map order, and two runs of one scenario would differ.
func priorityTable(scenario domain.Scenario) ([]domain.Priority, []float64) {
	priorities := scenario.SortedPriorities()

	cumulative := make([]float64, len(priorities))
	total := 0.0
	for i, priority := range priorities {
		total += scenario.PriorityMix[priority]
		cumulative[i] = total
	}
	for i := range cumulative {
		cumulative[i] /= total
	}
	return priorities, cumulative
}

func samplePriority(priorities []domain.Priority, cumulative []float64, random *rand.Rand) domain.Priority {
	draw := random.Float64()
	for i, threshold := range cumulative {
		if draw <= threshold {
			return priorities[i]
		}
	}
	return priorities[len(priorities)-1]
}

// sampleDuration spreads job durations around the scenario's nominal figure.
//
// Log-normal, because processing time is bounded below by zero and has a long
// right tail — a few jobs take far longer than typical, and those are the ones
// that hold an executor while the queue behind them ages.
func sampleDuration(nominal float64, random *rand.Rand) float64 {
	const sigma = 0.35
	// Shifting by -sigma^2/2 keeps the mean at the nominal value rather than
	// letting the tail drag it upward.
	return nominal * math.Exp(sigma*random.NormFloat64()-sigma*sigma/2)
}
