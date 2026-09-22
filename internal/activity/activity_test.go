package activity_test

import (
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/activity"
	"github.com/casperlundberg/simlab-api/internal/domain"
)

// ten faces on a line, a hundred metres apart.
func faces() []domain.Point {
	var out []domain.Point
	for i := 0; i < 10; i++ {
		out = append(out, domain.Point{X: float64(i) * 100, Z: -500})
	}
	return out
}

func plan(t *testing.T, spec domain.ActivitySpec, duration time.Duration, seed uint64) activity.Plan {
	t.Helper()
	p, err := activity.NewPlan(spec, faces(), duration, rand.New(rand.NewPCG(seed, 1)))
	if err != nil {
		t.Fatalf("NewPlan() = %v", err)
	}
	return p
}

func contains(points []domain.Point, p domain.Point) bool {
	for _, q := range points {
		if q == p {
			return true
		}
	}
	return false
}

func TestEachShiftWorksAsManyDistinctFacesAsAsked(t *testing.T) {
	p := plan(t, domain.DefaultActivity(), 48*time.Hour, 1)
	var shifts [][]domain.Point
	for at := time.Duration(0); at < 48*time.Hour; at += 12 * time.Hour {
		active := p.ActiveAt(at)
		if len(active) != 4 {
			t.Fatalf("%d faces worked at %v, want 4", len(active), at)
		}
		seen := map[domain.Point]bool{}
		for _, f := range active {
			if seen[f] || !contains(faces(), f) {
				t.Fatalf("at %v the faces worked are %v: each must be a distinct face of the mine", at, active)
			}
			seen[f] = true
		}
		if !reflect.DeepEqual(p.ActiveAt(at+11*time.Hour), active) {
			t.Errorf("the faces worked changed within the shift starting %v", at)
		}
		shifts = append(shifts, active)
	}
	changed := false
	for i := 1; i < len(shifts); i++ {
		changed = changed || !reflect.DeepEqual(shifts[i], shifts[i-1])
	}
	if !changed {
		t.Error("four shifts worked the same faces; work should move on")
	}
}

func TestMoreAreasThanTheMineHasFacesIsRefused(t *testing.T) {
	spec := domain.DefaultActivity()
	spec.Areas = 11
	_, err := activity.NewPlan(spec, faces(), time.Hour, rand.New(rand.NewPCG(1, 1)))
	if err == nil || !strings.Contains(err.Error(), "activity.areas") || !strings.Contains(err.Error(), "10") {
		t.Errorf("NewPlan() = %v; want areas refused, naming the 10 faces there are", err)
	}
}

func TestBlastsFireInTheirWindowEachDayAtAFaceBeingWorked(t *testing.T) {
	p := plan(t, domain.DefaultActivity(), 48*time.Hour, 2)
	blasts := p.Blasts()
	// 01:15 to 01:45 every ten minutes: four a day.
	var want []time.Duration
	for day := time.Duration(0); day < 48*time.Hour; day += 24 * time.Hour {
		for k := 0; k < 4; k++ {
			want = append(want, day+time.Hour+15*time.Minute+time.Duration(k)*10*time.Minute)
		}
	}
	if len(blasts) != len(want) {
		t.Fatalf("%d blasts in two days, want %d", len(blasts), len(want))
	}
	for i, b := range blasts {
		if b.At != want[i] {
			t.Errorf("blast %d at %v, want %v", i, b.At, want[i])
		}
		if !contains(p.ActiveAt(b.At), b.Face) {
			t.Errorf("blast %d at %v is at %v, which is not being worked then", i, b.At, b.Face)
		}
	}
}

func TestAWindowOfZeroIsOneBlastADay(t *testing.T) {
	spec := domain.DefaultActivity()
	spec.Blasting.Start, spec.Blasting.Window, spec.Blasting.Every = 14*time.Hour, 0, 0
	blasts := plan(t, spec, 72*time.Hour, 3).Blasts()
	if len(blasts) != 3 || blasts[0].At != 14*time.Hour || blasts[2].At != 62*time.Hour {
		t.Errorf("blasts = %+v; want one at 14:00 each day", blasts)
	}
}

