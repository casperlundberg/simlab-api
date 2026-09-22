package usecase

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// A unit walking east along a drive at 1 m/s from x=0, reaching x=400 at 400 s,
// then standing there.
var eastward = []domain.Waypoint{
	{At: 0, Point: domain.Point{}},
	{At: 400 * time.Second, Point: domain.Point{X: 400}},
	{At: 24 * time.Hour, Point: domain.Point{X: 400}},
}

func TestAUnitEntersASphereWhereItsTrackFirstReachesTheSurface(t *testing.T) {
	// A sphere of 50 m around x=300: the surface is first reached at x=250.
	at, ok := firstEntry(eastward, domain.Point{X: 300}, 50, 0, time.Hour)
	if !ok || math.Abs(at.Seconds()-250) > 1e-6 {
		t.Errorf("firstEntry() = %v, %v; want 250 s", at, ok)
	}
}

func TestAUnitThatNeverComesWithinTheRadiusNeverEnters(t *testing.T) {
	if at, ok := firstEntry(eastward, domain.Point{X: 200, Y: 60}, 50, 0, time.Hour); ok {
		t.Errorf("firstEntry() = %v; the track passes 60 m from the centre of a 50 m sphere", at)
	}
}

func TestOnlyEntriesInsideTheWindowCount(t *testing.T) {
	if at, ok := firstEntry(eastward, domain.Point{X: 300}, 50, 0, 200*time.Second); ok {
		t.Errorf("firstEntry() = %v; the unit reaches the sphere at 250 s, after the window ends at 200 s", at)
	}
	// Starting the search after it is already inside is not an entry.
	if at, ok := firstEntry(eastward, domain.Point{X: 300}, 50, 260*time.Second, time.Hour); ok {
		t.Errorf("firstEntry() = %v; at 260 s the unit is already inside", at)
	}
}

func TestAUnitInsideLeavesWhereItsTrackFirstCrossesTheSurface(t *testing.T) {
	// Starting at x=0 inside a 50 m sphere around x=20: it leaves at x=70.
	at, ok := firstExit(eastward, domain.Point{X: 20}, 50, 0, time.Hour)
	if !ok || math.Abs(at.Seconds()-70) > 1e-6 {
		t.Errorf("firstExit() = %v, %v; want 70 s", at, ok)
	}
	// Standing at x=400 inside a sphere around x=390, it never leaves.
	if at, ok := firstExit(eastward, domain.Point{X: 390}, 50, 500*time.Second, time.Hour); ok {
		t.Errorf("firstExit() = %v; a unit standing inside never leaves", at)
	}
}

// Held against sampling the track finely: the entry found is inside, and no
// sampled moment before it is.
func TestFirstEntryAgreesWithSamplingTheTrack(t *testing.T) {
	random := rand.New(rand.NewPCG(4, 4))
	for trial := 0; trial < 300; trial++ {
		var track []domain.Waypoint
		at, p := time.Duration(0), domain.Point{}
		for k := 0; k < 12; k++ {
			track = append(track, domain.Waypoint{At: at, Point: p})
			at += time.Duration(10+random.IntN(120)) * time.Second
			p = domain.Point{X: p.X + random.Float64()*200 - 100, Y: p.Y + random.Float64()*200 - 100, Z: p.Z}
		}
		centre := domain.Point{X: random.Float64()*400 - 200, Y: random.Float64()*400 - 200}
		radius := 20 + random.Float64()*80
		from := time.Duration(random.Int64N(int64(at / 2)))
		until := from + time.Duration(random.Int64N(int64(at-from)))
		entry, ok := firstEntry(track, centre, radius, from, until)
		entity := domain.Entity{Track: track}
		if entity.PositionAt(from).DistanceTo(centre) <= radius {
			if ok {
				t.Fatalf("trial %d: an entry at %v for a unit already inside at %v", trial, entry, from)
			}
			continue
		}
		step := 100 * time.Millisecond
		for s := from; s <= until; s += step {
			inside := entity.PositionAt(s).DistanceTo(centre) <= radius-1e-6
			if inside && (!ok || s < entry-step) {
				t.Fatalf("trial %d: inside at %v, but firstEntry() = %v, %v", trial, s, entry, ok)
			}
		}
		if ok && math.Abs(entity.PositionAt(entry).DistanceTo(centre)-radius) > 1e-6 {
			t.Fatalf("trial %d: at the entry %v the unit is %.4f m from the centre, not on the surface",
				trial, entry, entity.PositionAt(entry).DistanceTo(centre))
		}
	}
}
