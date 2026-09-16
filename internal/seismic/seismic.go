// Package seismic turns an event in the rock into what the sensors saw, and
// turns what the sensors saw back into an estimate of where it was.
//
// It is deliberately separate from the workload generator. Generating jobs from
// a scenario and solving a hypocentre are different concerns, and this one is
// worth testing against known geometry where the answer can be checked rather
// than only asserted to exist.
//
// The point of solving rather than assuming is error. If the mine's intent were
// handed the true epicentre it would be reasoning from something no real
// installation has, and the result would prove nothing about whether
// intent-based ordering survives an estimate that is merely close. Everything
// downstream should consume Estimate, never Event.
//
// Pure functions: no I/O, no clock, and randomness only from an injected
// source, so a scenario replays identically.
package seismic

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Model is the rock, as far as arrival times are concerned.
//
// One uniform P-wave velocity. Real rock is layered and anisotropic, and a
// production system would carry a velocity model calibrated from blasts — but
// the thing being studied here is what happens downstream of an uncertain
// location, and a uniform model produces uncertainty honestly.
type Model struct {
	// PVelocitySeconds is metres per second. Hard rock is around 5800.
	PVelocitySeconds float64
}

// Event is something happening in the rock. Ground truth, known to the
// simulator and to nothing else.
type Event struct {
	At     domain.Point
	Origin time.Duration
}

// Pick is one sensor detecting an event: which sensor, and when.
//
// This is all a real system gets. Note what is absent: which event it came
// from, and where that event was.
type Pick struct {
	SensorID string        `json:"sensor_id"`
	At       time.Duration `json:"at"`
}

// Estimate is a solved location, with what it could not explain.
type Estimate struct {
	At     domain.Point  `json:"at"`
	Origin time.Duration `json:"origin"`

	// RMSResidualSeconds is the arrival-time error the estimate leaves
	// unaccounted for. It is the estimate's own account of how much it should
	// be trusted, and intent that ignores it is intent acting on noise.
	RMSResidualSeconds float64 `json:"rms_residual_seconds"`

	// Picks is how many detections it was solved from.
	Picks int `json:"picks"`
}

// TravelTime is how long the wave takes to reach a point.
func (m Model) TravelTime(from, to domain.Point) time.Duration {
	if m.PVelocitySeconds <= 0 {
		return 0
	}
	return time.Duration(from.DistanceTo(to) / m.PVelocitySeconds * float64(time.Second))
}

// Picks is what the sensors record for an event, ordered by arrival.
//
// jitter is the standard deviation of pick error — the difference between when
// the wave arrived and when the instrument said it did. Zero, with a nil
// source, gives the noiseless case used to check the solver itself.
func (m Model) Picks(event Event, sensors []domain.Sensor, jitter time.Duration, random *rand.Rand) []Pick {
	picks := make([]Pick, 0, len(sensors))
	for _, sensor := range sensors {
		at := event.Origin + m.TravelTime(event.At, sensor.At)
		if jitter > 0 && random != nil {
			at += time.Duration(random.NormFloat64() * float64(jitter))
		}
		// A detection cannot precede the event that caused it, whatever the
		// noise did.
		if at < event.Origin {
			at = event.Origin
		}
		picks = append(picks, Pick{SensorID: sensor.ID, At: at})
	}

	// Ordered by arrival, which is the order a real system receives them in —
	// and the order that makes the first few picks the interesting ones, since
	// an estimate is wanted before the last sensor has reported.
	sort.SliceStable(picks, func(i, j int) bool { return picks[i].At < picks[j].At })
	return picks
}

// minimumPicks is four: three coordinates and an unknown origin time.
const minimumPicks = 4

