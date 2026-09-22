package pipeline

import (
	"math/rand/v2"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// pipelineStream is the workflow's own randomness, off the scenario's seed.
//
// Its own stream because every other dial already has one: durations drawn here
// must not move because geometry, magnitudes or pick noise were turned up, and
// no existing stream may move because the workflow now exists.
const pipelineStream = 0xBF58476D1CE4E5B9

// Of builds the workflow a scenario describes over a generated workload.
func Of(spec domain.PipelineSpec, w workload.Workload, seed int64) *Pipeline {
	events := make([]Event, len(w.Events))
	next := domain.JobID(0)
	for i, event := range w.Events {
		events[i] = Event{
			Origin:   event.Origin,
			FirstJob: event.FirstJob,
			Picks:    len(event.Picks),
		}
		if last := event.FirstJob + domain.JobID(len(event.Picks)); last > next {
			next = last
		}
	}
	if generated := domain.JobID(len(w.Jobs)); generated > next {
		next = generated
	}
	return New(planOf(spec), events, next, rand.New(rand.NewPCG(uint64(seed), pipelineStream)))
}

func planOf(spec domain.PipelineSpec) Plan {
	return Plan{
		Trigger: triggerOf(spec.Sweep),
		Window:  spec.Window,
		Associate: Sweep{
			Priority:  spec.Associate.Priority,
			Seconds:   spec.Associate.Seconds,
			PerLocate: spec.Associate.PerLocate,
		},
		Locate: Stage{Priority: spec.Locate.Priority, Seconds: spec.Locate.Seconds},
	}
}

// triggerOf builds the trigger a spec names. The spec is validated before a run
// starts, so an unknown kind cannot reach here; it falls back to production's
// own timer rather than to no sweep at all, because a pipeline that never
// sweeps produces no locations and would look like a catastrophic result rather
// than a misconfiguration.
func triggerOf(spec domain.TriggerSpec) Trigger {
	var trigger Trigger
	switch spec.Kind {
	case domain.TriggerWhenDrained:
		trigger = WhenDrained{Deadline: spec.Deadline}
	case domain.TriggerJustInTime:
		trigger = JustInTime{Deadline: spec.Deadline}
	case domain.TriggerAdaptive:
		trigger = Adaptive{Min: spec.Min, Max: spec.Max}
	default:
		trigger = FixedInterval{Every: spec.Every}
	}
	if spec.Pressure != nil {
		trigger = UnderPressure{
			Trigger: trigger,
			Backlog: spec.Pressure.Backlog,
			Floor:   spec.Pressure.Floor,
		}
	}
	return trigger
}
