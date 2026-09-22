package observe_test

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
	"github.com/casperlundberg/simlab-api/internal/observe"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// world is a mine as the generator makes one: a plan of tunnels, and a
// workforce moving through it on its tracks.
func world(t *testing.T, seed int64) workload.Workload {
	t.Helper()
	w, err := workload.Build(
		domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 30, BackgroundRate: 60},
		domain.Scenario{ID: "day", MineID: "storhall", Duration: 2 * time.Hour, JobSeconds: 20, Seed: seed,
			PriorityMix: map[domain.Priority]float64{100: 1, 25: 3}})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if len(w.Entities) == 0 || len(w.Layout.Tunnels) == 0 {
		t.Fatalf("the generated world has %d entities and %d tunnels; the contract needs both",
			len(w.Entities), len(w.Layout.Tunnels))
	}
	return w
}

type view struct {
	name string
	of   func(w workload.Workload) observe.Whereabouts
}

// views is every implementation of Whereabouts. Each keeps the same promises,
// which is what lets the planner be handed any of them.
var views = []view{
	{"tracks", func(w workload.Workload) observe.Whereabouts { return observe.NewTracks(w.Entities) }},
	{"positioned", func(w workload.Workload) observe.Whereabouts {
		return observe.NewPositioned(w.Entities, w.Layout.Tunnels, workload.WalkingSpeed)
	}},
}

func everyone(string) bool { return true }

// rounding is how far a generated track may lie from the tunnels it follows:
// waypoints are rounded in place and in time, the tunnels are not. A reach
// along the tunnels can be that far from a unit's recorded position and still
// be exactly right.
var rounding = workload.TrackResolution*math.Sqrt(3)/2 +
	workload.WalkingSpeed*workload.TrackTick.Seconds()/2 + 1e-9

func reachOf(paths []observe.Path, entity string) []observe.Path {
	var out []observe.Path
	for _, p := range paths {
		if p.Entity == entity {
			out = append(out, p)
		}
	}
	return out
}

func distance(paths []observe.Path, q domain.Point) float64 {
	_, d, ok := observe.Nearest(paths, q)
	if !ok {
		return math.Inf(1)
	}
	return d
}

func TestEveryViewPutsEachUnitExactlyWhereItIs(t *testing.T) {
	for _, v := range views {
		w := world(t, 7)
		got := v.of(w)
		for _, at := range []time.Duration{0, 7 * time.Minute, 33*time.Minute + 5*time.Second} {
			positions := got.Positions(at, everyone)
			if len(positions) != len(w.Entities) {
				t.Fatalf("%s: %d positions at %v for %d units", v.name, len(positions), at, len(w.Entities))
			}
			for i, entity := range w.Entities {
				if positions[i].Entity != entity.ID || positions[i].At != entity.PositionAt(at) {
					t.Errorf("%s: %+v at %v, want %s at %+v", v.name, positions[i], at, entity.ID, entity.PositionAt(at))
				}
			}
		}
	}
}

// The promise that keeps a view safe to decide from: it may cover more ground
// than a unit really crosses, never less. A view narrower than the truth would
// relax work on an event someone was about to walk into.
func TestEveryViewCoversWhereEachUnitReallyGoesOverTheLookahead(t *testing.T) {
	const lookahead = 300 * time.Second
	for _, v := range views {
		for seed := int64(1); seed <= 3; seed++ {
			w := world(t, seed)
			got := v.of(w)
			random := rand.New(rand.NewPCG(uint64(seed), 3))
			for trial := 0; trial < 20; trial++ {
				at := time.Duration(random.Int64N(int64(90 * time.Minute)))
				reach := got.Reach(at, lookahead, everyone)
				for _, entity := range w.Entities {
					mine := reachOf(reach, entity.ID)
					for s := at; s <= at+lookahead; s += 10 * time.Second {
						if d := distance(mine, entity.PositionAt(s)); d > rounding {
							t.Fatalf("%s, seed %d: %s is at %+v at %v, %.3f m outside the reach it was given at %v",
								v.name, seed, entity.ID, entity.PositionAt(s), s, d, at)
						}
					}
				}
			}
		}
	}
}

// The planner skips judging an event again until something protected could
// have covered the distance to the boundary that decided it. That is sound
// only if no view's ground moves toward a point faster than the view says.
func TestNoViewsReachMovesTowardAnythingFasterThanItsSpeed(t *testing.T) {
	const lookahead = 300 * time.Second
	for _, v := range views {
		w := world(t, 11)
		got := v.of(w)
		speed := got.Speed()
		random := rand.New(rand.NewPCG(11, 5))
		extent := w.Layout.Extent
		for trial := 0; trial < 200; trial++ {
			at := time.Duration(random.Int64N(int64(90 * time.Minute)))
			later := at + []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute}[trial%3]
			q := domain.Point{
				X: extent.Min.X + random.Float64()*(extent.Max.X-extent.Min.X),
				Y: extent.Min.Y + random.Float64()*(extent.Max.Y-extent.Min.Y),
				Z: extent.Min.Z + random.Float64()*(extent.Max.Z-extent.Min.Z),
			}
			before := distance(got.Reach(at, lookahead, everyone), q)
			after := distance(got.Reach(later, lookahead, everyone), q)
			if limit := before - speed*(later-at).Seconds(); after < limit-2*rounding {
				t.Fatalf("%s: the reach came %.2f m nearer %+v in %v, more than its speed of %.2f m/s allows",
					v.name, before-after, q, later-at, speed)
			}
		}
	}
}

