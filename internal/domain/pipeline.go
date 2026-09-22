package domain

import (
	"fmt"
	"strings"
	"time"
)

// PipelineSpec is a scenario running the mine's real workflow — a seismogram is
// picked, an associate sweep groups the picks that are ready, and each group is
// located — rather than one undifferentiated stream of jobs.
//
// A scenario without one generates picks alone, at the priorities its mix
// states, which is what every scenario recorded before the pipeline existed
// does. The defaults here are the operational extract's own measurements;
// platform-experiments docs/workflow-inference.md says where each comes from and
// what the extract could not settle.
type PipelineSpec struct {
	// Sweep is when an associate sweep runs. Production runs a fixed 10 s
	// timer; the alternatives are the point of the experiment.
	Sweep TriggerSpec `json:"sweep"`

	// Window is how far back a sweep groups picks. The real associate reads a
	// 30-minute sliding window.
	Window time.Duration `json:"window"`

	Pick      StageSpec `json:"pick"`
	Associate SweepSpec `json:"associate"`
	Locate    StageSpec `json:"locate"`
}

// StageSpec is what one stage's work is worth and what it costs.
type StageSpec struct {
	// Priority is where this stage's work is submitted. Production assigns
	// associate 100, locate 50 and pick 0 — statically, never varying within a
	// type — but nothing says that assignment is the right one, which is why
	// it is a parameter here rather than a constant.
	Priority Priority `json:"priority"`

	// Seconds is the nominal execution time. Measured means: pick 14.5 s,
	// locate 47.7 s.
	Seconds float64 `json:"seconds"`
}

// SweepSpec is the associate stage. Its cost follows what it emits rather than
// being a fixed duration, because that is what the extract shows: a sweep
// costs 1.126 s empty and 0.080 s more for each locate it submits.
//
// It matters because the sweep policy is what this experiment varies. Sweeping
// half as often does not halve the associate work — it makes each sweep emit
// twice as much, and a flat cost would make every slower policy look free.
type SweepSpec struct {
	Priority Priority `json:"priority"`

	// Seconds is what an empty sweep costs.
	Seconds float64 `json:"seconds"`

	// PerLocate is added for each locate the sweep submits.
	PerLocate float64 `json:"per_locate"`
}

// Trigger kinds.
const (
	// TriggerFixed sweeps on a timer whatever is happening. Production's own,
	// and the control arm.
	TriggerFixed = "fixed"

	// TriggerWhenDrained waits for the backlog to clear, so a sweep sees every
	// pick that was in flight rather than an arbitrary prefix of them.
	TriggerWhenDrained = "when-drained"

	// TriggerJustInTime waits only for the picks the mine believes matter.
	TriggerJustInTime = "just-in-time"

	// TriggerAdaptive predicts when the backlog will clear and aims to sweep
	// as it does.
	TriggerAdaptive = "adaptive"
)

// TriggerSpec is how a mine decides when to run an associate sweep.
type TriggerSpec struct {
	Kind string `json:"kind"`

	// Every is TriggerFixed's interval.
	Every time.Duration `json:"every,omitempty"`

	// Deadline bounds how long TriggerWhenDrained and TriggerJustInTime will
	// wait. A backlog that never drains would otherwise mean a sweep that
	// never happens, which is the worst outcome for an operator waiting on a
	// location.
	Deadline time.Duration `json:"deadline,omitempty"`

	// Min and Max bound TriggerAdaptive at both ends.
	Min time.Duration `json:"min,omitempty"`
	Max time.Duration `json:"max,omitempty"`

	// Pressure, when set, overrides the trigger once this many picks are
	// ready. It only ever sweeps earlier, never later.
	Pressure *PressureSpec `json:"pressure,omitempty"`
}

// PressureSpec is the override for when work has piled up past anything the
// normal rhythm was designed for.
type PressureSpec struct {
	// Backlog is how many ready picks constitute pressure.
	Backlog int `json:"backlog"`

	// Floor stops it firing every cycle once over the threshold.
	Floor time.Duration `json:"floor,omitempty"`
}

