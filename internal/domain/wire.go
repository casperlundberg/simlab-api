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
	Pipeline        *PipelineSpec      `json:"pipeline,omitempty"`
	Activity        *ActivitySpec      `json:"activity,omitempty"`
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
		Pipeline:        s.Pipeline,
		Activity:        s.Activity,
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
		Pipeline:    wire.Pipeline,
		Activity:    wire.Activity,
		Description: wire.Description,
		CreatedAt:   wire.CreatedAt,
	}
	return nil
}

type runWire struct {
	ID                      string     `json:"id"`
	Name                    string     `json:"name,omitempty"`
	ScenarioID              string     `json:"scenario_id,omitempty"`
	TargetID                string     `json:"target_id"`
	Mode                    RunMode    `json:"mode"`
	Status                  RunStatus  `json:"status"`
	SimulatedStart          time.Time  `json:"simulated_start,omitempty"`
	TimeCompression         float64    `json:"time_compression,omitempty"`
	DecisionIntervalSeconds float64    `json:"decision_interval_seconds"`
	StartedAt               time.Time  `json:"started_at,omitempty"`
	FinishedAt              time.Time  `json:"finished_at,omitempty"`
	Error                   string     `json:"error,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	BuiltWith               *BuiltWith `json:"built_with,omitempty"`
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
		BuiltWith:               r.BuiltWith,
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
		BuiltWith:        wire.BuiltWith,
	}
	return nil
}

func seconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
}

// The pipeline on the wire. Durations are seconds, like every other duration
// this API takes, so a scenario reads the same way throughout.
type pipelineWire struct {
	Sweep         triggerWire `json:"sweep"`
	WindowSeconds float64     `json:"window_seconds"`
	Pick          StageSpec   `json:"pick"`
	Associate     SweepSpec   `json:"associate"`
	Locate        StageSpec   `json:"locate"`
}

type triggerWire struct {
	Kind            string        `json:"kind"`
	EverySeconds    float64       `json:"every_seconds,omitempty"`
	DeadlineSeconds float64       `json:"deadline_seconds,omitempty"`
	MinSeconds      float64       `json:"min_seconds,omitempty"`
	MaxSeconds      float64       `json:"max_seconds,omitempty"`
	Pressure        *pressureWire `json:"pressure,omitempty"`
}

type pressureWire struct {
	Backlog      int     `json:"backlog"`
	FloorSeconds float64 `json:"floor_seconds,omitempty"`
}

// MarshalJSON renders a pipeline in the wire shape.
func (p PipelineSpec) MarshalJSON() ([]byte, error) {
	return json.Marshal(pipelineWire{
		Sweep:         triggerWireOf(p.Sweep),
		WindowSeconds: p.Window.Seconds(),
		Pick:          p.Pick,
		Associate:     p.Associate,
		Locate:        p.Locate,
	})
}

// UnmarshalJSON reads a pipeline from the wire shape.
func (p *PipelineSpec) UnmarshalJSON(data []byte) error {
	var wire pipelineWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*p = PipelineSpec{
		Sweep:     triggerSpecOf(wire.Sweep),
		Window:    seconds(wire.WindowSeconds),
		Pick:      wire.Pick,
		Associate: wire.Associate,
		Locate:    wire.Locate,
	}
	return nil
}

func triggerWireOf(t TriggerSpec) triggerWire {
	wire := triggerWire{
		Kind:            t.Kind,
		EverySeconds:    t.Every.Seconds(),
		DeadlineSeconds: t.Deadline.Seconds(),
		MinSeconds:      t.Min.Seconds(),
		MaxSeconds:      t.Max.Seconds(),
	}
	if t.Pressure != nil {
		wire.Pressure = &pressureWire{
			Backlog: t.Pressure.Backlog, FloorSeconds: t.Pressure.Floor.Seconds(),
		}
	}
	return wire
}

func triggerSpecOf(wire triggerWire) TriggerSpec {
	spec := TriggerSpec{
		Kind:     wire.Kind,
		Every:    seconds(wire.EverySeconds),
		Deadline: seconds(wire.DeadlineSeconds),
		Min:      seconds(wire.MinSeconds),
		Max:      seconds(wire.MaxSeconds),
	}
	if wire.Pressure != nil {
		spec.Pressure = &PressureSpec{
			Backlog: wire.Pressure.Backlog, Floor: seconds(wire.Pressure.FloorSeconds),
		}
	}
	return spec
}

// The activity on the wire, durations in seconds like everything else.
type activityWire struct {
	Areas         int          `json:"areas"`
	RotateSeconds float64      `json:"rotate_seconds"`
	Mix           *ActivityMix `json:"mix"`
	Blasting      blastingWire `json:"blasting"`
	Spread        float64      `json:"spread_m"`
}

type blastingWire struct {
	StartSeconds  float64 `json:"start_seconds"`
	WindowSeconds float64 `json:"window_seconds"`
	EverySeconds  float64 `json:"every_seconds"`
	OmoriP        float64 `json:"omori_p"`
	OmoriCSeconds float64 `json:"omori_c_seconds"`
	LengthSeconds float64 `json:"length_seconds"`
}

// MarshalJSON renders an activity in the wire shape.
func (a ActivitySpec) MarshalJSON() ([]byte, error) {
	b := a.Blasting
	return json.Marshal(activityWire{
		Areas: a.Areas, RotateSeconds: a.Rotate.Seconds(), Mix: &a.Mix, Spread: a.Spread,
		Blasting: blastingWire{
			StartSeconds: b.Start.Seconds(), WindowSeconds: b.Window.Seconds(), EverySeconds: b.Every.Seconds(),
			OmoriP: b.OmoriP, OmoriCSeconds: b.OmoriC.Seconds(), LengthSeconds: b.Length.Seconds(),
		},
	})
}

// UnmarshalJSON reads an activity from the wire shape; what it leaves out
// takes the default.
func (a *ActivitySpec) UnmarshalJSON(data []byte) error {
	d := DefaultActivity()
	wire := activityWire{
		Areas: d.Areas, RotateSeconds: d.Rotate.Seconds(), Spread: d.Spread,
		Blasting: blastingWire{
			StartSeconds: d.Blasting.Start.Seconds(), WindowSeconds: d.Blasting.Window.Seconds(),
			EverySeconds: d.Blasting.Every.Seconds(), OmoriP: d.Blasting.OmoriP,
			OmoriCSeconds: d.Blasting.OmoriC.Seconds(), LengthSeconds: d.Blasting.Length.Seconds(),
		},
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	// A mix given is the whole mix: shares mean something only together.
	mix := d.Mix
	if wire.Mix != nil {
		mix = *wire.Mix
	}
	w := wire.Blasting
	*a = ActivitySpec{
		Areas: wire.Areas, Rotate: seconds(wire.RotateSeconds), Mix: mix, Spread: wire.Spread,
		Blasting: BlastSchedule{
			Start: seconds(w.StartSeconds), Window: seconds(w.WindowSeconds), Every: seconds(w.EverySeconds),
			OmoriP: w.OmoriP, OmoriC: seconds(w.OmoriCSeconds), Length: seconds(w.LengthSeconds),
		},
	}
	return nil
}
