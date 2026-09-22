// Package observe is what the mine can read about where its people and
// vehicles are and will be, as distinct from what the simulator knows.
//
// The simulator holds every unit's whole track, future included; no mine does.
// Positioning tells it where everyone is now, and the fleet system where each
// vehicle is going, but nobody knows the route a person will walk. Deciding
// from the tracks would credit the mine with foresight it does not have, and
// flatter anything that orders work by who is nearby.
//
// So the application reads a Whereabouts, never the tracks. Which one it is
// given — the mine's own reading, or the simulator's truth for the oracle arm —
// is decided where a run is wired, not by the code that uses it.
package observe

import (
	"math"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Whereabouts is where the protected units are and could be.
//
// Every implementation keeps the same promises, held by one contract test:
// positions are exact; a reach covers everywhere its unit really goes over the
// lookahead, and may cover more but never less; and no reach moves toward any
// point faster than Speed.
type Whereabouts interface {
	// Reach is the ground each protected unit is on, or could be on, from at
	// until at+lookahead. A unit may have several paths — one for each way it
	// could go — in the order the units are listed.
	Reach(at, lookahead time.Duration, protects func(kind string) bool) []Path

	// Positions is where each protected unit was at a moment.
	Positions(at time.Duration, protects func(kind string) bool) []Position

	// Speed bounds how fast any reach can move toward a point, in metres per
	// second: until the time since a judgement covers the distance to the
	// boundary that decided it at this speed, nothing can have crossed.
	Speed() float64
}

// Position is where one unit is.
type Position struct {
	Entity string
	At     domain.Point
}

// Path is ground a unit is on or could be on: straight lines between points.
// A single point is ground too — a unit that is only known to be there.
type Path struct {
	Entity string
	Points []domain.Point
}

// DistanceTo is how close the path comes to a point, in metres.
func (p Path) DistanceTo(q domain.Point) float64 {
	nearest := math.Inf(1)
	for i := range p.Points {
		if i == 0 {
			nearest = p.Points[0].DistanceTo(q)
			continue
		}
		nearest = math.Min(nearest, segmentDistance(p.Points[i-1], p.Points[i], q))
	}
	return nearest
}

// segmentDistance is the distance from q to the segment from a to b.
func segmentDistance(a, b, q domain.Point) float64 {
	dx, dy, dz := b.X-a.X, b.Y-a.Y, b.Z-a.Z
	length := dx*dx + dy*dy + dz*dz
	if length == 0 {
		return a.DistanceTo(q)
	}
	t := ((q.X-a.X)*dx + (q.Y-a.Y)*dy + (q.Z-a.Z)*dz) / length
	t = math.Max(0, math.Min(1, t))
	return domain.Point{X: a.X + t*dx, Y: a.Y + t*dy, Z: a.Z + t*dz}.DistanceTo(q)
}

// Nearest is the path that comes closest to a point, and how close. ok is
// false when nothing is protected.
func Nearest(paths []Path, q domain.Point) (entity string, distance float64, ok bool) {
	distance = math.Inf(1)
	for _, path := range paths {
		// Strictly nearer, so a tie goes to the path listed first and the
		// answer never depends on anything but the input order.
		if d := path.DistanceTo(q); d < distance {
			entity, distance, ok = path.Entity, d, true
		}
	}
	return entity, distance, ok
}
