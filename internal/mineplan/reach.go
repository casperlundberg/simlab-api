package mineplan

import (
	"math"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Walkable reports whether people travel a tunnel on foot. Everything but the
// hoisting shaft: people ride a cage in it, which is not a place anyone is
// exposed for long, and no vehicle can use it at all.
func Walkable(t domain.Tunnel) bool { return t.Kind != Shaft }

// Segment is a straight stretch of tunnel.
type Segment struct {
	A, B domain.Point
}

// DistanceTo is how close the segment comes to a point, in metres.
func (s Segment) DistanceTo(p domain.Point) float64 {
	return p.DistanceTo(s.closest(p))
}

// closest is the point of the segment nearest p.
func (s Segment) closest(p domain.Point) domain.Point {
	dx, dy, dz := s.B.X-s.A.X, s.B.Y-s.A.Y, s.B.Z-s.A.Z
	length := dx*dx + dy*dy + dz*dz
	if length == 0 {
		return s.A
	}
	f := math.Max(0, math.Min(1, ((p.X-s.A.X)*dx+(p.Y-s.A.Y)*dy+(p.Z-s.A.Z)*dz)/length))
	return domain.Point{X: s.A.X + f*dx, Y: s.A.Y + f*dy, Z: s.A.Z + f*dz}
}

// Reach answers where along the tunnels something could get to from a point,
// within a distance — for a person whose route the mine does not know, the
// ground they could be on by the end of a lookahead.
//
// It keeps the distance between every pair of nodes, found once. A planner
// asks for every protected person on every cycle; one route search per ask
// would be quadratic in the nodes each time, and a day is thousands of cycles.
type Reach struct {
	graph    *Graph
	distance [][]float64
}

// NewReach measures the network once.
func NewReach(g *Graph) *Reach {
	r := &Reach{graph: g, distance: make([][]float64, g.Nodes())}
	for node := range r.distance {
		r.distance[node] = g.Routes(node).distance
	}
	return r
}

// Within is every stretch of tunnel within distance of from, travelling along
// the tunnels, as segments. From is taken to be on the tunnel nearest it. The
// ground is all of it and no more: a point is on a returned segment exactly
// when the way there along the tunnels is no longer than distance.
func (r *Reach) Within(from domain.Point, distance float64) []Segment {
	g := r.graph
	a, b, at, ok := g.nearestLeg(from)
	if !ok {
		return nil
	}
	toA, toB := at.DistanceTo(g.points[a]), at.DistanceTo(g.points[b])
	out := []Segment{
		{A: at, B: toward(at, g.points[a], math.Min(distance, toA))},
		{A: at, B: toward(at, g.points[b], math.Min(distance, toB))},
	}
	// Every leg is entered from whichever of its ends is reached first; a leg
	// reached from both ends is covered by the two stretches together.
	for node := range g.points {
		reached := math.Min(toA+r.distance[a][node], toB+r.distance[b][node])
		if reached > distance {
			continue
		}
		left := distance - reached
		for _, e := range g.edges[node] {
			out = append(out, Segment{A: g.points[node], B: toward(g.points[node], g.points[e.to], math.Min(left, e.length))})
		}
	}
	return out
}

// nearestLeg is the leg of tunnel nearest p, by its two nodes, and the point
// on it nearest p. Ties go to the leg met first, so the answer is the same
// every time.
func (g *Graph) nearestLeg(p domain.Point) (a, b int, at domain.Point, ok bool) {
	best := math.Inf(1)
	for node, edges := range g.edges {
		for _, e := range edges {
			if e.to < node {
				continue
			}
			leg := Segment{A: g.points[node], B: g.points[e.to]}
			if d := leg.DistanceTo(p); d < best {
				best, a, b, at, ok = d, node, e.to, leg.closest(p), true
			}
		}
	}
	return a, b, at, ok
}

// toward is the point distance along the straight line from a to b.
func toward(a, b domain.Point, distance float64) domain.Point {
	length := a.DistanceTo(b)
	if length == 0 {
		return a
	}
	f := distance / length
	return domain.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f}
}
