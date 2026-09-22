package mineplan_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
)

// covered is how far a point is from the nearest of the segments: zero on them.
func covered(segments []mineplan.Segment, p domain.Point) float64 {
	nearest := math.Inf(1)
	for _, s := range segments {
		nearest = math.Min(nearest, s.DistanceTo(p))
	}
	return nearest
}

func tunnel(kind string, points ...domain.Point) domain.Tunnel {
	return domain.Tunnel{ID: kind, Kind: kind, Path: points}
}

func pt(x, y, z float64) domain.Point { return domain.Point{X: x, Y: y, Z: z} }

func TestWithinADistanceShorterThanTheTunnelOnlyThatStretchIsReached(t *testing.T) {
	reach := mineplan.NewReach(mineplan.NewGraph([]domain.Tunnel{
		tunnel(mineplan.Drive, pt(0, 0, 0), pt(100, 0, 0)),
	}, nil))

	got := reach.Within(pt(30, 0, 0), 20)
	for _, x := range []float64{10, 30, 50} {
		if d := covered(got, pt(x, 0, 0)); d > 1e-9 {
			t.Errorf("x=%v is %v m from the reach; 20 m from x=30 covers it", x, d)
		}
	}
	for _, x := range []float64{9, 51} {
		if d := covered(got, pt(x, 0, 0)); math.Abs(d-1) > 1e-9 {
			t.Errorf("x=%v is %v m from the reach, want 1: it is 21 m away along the drive", x, d)
		}
	}
}

func TestReachTurnsAtAJunctionWithWhatIsLeftOfTheDistance(t *testing.T) {
	reach := mineplan.NewReach(mineplan.NewGraph([]domain.Tunnel{
		tunnel(mineplan.Drive, pt(0, 0, 0), pt(100, 0, 0), pt(200, 0, 0)),
		tunnel(mineplan.Crosscut, pt(100, 0, 0), pt(100, 100, 0)),
	}, nil))

	// 20 m to the junction leaves 30 m up the crosscut.
	got := reach.Within(pt(80, 0, 0), 50)
	if d := covered(got, pt(100, 30, 0)); d > 1e-9 {
		t.Errorf("30 m up the crosscut is %v m from the reach, want on it", d)
	}
	if d := covered(got, pt(100, 31, 0)); math.Abs(d-1) > 1e-9 {
		t.Errorf("31 m up the crosscut is %v m from the reach, want 1", d)
	}
	if d := covered(got, pt(130, 0, 0)); d > 1e-9 {
		t.Errorf("50 m along the drive is %v m from the reach, want on it", d)
	}
}

func TestTheLevelBelowIsReachedByTheRampNotThroughTheRock(t *testing.T) {
	// Two drives 150 m apart vertically, joined only by a 300 m ramp at one end.
	tunnels := []domain.Tunnel{
		tunnel(mineplan.Drive, pt(0, 0, 0), pt(400, 0, 0)),
		tunnel(mineplan.Drive, pt(0, 0, -150), pt(400, 0, -150)),
		tunnel(mineplan.Ramp, pt(400, 0, 0), pt(400, 265, -75), pt(400, 0, -150)),
	}
	reach := mineplan.NewReach(mineplan.NewGraph(tunnels, nil))
	below := pt(200, 0, -150)

	if d := covered(reach.Within(pt(200, 0, 0), 300), below); d < 150-1e-9 {
		t.Errorf("the drive 150 m below is %v m from a 300 m reach; the way down is 200 m of drive "+
			"and a ramp of over 500 m, so it must not be reached", d)
	}
	if d := covered(reach.Within(pt(200, 0, 0), 1200), below); d > 1e-9 {
		t.Errorf("the drive below is %v m from a 1200 m reach, which covers the ramp; want on it", d)
	}
}

func TestPeopleDoNotWalkTheShaft(t *testing.T) {
	tunnels := []domain.Tunnel{
		tunnel(mineplan.Drive, pt(0, 0, 0), pt(100, 0, 0)),
		tunnel(mineplan.Drive, pt(0, 0, -150), pt(100, 0, -150)),
		tunnel(mineplan.Shaft, pt(0, 0, 0), pt(0, 0, -150)),
	}
	reach := mineplan.NewReach(mineplan.NewGraph(tunnels, mineplan.Walkable))
	if d := covered(reach.Within(pt(0, 0, 0), 500), pt(50, 0, -150)); math.IsInf(d, 1) || d < 150-1e-9 {
		t.Errorf("the level below is %v m from the reach; it is joined only by the shaft, which "+
			"people ride in a cage rather than walk", d)
	}
}