func sources(t *testing.T, spec domain.ActivitySpec, duration time.Duration, seed uint64) (activity.Plan, []activity.Source) {
	t.Helper()
	p := plan(t, spec, duration, seed)
	return p, p.Sources(91, duration, rand.New(rand.NewPCG(seed, 2)))
}

func TestTheDayKeepsTheMinesRateAndTheMixItsShares(t *testing.T) {
	_, got := sources(t, domain.DefaultActivity(), 96*time.Hour, 4)
	expected := 91.0 * 96
	if math.Abs(float64(len(got))-expected) > 5*math.Sqrt(expected) {
		t.Errorf("%d events over four days at 91 an hour, want about %.0f", len(got), expected)
	}
	counts := map[activity.Kind]float64{}
	for _, s := range got {
		counts[s.Kind]++
	}
	for kind, share := range map[activity.Kind]float64{activity.Blast: 0.3, activity.Work: 0.5, activity.Background: 0.2} {
		if got := counts[kind] / float64(len(got)); math.Abs(got-share) > 0.03 {
			t.Errorf("%v: %.3f of events, want %.2f", kind, got, share)
		}
	}
}

func TestWorkHappensAroundTheFacesBeingWorkedAndBlastsAfterTheirBlast(t *testing.T) {
	p, got := sources(t, domain.DefaultActivity(), 48*time.Hour, 5)
	blasts := p.Blasts()
	for i, s := range got {
		if i > 0 && s.At < got[i-1].At {
			t.Fatalf("sources out of time order at %d", i)
		}
		switch s.Kind {
		case activity.Work:
			if !contains(p.ActiveAt(s.At), s.Near) || s.Blast != -1 {
				t.Fatalf("work at %v near %v, which is not being worked then", s.At, s.Near)
			}
		case activity.Blast:
			b := blasts[s.Blast]
			if s.At < b.At || s.At > b.At+12*time.Hour || s.Near != b.Face {
				t.Fatalf("an event of blast %d (%v at %v) at %v near %v", s.Blast, b.At, b.Face, s.At, s.Near)
			}
		case activity.Background:
			if s.Blast != -1 {
				t.Fatalf("a background event tied to blast %d", s.Blast)
			}
		}
	}
}

// Omori's law with p = 1: the share of a sequence within the first hour of
// twelve is ln((c+1h)/c) / ln((c+12h)/c).
func TestABlastsSequenceDecaysAsOmoriSays(t *testing.T) {
	spec := domain.DefaultActivity()
	spec.Mix = domain.ActivityMix{Blast: 1}
	p, got := sources(t, spec, 20*24*time.Hour, 6)
	blasts := p.Blasts()
	first, all := 0.0, 0.0
	for _, s := range got {
		since := s.At - blasts[s.Blast].At
		if blasts[s.Blast].At+12*time.Hour > 20*24*time.Hour {
			continue // a sequence the scenario cuts short
		}
		all++
		if since <= time.Hour {
			first++
		}
	}
	c, hour, length := (5 * time.Minute).Seconds(), time.Hour.Seconds(), (12 * time.Hour).Seconds()
	want := math.Log((c+hour)/c) / math.Log((c+length)/c)
	if got := first / all; math.Abs(got-want) > 0.02 {
		t.Errorf("%.3f of each sequence in its first hour, want %.3f", got, want)
	}
}

func TestTheSameSeedGivesTheSameActivity(t *testing.T) {
	_, a := sources(t, domain.DefaultActivity(), 24*time.Hour, 7)
	_, b := sources(t, domain.DefaultActivity(), 24*time.Hour, 7)
	if !reflect.DeepEqual(a, b) {
		t.Error("the same seed drew different sources")
	}
}
