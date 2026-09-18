// Package pipeline is the mine's own workflow: picks are processed, an
// associate sweep groups what is ready, and each group is located — and
// located again as later picks join it.
//
// The workflow belongs to the application, not to the orchestrator. The mine
// knows that a locate needs an associate and an associate needs picks; the
// queue only knows it has been handed work. That keeps dependency support out
// of the orchestrator capability set, which matters because Kueue, ColonyOS and
// Celery disagree about dependencies far more than they disagree about
// priority.
//
// The shape of the workflow is measured rather than assumed. Every locate in
// the operational extract was submitted inside an associate sweep's own
// execution, a sweep's cost follows how many locates it emits, and the sweep
// reads a 30-minute sliding window — see platform-experiments
// docs/workflow-inference.md, which also says what the extract cannot settle.
//
// A trigger is pure: given a time and an observation, decide. Nothing here
// reads a clock or does I/O, and the only randomness is the injected source
// that spreads the durations of the work Pipeline submits, so a scenario
// replays identically.
package pipeline

import (
	"fmt"
	"time"
)

// Observation is what the mine can see about its own pipeline when deciding
// whether to sweep.
//
// Note what is absent: anything about executors, pods or cloud capacity. That
// is the autoscaler's business. The mine sees its own work, and backlog is a
// better signal than capacity anyway — it is the thing that actually delays an
// operator.
type Observation struct {
	// PicksPending have been submitted and have not finished.
	PicksPending int

	// PicksReady have been processed and are waiting to be associated. These
	// are what a sweep would consume.
	PicksReady int

	// HighValuePending is how many of the pending picks belong to events the
	// mine currently believes matter. Its own estimate, with all the error
	// that implies — never ground truth.
	HighValuePending int

	// DrainPerSecond is the observed rate at which picks are completing,
	// measured rather than assumed, so it already reflects whatever the
	// autoscaler has done about capacity.
	DrainPerSecond float64

	// SinceLast is how long since the last sweep.
	SinceLast time.Duration
}

// Decision is whether to sweep now, and why.
//
// The reason is not decoration. The argument for ordering work by application
// intent is that an operator can be told why the system did what it did, and a
// sweep nobody can explain is not decision support.
type Decision struct {
	Associate bool
	Reason    string
}

func yes(format string, args ...any) Decision {
	return Decision{Associate: true, Reason: fmt.Sprintf(format, args...)}
}

func no(format string, args ...any) Decision {
	return Decision{Associate: false, Reason: fmt.Sprintf(format, args...)}
}

// Trigger decides when to run an associate sweep.
//
// The implementations are the arms of an experiment, not alternatives one of
// which is obviously right: a sweep too early wastes an expensive job on thin
// data, and a sweep too late leaves an operator waiting for a location that
// could already have been computed.
type Trigger interface {
	Name() string
	Decide(now time.Duration, o Observation) Decision
}

// FixedInterval sweeps on a timer, whatever is happening.
//
// The control arm. It is also what most installations actually do, so a result
// is only interesting relative to it.
type FixedInterval struct {
	Every time.Duration
}

func (f FixedInterval) Name() string { return "fixed" }

func (f FixedInterval) Decide(_ time.Duration, o Observation) Decision {
	if o.SinceLast < f.Every {
		return no("the interval has %s left to run", (f.Every - o.SinceLast).Round(time.Second))
	}
	if o.PicksReady == 0 {
		return no("the interval elapsed but nothing is ready to associate")
	}
	return yes("the %s interval elapsed with %d picks ready", f.Every, o.PicksReady)
}

// WhenDrained waits for the backlog to clear, so a sweep sees every pick that
// was in flight rather than an arbitrary prefix of them.
//
// Deadline bounds the wait: a backlog that never drains would otherwise mean a
// sweep that never happens, which is the worst outcome for an operator waiting
// on a location.
type WhenDrained struct {
	Deadline time.Duration
}

func (w WhenDrained) Name() string { return "when-drained" }

func (w WhenDrained) Decide(_ time.Duration, o Observation) Decision {
	if o.PicksReady == 0 {
		return no("nothing is ready to associate")
	}
	if o.PicksPending == 0 {
		return yes("the backlog is clear, so a sweep now sees all %d ready picks", o.PicksReady)
	}
	if w.Deadline > 0 && o.SinceLast >= w.Deadline {
		return yes("waited %s for a backlog of %d to clear; sweeping anyway rather than "+
			"leaving an operator without a location", o.SinceLast.Round(time.Second), o.PicksPending)
	}
	return no("%d picks still in flight", o.PicksPending)
}

