package observe

import (
	"math"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Tracks is the simulator's own knowledge: every unit's whole track, its
// future included. No mine reads this. It is what the oracle arm decides from,
// and the ceiling a mine's own reading is measured against.
type Tracks struct {
	entities []domain.Entity
	speed    float64
}

// NewTracks knows where every unit is and will be.
func NewTracks(entities []domain.Entity) *Tracks {
	return &Tracks{entities: entities, speed: fastest(entities)}
}

// Reach is each protected unit's track over the lookahead: where it is at the
// start, the waypoints it reaches before the end, and where it is at the end.
func (t *Tracks) Reach(at, lookahead time.Duration, protects func(string) bool) []Path {
	var out []Path
	for _, entity := range t.entities {
		if !protects(entity.Kind) || len(entity.Track) == 0 {
			continue
		}
		out = append(out, route(entity, at, lookahead))
	}
	return out
}

// Positions is where each protected unit was at a moment.
func (t *Tracks) Positions(at time.Duration, protects func(string) bool) []Position {
	return positions(t.entities, at, protects)
}

// Speed is how fast the quickest unit moves anywhere on its track.
func (t *Tracks) Speed() float64 { return t.speed }

// route is one unit's track from at, over lookahead.
func route(entity domain.Entity, at, lookahead time.Duration) Path {
	points := []domain.Point{entity.PositionAt(at)}
	if lookahead > 0 {
		track, until := entity.Track, at+lookahead
		first := sort.Search(len(track), func(i int) bool { return track[i].At > at })
		for i := first; i < len(track) && track[i].At < until; i++ {
			points = append(points, track[i].Point)
		}
		points = append(points, entity.PositionAt(until))
	}
	return Path{Entity: entity.ID, Points: points}
}

func positions(entities []domain.Entity, at time.Duration, protects func(string) bool) []Position {
	var out []Position
	for _, entity := range entities {
		if !protects(entity.Kind) || len(entity.Track) == 0 {
			continue
		}
		out = append(out, Position{Entity: entity.ID, At: entity.PositionAt(at)})
	}
	return out
}

// fastest is how quickly the quickest unit moves, in metres per second, over
// its whole track. Measured from the tracks rather than assumed, so a scenario
// with faster vehicles cannot quietly break the planner's skipping.
func fastest(entities []domain.Entity) float64 {
	speed := 0.0
	for _, entity := range entities {
		for k := 1; k < len(entity.Track); k++ {
			a, b := entity.Track[k-1], entity.Track[k]
			if seconds := (b.At - a.At).Seconds(); seconds > 0 {
				speed = math.Max(speed, a.Point.DistanceTo(b.Point)/seconds)
			}
		}
	}
	return speed
}
