package usecase

import (
	"math"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// firstEntry is when a track first crosses into a sphere, between from and
// until. A unit already inside at from has not entered; that is a different
// decision (see WayOut).
//
// Between two waypoints a unit moves in a straight line at constant speed, so
// its distance from the centre over a leg is the root of a quadratic in time,
// and the crossing is solved rather than sampled.
func firstEntry(track []domain.Waypoint, centre domain.Point, radius float64, from, until time.Duration) (time.Duration, bool) {
	if inside(track, centre, radius, from) {
		return 0, false
	}
	return crossing(track, centre, radius, from, until, true)
}

// firstExit is when a track first leaves a sphere it is inside at from, before
// until.
func firstExit(track []domain.Waypoint, centre domain.Point, radius float64, from, until time.Duration) (time.Duration, bool) {
	if !inside(track, centre, radius, from) {
		return 0, false
	}
	return crossing(track, centre, radius, from, until, false)
}

func inside(track []domain.Waypoint, centre domain.Point, radius float64, at time.Duration) bool {
	return domain.Entity{Track: track}.PositionAt(at).DistanceTo(centre) <= radius
}

// crossing is the first moment in [from, until] the track crosses the sphere's
// surface going in (entering) or going out.
func crossing(track []domain.Waypoint, centre domain.Point, radius float64, from, until time.Duration, entering bool) (time.Duration, bool) {
	if len(track) == 0 || until < from {
		return 0, false
	}
	unit := domain.Entity{Track: track}
	// The legs overlapping the window: from the last waypoint at or before
	// from, to the first at or after until. Outside the track a unit stands
	// still, and a unit standing still crosses nothing.
	first := sort.Search(len(track), func(i int) bool { return track[i].At > from })
	if first > 0 {
		first--
	}
	for i := first; i+1 < len(track) && track[i].At <= until; i++ {
		start, end := maxDuration(track[i].At, from), minDuration(track[i+1].At, until)
		if end <= start {
			continue
		}
		a, b := unit.PositionAt(start), unit.PositionAt(end)
		if f, ok := surface(a, b, centre, radius, entering); ok {
			return start + time.Duration(f*float64(end-start)), true
		}
	}
	return 0, false
}

// surface is where along the straight line from a to b, as a fraction, it
// first crosses the sphere's surface in the direction asked for.
func surface(a, b, centre domain.Point, radius float64, entering bool) (float64, bool) {
	dx, dy, dz := b.X-a.X, b.Y-a.Y, b.Z-a.Z
	fx, fy, fz := a.X-centre.X, a.Y-centre.Y, a.Z-centre.Z
	qa := dx*dx + dy*dy + dz*dz
	qb := 2 * (fx*dx + fy*dy + fz*dz)
	qc := fx*fx + fy*fy + fz*fz - radius*radius
	if qa == 0 {
		return 0, false
	}
	disc := qb*qb - 4*qa*qc
	if disc < 0 {
		return 0, false
	}
	root := math.Sqrt(disc)
	// Going in is the smaller root (distance falling through the radius),
	// going out the larger.
	f := (-qb - root) / (2 * qa)
	if !entering {
		f = (-qb + root) / (2 * qa)
	}
	if f < 0 || f > 1 {
		return 0, false
	}
	return f, true
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
