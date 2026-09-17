package domain

import (
	"fmt"
	"math"
)

// Point is a position in a mine, in metres.
//
// Z is elevation against the surface datum, so underground is negative and a
// level named "-500" is at Z = -500. That matches how the levels are spoken
// about, which matters more here than any mathematical convention: a reader
// checking a sensor position against a mine plan should not have to flip a
// sign in their head.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// DistanceTo is the straight-line distance in metres.
//
// Straight-line, not along drifts: a seismic wave travels through rock rather
// than along the tunnels. Personnel distance is a different question and is
// deliberately not this function.
func (p Point) DistanceTo(q Point) float64 {
	dx, dy, dz := p.X-q.X, p.Y-q.Y, p.Z-q.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// Sensor is one seismic sensor, at a known position.
type Sensor struct {
	ID string `json:"id"`
	At Point  `json:"at"`
}

// Extent is the volume a mine occupies, as an axis-aligned box.
//
// It bounds where events can originate and where an epicentre may be solved
// for. A solver allowed to place an event outside the rock would report
// locations no operator could act on.
type Extent struct {
	Min Point `json:"min"`
	Max Point `json:"max"`
}

// Contains reports whether a point is inside the extent.
func (e Extent) Contains(p Point) bool {
	return p.X >= e.Min.X && p.X <= e.Max.X &&
		p.Y >= e.Min.Y && p.Y <= e.Max.Y &&
		p.Z >= e.Min.Z && p.Z <= e.Max.Z
}

// Centre is the middle of the extent, which is where a search starts.
func (e Extent) Centre() Point {
	return Point{
		X: (e.Min.X + e.Max.X) / 2,
		Y: (e.Min.Y + e.Max.Y) / 2,
		Z: (e.Min.Z + e.Max.Z) / 2,
	}
}

// Span is the size of the extent along each axis.
func (e Extent) Span() (x, y, z float64) {
	return e.Max.X - e.Min.X, e.Max.Y - e.Min.Y, e.Max.Z - e.Min.Z
}

// Clamp returns the nearest point inside the extent.
func (e Extent) Clamp(p Point) Point {
	return Point{
		X: math.Min(math.Max(p.X, e.Min.X), e.Max.X),
		Y: math.Min(math.Max(p.Y, e.Min.Y), e.Max.Y),
		Z: math.Min(math.Max(p.Z, e.Min.Z), e.Max.Z),
	}
}

// Layout is where a mine's sensors are, and the volume they watch.
//
// Optional on a Mine: a mine defined only by a sensor count still works, and
// a layout is derived from that count deterministically. Geometry is what
// makes an epicentre meaningful, so it is worth being able to state exactly
// rather than only generate.
type Layout struct {
	Extent  Extent   `json:"extent"`
	Sensors []Sensor `json:"sensors"`
}

// problems is what makes a layout unusable for a mine declaring sensors of
// them. Every problem is reported at once, the way the rest of validation
// does, and each names the axis or sensor it is about.
func (l Layout) problems(sensors int) []string {
	var problems []string

	if len(l.Sensors) != sensors {
		problems = append(problems, fmt.Sprintf(
			"layout places %d sensors but the mine declares %d; they describe one array and must agree",
			len(l.Sensors), sensors))
	}

	for _, axis := range []struct {
		name     string
		min, max float64
	}{
		{"X", l.Extent.Min.X, l.Extent.Max.X},
		{"Y", l.Extent.Min.Y, l.Extent.Max.Y},
		{"Z", l.Extent.Min.Z, l.Extent.Max.Z},
	} {
		if axis.min >= axis.max {
			problems = append(problems, fmt.Sprintf(
				"layout extent has no volume along %s: min %v must be below max %v",
				axis.name, axis.min, axis.max))
		}
	}

	seen := make(map[string]bool, len(l.Sensors))
	for i, sensor := range l.Sensors {
		if sensor.ID == "" {
			problems = append(problems, fmt.Sprintf("layout sensor %d has no id", i))
			continue
		}
		if seen[sensor.ID] {
			problems = append(problems, fmt.Sprintf("layout names sensor %q more than once", sensor.ID))
		}
		seen[sensor.ID] = true
		if !l.Extent.Contains(sensor.At) {
			problems = append(problems, fmt.Sprintf(
				"layout sensor %q at (%v, %v, %v) is outside the extent", sensor.ID,
				sensor.At.X, sensor.At.Y, sensor.At.Z))
		}
	}
	return problems
}
