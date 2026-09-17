package workload_test

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
)

func byKind(entities []domain.Entity) map[string][]domain.Entity {
	out := map[string][]domain.Entity{}
	for _, e := range entities {
		out[e.Kind] = append(out[e.Kind], e)
	}
	return out
}

func TestAScenarioWithNoStatedWorkforceHasTheDefaultOneUnderground(t *testing.T) {
	m, s := liveScenario()
	kinds := byKind(build(t, m, s).Entities)

	if len(kinds[domain.EntityPerson]) == 0 || len(kinds[domain.EntityCrewedVehicle]) == 0 ||
		len(kinds[domain.EntityAutonomousVehicle]) == 0 {
		t.Errorf("default workforce: %d people, %d crewed and %d autonomous vehicles",
			len(kinds[domain.EntityPerson]), len(kinds[domain.EntityCrewedVehicle]),
			len(kinds[domain.EntityAutonomousVehicle]))
	}
}

func TestAStatedWorkforceIsTheOneUnderground(t *testing.T) {
	m, s := liveScenario()
	s.Workforce = &domain.Workforce{People: 2, CrewedVehicles: 1}
	kinds := byKind(build(t, m, s).Entities)

	if len(kinds[domain.EntityPerson]) != 2 || len(kinds[domain.EntityCrewedVehicle]) != 1 ||
		len(kinds[domain.EntityAutonomousVehicle]) != 0 {
		t.Errorf("got %d people, %d crewed, %d autonomous; want 2, 1, 0",
			len(kinds[domain.EntityPerson]), len(kinds[domain.EntityCrewedVehicle]),
			len(kinds[domain.EntityAutonomousVehicle]))
	}
}

// Nothing walks or drives through rock.
func TestEveryoneStaysInTheTunnels(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	for _, e := range w.Entities {
		for at := time.Duration(0); at < s.Duration; at += 37 * time.Second {
			if d := mineplan.DistanceToTunnels(w.Layout.Tunnels, e.PositionAt(at)); d > 0.1 {
				t.Fatalf("%s is %.1f m into the rock at %v", e.ID, d, at)
			}
		}
	}
}

// The hoisting shaft is the only vertical opening, and nobody walks or drives
// up it.
func TestNobodyTravelsTheShaft(t *testing.T) {
	m, s := liveScenario()
	for _, e := range build(t, m, s).Entities {
		for i := 1; i < len(e.Track); i++ {
			a, b := e.Track[i-1].Point, e.Track[i].Point
			if a.X == b.X && a.Y == b.Y && a.Z != b.Z {
				t.Fatalf("%s rode the shaft from %.0f to %.0f", e.ID, a.Z, b.Z)
			}
		}
	}
}

// People on foot are slow; vehicles are not; and nothing exceeds what its kind
// can do.
func TestEachKindMovesAtItsOwnPace(t *testing.T) {
	m, s := liveScenario()
	fastest := map[string]float64{}
	for _, e := range build(t, m, s).Entities {
		for i := 1; i < len(e.Track); i++ {
			dt := (e.Track[i].At - e.Track[i-1].At).Seconds()
			if dt <= 0.2 {
				continue // rounding makes a very short leg's speed meaningless
			}
			v := e.Track[i-1].Point.DistanceTo(e.Track[i].Point) / dt
			fastest[e.Kind] = math.Max(fastest[e.Kind], v)
		}
	}
	for kind, limit := range map[string]float64{
		domain.EntityPerson: 1.6, domain.EntityCrewedVehicle: 5, domain.EntityAutonomousVehicle: 4,
	} {
		if fastest[kind] == 0 || fastest[kind] > limit {
			t.Errorf("fastest %s moved at %.2f m/s; want some movement under %.1f", kind, fastest[kind], limit)
		}
	}
	if fastest[domain.EntityPerson] >= fastest[domain.EntityCrewedVehicle] {
		t.Error("people on foot are as fast as vehicles")
	}
}

// A run goes on until its queue drains, which can be hours after its
// scenario ends. The workforce keeps moving for as long as anyone could watch.
func TestTracksRunWellPastTheEndOfTheScenario(t *testing.T) {
	m, s := liveScenario()
	for _, e := range build(t, m, s).Entities {
		if last := e.Track[len(e.Track)-1].At; last < s.Duration+12*time.Hour {
			t.Fatalf("%s stops moving at %v, an hour into a scenario that runs longer", e.ID, last)
		}
	}
}

// Autonomous haulers cycle between loading and tipping, which is what makes
// their exposure regular and a person's not.
func TestAutonomousVehiclesKeepReturningToTip(t *testing.T) {
	m, s := liveScenario()
	for _, e := range byKind(build(t, m, s).Entities)[domain.EntityAutonomousVehicle] {
		counts := map[domain.Point]int{}
		for i := 1; i < len(e.Track); i++ {
			if e.Track[i].Point == e.Track[i-1].Point {
				counts[e.Track[i].Point]++
			}
		}
		most := 0
		for _, n := range counts {
			most = max(most, n)
		}
		if most < 5 {
			t.Errorf("%s stopped at no place more than %d times in a day", e.ID, most)
		}
	}
}

// Workforce is a dial of its own. Adding a person moves nobody already there,
// and no job either.
func TestAddingSomeoneMovesNobodyElse(t *testing.T) {
	m, s := liveScenario()
	s.Workforce = &domain.Workforce{People: 3, CrewedVehicles: 2, AutonomousVehicles: 1}
	before := build(t, m, s)
	s.Workforce = &domain.Workforce{People: 4, CrewedVehicles: 2, AutonomousVehicles: 1}
	after := build(t, m, s)

	index := map[string]domain.Entity{}
	for _, e := range after.Entities {
		index[e.ID] = e
	}
	for _, e := range before.Entities {
		if !reflect.DeepEqual(e, index[e.ID]) {
			t.Errorf("%s moved differently once another person was added", e.ID)
		}
	}
	if fingerprint(before.Jobs) != fingerprint(after.Jobs) {
		t.Error("the workforce changed the jobs")
	}
}
