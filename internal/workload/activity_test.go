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
