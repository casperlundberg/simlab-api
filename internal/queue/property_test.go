package queue_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/queue"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// Property tests. These assert things that must hold for every run rather than
// for one worked example, and they are here because that is what catches the
// bugs a hand-written case does not: an exhaustive sweep found work being
// dropped whenever the fleet shrank, which every example test in this package
// had walked straight past.

// Every job admitted is either completed, waiting, or in progress. There is no
// other legal place for one to be, and a job that reaches none of them is
// invisible twice over: it never completes and it never breaches.
func TestNoJobIsEverLostHoweverTheFleetChanges(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))

	for trial := 0; trial < 400; trial++ {
		var jobs []workload.Job
		n := 1 + r.IntN(120)
		for i := 0; i < n; i++ {
			jobs = append(jobs, workload.Job{
				SubmittedAt: time.Duration(r.IntN(600)) * time.Second,
				Priority:    []domain.Priority{100, 50, 25}[r.IntN(3)],
				Seconds:     1 + r.Float64()*300,
			})
		}

		q := queue.New(jobs, queue.Deadlines{Default: time.Duration(30+r.IntN(600)) * time.Second})

		at := time.Duration(0)
		for cycle := 0; cycle < 200; cycle++ {
			at += time.Duration(1+r.IntN(120)) * time.Second
			// Executor count jumps around, which is what a scale-up followed
			// by a scale-down looks like from in here.
			q.Advance(at, r.IntN(8))

			stats := q.Stats()
			waiting := 0
			for _, level := range q.Snapshot(at) {
				waiting += level.Depth
			}
			if stats.Completed+waiting > stats.Submitted {
				t.Fatalf("trial %d cycle %d: completed %d + waiting %d > submitted %d",
					trial, cycle, stats.Completed, waiting, stats.Submitted)
			}
		}

		// Drain it: unlimited capacity, plenty of time.
		for cycle := 0; cycle < 5000 && !q.Done(); cycle++ {
			at += time.Minute
			q.Advance(at, 1000)
		}
		if !q.Done() {
			t.Fatalf("trial %d: queue never drained with 1000 executors", trial)
		}
		stats := q.Stats()
		if stats.Completed != stats.Submitted {
			t.Fatalf("trial %d: %d of %d jobs completed — the rest were lost",
				trial, stats.Completed, stats.Submitted)
		}
		if stats.Submitted != len(jobs) {
			t.Fatalf("trial %d: %d jobs submitted, %d were handed over", trial, stats.Submitted, len(jobs))
		}
	}
}
