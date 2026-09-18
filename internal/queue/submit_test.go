package queue_test

import (
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/queue"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// Work submitted while a run is in flight is what a real pipeline does: an
// associate sweep decides there is something to locate, and the locate did not
// exist until it said so.
func TestWorkSubmittedDuringARunIsServedLikeAnyOther(t *testing.T) {
	q := queue.New(nil, deadlines())
	q.Advance(time.Minute, 1)

	if err := q.Submit(workload.Job{
		ID: 7, SubmittedAt: time.Minute, Priority: domain.PriorityLocate, Seconds: 10,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := q.Advance(2*time.Minute, 1)

	if got.Completed != 1 {
		t.Errorf("Completed = %d, want the submitted job", got.Completed)
	}
	if len(got.Finished) != 1 || got.Finished[0] != 7 {
		t.Errorf("Finished = %v, want job 7", got.Finished)
	}
}

func TestSubmittedWorkIsNotServedBeforeItWasSubmitted(t *testing.T) {
	q := queue.New(nil, deadlines())

	if err := q.Submit(workload.Job{
		ID: 1, SubmittedAt: 10 * time.Minute, Priority: domain.PriorityLocate, Seconds: 1,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := q.Advance(time.Minute, 4)

	if got.Completed != 0 {
		t.Errorf("Completed = %d, want nothing: the job arrives nine minutes later", got.Completed)
	}
}

// A run is over when its queue is empty. Work submitted but not yet served
// would otherwise end the run and be silently dropped.
func TestAQueueIsNotDoneWhileSubmittedWorkIsOutstanding(t *testing.T) {
	q := queue.New(nil, deadlines())
	q.Advance(time.Minute, 1)

	if err := q.Submit(workload.Job{
		ID: 3, SubmittedAt: 2 * time.Minute, Priority: domain.PriorityLocate, Seconds: 30,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if q.Done() {
		t.Error("Done() = true with work submitted and not yet served")
	}
}

// Identity is how the mine directs intent at a job and how a run attributes a
// completion. Two jobs sharing one would quietly corrupt both.
func TestSubmittingAJobThatIsAlreadyQueuedIsRefused(t *testing.T) {
	q := queue.New([]workload.Job{
		{ID: 1, SubmittedAt: 0, Priority: domain.PriorityPick, Seconds: 600},
	}, deadlines())
	q.Advance(time.Second, 0)

	err := q.Submit(workload.Job{
		ID: 1, SubmittedAt: time.Second, Priority: domain.PriorityLocate, Seconds: 1,
	})

	if err == nil {
		t.Error("submitting a job with an identity already in the queue was accepted")
	}
}

// The deadline runs from the job's own submission. A locate submitted an hour
// into a run has its full deadline, not what is left of the run's first hour.
func TestSubmittedWorkIsJudgedFromItsOwnSubmission(t *testing.T) {
	q := queue.New(nil, deadlines())
	q.Advance(time.Hour, 0)

	// PriorityAssociate's deadline is a minute here.
	if err := q.Submit(workload.Job{
		ID: 2, SubmittedAt: time.Hour, Priority: domain.PriorityAssociate, Seconds: 1,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := q.Advance(time.Hour+30*time.Second, 1)

	if got.Breached != 0 {
		t.Errorf("Breached = %d, want none: the job is 30s into a 60s deadline", got.Breached)
	}
}

func TestSubmittedWorkBreachesWhenItWaitsPastItsOwnDeadline(t *testing.T) {
	q := queue.New(nil, deadlines())
	q.Advance(time.Hour, 0)

	if err := q.Submit(workload.Job{
		ID: 2, SubmittedAt: time.Hour, Priority: domain.PriorityAssociate, Seconds: 1,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// No executors for two minutes, against a one-minute deadline.
	got := q.Advance(time.Hour+2*time.Minute, 0)

	if got.Breached != 1 {
		t.Errorf("Breached = %d, want 1", got.Breached)
	}
}

// Work handed over dated earlier than the queue's own clock would otherwise
// arrive with part of its deadline already spent, in a stretch of time the
// queue never had it and could not have served it.
func TestWorkSubmittedForAMomentAlreadyPastIsJudgedFromNow(t *testing.T) {
	q := queue.New(nil, deadlines())
	q.Advance(time.Hour, 0)

	// Dated half an hour ago, against PriorityAssociate's one-minute deadline.
	if err := q.Submit(workload.Job{
		ID: 4, SubmittedAt: 30 * time.Minute, Priority: domain.PriorityAssociate, Seconds: 1,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	got := q.Advance(time.Hour+30*time.Second, 0)

	if got.Breached != 0 {
		t.Errorf("Breached = %d, want none: the queue has held it for 30s of a 60s deadline", got.Breached)
	}
}

// Submitted work is work: a run that did not count it would report a job total
// smaller than the number of jobs it actually served.
func TestSubmittedWorkIsCountedInTheRunTotals(t *testing.T) {
	q := queue.New(nil, deadlines())
	q.Advance(time.Minute, 1)
	if err := q.Submit(workload.Job{
		ID: 5, SubmittedAt: time.Minute, Priority: domain.PriorityLocate, Seconds: 5,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	q.Advance(2*time.Minute, 1)

	stats := q.Stats()

	if stats.Submitted != 1 || stats.Completed != 1 {
		t.Errorf("Stats() submitted %d and completed %d, want 1 and 1", stats.Submitted, stats.Completed)
	}
}
