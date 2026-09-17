package mineplan_test

import (
	"math"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
)

func extent() domain.Extent {
	return domain.Extent{
		Min: domain.Point{X: 0, Y: 0, Z: -1400},
		Max: domain.Point{X: 1600, Y: 1000, Z: -400},
	}
}

func plan(seed uint64) []domain.Tunnel {
	return mineplan.Tunnels(extent(), rand.New(rand.NewPCG(seed, 1)))
}

func TestAPlanIsTheSameEveryTimeForASeed(t *testing.T) {
	if !reflect.DeepEqual(plan(7), plan(7)) {
		t.Error("two plans from one seed differ")
	}
	if reflect.DeepEqual(plan(7), plan(8)) {
		t.Error("two seeds drew the identical mine")
	}
}

func TestEveryTunnelIsInsideTheRock(t *testing.T) {
	for _, tunnel := range plan(3) {
		if len(tunnel.Path) < 2 {
			t.Fatalf("tunnel %s has %d points", tunnel.ID, len(tunnel.Path))
		}
		for _, p := range tunnel.Path {
			if !extent().Contains(p) {
				t.Fatalf("tunnel %s passes through %+v, outside the mine", tunnel.ID, p)
			}
		}
	}
}

// Everything is reachable from everything else, or a vehicle sent to a
// crosscut on another level would have no way there.
func TestEveryTunnelConnectsToEveryOther(t *testing.T) {
	graph := mineplan.NewGraph(plan(5), nil)
	if graph.Nodes() == 0 {
		t.Fatal("the graph is empty")
	}
	if parts := graph.Components(); parts != 1 {
		t.Errorf("the tunnels form %d separate networks, want one", parts)
	}
}

// Levels are what a mine plan names, and what a reader orients by.
func TestLevelsAreEvenlySpacedWorkingHorizons(t *testing.T) {
	levels := map[float64]bool{}
	for _, tunnel := range plan(9) {
		if tunnel.Kind == mineplan.Drive {
			levels[tunnel.Path[0].Z] = true
		}
	}
	var zs []float64
	for z := range levels {
		zs = append(zs, z)
	}
	sort.Float64s(zs)
	if len(zs) < 4 {
		t.Fatalf("%d levels in a kilometre of rock", len(zs))
	}
	for i := 1; i < len(zs); i++ {
		if gap := zs[i] - zs[i-1]; gap != zs[1]-zs[0] {
			t.Fatalf("levels at %v are not evenly spaced", zs)
		}
	}
}

// A decline steeper than about one in five is not driveable by a loaded
// truck, and one shallower than one in ten wastes development.
func TestTheRampHasADriveableGradient(t *testing.T) {
	for _, tunnel := range plan(11) {
		if tunnel.Kind != mineplan.Ramp {
			continue
		}
		for i := 1; i < len(tunnel.Path); i++ {
			a, b := tunnel.Path[i-1], tunnel.Path[i]
			run := math.Hypot(b.X-a.X, b.Y-a.Y)
			grade := math.Abs(b.Z-a.Z) / run
			if grade < 0.1 || grade > 0.2 {
				t.Fatalf("ramp segment %d has a gradient of 1:%.1f", i, 1/grade)
			}
		}
		return
	}
	t.Fatal("the mine has no ramp")
}

// Sensors are installed in excavations, because that is the only place
// anyone can reach to install one.
func TestSensorsAreInTheTunnels(t *testing.T) {
	tunnels := plan(13)
	sensors := mineplan.Sensors(tunnels, 40, rand.New(rand.NewPCG(13, 2)))
	if len(sensors) != 40 {
		t.Fatalf("%d sensors placed, want 40", len(sensors))
	}
	ids := map[string]bool{}
	for _, sensor := range sensors {
		if ids[sensor.ID] {
			t.Fatalf("sensor id %s used twice", sensor.ID)
		}
		ids[sensor.ID] = true
		if d := mineplan.DistanceToTunnels(tunnels, sensor.At); d > 0.001 {
			t.Errorf("sensor %s is %.1f m from the nearest tunnel", sensor.ID, d)
		}
	}
}

