// Package intent is the mine deciding which of its queued work matters most,
// from what it knows about where events are and who is near them.
//
// What is protected is a volume, not a set of points: the union, over every
// protected person and vehicle, of the ground they occupy now and the ground
// their planned route takes them through over the lookahead. An event threatens
// that volume when the ground motion it can cause reaches it, which with the
// hazard model's zones is a sphere around the event's estimated hypocentre;
// equivalently, a sphere of the zone's radius swept along each protected path.
// The distance that matters is three-dimensional throughout — a crew on the
// level above an event is closer to it than the plan view suggests.
//
// An event whose zone reaches no protected path is decayed; one whose high zone
// reaches one can be promoted. Work is never decided from a job's own contents,
// only from its event, because every pick of one event serves the same
// location.
package intent

import (
	"math"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Path is where one protected entity is over the lookahead: its position now,
// the waypoints it reaches before the lookahead ends, and its position then.
// Between two points it moves in a straight line, as its track says.
type Path struct {
	Entity string
	Points []domain.Point
}

// Paths is every protected entity's path from at, over lookahead, in the
// order the entities are listed.
func Paths(entities []domain.Entity, at time.Duration, settings domain.IntentSettings) []Path {
	var out []Path
	until := at + settings.Lookahead
	for _, entity := range entities {
		if !settings.Protects(entity.Kind) || len(entity.Track) == 0 {
			continue
		}
		points := []domain.Point{entity.PositionAt(at)}
		if settings.Lookahead > 0 {
			track := entity.Track
			first := sort.Search(len(track), func(i int) bool { return track[i].At > at })
			for i := first; i < len(track) && track[i].At < until; i++ {
				points = append(points, track[i].Point)
			}
			points = append(points, entity.PositionAt(until))
		}
		out = append(out, Path{Entity: entity.ID, Points: points})
	}
	return out
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
		// Strictly nearer, so a tie goes to the entity listed first and the
		// answer never depends on anything but the input order.
		if d := path.DistanceTo(q); d < distance {
			entity, distance, ok = path.Entity, d, true
		}
	}
	return entity, distance, ok
}
