package queue_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/orchestrator"
	"github.com/casperlundberg/simlab-api/internal/queue"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// The queue is counted two ways: by the priority each job holds now, which is
// the order it will be served in, and by the priority it was submitted at.
// They differ only once something has changed a priority, and that difference
// is what the second count exists to show.

func TestAPromotionMovesTheQueueButNotWhatWasSubmitted(t *testing.T) {
	simulator := queue.New(threeWaitingJobs(), slaLevels())
	simulator.Advance(time.Second, 0)

	simulator.Reprioritise(time.Second, []orchestrator.PriorityUpdate{
		{At: time.Second, JobID: 1, Priority: 100},
	})

	now := simulator.Snapshot(time.Second)
	if now[25].Depth != 2 || now[100].Depth != 1 {
		t.Errorf("by current priority: %+v, want two at 25 and one at 100", now)
	}
	submitted := simulator.DepthBySubmittedPriority()
	if len(submitted) != 1 || submitted[25] != 3 {
		t.Errorf("by submitted priority: %v, want all three still at 25", submitted)
	}
}

// Nil would be recorded as "not tracked", which is a different statement from
// "nothing is waiting".
func TestAnEmptyQueueHasAnEmptyCompositionRatherThanNone(t *testing.T) {
	simulator := queue.New(nil, slaLevels())
	simulator.Advance(time.Second, 1)

	if got := simulator.DepthBySubmittedPriority(); got == nil || len(got) != 0 {
		t.Errorf("DepthBySubmittedPriority() = %#v, want an empty map", got)
	}
}

// Work that an executor has started is no longer waiting, whichever way it is
// counted. Including it in one count and not the other would make the two
// charts disagree about how deep the queue is.
func TestWorkInProgressIsInNeitherCount(t *testing.T) {
	simulator := queue.New(threeWaitingJobs(), slaLevels())
	simulator.Advance(time.Second, 0)
	// Five seconds of one executor starts the first job and finishes nothing.
	simulator.Advance(6*time.Second, 1)

	if got := simulator.DepthBySubmittedPriority()[25]; got != 2 {
		t.Errorf("%d counted as waiting by submitted priority, want the 2 not yet started", got)
	}
	if got := simulator.Snapshot(6 * time.Second)[25].Depth; got != 2 {
		t.Errorf("%d counted as waiting by current priority, want 2", got)
	}
}

// Property: however the fleet moves and whatever is promoted or demoted, the
// two counts describe the same waiting work, so their totals agree.
func TestBothCountsAlwaysDescribeTheSameWaitingWork(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 13))
	levels := []domain.Priority{100, 50, 25}

	for trial := 0; trial < 200; trial++ {
		jobs := randomJobs(r, 1+r.IntN(80))
		q := queue.New(jobs, queue.Deadlines{Default: 10 * time.Minute})

		at := time.Duration(0)
		for cycle := 0; cycle < 120; cycle++ {
			at += time.Duration(1+r.IntN(90)) * time.Second
			q.Advance(at, r.IntN(6))

			var updates []orchestrator.PriorityUpdate
			for i := 0; i < r.IntN(4); i++ {
				updates = append(updates, orchestrator.PriorityUpdate{
					At: at, JobID: domain.JobID(r.IntN(len(jobs))), Priority: levels[r.IntN(3)],
				})
			}
			q.Reprioritise(at, updates)

			current := 0
			for _, level := range q.Snapshot(at) {
				current += level.Depth
			}
			submitted := 0
			for _, depth := range q.DepthBySubmittedPriority() {
				submitted += depth
			}
			if current != submitted {
				t.Fatalf("trial %d cycle %d: %d waiting by current priority, %d by submitted",
					trial, cycle, current, submitted)
			}
		}
	}
}

// Property: the mine learns that a job finished from the queue, and it decides
// when an event has a location from that. A job reported twice would locate
// events early; a job never reported would leave them unlocated forever.
func TestEveryJobIsReportedFinishedExactlyOnce(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 19))

	for trial := 0; trial < 300; trial++ {
		jobs := randomJobs(r, 1+r.IntN(100))
		q := queue.New(jobs, queue.Deadlines{Default: 10 * time.Minute})
		finished := map[domain.JobID]int{}

		at := time.Duration(0)
		observe := func(progress orchestrator.Progress) {
			if len(progress.Finished) != progress.Completed {
				t.Fatalf("trial %d: %d completed but %d reported finished",
					trial, progress.Completed, len(progress.Finished))
			}
			for _, id := range progress.Finished {
				finished[id]++
			}
		}
		for cycle := 0; cycle < 150; cycle++ {
			at += time.Duration(1+r.IntN(120)) * time.Second
			observe(q.Advance(at, r.IntN(8)))
		}
		for cycle := 0; cycle < 5000 && !q.Done(); cycle++ {
			at += time.Minute
			observe(q.Advance(at, 1000))
		}

		for _, job := range jobs {
			if finished[job.ID] != 1 {
				t.Fatalf("trial %d: job %d reported finished %d times", trial, job.ID, finished[job.ID])
			}
		}
	}
}

func randomJobs(r *rand.Rand, n int) []workload.Job {
	jobs := make([]workload.Job, n)
	for i := range jobs {
		jobs[i] = workload.Job{
			ID:          domain.JobID(i),
			SubmittedAt: time.Duration(r.IntN(600)) * time.Second,
			Priority:    []domain.Priority{100, 50, 25}[r.IntN(3)],
			Seconds:     1 + r.Float64()*200,
		}
	}
	return jobs
}
