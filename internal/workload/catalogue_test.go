package workload_test

import (
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
	"github.com/casperlundberg/simlab-api/internal/seismic"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// The catalogue is the mine keeping track of its own work. It learns that a
// pick was processed from the orchestrator, and from that alone decides when it
// has a location for an event — which is the moment an operator first has
// somewhere to point, and the moment the queue, and so the autoscaler, decides.

// eventWith returns the index of the first event with at least n picks.
func eventWith(t *testing.T, w workload.Workload, n int) int {
	t.Helper()
	for i, event := range w.Events {
		if len(event.Picks) >= n {
			return i
		}
	}
	t.Fatalf("no event has %d picks", n)
	return -1
}

func jobsOf(event workload.Event, from, to int) []domain.JobID {
	var ids []domain.JobID
	for k := from; k < to; k++ {
		ids = append(ids, event.FirstJob+domain.JobID(k))
	}
	return ids
}

func observe(t *testing.T, c *workload.Catalogue, at time.Duration, ids []domain.JobID) []domain.SeismicEvent {
	t.Helper()
	changed, err := c.Observe(at, ids)
	if err != nil {
		t.Fatalf("Observe() = %v", err)
	}
	return changed
}

func TestBeforeAnythingIsProcessedEveryEventIsDetectedAndNoneLocated(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	events := workload.NewCatalogue(w).Events()

	if len(events) != len(w.Events) {
		t.Fatalf("%d events catalogued, %d generated", len(events), len(w.Events))
	}
	for i, event := range events {
		if event.Sequence != i+1 {
			t.Fatalf("event %d has sequence %d", i, event.Sequence)
		}
		if event.LocatedAt != nil || event.ProcessedAt != nil {
			t.Fatalf("event %d is located before any work was done", event.Sequence)
		}
		if len(event.Sensors) != len(w.Events[i].Picks) || event.Sensors[0] != w.Events[i].Picks[0].SensorID {
			t.Fatalf("event %d names its sensors out of arrival order", event.Sequence)
		}
		if event.Truth != w.Events[i].Truth || event.Origin != w.Events[i].Origin {
			t.Fatalf("event %d does not carry where and when it happened", event.Sequence)
		}
	}
}

// Four is three coordinates and an origin time. Fewer processed picks leave
// the mine with nothing to point at, however many more are still queued.
func TestAnEventIsLocatedWhenItsFourthPickIsProcessed(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	i := eventWith(t, w, 8)
	catalogue := workload.NewCatalogue(w)

	if changed := observe(t, catalogue, 30*time.Second, jobsOf(w.Events[i], 0, 3)); len(changed) != 1 || changed[0].LocatedAt != nil {
		t.Fatalf("three picks should record three processed picks and no location, changed %+v", changed)
	}

	changed := observe(t, catalogue, 45*time.Second, jobsOf(w.Events[i], 3, 4))
	if len(changed) != 1 || changed[0].Sequence != i+1 {
		t.Fatalf("the fourth pick should locate event %d, changed %+v", i+1, changed)
	}
	event := changed[0]
	if event.LocatedAt == nil || *event.LocatedAt != 45*time.Second {
		t.Errorf("located at %v, want the moment the fourth pick was reported", event.LocatedAt)
	}
	if event.Located == nil || event.Located.Picks != 4 {
		t.Errorf("first location = %+v, want one solved from the 4 processed picks", event.Located)
	}
	if event.ProcessedAt != nil {
		t.Error("an event with picks still queued is not processed")
	}
}

// The first location is what the mine could say with what it had, not what it
// will eventually be able to say.
func TestTheFirstLocationUsesEveryPickProcessedByThenAndNoOthers(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	i := eventWith(t, w, 10)
	catalogue := workload.NewCatalogue(w)

	changed := observe(t, catalogue, time.Minute, jobsOf(w.Events[i], 2, 8))
	if len(changed) != 1 || changed[0].Located == nil || changed[0].Located.Picks != 6 {
		t.Fatalf("six picks processed together should locate from six, got %+v", changed)
	}
}

func TestAnEventIsProcessedWhenEveryPickIsAndGetsItsFinalLocation(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	i := eventWith(t, w, 6)
	all := len(w.Events[i].Picks)
	catalogue := workload.NewCatalogue(w)

	observe(t, catalogue, time.Minute, jobsOf(w.Events[i], 0, 5))
	if changed := observe(t, catalogue, 2*time.Minute, jobsOf(w.Events[i], 5, all-1)); len(changed) > 0 && changed[0].ProcessedAt != nil {
		t.Fatalf("an event one pick short was marked processed: %+v", changed)
	}
	changed := observe(t, catalogue, 3*time.Minute, jobsOf(w.Events[i], all-1, all))
	if len(changed) != 1 {
		t.Fatalf("the last pick should finish event %d, changed %+v", i+1, changed)
	}
	event := changed[0]
	if event.ProcessedAt == nil || *event.ProcessedAt != 3*time.Minute {
		t.Errorf("processed at %v, want 3m0s", event.ProcessedAt)
	}
	if event.Final == nil || event.Final.Picks != all {
		t.Errorf("final location = %+v, want one solved from all %d picks", event.Final, all)
	}
	if event.LocatedAt == nil || *event.LocatedAt != time.Minute {
		t.Errorf("the first location moved to %v; it happened at 1m0s", event.LocatedAt)
	}
}

// This is the rule the whole study depends on. If the mine's estimate could see
// where an event really was, any result about ordering work by that estimate
// would be assuming what it set out to show.
func TestALocationIsSolvedFromPicksAndNeverFromTheTruth(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	i := eventWith(t, w, 12)
	real := w.Events[i].Truth

	// Corrupt the ground truth after the picks were generated from it. A
	// catalogue reading the truth would now report the wrong place.
	w.Events[i].Truth = domain.Point{X: w.Layout.Extent.Min.X, Y: w.Layout.Extent.Min.Y, Z: w.Layout.Extent.Min.Z}

	catalogue := workload.NewCatalogue(w)
	changed := observe(t, catalogue, time.Minute, jobsOf(w.Events[i], 0, len(w.Events[i].Picks)))
	if len(changed) != 1 || changed[0].Final == nil {
		t.Fatalf("expected a final location, got %+v", changed)
	}
	if off := changed[0].Final.At.DistanceTo(real); off > 50 {
		t.Errorf("the solution is %.0f m from where the picks came from; it did not come from the picks", off)
	}
}

// The first sensors to detect an event are the nearest, so an event three
// sensors saw has nothing a solver can use — but its work still gets done, and
// an operator should still see that it has been.
func TestAnEventTooFewSensorsSawIsProcessedButNeverLocated(t *testing.T) {
	m, s := liveScenario()
	full := build(t, m, s)
	three := full.Events[0]
	three.Picks = three.Picks[:3]
	three.FirstJob = 0
	w := workload.Workload{
		Layout: full.Layout, Model: full.Model, Events: []workload.Event{three},
		Jobs: []workload.Job{{ID: 0, Event: 0}, {ID: 1, Event: 0}, {ID: 2, Event: 0}},
	}

	changed := observe(t, workload.NewCatalogue(w), time.Minute, []domain.JobID{0, 1, 2})
	if len(changed) != 1 || changed[0].ProcessedAt == nil {
		t.Fatalf("all three picks processed should mark the event processed, got %+v", changed)
	}
	if changed[0].LocatedAt != nil || changed[0].Located != nil || changed[0].Final != nil {
		t.Errorf("three picks produced a location: %+v", changed[0])
	}
}

func TestExactPicksLocateEventsCloseToWhereTheyHappened(t *testing.T) {
	m, s := verifyScenario()
	w := build(t, m, s)
	catalogue := workload.NewCatalogue(w)

	all := make([]domain.JobID, len(w.Jobs))
	for i := range all {
		all[i] = domain.JobID(i)
	}
	observe(t, catalogue, time.Hour, all)

	var errors []float64
	for _, event := range catalogue.Events() {
		if event.Final != nil {
			errors = append(errors, event.Final.At.DistanceTo(event.Truth))
		}
	}
	if len(errors) < len(w.Events)/2 {
		t.Fatalf("only %d of %d events located", len(errors), len(w.Events))
	}
	sort.Float64s(errors)
	if median := errors[len(errors)/2]; median > 30 {
		t.Errorf("exact picks locate a median %.0f m from the truth; the geometry is wired wrong", median)
	}
}

// Both of these are the engine and the queue disagreeing about what work
// exists. Absorbing either would locate events early or never, silently.
func TestAJobReportedFinishedTwiceIsRefused(t *testing.T) {
	m, s := liveScenario()
	catalogue := workload.NewCatalogue(build(t, m, s))
	observe(t, catalogue, time.Minute, []domain.JobID{7})

	if _, err := catalogue.Observe(2*time.Minute, []domain.JobID{7}); err == nil || !strings.Contains(err.Error(), "7") {
		t.Errorf("Observe() = %v, want job 7 named as reported twice", err)
	}
}

func TestAJobTheMineNeverSubmittedIsRefused(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	unknown := domain.JobID(len(w.Jobs))

	if _, err := workload.NewCatalogue(w).Observe(time.Minute, []domain.JobID{unknown}); err == nil ||
		!strings.Contains(err.Error(), "never submitted") {
		t.Errorf("Observe() = %v, want the unknown job refused", err)
	}
}

func TestTheMinimumIsFourPicks(t *testing.T) {
	if seismic.MinimumPicks != 4 {
		t.Errorf("MinimumPicks = %d; three coordinates and an origin time are four unknowns", seismic.MinimumPicks)
	}
}

// A sensor has work waiting while its own pick is unprocessed, not while any
// pick of any event it heard is. Recording only whole events, the view lit
// every sensor of an event until its last pick finished, and showed more busy
// sensors than there was work waiting on.
func TestEachPickRecordsWhenItWasProcessed(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	i := eventWith(t, w, 6)
	catalogue := workload.NewCatalogue(w)

	before := catalogue.Events()[i]
	if len(before.PickProcessedAt) != len(before.Sensors) {
		t.Fatalf("%d pick times for %d sensors", len(before.PickProcessedAt), len(before.Sensors))
	}
	for k, at := range before.PickProcessedAt {
		if at != nil {
			t.Fatalf("pick %d processed at %v before anything ran", k, *at)
		}
	}

	observe(t, catalogue, time.Minute, jobsOf(w.Events[i], 1, 3))
	changed := observe(t, catalogue, 2*time.Minute, jobsOf(w.Events[i], 4, 5))
	if len(changed) != 1 {
		t.Fatalf("a processed pick should update its event, changed %+v", changed)
	}
	got := changed[0].PickProcessedAt
	for k, want := range map[int]time.Duration{1: time.Minute, 2: time.Minute, 4: 2 * time.Minute} {
		if got[k] == nil || *got[k] != want {
			t.Errorf("pick %d (sensor %s) processed at %v, want %v", k, changed[0].Sensors[k], got[k], want)
		}
	}
	for _, k := range []int{0, 3, 5} {
		if got[k] != nil {
			t.Errorf("pick %d was never processed but reads %v", k, *got[k])
		}
	}
}

// Magnitude is estimated the way location is: from what the processed picks
// read, never from the truth. Each sensor's amplitude gives its own reading,
// with scatter, and the mine averages the ones it has.
func TestAMagnitudeIsEstimatedFromThePicksReadingsNeverTheTruth(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	i := eventWith(t, w, 10)
	readings := 0.0
	for _, pick := range w.Events[i].Picks[:6] {
		readings += pick.Magnitude
	}
	w.Events[i].Magnitude = 9 // corrupt the truth; the estimate must not move

	changed := observe(t, workload.NewCatalogue(w), time.Minute, jobsOf(w.Events[i], 0, 6))
	if len(changed) != 1 || changed[0].Located == nil || changed[0].Located.Magnitude == nil {
		t.Fatalf("six picks should give a located magnitude, got %+v", changed)
	}
	if got, want := *changed[0].Located.Magnitude, readings/6; math.Abs(got-want) > 1e-9 {
		t.Errorf("estimated magnitude %.3f, want the mean of the six readings %.3f", got, want)
	}
}

// More readings, less scatter: the final estimate is on average closer to the
// truth than the first.
func TestAMagnitudeEstimateSharpensAsPicksArrive(t *testing.T) {
	m, s := verifyScenario()
	w := build(t, m, s)
	catalogue := workload.NewCatalogue(w)
	all := make([]domain.JobID, 0, len(w.Jobs))
	firstFour := make([]domain.JobID, 0)
	for _, event := range w.Events {
		for k := range event.Picks {
			id := event.FirstJob + domain.JobID(k)
			if k < 4 {
				firstFour = append(firstFour, id)
			} else {
				all = append(all, id)
			}
		}
	}
	observe(t, catalogue, time.Minute, firstFour)
	observe(t, catalogue, time.Hour, all)

	first, final, n := 0.0, 0.0, 0
	for _, event := range catalogue.Events() {
		if event.Located == nil || event.Final == nil || event.Final.Picks < 12 {
			continue
		}
		first += math.Abs(*event.Located.Magnitude - *event.Magnitude)
		final += math.Abs(*event.Final.Magnitude - *event.Magnitude)
		n++
	}
	if n < 50 {
		t.Fatalf("only %d events to compare", n)
	}
	if final >= first {
		t.Errorf("mean magnitude error %.3f from all picks against %.3f from four", final/float64(n), first/float64(n))
	}
}

// Who is exposed is judged twice: by the simulator, from where an event really
// was and how big it really was, at the moment it happened; and by the mine,
// from its estimate, at the moment it had one. The distance between the two is
// what the study measures.
func TestExposureIsJudgedFromTheTruthAtTheEventAndFromTheEstimateWhenLocated(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)

	// Put a person right on the burst's main shock for the whole run.
	main := -1
	for i, event := range w.Events {
		if event.Burst != nil {
			main = i
			break
		}
	}
	if main < 0 {
		t.Fatal("no burst event")
	}
	truth := w.Events[main].Truth
	w.Entities = []domain.Entity{{ID: "person-99", Kind: domain.EntityPerson,
		Track: []domain.Waypoint{{At: 0, Point: truth}}}}

	catalogue := workload.NewCatalogue(w)
	event := catalogue.Events()[main]
	// At the hypocentre, the worst the event can do anywhere. For mN 2.5 that
	// is high, not very high: the design law holds ground motion at its value
	// at the near-field limit, 0.87 m/s at 16 m.
	worst := hazard.Default.LevelAt(*event.Magnitude, 0, 0).String()
	if !exposed(event.Exposed, "person-99", worst) {
		t.Errorf("a person at the true hypocentre of mN %.1f is not %s exposed by the truth: %+v",
			*event.Magnitude, worst, event.Exposed)
	}

	changed := observe(t, catalogue, 10*time.Minute, jobsOf(w.Events[main], 0, len(w.Events[main].Picks)))
	located := changed[0].Located
	if located == nil || len(located.Zones) == 0 {
		t.Fatalf("a located event has no zones: %+v", located)
	}
	if !exposed(located.Exposed, "person-99", "") {
		t.Errorf("a person on the event is not exposed by the mine's own estimate: %+v", located.Exposed)
	}
}

func exposed(list []domain.Exposure, entity, level string) bool {
	for _, e := range list {
		if e.Entity == entity && (level == "" || e.Level == level) {
			return true
		}
	}
	return false
}
