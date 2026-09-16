package queue_test

import (
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/orchestrator"
	"github.com/casperlundberg/simlab-api/internal/queue"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// Mutable priority is the capability the whole intent-based feature rests on,
// and the one a real orchestrator has to be checked for. These are its
// mechanics only: why a job's priority changed is the mine's business, and the
// queue never asks.

func slaLevels() queue.Deadlines {
	return queue.Deadlines{
		Levels:  map[domain.Priority]time.Duration{100: 60 * time.Second, 25: 3600 * time.Second},
		Default: 3600 * time.Second,
	}
}

// jobs submits three low-priority jobs, each long enough that one executor can
// only be working on one of them at a time.
func threeWaitingJobs() []workload.Job {
	return []workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: 25, Seconds: 10},
		{ID: 1, SubmittedAt: 0, Priority: 25, Seconds: 10},
		{ID: 2, SubmittedAt: 0, Priority: 25, Seconds: 10},
	}
}

func TestAPromotedJobIsServedBeforeWorkThatWasAheadOfIt(t *testing.T) {
	simulator := queue.New(threeWaitingJobs(), slaLevels())

	// Admit the arrivals without running any of them: advancing from zero to
	// zero moves nothing, so time has to pass for the jobs to be queued at all.
	simulator.Advance(time.Second, 0)

	applied := simulator.Reprioritise(time.Second, []orchestrator.PriorityUpdate{
		{At: time.Second, JobID: 2, Priority: 100, Reason: "epicentre moved within 50m of a crew"},
	})
	if applied.Changed != 1 {
		t.Fatalf("expected the update to be applied, got %+v", applied)
	}

	// One executor, and exactly enough budget for one ten-second job.
	simulator.Advance(11*time.Second, 1)

	snapshot := simulator.Snapshot(11 * time.Second)
	if _, stillWaiting := snapshot[100]; stillWaiting {
		t.Error("the promoted job is still waiting; promotion did not move it to the front")
	}
	if snapshot[25].Depth != 2 {
		t.Errorf("expected the two unpromoted jobs still waiting, got depth %d", snapshot[25].Depth)
	}
}

func TestAPromotedJobKeepsItsOriginalArrivalForTheDeadline(t *testing.T) {
	// Submitted at zero, promoted at 90s to a level whose deadline is 60s.
	// Measured from arrival it is already late; measured from the promotion it
	// would have a fresh minute, which would let a promotion erase the very
	// breach it was meant to prevent.
	simulator := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: 25, Seconds: 10},
	}, slaLevels())

	simulator.Advance(90*time.Second, 0)
	simulator.Reprioritise(90*time.Second, []orchestrator.PriorityUpdate{
		{At: 90 * time.Second, JobID: 0, Priority: 100},
	})

	progress := simulator.Advance(100*time.Second, 0)
	if progress.Breached != 1 {
		t.Errorf("a job promoted past its new deadline should breach; got %d breaches", progress.Breached)
	}
}

func TestAnUpdateForWorkAlreadyRunningIsReportedRatherThanApplied(t *testing.T) {
	simulator := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: 25, Seconds: 100},
	}, slaLevels())

	// One executor picks it up and is still on it.
	simulator.Advance(10*time.Second, 1)

	applied := simulator.Reprioritise(10*time.Second, []orchestrator.PriorityUpdate{
		{At: 10 * time.Second, JobID: 0, Priority: 100},
	})
	if applied.Changed != 0 || applied.TooLate != 1 {
		t.Errorf("expected the update to be too late, got %+v", applied)
	}
}

func TestPromotionDoesNotJumpAheadOfOlderWorkAtTheNewLevel(t *testing.T) {
	simulator := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: 100, Seconds: 10},               // waiting longest at 100
		{ID: 1, SubmittedAt: 30 * time.Second, Priority: 25, Seconds: 10}, // promoted later
	}, slaLevels())

	simulator.Advance(30*time.Second, 0)
	simulator.Reprioritise(30*time.Second, []orchestrator.PriorityUpdate{
		{At: 30 * time.Second, JobID: 1, Priority: 100},
	})

	// Room for one job only: the one that has been at this priority longest.
	simulator.Advance(40*time.Second, 1)

	snapshot := simulator.Snapshot(40 * time.Second)
	if snapshot[100].Depth != 1 {
		t.Fatalf("expected one job left at priority 100, got %d", snapshot[100].Depth)
	}
	if snapshot[100].OldestJobAgeSeconds > 11 {
		t.Error("the older job was left waiting; promotion jumped the queue within its new level")
	}
}

func TestTheSimulatedQueueDeclaresMutablePriority(t *testing.T) {
	simulator := queue.New(threeWaitingJobs(), slaLevels())
	capabilities := simulator.Capabilities()

	if !capabilities.MutablePriority {
		t.Error("the simulated queue must declare mutable priority; the mine's intent depends on it")
	}
	if capabilities.PreemptsOnPromotion {
		t.Error("preemption is not modelled, and claiming it would flatter every result")
	}
	if simulator.Kind() != "simulation" {
		t.Errorf("kind is %q", simulator.Kind())
	}
}
