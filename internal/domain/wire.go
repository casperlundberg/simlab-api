package domain

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Durations cross the wire as seconds, everywhere, in both directions.
//
// Go renders a time.Duration as nanoseconds, which is unreadable to a person
// and — worse — silently asymmetric: a request accepting `duration_seconds`
// while the response returns `duration` in nanoseconds means a client cannot
// send back what it was given. That asymmetry is exactly what an integration
// run surfaced, so the conversion lives here, on the domain types themselves,
// rather than in a hand-written request struct that only covered one
// direction.

type burstWire struct {
	AtSeconds              float64  `json:"at_seconds"`
	Magnitude              float64  `json:"magnitude"`
	AftershockDecaySeconds float64  `json:"aftershock_decay_seconds,omitempty"`
	Epicentre              *Point   `json:"epicentre,omitempty"`
	MainMagnitude          *float64 `json:"main_magnitude,omitempty"`
}

// MarshalJSON renders a burst with its offsets in seconds.
func (b Burst) MarshalJSON() ([]byte, error) {
	return json.Marshal(burstWire{
		AtSeconds:              b.At.Seconds(),
		Magnitude:              b.Magnitude,
		AftershockDecaySeconds: b.AftershockDecay.Seconds(),
		Epicentre:              b.Epicentre,
		MainMagnitude:          b.MainMagnitude,
	})
}

// UnmarshalJSON reads a burst whose offsets are in seconds.
func (b *Burst) UnmarshalJSON(data []byte) error {
	var wire burstWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*b = Burst{
		At:              seconds(wire.AtSeconds),
		Magnitude:       wire.Magnitude,
		AftershockDecay: seconds(wire.AftershockDecaySeconds),
		Epicentre:       wire.Epicentre,
		MainMagnitude:   wire.MainMagnitude,
	}
	return nil
}

type scenarioWire struct {
	ID              string             `json:"id"`
	MineID          string             `json:"mine_id"`
	Name            string             `json:"name"`
	DurationSeconds float64            `json:"duration_seconds"`
	JobSeconds      float64            `json:"job_seconds"`
	PickJitter      float64            `json:"pick_jitter_seconds,omitempty"`
	Workforce       *Workforce         `json:"workforce,omitempty"`
	Seed            int64              `json:"seed"`
	PriorityMix     map[string]float64 `json:"priority_mix"`
	Bursts          []Burst            `json:"bursts,omitempty"`
	Description     string             `json:"description,omitempty"`
	CreatedAt       time.Time          `json:"created_at,omitempty"`
}

// MarshalJSON renders a scenario in the wire shape.
func (s Scenario) MarshalJSON() ([]byte, error) {
	mix := make(map[string]float64, len(s.PriorityMix))
	for priority, weight := range s.PriorityMix {
		mix[strconv.Itoa(int(priority))] = weight
	}
	return json.Marshal(scenarioWire{
		ID: s.ID, MineID: s.MineID, Name: s.Name,
		DurationSeconds: s.Duration.Seconds(),
		JobSeconds:      s.JobSeconds,
		PickJitter:      s.PickJitter.Seconds(),
		Workforce:       s.Workforce,
		Seed:            s.Seed,
		PriorityMix:     mix,
		Bursts:          s.Bursts,
		Description:     s.Description,
		CreatedAt:       s.CreatedAt,
	})
}

// UnmarshalJSON reads a scenario from the wire shape.
func (s *Scenario) UnmarshalJSON(data []byte) error {
	var wire scenarioWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	mix := make(map[Priority]float64, len(wire.PriorityMix))
	for key, weight := range wire.PriorityMix {
		priority, err := strconv.Atoi(key)
		if err != nil {
			return fmt.Errorf("priority_mix has the key %q, which is not a priority level", key)
		}
		mix[Priority(priority)] = weight
	}

	*s = Scenario{
		ID: wire.ID, MineID: wire.MineID, Name: wire.Name,
		Duration:    seconds(wire.DurationSeconds),
		JobSeconds:  wire.JobSeconds,
		PickJitter:  seconds(wire.PickJitter),
		Workforce:   wire.Workforce,
		Seed:        wire.Seed,
		PriorityMix: mix,
		Bursts:      wire.Bursts,
		Description: wire.Description,
		CreatedAt:   wire.CreatedAt,
	}
	return nil
}

type runWire struct {
	ID                      string    `json:"id"`
	Name                    string    `json:"name,omitempty"`
	ScenarioID              string    `json:"scenario_id,omitempty"`
	TargetID                string    `json:"target_id"`
	Mode                    RunMode   `json:"mode"`
	Status                  RunStatus `json:"status"`
	SimulatedStart          time.Time `json:"simulated_start,omitempty"`
	TimeCompression         float64   `json:"time_compression,omitempty"`
	DecisionIntervalSeconds float64   `json:"decision_interval_seconds"`
	StartedAt               time.Time `json:"started_at,omitempty"`
	FinishedAt              time.Time `json:"finished_at,omitempty"`
	Error                   string    `json:"error,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
}

// MarshalJSON renders a run with its interval in seconds.
func (r Run) MarshalJSON() ([]byte, error) {
	return json.Marshal(runWire{
		ID: r.ID, Name: r.Name, ScenarioID: r.ScenarioID, TargetID: r.TargetID,
		Mode: r.Mode, Status: r.Status,
		SimulatedStart:          r.SimulatedStart,
		TimeCompression:         r.TimeCompression,
		DecisionIntervalSeconds: r.DecisionInterval.Seconds(),
		StartedAt:               r.StartedAt,
		FinishedAt:              r.FinishedAt,
		Error:                   r.Error,
		CreatedAt:               r.CreatedAt,
	})
}

// UnmarshalJSON reads a run whose interval is in seconds.
func (r *Run) UnmarshalJSON(data []byte) error {
	var wire runWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*r = Run{
		ID: wire.ID, Name: wire.Name, ScenarioID: wire.ScenarioID, TargetID: wire.TargetID,
		Mode: wire.Mode, Status: wire.Status,
		SimulatedStart:   wire.SimulatedStart,
		TimeCompression:  wire.TimeCompression,
		DecisionInterval: seconds(wire.DecisionIntervalSeconds),
		StartedAt:        wire.StartedAt,
		FinishedAt:       wire.FinishedAt,
		Error:            wire.Error,
		CreatedAt:        wire.CreatedAt,
	}
	return nil
}

func seconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
}