// DefaultPipeline is the workflow as the operational extract runs it: a fixed
// 10-second sweep over a 30-minute window, production's static priorities, and
// the measured costs.
//
// This is the control arm, not a recommendation. Whether those priorities are
// the right ones is the question, and 27 candidate events an hour is the rate
// the extract implies, not the 91 the workday mine's job counts were fitted to.
func DefaultPipeline() PipelineSpec {
	return PipelineSpec{
		Sweep:     TriggerSpec{Kind: TriggerFixed, Every: 10 * time.Second},
		Window:    30 * time.Minute,
		Pick:      StageSpec{Priority: PriorityFloor, Seconds: 14.5},
		Associate: SweepSpec{Priority: PriorityAssociate, Seconds: 1.126, PerLocate: 0.080},
		Locate:    StageSpec{Priority: PriorityLocate, Seconds: 47.7},
	}
}

// Validate rejects a pipeline that could not be replayed.
func (p PipelineSpec) Validate() error {
	var problems []string
	if p.Window <= 0 {
		problems = append(problems, fmt.Sprintf("window must be > 0, got %v: a sweep that looks "+
			"back no distance can never group anything", p.Window))
	}
	problems = append(problems, p.Pick.problems("pick")...)
	problems = append(problems, p.Locate.problems("locate")...)
	if p.Associate.Seconds <= 0 {
		problems = append(problems, fmt.Sprintf("associate.seconds must be > 0, got %v: a sweep "+
			"that costs nothing makes every sweep policy look equally cheap", p.Associate.Seconds))
	}
	if p.Associate.PerLocate < 0 {
		problems = append(problems, fmt.Sprintf("associate.per_locate must be >= 0, got %v",
			p.Associate.PerLocate))
	}
	problems = append(problems, p.Sweep.problems()...)
	if len(problems) > 0 {
		return fmt.Errorf("pipeline is not usable: %s", strings.Join(problems, "; "))
	}
	return nil
}

func (s StageSpec) problems(name string) []string {
	if s.Seconds <= 0 {
		return []string{fmt.Sprintf("%s.seconds must be > 0, got %v: a job that takes no time "+
			"makes every capacity calculation meaningless", name, s.Seconds)}
	}
	return nil
}

func (t TriggerSpec) problems() []string {
	var problems []string
	switch t.Kind {
	case TriggerFixed:
		if t.Every <= 0 {
			problems = append(problems, fmt.Sprintf("sweep.every must be > 0 for a %s trigger, "+
				"got %v", TriggerFixed, t.Every))
		}
	case TriggerWhenDrained, TriggerJustInTime:
		if t.Deadline < 0 {
			problems = append(problems, "sweep.deadline must be >= 0")
		}
	case TriggerAdaptive:
		if t.Max > 0 && t.Min > t.Max {
			problems = append(problems, fmt.Sprintf("sweep.min (%v) is past sweep.max (%v), so "+
				"the trigger could never sweep", t.Min, t.Max))
		}
	case "":
		problems = append(problems, fmt.Sprintf("sweep.kind is required: one of %s, %s, %s, %s",
			TriggerFixed, TriggerWhenDrained, TriggerJustInTime, TriggerAdaptive))
	default:
		problems = append(problems, fmt.Sprintf("sweep.kind %q is not a trigger this build has: "+
			"it knows %s, %s, %s and %s", t.Kind,
			TriggerFixed, TriggerWhenDrained, TriggerJustInTime, TriggerAdaptive))
	}
	if t.Pressure != nil && t.Pressure.Backlog <= 0 {
		problems = append(problems, fmt.Sprintf("sweep.pressure.backlog must be > 0, got %d: a "+
			"threshold of zero is always met and would sweep every cycle", t.Pressure.Backlog))
	}
	return problems
}
