package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// The kinds of thing that move through a mine.
//
// Whether a person is exposed is the distinction that matters for safety, so
// a vehicle with someone in it and one without are different kinds rather
// than one kind with a flag.
const (
	EntityPerson            = "person"
	EntityCrewedVehicle     = "crewed-vehicle"
	EntityAutonomousVehicle = "autonomous-vehicle"
)

// Workforce is how many of each kind are underground during a scenario.
type Workforce struct {
	People             int `json:"people"`
	CrewedVehicles     int `json:"crewed_vehicles"`
	AutonomousVehicles int `json:"autonomous_vehicles"`
}

func (w Workforce) problems() []string {
	var problems []string
	for _, c := range []struct {
		name  string
		count int
	}{
		{"people", w.People}, {"crewed_vehicles", w.CrewedVehicles}, {"autonomous_vehicles", w.AutonomousVehicles},
	} {
		if c.count < 0 {
			problems = append(problems, fmt.Sprintf("workforce %s must be >= 0, got %d", c.name, c.count))
		}
	}
	return problems
}

// Entity is a person or vehicle, and where it goes during a run.
type Entity struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`

	// Track is the waypoints it reaches, in time order. Between two it moves
	// in a straight line at constant speed; two in a row at one position are a
	// stop.
	Track []Waypoint `json:"track"`
}

// Waypoint is where an entity is at a moment.
type Waypoint struct {
	At    time.Duration
	Point Point
}

// MarshalJSON renders a waypoint as [seconds, x, y, z]. A day of a vehicle's
// movement is thousands of them, and an object repeating its keys for each
// would be most of the payload.
func (w Waypoint) MarshalJSON() ([]byte, error) {
	return json.Marshal([4]float64{w.At.Seconds(), w.Point.X, w.Point.Y, w.Point.Z})
}

// UnmarshalJSON reads a waypoint from [seconds, x, y, z].
func (w *Waypoint) UnmarshalJSON(data []byte) error {
	var wire [4]float64
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("a waypoint is [seconds, x, y, z]: %w", err)
	}
	*w = Waypoint{At: seconds(wire[0]), Point: Point{X: wire[1], Y: wire[2], Z: wire[3]}}
	return nil
}

// PositionAt is where the entity is at a moment: interpolated along its
// track, and held at its first or last waypoint outside it.
func (e Entity) PositionAt(at time.Duration) Point {
	track := e.Track
	if len(track) == 0 {
		return Point{}
	}
	// The first waypoint after the moment.
	next := sort.Search(len(track), func(i int) bool { return track[i].At > at })
	if next == 0 {
		return track[0].Point
	}
	if next == len(track) {
		return track[len(track)-1].Point
	}
	a, b := track[next-1], track[next]
	span := b.At - a.At
	if span <= 0 {
		return b.Point
	}
	f := float64(at-a.At) / float64(span)
	return Point{
		X: a.Point.X + (b.Point.X-a.Point.X)*f,
		Y: a.Point.Y + (b.Point.Y-a.Point.Y)*f,
		Z: a.Point.Z + (b.Point.Z-a.Point.Z)*f,
	}
}
