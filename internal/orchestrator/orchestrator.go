// Package orchestrator is the seam between the mine and whatever actually runs
// its work.
//
// The mine decides what matters; an orchestrator holds the queue and dispatches
// from it. Simlab ships a simulated orchestrator, and the intent is that
// ColonyOS, Kueue or Celery sit behind the same interface — the way the
// autoscaler's Provisioner already admits kubernetes, colonyos and simulation
// without any of them being a special case.
//
// The reason this is an interface rather than a struct is Capabilities. Real
// orchestrators do not agree on what may be done to work that is already
// queued, and a result obtained under capabilities a given orchestrator does
// not have does not transfer to it. Declaring them lets a run be restricted to
// what a real system can do, so "this holds under ColonyOS's constraints" is a
// claim that can be made rather than assumed.
package orchestrator

import (
	"fmt"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Capabilities is what an orchestrator can do to work that is already queued.
//
// Every field is deliberately about the queue rather than about scaling:
// capacity is the autoscaler's concern and reaches this package only as a
// number of executors ready to take work.
type Capabilities struct {
	// MutablePriority is whether the priority of a queued job can be changed
	// after submission. Without it, intent-based reordering is not
	// implementable at all and a run that needs it must fail rather than
	// quietly produce a result that means nothing.
	MutablePriority bool

	// PreemptsOnPromotion is whether raising a priority can interrupt work
	// already running, or only reorder what is still waiting.
	//
	// Most orchestrators only reorder. It matters for the mine's case because
	// a long low-priority job that has already started will still finish
	// first, which is exactly the effect an operator would notice.
	PreemptsOnPromotion bool

	// Levels, when set, is the discrete set of priorities this orchestrator
	// admits. Empty means any value is accepted.
	Levels []domain.Priority

	// UpdateLatency is how long a priority change takes to be reflected in
	// dispatch order. Zero is instantaneous, which no real system is.
	UpdateLatency time.Duration
}

// Accepts reports whether a priority level can be expressed here.
func (c Capabilities) Accepts(priority domain.Priority) bool {
	if len(c.Levels) == 0 {
		return true
	}
	for _, level := range c.Levels {
		if level == priority {
			return true
		}
	}
	return false
}

// PriorityUpdate is the mine telling the orchestrator that something it is
// already holding has become more or less important.
//
// Reason travels with it and is stored. The whole argument for intent-based
// ordering is that an operator can be told why the queue reordered itself, and
// a reordering nobody can explain is not decision support.
type PriorityUpdate struct {
	At       time.Duration
	JobID    domain.JobID
	Priority domain.Priority
	Reason   string
}

// Applied is what an orchestrator did with a batch of updates.
//
// Rejected is not an error: a job that finished before the update arrived is
// the normal case, not a fault. It is counted because the proportion of
// intent that arrived too late to matter is itself a result.
type Applied struct {
	Changed  int
	TooLate  int
	Rejected []Rejection
}

// Rejection is one update the orchestrator would not make, and why.
type Rejection struct {
	JobID  domain.JobID
	Reason string
}

// Progress is what one interval produced.
type Progress struct {
	Completed int
	Breached  int
}

// Stats is what a whole run amounted to.
type Stats struct {
	Submitted int
	Completed int
	Breached  int

	MeanWaitSeconds float64
	P95WaitSeconds  float64
	MaxWaitSeconds  float64

	PeakDepth int
}

// Orchestrator holds queued work and dispatches it as capacity allows.
//
// Time is passed in rather than read, as everywhere else in this service: a
// simulated run compresses it and a test needs to control it.
type Orchestrator interface {
	// Kind names the implementation, as a platform adapter does.
	Kind() string

	// Capabilities is what this orchestrator admits being done to queued work.
	Capabilities() Capabilities

	// Advance runs the queue forward to elapsed, with capacity executors ready
	// to take work.
	Advance(elapsed time.Duration, capacity int) Progress

	// Reprioritise applies the mine's intent to work already queued.
	Reprioritise(at time.Duration, updates []PriorityUpdate) Applied

	// Snapshot is the queue as the autoscaler is shown it.
	Snapshot(now time.Duration) map[domain.Priority]domain.QueueSnapshot

	// Done reports whether there is nothing left to run.
	Done() bool

	// Stats is the whole run.
	Stats() Stats
}

// Restrict narrows an orchestrator to the capabilities of another system, so a
// result can be claimed to hold under that system's constraints rather than
// only under the simulator's.
//
// It can only ever take capability away. Granting one the underlying
// orchestrator does not have would produce a run that could not be reproduced
// on the thing being claimed about, which is the opposite of the point.
func Restrict(inner Orchestrator, to Capabilities) (Orchestrator, error) {
	have := inner.Capabilities()
	if to.MutablePriority && !have.MutablePriority {
		return nil, fmt.Errorf("cannot restrict %s to mutable priority: it does not have it",
			inner.Kind())
	}
	if to.PreemptsOnPromotion && !have.PreemptsOnPromotion {
		return nil, fmt.Errorf("cannot restrict %s to preemption on promotion: it does not have it",
			inner.Kind())
	}
	return &restricted{Orchestrator: inner, caps: to}, nil
}

type restricted struct {
	Orchestrator
	caps Capabilities
}

func (r *restricted) Capabilities() Capabilities { return r.caps }

func (r *restricted) Reprioritise(at time.Duration, updates []PriorityUpdate) Applied {
	if !r.caps.MutablePriority {
		// Refused rather than ignored. A run that silently dropped the mine's
		// intent would report a comparison between intent-based and static
		// ordering in which both arms were static.
		out := Applied{}
		for _, u := range updates {
			out.Rejected = append(out.Rejected, Rejection{
				JobID:  u.JobID,
				Reason: fmt.Sprintf("%s does not support changing the priority of queued work", r.Kind()),
			})
		}
		return out
	}

	admitted := updates[:0:0]
	out := Applied{}
	for _, u := range updates {
		if !r.caps.Accepts(u.Priority) {
			out.Rejected = append(out.Rejected, Rejection{
				JobID: u.JobID,
				Reason: fmt.Sprintf("priority %d is not one of the levels %s admits",
					u.Priority, r.Kind()),
			})
			continue
		}
		admitted = append(admitted, u)
	}
	if len(admitted) == 0 {
		return out
	}

	inner := r.Orchestrator.Reprioritise(at, admitted)
	inner.Rejected = append(inner.Rejected, out.Rejected...)
	return inner
}
