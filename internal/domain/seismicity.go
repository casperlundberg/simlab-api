package domain

import (
	"encoding/json"
	"time"
)

// SeismicEvent is one event in a run's virtual mine: where it really was,
// which sensors saw it, and when the mine had a location for it.
//
// It carries two different kinds of fact and keeps them apart. Truth is the
// simulator's: no real installation knows it, and nothing that orders work may
// read it. Located and Final are the mine's: solved from picks it had actually
// processed, at the time it had processed them. Showing both side by side is
// what lets a reader see how far an estimate was from the thing it estimated —
// which is only possible in a simulation, and is most of the reason for one.
type SeismicEvent struct {
	RunID string

	// Sequence is the event's position in the run, from 1, in origin order.
	Sequence int

	// Origin is when it happened, from the start of the scenario.
	Origin time.Duration

	// Burst is the index of the scenario burst it belongs to, or nil for
	// background activity.
	Burst *int

	// Truth is where it really was. Ground truth; see the type comment.
	Truth Point

	// Magnitude is how large it really was, as a Nuttli magnitude. Ground
	// truth, like Truth. Nil for an event recorded before magnitudes were.
	Magnitude *float64

	// Exposed is who was really exposed, and how badly: judged from Truth and
	// Magnitude, at the moment it happened, with no uncertainty. The simulator's
	// answer, which the mine's own is scored against.
	Exposed []Exposure

	// Sensors are the ids of the sensors that detected it, first arrival
	// first. Each one is a pick job in the queue.
	Sensors []string

	// LocatedAt is when enough of its picks had been processed to solve for a
	// location at all, and Located is that first solution. Nil until then.
	//
	// This is the moment an operator first has somewhere to point at, and how
	// long it takes after Origin is set by the queue — which is to say by the
	// autoscaler.
	LocatedAt *time.Duration
	Located   *Location

	// ProcessedAt is when every one of its picks had been processed, and
	// Final is the location solved from all of them. Final stays nil for an
	// event too few sensors saw to locate.
	ProcessedAt *time.Duration
	Final       *Location

	// PickProcessedAt is when each pick was processed, one entry per sensor
	// in Sensors and in the same order; nil while it is still waiting. It is
	// what says which sensors have work outstanding at a moment, which the
	// event-level times cannot: an event's picks finish one by one.
	PickProcessedAt []*time.Duration
}

// Location is an estimate the mine solved, and how much it should be trusted.
type Location struct {
	At Point `json:"at"`

	// RMSResidualSeconds is the arrival-time error the estimate could not
	// explain. An estimate that did not carry it would be read as exact.
	RMSResidualSeconds float64 `json:"rms_residual_seconds"`

	// Picks is how many detections it was solved from.
	Picks int `json:"picks"`

	// Magnitude is the mine's estimate, from the amplitudes of the same picks.
	// Nil for a location recorded before magnitudes were.
	Magnitude *float64 `json:"magnitude"`

	// Zones is, for each level of ground motion the event can reach, how far
	// from this location that level extends — widened by how far the location
	// may be out. Keyed by level: moderate, high, very-high.
	Zones map[string]float64 `json:"zones,omitempty"`

	// Exposed is who the mine judged exposed from this location, at the moment
	// it had it.
	Exposed []Exposure `json:"exposed"`
}

// Exposure is one person or vehicle within reach of an event.
type Exposure struct {
	Entity string `json:"entity"`

	// Level is the band of ground motion: moderate, high or very-high.
	Level string `json:"level"`

	// PPV is the predicted peak particle velocity where they were, in m/s.
	PPV float64 `json:"ppv_mps"`

	// Distance is how far they were from the location it was judged from, in
	// metres.
	Distance float64 `json:"distance_m"`
}

type seismicEventWire struct {
	RunID              string     `json:"run_id"`
	Sequence           int        `json:"sequence"`
	OriginSeconds      float64    `json:"origin_seconds"`
	Burst              *int       `json:"burst"`
	Truth              Point      `json:"truth"`
	Magnitude          *float64   `json:"magnitude"`
	Exposed            []Exposure `json:"exposed"`
	Sensors            []string   `json:"sensors"`
	LocatedAtSeconds   *float64   `json:"located_at_seconds"`
	Located            *Location  `json:"located"`
	ProcessedAtSeconds *float64   `json:"processed_at_seconds"`
	Final              *Location  `json:"final"`
	PicksProcessedAt   []*float64 `json:"picks_processed_at_seconds"`
}

// MarshalJSON renders an event with its times in seconds, and with the times
// it has not reached yet as null rather than absent, so a client can tell "not
// located" from "this field does not exist".
func (e SeismicEvent) MarshalJSON() ([]byte, error) {
	sensors := e.Sensors
	if sensors == nil {
		sensors = []string{}
	}
	// Null, not empty, for an event recorded before pick times were: every
	// event has at least one pick, so an empty list would claim a count that
	// is wrong rather than say nothing.
	var picks []*float64
	if e.PickProcessedAt != nil {
		picks = make([]*float64, len(e.PickProcessedAt))
		for i, at := range e.PickProcessedAt {
			picks[i] = secondsOf(at)
		}
	}
	return json.Marshal(seismicEventWire{
		RunID: e.RunID, Sequence: e.Sequence,
		OriginSeconds:      e.Origin.Seconds(),
		Burst:              e.Burst,
		Truth:              e.Truth,
		Magnitude:          e.Magnitude,
		Exposed:            e.Exposed,
		Sensors:            sensors,
		LocatedAtSeconds:   secondsOf(e.LocatedAt),
		Located:            e.Located,
		ProcessedAtSeconds: secondsOf(e.ProcessedAt),
		Final:              e.Final,
		PicksProcessedAt:   picks,
	})
}

// UnmarshalJSON reads an event whose times are in seconds.
func (e *SeismicEvent) UnmarshalJSON(data []byte) error {
	var wire seismicEventWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*e = SeismicEvent{
		RunID: wire.RunID, Sequence: wire.Sequence,
		Origin:      seconds(wire.OriginSeconds),
		Burst:       wire.Burst,
		Truth:       wire.Truth,
		Magnitude:   wire.Magnitude,
		Exposed:     wire.Exposed,
		Sensors:     wire.Sensors,
		LocatedAt:   durationOf(wire.LocatedAtSeconds),
		Located:     wire.Located,
		ProcessedAt: durationOf(wire.ProcessedAtSeconds),
		Final:       wire.Final,
	}
	if wire.PicksProcessedAt != nil {
		e.PickProcessedAt = make([]*time.Duration, len(wire.PicksProcessedAt))
		for i, at := range wire.PicksProcessedAt {
			e.PickProcessedAt[i] = durationOf(at)
		}
	}
	return nil
}

func secondsOf(d *time.Duration) *float64 {
	if d == nil {
		return nil
	}
	value := d.Seconds()
	return &value
}

func durationOf(value *float64) *time.Duration {
	if value == nil {
		return nil
	}
	d := seconds(*value)
	return &d
}