// An array bunched on one level cannot resolve depth, which is the axis a
// mine most needs resolved.
func TestSensorsReachEveryLevelAndDoNotBunch(t *testing.T) {
	tunnels := plan(17)
	sensors := mineplan.Sensors(tunnels, 40, rand.New(rand.NewPCG(17, 2)))

	levels := map[float64]bool{}
	for _, tunnel := range tunnels {
		if tunnel.Kind == mineplan.Drive {
			levels[tunnel.Path[0].Z] = true
		}
	}
	top, bottom := math.Inf(-1), math.Inf(1)
	closest := math.Inf(1)
	for i, a := range sensors {
		top, bottom = math.Max(top, a.At.Z), math.Min(bottom, a.At.Z)
		for _, b := range sensors[i+1:] {
			closest = math.Min(closest, a.At.DistanceTo(b.At))
		}
	}
	var zs []float64
	for z := range levels {
		zs = append(zs, z)
	}
	sort.Float64s(zs)
	if top < zs[len(zs)-1] || bottom > zs[0] {
		t.Errorf("sensors span %.0f to %.0f m but the levels span %.0f to %.0f m", bottom, top, zs[0], zs[len(zs)-1])
	}
	if closest < 60 {
		t.Errorf("two sensors are %.0f m apart; forty in a mine this size should spread further", closest)
	}
}

func TestAPathFollowsTheTunnelsFromOnePlaceToAnother(t *testing.T) {
	tunnels := plan(19)
	graph := mineplan.NewGraph(tunnels, nil)
	random := rand.New(rand.NewPCG(19, 3))
	from, to := graph.RandomNode(random), graph.RandomNode(random)
	for from == to {
		to = graph.RandomNode(random)
	}

	path, length := graph.Path(from, to)
	if len(path) < 2 {
		t.Fatalf("no path between two nodes of a connected mine")
	}
	if path[0] != graph.Point(from) || path[len(path)-1] != graph.Point(to) {
		t.Errorf("the path does not run from %+v to %+v", graph.Point(from), graph.Point(to))
	}
	walked := 0.0
	for i := 1; i < len(path); i++ {
		walked += path[i-1].DistanceTo(path[i])
		mid := domain.Point{X: (path[i-1].X + path[i].X) / 2, Y: (path[i-1].Y + path[i].Y) / 2, Z: (path[i-1].Z + path[i].Z) / 2}
		if d := mineplan.DistanceToTunnels(tunnels, mid); d > 0.001 {
			t.Fatalf("leg %d leaves the tunnels by %.1f m", i, d)
		}
	}
	if math.Abs(walked-length) > 1e-6 {
		t.Errorf("reported length %.1f, walked %.1f", length, walked)
	}
	if straight := graph.Point(from).DistanceTo(graph.Point(to)); length < straight-1e-6 {
		t.Errorf("a path of %.1f m is shorter than the straight line of %.1f m", length, straight)
	}
}

// A vehicle cannot ride the hoisting shaft; a graph built for vehicles must
// not route one through it.
func TestAGraphCanLeaveOutTunnelsAKindOfTrafficCannotUse(t *testing.T) {
	tunnels := plan(23)
	graph := mineplan.NewGraph(tunnels, func(t domain.Tunnel) bool { return t.Kind != mineplan.Shaft })
	if parts := graph.Components(); parts != 1 {
		t.Errorf("without the shaft the tunnels form %d networks; the ramp should connect every level", parts)
	}
}

func TestSomewhereNearTheWorkingsIsNearTheWorkings(t *testing.T) {
	tunnels := plan(29)
	random := rand.New(rand.NewPCG(29, 4))
	var distances []float64
	for i := 0; i < 400; i++ {
		p := mineplan.NearWorkings(tunnels, extent(), 40, random)
		if !extent().Contains(p) {
			t.Fatalf("%+v is outside the mine", p)
		}
		distances = append(distances, mineplan.DistanceToTunnels(tunnels, p))
	}
	sort.Float64s(distances)
	if median := distances[len(distances)/2]; median > 80 || median < 10 {
		t.Errorf("median distance from the workings is %.0f m for a 40 m spread", median)
	}
}