// JustInTime waits only for the picks the mine believes matter, rather than for
// the whole backlog.
//
// This is the one the argument rests on. If ordering by application intent
// works, the valuable picks finish early, and a sweep can happen long before
// the queue drains without costing the operator anything they needed. It is
// also the arm most exposed to the mine being wrong about what matters — which
// is the point, since the mine only ever has estimates.
type JustInTime struct {
	Deadline time.Duration
}

func (j JustInTime) Name() string { return "just-in-time" }

func (j JustInTime) Decide(_ time.Duration, o Observation) Decision {
	if o.PicksReady == 0 {
		return no("nothing is ready to associate")
	}
	if o.HighValuePending == 0 {
		return yes("every pick the mine currently values is processed; %d ready, %d still "+
			"in flight and none of them valuable", o.PicksReady, o.PicksPending)
	}
	if j.Deadline > 0 && o.SinceLast >= j.Deadline {
		return yes("waited %s for %d valuable picks; sweeping without them",
			o.SinceLast.Round(time.Second), o.HighValuePending)
	}
	return no("%d picks the mine values are still in flight", o.HighValuePending)
}

// Adaptive aims to sweep just as the backlog clears, learning the drain rate
// rather than being told it.
//
// The tradeoff it is trying to sit on: sweeping early spends an expensive
// associate on thin data, and sweeping late leaves locations uncomputed that
// the data already supports. Predicting when the backlog will clear lets it
// wait exactly as long as waiting is still buying more data.
//
// It is deliberately bounded at both ends. Without Min it would sweep
// continuously whenever the queue happened to be empty, and each sweep costs;
// without Max a backlog growing faster than it drains would push the sweep out
// forever, which is precisely the situation an operator most needs one.
type Adaptive struct {
	Min time.Duration
	Max time.Duration
}

func (a Adaptive) Name() string { return "adaptive" }

func (a Adaptive) Decide(_ time.Duration, o Observation) Decision {
	if o.PicksReady == 0 {
		return no("nothing is ready to associate")
	}
	if a.Min > 0 && o.SinceLast < a.Min {
		return no("only %s since the last sweep; each one costs", o.SinceLast.Round(time.Second))
	}
	if a.Max > 0 && o.SinceLast >= a.Max {
		return yes("reached the %s ceiling with %d pending; waiting longer would not be "+
			"gathering data, it would be losing time", a.Max, o.PicksPending)
	}
	if o.PicksPending == 0 {
		return yes("the backlog is clear with %d ready", o.PicksReady)
	}
	if o.DrainPerSecond <= 0 {
		return no("%d picks pending and nothing completing, so there is no rate to predict from",
			o.PicksPending)
	}

	// How long the current backlog would take to clear at the rate observed.
	drain := time.Duration(float64(o.PicksPending) / o.DrainPerSecond * float64(time.Second))
	if a.Max > 0 && o.SinceLast+drain > a.Max {
		return yes("the backlog of %d would take %s to clear at %.2f/s, past the %s ceiling",
			o.PicksPending, drain.Round(time.Second), o.DrainPerSecond, a.Max)
	}
	return no("%d pending should clear in %s at %.2f/s; waiting buys that much more data",
		o.PicksPending, drain.Round(time.Second), o.DrainPerSecond)
}

// UnderPressure overrides another trigger when work has piled up.
//
// The case it exists for: a burst arrives, the backlog grows past anything the
// normal rhythm was designed for, and continuing to wait for an interval or for
// a drain that is not coming is the worst thing the pipeline could do. Enough
// data to locate something is already available; an operator should have it.
//
// It only ever fires earlier than the trigger it wraps, never later, so it
// cannot make a pipeline less responsive than the one it is layered onto.
type UnderPressure struct {
	Trigger Trigger

	// Backlog is how many ready picks constitute pressure.
	Backlog int

	// Floor stops it firing continuously once over the threshold. Zero means
	// every decision cycle, which is rarely what anyone wants.
	Floor time.Duration
}

func (u UnderPressure) Name() string {
	return u.Trigger.Name() + "+pressure"
}

func (u UnderPressure) Decide(now time.Duration, o Observation) Decision {
	if inner := u.Trigger.Decide(now, o); inner.Associate {
		return inner
	} else if u.Backlog <= 0 || o.PicksReady < u.Backlog {
		return inner
	}
	if u.Floor > 0 && o.SinceLast < u.Floor {
		return no("%d ready is over the pressure threshold, but only %s since the last sweep",
			o.PicksReady, o.SinceLast.Round(time.Second))
	}
	return yes("%d picks ready is past the pressure threshold of %d: sweeping now rather than "+
		"holding a location an operator could already have", o.PicksReady, u.Backlog)
}