// Locate solves for where an event was, from picks alone.
//
// The origin time is not searched for. For any candidate position the best
// origin time is the mean of (arrival - travel time), so it is computed rather
// than sought, which removes an unknown and makes the search three-dimensional.
//
// The search itself is coarse-to-fine over the extent rather than a gradient
// method: the residual surface has local minima when the array is one-sided,
// and a deterministic search that cannot get stuck is worth more here than a
// faster one that sometimes reports a confident wrong answer.
func (m Model) Locate(sensors []domain.Sensor, picks []Pick, within domain.Extent) (Estimate, error) {
	if m.PVelocitySeconds <= 0 {
		return Estimate{}, fmt.Errorf("the velocity model has no velocity")
	}
	if len(picks) < minimumPicks {
		return Estimate{}, fmt.Errorf(
			"locating in three dimensions needs at least %d picks and an origin time to solve for; got %d",
			minimumPicks, len(picks))
	}

	positions := make(map[string]domain.Point, len(sensors))
	for _, sensor := range sensors {
		positions[sensor.ID] = sensor.At
	}

	// Only picks from sensors whose position is known. A pick from a sensor
	// that is not in the layout cannot constrain anything.
	type observation struct {
		at     float64
		sensor domain.Point
	}
	observations := make([]observation, 0, len(picks))
	for _, pick := range picks {
		at, known := positions[pick.SensorID]
		if !known {
			continue
		}
		observations = append(observations, observation{at: pick.At.Seconds(), sensor: at})
	}
	if len(observations) < minimumPicks {
		return Estimate{}, fmt.Errorf(
			"only %d of %d picks came from sensors in the layout, which is fewer than the %d needed",
			len(observations), len(picks), minimumPicks)
	}

	// residual returns the RMS arrival-time error for a candidate position,
	// together with the origin time that best fits it.
	residual := func(candidate domain.Point) (rms, origin float64) {
		sum := 0.0
		for _, o := range observations {
			sum += o.at - candidate.DistanceTo(o.sensor)/m.PVelocitySeconds
		}
		origin = sum / float64(len(observations))

		square := 0.0
		for _, o := range observations {
			e := o.at - (origin + candidate.DistanceTo(o.sensor)/m.PVelocitySeconds)
			square += e * e
		}
		return math.Sqrt(square / float64(len(observations))), origin
	}

	const (
		divisions = 8  // cells per axis, per pass
		passes    = 10 // each pass narrows the box around the best cell
	)

	box := within
	best := box.Centre()
	bestRMS, bestOrigin := residual(best)

	for pass := 0; pass < passes; pass++ {
		spanX, spanY, spanZ := box.Span()
		stepX, stepY, stepZ := spanX/divisions, spanY/divisions, spanZ/divisions

		for i := 0; i <= divisions; i++ {
			for j := 0; j <= divisions; j++ {
				for k := 0; k <= divisions; k++ {
					candidate := within.Clamp(domain.Point{
						X: box.Min.X + float64(i)*stepX,
						Y: box.Min.Y + float64(j)*stepY,
						Z: box.Min.Z + float64(k)*stepZ,
					})
					rms, origin := residual(candidate)
					if rms < bestRMS {
						best, bestRMS, bestOrigin = candidate, rms, origin
					}
				}
			}
		}

		// Narrow to one cell either side of the winner, clamped to the mine.
		// An estimate outside the rock is not a location anybody can act on.
		box = domain.Extent{
			Min: within.Clamp(domain.Point{X: best.X - stepX, Y: best.Y - stepY, Z: best.Z - stepZ}),
			Max: within.Clamp(domain.Point{X: best.X + stepX, Y: best.Y + stepY, Z: best.Z + stepZ}),
		}
	}

	return Estimate{
		At:                 best,
		Origin:             time.Duration(bestOrigin * float64(time.Second)),
		RMSResidualSeconds: bestRMS,
		Picks:              len(observations),
	}, nil
}

// Layout places sensors through a mine, deterministically from the source.
//
// A mine defined only by a sensor count still needs positions before any of
// this means anything, and inventing them at generation time keeps every
// existing scenario working.
//
// Placement is stratified rather than uniformly random: sensors are spread
// across cells of the extent and jittered within them. A purely random array
// clumps, and a clumped array locates badly in the directions it does not
// cover — which would make the solver look worse than the physics deserves.
func Layout(extent domain.Extent, count int, random *rand.Rand) domain.Layout {
	if count < 0 {
		count = 0
	}
	layout := domain.Layout{Extent: extent, Sensors: make([]domain.Sensor, 0, count)}
	if count == 0 {
		return layout
	}

	// Cells per axis: the cube root of the count, so the array spreads in all
	// three dimensions rather than forming a plane.
	perAxis := int(math.Ceil(math.Cbrt(float64(count))))
	spanX, spanY, spanZ := extent.Span()
	stepX, stepY, stepZ := spanX/float64(perAxis), spanY/float64(perAxis), spanZ/float64(perAxis)

	placed := 0
	for i := 0; i < perAxis && placed < count; i++ {
		for j := 0; j < perAxis && placed < count; j++ {
			for k := 0; k < perAxis && placed < count; k++ {
				at := extent.Clamp(domain.Point{
					X: extent.Min.X + (float64(i)+jitter(random))*stepX,
					Y: extent.Min.Y + (float64(j)+jitter(random))*stepY,
					Z: extent.Min.Z + (float64(k)+jitter(random))*stepZ,
				})
				layout.Sensors = append(layout.Sensors, domain.Sensor{
					ID: fmt.Sprintf("s%02d", placed+1),
					At: at,
				})
				placed++
			}
		}
	}
	return layout
}

// jitter is a position within a cell, kept away from the faces so two sensors
// in neighbouring cells do not end up on top of each other.
func jitter(random *rand.Rand) float64 {
	if random == nil {
		return 0.5
	}
	return 0.25 + random.Float64()*0.5
}
