package workload_test

import (
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// A calibrated-sized mine worked as DefaultActivity says, over a day.
func worked(t *testing.T, seed int64, bursts ...domain.Burst) workload.Workload {
	t.Helper()
	a := domain.DefaultActivity()
	w, err := workload.Build(
		domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 30, BackgroundRate: 91},
		domain.Scenario{ID: "day", MineID: "storhall", Duration: 24 * time.Hour, JobSeconds: 28, Seed: seed,
			PriorityMix: map[domain.Priority]float64{100: 1, 50: 2, 0: 1}, Bursts: bursts, Activity: &a})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return w
}

func TestAWorkedMinesEventsClusterAroundTheFacesTheyComeFrom(t *testing.T) {
	w := worked(t, 1)
	var distances []float64
	for _, e := range w.Events {
		if e.Activity != "work" && e.Activity != "blast" {
			continue
		}
		distances = append(distances, e.Truth.DistanceTo(e.Near))
	}
	if len(distances) == 0 {
		t.Fatal("no events came from work or blasts")
	}
	sort.Float64s(distances)
	// With a spread of 75 m along each axis, 95 % of events lie within about
	// 2.8 x 75 = 210 m of their face; clamping to the mine can only bring
	// them nearer.
	if p95 := distances[len(distances)*95/100]; p95 > 250 {
		t.Errorf("95 %% of work and blast events lie within %.0f m of their face, want within 250", p95)
	}
}

func TestAWorkedMineKeepsItsRateAndItsMix(t *testing.T) {
	w := worked(t, 2)
	expected := 91.0 * 24
	if n := float64(len(w.Events)); math.Abs(n-expected) > 5*math.Sqrt(expected) {
		t.Errorf("%v events in a day at 91 an hour, want about %.0f", n, expected)
	}
	counts := map[string]float64{}
	for _, e := range w.Events {
		counts[e.Activity]++
	}
	for kind, share := range map[string]float64{"blast": 0.3, "work": 0.5, "background": 0.2} {
		if got := counts[kind] / float64(len(w.Events)); math.Abs(got-share) > 0.04 {
			t.Errorf("%s: %.3f of events, want %.2f", kind, got, share)
		}
	}
}

func TestAWorkedMineRecordsItsBlastsAndIsBusiestAfterThem(t *testing.T) {
	w := worked(t, 3)
	if len(w.Blasts) != 4 || w.Blasts[0].At != time.Hour+15*time.Minute {
		t.Fatalf("blasts = %+v; want the four of the night window, the first at 01:15", w.Blasts)
	}
	after, before := 0, 0
	for _, e := range w.Events {
		switch {
		case e.Origin >= w.Blasts[0].At && e.Origin < w.Blasts[0].At+time.Hour:
			after++
		case e.Origin >= 12*time.Hour && e.Origin < 13*time.Hour:
			before++
		}
	}
	if after < 3*before {
		t.Errorf("%d events in the hour after the first blast and %d in an hour at midday; the "+
			"hour after blasting should be several times busier", after, before)
	}
}

func TestBurstsStillHappenInAWorkedMine(t *testing.T) {
	w := worked(t, 4, domain.Burst{At: 8 * time.Hour, Magnitude: 30, AftershockDecay: 30 * time.Minute})
	bursts := 0
	for _, e := range w.Events {
		if e.Burst != nil {
			bursts++
			if e.Activity != "" {
				t.Fatalf("an event of the burst is also marked %q", e.Activity)
			}
		}
	}
	if bursts == 0 {
		t.Error("a burst of magnitude 30 in a worked mine produced no events")
	}
}

func TestTheSameSeedWorksTheMineTheSameWay(t *testing.T) {
	if !reflect.DeepEqual(worked(t, 5), worked(t, 5)) {
		t.Error("the same seed built two different worked mines")
	}
}

func TestAMineNobodyDescribedTheWorkingOfIsGeneratedAsBefore(t *testing.T) {
	m, s := liveScenario()
	for _, e := range build(t, m, s).Events {
		if e.Activity != "" || e.Blast != -1 {
			t.Fatalf("an event marked %q (blast %d) in a scenario with no activity", e.Activity, e.Blast)
		}
	}
}

// crews is everyone with people in them: people and crewed vehicles.
func crews(w workload.Workload) []domain.Entity {
	var out []domain.Entity
	for _, e := range w.Entities {
		if e.Kind == domain.EntityPerson || e.Kind == domain.EntityCrewedVehicle {
			out = append(out, e)
		}
	}
	return out
}

func TestNobodyIsNearAFaceWhenItIsBlasted(t *testing.T) {
	w := worked(t, 6)
	for _, b := range w.Blasts {
		for _, e := range crews(w) {
			if d := e.PositionAt(b.At).DistanceTo(b.Face); d < 150 {
				t.Errorf("%s is %.0f m from the face blasted at %v; the production areas are cleared first",
					e.ID, d, b.At)
			}
		}
	}
}

// facesNear reports whether a point is at one of the faces being worked.
func atAFace(p domain.Point, faces []domain.Point) bool {
	for _, f := range faces {
		if p.DistanceTo(f) < 30 {
			return true
		}
	}
	return false
}

func TestCrewsWorkAtTheFacesBeingWorkedAndComeBackAfterReEntry(t *testing.T) {
	w := worked(t, 7)
	active := activeFaces(t, w)
	at, total := 0, 0
	for s := 6 * time.Hour; s < 24*time.Hour; s += 10 * time.Minute {
		for _, e := range crews(w) {
			if e.Kind != domain.EntityPerson {
				continue
			}
			total++
			if atAFace(e.PositionAt(s), active(s)) {
				at++
			}
		}
	}
	if share := float64(at) / float64(total); share < 0.3 {
		t.Errorf("people are at a face being worked %.0f %% of the time outside blasting; crews work "+
			"there, so they should be most of the time they are not walking between them", share*100)
	}
	// Re-entry is three hours after the last blast at 01:45; an hour on, some
	// crew is back at work.
	back := false
	for _, e := range crews(w) {
		back = back || atAFace(e.PositionAt(5*time.Hour+45*time.Minute), active(5*time.Hour+45*time.Minute))
	}
	if !back {
		t.Error("an hour after re-entry nobody is back at a face being worked")
	}
}

// activeFaces is which faces were worked when, read from the events of work:
// every face some work event happened around, by the shift it happened in.
func activeFaces(t *testing.T, w workload.Workload) func(time.Duration) []domain.Point {
	t.Helper()
	byShift := map[int]map[domain.Point]bool{}
	for _, e := range w.Events {
		if e.Activity != "work" {
			continue
		}
		shift := int(e.Origin / (12 * time.Hour))
		if byShift[shift] == nil {
			byShift[shift] = map[domain.Point]bool{}
		}
		byShift[shift][e.Near] = true
	}
	return func(at time.Duration) []domain.Point {
		var out []domain.Point
		for f := range byShift[int(at/(12*time.Hour))] {
			out = append(out, f)
		}
		return out
	}
}