func TestEveryViewReportsOnlyTheUnitsItIsAskedToProtect(t *testing.T) {
	people := func(kind string) bool { return kind == domain.EntityPerson }
	for _, v := range views {
		w := world(t, 5)
		got := v.of(w)
		for _, p := range got.Reach(time.Minute, time.Minute, people) {
			if !isPerson(w, p.Entity) {
				t.Errorf("%s: a reach for %s, who is not a person", v.name, p.Entity)
			}
		}
		for _, p := range got.Positions(time.Minute, people) {
			if !isPerson(w, p.Entity) {
				t.Errorf("%s: a position for %s, who is not a person", v.name, p.Entity)
			}
		}
	}
}

func isPerson(w workload.Workload, id string) bool {
	for _, e := range w.Entities {
		if e.ID == id {
			return e.Kind == domain.EntityPerson
		}
	}
	return false
}

// ---------------------------------------------------------------- tracks

func TestTheTrueReachIsWhereAUnitIsAndWhereItIsGoing(t *testing.T) {
	entity := domain.Entity{ID: "crewed-vehicle-01", Kind: domain.EntityCrewedVehicle, Track: []domain.Waypoint{
		{At: 0, Point: domain.Point{}},
		{At: 100 * time.Second, Point: domain.Point{X: 100}},
		{At: 200 * time.Second, Point: domain.Point{X: 100, Y: 100}},
	}}

	paths := observe.NewTracks([]domain.Entity{entity}).Reach(50*time.Second, 100*time.Second, everyone)

	if len(paths) != 1 {
		t.Fatalf("Reach() = %+v, want one path", paths)
	}
	want := []domain.Point{{X: 50}, {X: 100}, {X: 100, Y: 50}}
	if len(paths[0].Points) != len(want) {
		t.Fatalf("Points = %+v, want %+v", paths[0].Points, want)
	}
	for i := range want {
		if paths[0].Points[i] != want[i] {
			t.Errorf("Points[%d] = %+v, want %+v", i, paths[0].Points[i], want[i])
		}
	}
	// Level with the middle of the second leg, 30 m to the side of it.
	if d := paths[0].DistanceTo(domain.Point{X: 130, Y: 25}); math.Abs(d-30) > 1e-9 {
		t.Errorf("DistanceTo() = %v, want 30", d)
	}
}

// -------------------------------------------------------------- positioned

// Two drives crossing at a junction, the way a person at it could go.
func crossing() []domain.Tunnel {
	return []domain.Tunnel{
		{ID: "d1", Kind: mineplan.Drive, Path: []domain.Point{{X: -500}, {X: 0}, {X: 500}}},
		{ID: "d2", Kind: mineplan.Drive, Path: []domain.Point{{Y: -500}, {}, {Y: 500}}},
	}
}

func walking(id string, from domain.Point, to domain.Point) domain.Entity {
	return domain.Entity{ID: id, Kind: domain.EntityPerson, Track: []domain.Waypoint{
		{At: 0, Point: from}, {At: time.Duration(from.DistanceTo(to)) * time.Second, Point: to},
	}}
}

func TestTheMineDoesNotKnowWhereAPersonIsGoing(t *testing.T) {
	east := walking("person-01", domain.Point{}, domain.Point{X: 400})
	north := walking("person-02", domain.Point{}, domain.Point{Y: 400})
	view := observe.NewPositioned([]domain.Entity{east, north}, crossing(), 1)

	reach := view.Reach(0, 100*time.Second, everyone)
	// Each could have gone 100 m down any of the four drives.
	for _, id := range []string{"person-01", "person-02"} {
		for _, q := range []domain.Point{{X: 100}, {X: -100}, {Y: 100}, {Y: -100}} {
			if d := distance(reachOf(reach, id), q); d > 1e-9 {
				t.Errorf("%s: %+v is %v m from the reach; from the junction, 100 m down any drive is within it",
					id, q, d)
			}
		}
		if d := distance(reachOf(reach, id), domain.Point{X: 150}); math.Abs(d-50) > 1e-9 {
			t.Errorf("%s: 150 m east is %v m from the reach, want 50", id, d)
		}
	}
}

func TestTheMineKnowsEachVehiclesPlannedRoute(t *testing.T) {
	vehicle := domain.Entity{ID: "crewed-vehicle-01", Kind: domain.EntityCrewedVehicle, Track: []domain.Waypoint{
		{At: 0, Point: domain.Point{X: -400}}, {At: 200 * time.Second, Point: domain.Point{X: 400}},
	}}
	entities := []domain.Entity{vehicle}
	mine := observe.NewPositioned(entities, crossing(), 1).Reach(50*time.Second, 60*time.Second, everyone)
	truth := observe.NewTracks(entities).Reach(50*time.Second, 60*time.Second, everyone)
	if len(mine) != 1 || len(truth) != 1 || !samePoints(mine[0].Points, truth[0].Points) {
		t.Errorf("a vehicle's reach is %+v, want its route %+v", mine, truth)
	}
}

func TestWithoutTunnelsAPersonIsProtectedWhereTheyAre(t *testing.T) {
	person := walking("person-01", domain.Point{X: 10}, domain.Point{X: 20})
	reach := observe.NewPositioned([]domain.Entity{person}, nil, 1).Reach(0, time.Minute, everyone)
	if len(reach) != 1 || len(reach[0].Points) != 1 || reach[0].Points[0] != (domain.Point{X: 10}) {
		t.Errorf("Reach() = %+v; with no tunnels there is nothing to say where they could go, "+
			"so the reach is where they are", reach)
	}
}

func samePoints(a, b []domain.Point) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