func TestAStartOffTheTunnelsIsTakenFromTheNearestTunnel(t *testing.T) {
	reach := mineplan.NewReach(mineplan.NewGraph([]domain.Tunnel{
		tunnel(mineplan.Drive, pt(0, 0, 0), pt(100, 0, 0)),
	}, nil))
	got := reach.Within(pt(30, 0.001, 0), 10)
	if d := covered(got, pt(40, 0, 0)); d > 1e-9 {
		t.Errorf("x=40 is %v m from a 10 m reach started a millimetre off x=30", d)
	}
}

func TestAnEmptyNetworkReachesNothing(t *testing.T) {
	reach := mineplan.NewReach(mineplan.NewGraph(nil, nil))
	if got := reach.Within(pt(0, 0, 0), 100); len(got) != 0 {
		t.Errorf("Within() = %v on a network with no tunnels, want nothing", got)
	}
}

// The property the planner relies on: what Within returns is exactly the
// ground within the distance along the tunnels — all of it, so no one is
// judged out of reach who could be there, and none further, so no event is
// kept for ground no one could reach. Checked against the graph's own route
// search at points along every leg of generated plans.
func TestWithinIsExactlyTheGroundWithinTheDistanceAlongTheTunnels(t *testing.T) {
	for seed := uint64(1); seed <= 6; seed++ {
		random := rand.New(rand.NewPCG(seed, 11))
		tunnels := mineplan.Tunnels(extent(), random)
		graph := mineplan.NewGraph(tunnels, mineplan.Walkable)
		reach := mineplan.NewReach(graph)

		for trial := 0; trial < 8; trial++ {
			// A start partway along a random leg, and a budget up to a kilometre.
			leg := legs(tunnels)[random.IntN(len(legs(tunnels)))]
			f := random.Float64()
			start := lerp(leg[0], leg[1], f)
			budget := random.Float64() * 1000
			got := reach.Within(start, budget)

			a, _ := graph.NodeAt(leg[0])
			b, _ := graph.NodeAt(leg[1])
			fromA, fromB := graph.Routes(a), graph.Routes(b)
			toA, toB := start.DistanceTo(leg[0]), start.DistanceTo(leg[1])
			for _, other := range legs(tunnels) {
				u, _ := graph.NodeAt(other[0])
				v, _ := graph.NodeAt(other[1])
				length := other[0].DistanceTo(other[1])
				for _, g := range []float64{0, 0.25, 0.5, 0.75, 1} {
					p := lerp(other[0], other[1], g)
					along := math.Min(
						math.Min(toA+distanceTo(fromA, u), toB+distanceTo(fromB, u))+g*length,
						math.Min(toA+distanceTo(fromA, v), toB+distanceTo(fromB, v))+(1-g)*length)
					if offset, ok := onLeg(leg, other, g); ok {
						// Along the start's own leg, directly.
						along = math.Min(along, math.Abs(offset-f)*length)
					}
					d := covered(got, p)
					switch {
					case along <= budget-1e-6 && d > 1e-6:
						t.Fatalf("seed %d: %v is %.2f m away along the tunnels, within %.2f, but %.4f m from the reach",
							seed, p, along, budget, d)
					case along > budget+1e-6 && d < 1e-9:
						t.Fatalf("seed %d: %v is %.2f m away along the tunnels, beyond %.2f, but on the reach",
							seed, p, along, budget)
					}
				}
			}
		}
	}
}

// legs is every straight stretch of walkable tunnel.
func legs(tunnels []domain.Tunnel) [][2]domain.Point {
	var out [][2]domain.Point
	for _, t := range tunnels {
		if !mineplan.Walkable(t) {
			continue
		}
		for i := 1; i < len(t.Path); i++ {
			if t.Path[i-1] != t.Path[i] {
				out = append(out, [2]domain.Point{t.Path[i-1], t.Path[i]})
			}
		}
	}
	return out
}

func lerp(a, b domain.Point, f float64) domain.Point {
	return domain.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f}
}

func distanceTo(r mineplan.Routes, node int) float64 {
	route, d := r.To(node)
	if route == nil {
		return math.Inf(1)
	}
	return d
}

// onLeg is where fraction g of other lies along leg, when the two are one
// stretch of tunnel — either way round.
func onLeg(leg, other [2]domain.Point, g float64) (float64, bool) {
	switch {
	case leg[0] == other[0] && leg[1] == other[1]:
		return g, true
	case leg[0] == other[1] && leg[1] == other[0]:
		return 1 - g, true
	}
	return 0, false
}
