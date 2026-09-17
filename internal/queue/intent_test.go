package queue_test

import (
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/orchestrator"
	"github.com/casperlundberg/simlab-api/internal/queue"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// A job that has waited past its deadline by the time an executor takes it
// has breached, however quickly it then runs. Counting only work still
// waiting or running when a cycle ends missed every such job that also
// finished inside the interval.
func TestAShortJobThatStartsAfterItsDeadlineIsStillABreach(t *testing.T) {
	q := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: domain.PriorityAssociate, Seconds: 5},
	}, deadlines())

	q.Advance(time.Minute, 0)
	got := q.Advance(75*time.Second, 1)

	if got.Completed != 1 {
		t.Fatalf("Completed = %d, want the job run", got.Completed)
	}
	if got.Breached != 1 || q.Stats().Breached != 1 {
		t.Errorf("Breached = %d this interval, %d in total, want 1: it waited 75s against a 60s deadline",
			got.Breached, q.Stats().Breached)
	}
}

func TestAJobWhoseClockRestartsOnAChangeIsJudgedFromTheChange(t *testing.T) {
	q := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: domain.PriorityPick, Seconds: 10},
	}, deadlines())

	q.Advance(90*time.Second, 0)
	q.Reprioritise(90*time.Second, []orchestrator.PriorityUpdate{
		{At: 90 * time.Second, JobID: 0, Priority: domain.PriorityAssociate, RestartDeadline: true},
	})

	if got := q.Advance(140*time.Second, 0); got.Breached != 0 {
		t.Errorf("Breached = %d 50s after the change, want 0 against a 60s deadline from the change", got.Breached)
	}
	if age := q.Snapshot(140 * time.Second)[domain.PriorityAssociate].OldestJobAgeSeconds; age != 50 {
		t.Errorf("OldestJobAgeSeconds = %v, want 50: the autoscaler has to see the age the deadline is judged on", age)
	}
	if got := q.Advance(155*time.Second, 0); got.Breached != 1 {
		t.Errorf("Breached = %d 65s after the change, want 1", got.Breached)
	}
}

// Restarting the clock is arriving at the new level now, so the job queues
// behind work that has been waiting there since before the change.
func TestARestartedJobQueuesBehindWorkAlreadyWaitingAtItsNewLevel(t *testing.T) {
	q := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: domain.PriorityPick, Seconds: 10},
		{ID: 1, SubmittedAt: 30 * time.Second, Priority: domain.PriorityAssociate, Seconds: 10},
	}, deadlines())

	q.Advance(45*time.Second, 0)
	q.Reprioritise(45*time.Second, []orchestrator.PriorityUpdate{
		{At: 45 * time.Second, JobID: 0, Priority: domain.PriorityAssociate, RestartDeadline: true},
	})
	got := q.Advance(55*time.Second, 1)

	if len(got.Finished) != 1 || got.Finished[0] != 1 {
		t.Errorf("Finished = %v, want job 1, which was at the level first", got.Finished)
	}
}

func TestExemptWorkIsReportedApartFromCountedWork(t *testing.T) {
	q := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: domain.PriorityAssociate, Seconds: 10},
		{ID: 1, SubmittedAt: 10 * time.Second, Priority: domain.PriorityAssociate, Seconds: 10},
		{ID: 2, SubmittedAt: 20 * time.Second, Priority: domain.PriorityAssociate, Seconds: 10},
		{ID: 3, SubmittedAt: 20 * time.Second, Priority: domain.PriorityPick, Seconds: 10},
	}, deadlines())
	q.Advance(30*time.Second, 0)
	whole := q.Snapshot(30 * time.Second)

	applied := q.Reprioritise(30*time.Second, []orchestrator.PriorityUpdate{
		{At: 30 * time.Second, JobID: 0, Priority: domain.PriorityAssociate, BurstExempt: true},
		{At: 30 * time.Second, JobID: 3, Priority: domain.PriorityPick, BurstExempt: true},
	})
	if applied.Changed != 2 {
		t.Fatalf("Applied = %+v, want both marked", applied)
	}

	counted, exempt := q.SnapshotByBurst(30 * time.Second)

	if got := q.Snapshot(30 * time.Second); !reflect.DeepEqual(got, whole) {
		t.Errorf("Snapshot = %+v after marking, want the whole queue unchanged: %+v", got, whole)
	}
	if c := counted[domain.PriorityAssociate]; c.Depth != 2 || c.OldestJobAgeSeconds != 20 {
		t.Errorf("counted P100 = %+v, want jobs 1 and 2, the oldest 20s", c)
	}
	if e := exempt[domain.PriorityAssociate]; e.Depth != 1 || e.OldestJobAgeSeconds != 30 {
		t.Errorf("exempt P100 = %+v, want job 0 at 30s", e)
	}
	if c, present := counted[domain.PriorityPick]; present && c.Depth != 0 {
		t.Errorf("counted P25 = %+v, want nothing waiting", c)
	}
	if e := exempt[domain.PriorityPick]; e.Depth != 1 {
		t.Errorf("exempt P25 = %+v, want job 3", e)
	}
	// Arrivals were submitted counted; exemption is something intent does
	// later, so the rate stays with the counted work that brings the load.
	if counted[domain.PriorityAssociate].ArrivalRate != whole[domain.PriorityAssociate].ArrivalRate {
		t.Errorf("counted arrival rate = %v, want the level's %v",
			counted[domain.PriorityAssociate].ArrivalRate, whole[domain.PriorityAssociate].ArrivalRate)
	}
}

func TestWithNothingExemptTheCountedSnapshotIsTheWholeQueue(t *testing.T) {
	q := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: domain.PriorityAssociate, Seconds: 30},
		{ID: 1, SubmittedAt: 5 * time.Second, Priority: domain.PriorityPick, Seconds: 30},
		{ID: 2, SubmittedAt: 25 * time.Second, Priority: domain.PriorityPick, Seconds: 30},
	}, deadlines())
	q.Advance(30*time.Second, 1)

	counted, exempt := q.SnapshotByBurst(30 * time.Second)

	if want := q.Snapshot(30 * time.Second); !reflect.DeepEqual(counted, want) {
		t.Errorf("counted = %+v, want exactly the snapshot %+v", counted, want)
	}
	if len(exempt) != 0 {
		t.Errorf("exempt = %+v, want empty", exempt)
	}
}

func TestMarkingAJobExemptDoesNotMoveIt(t *testing.T) {
	q := queue.New(threeWaitingJobs(), slaLevels())
	q.Advance(time.Second, 0)

	q.Reprioritise(time.Second, []orchestrator.PriorityUpdate{
		{At: time.Second, JobID: 0, Priority: 25, BurstExempt: true},
	})
	got := q.Advance(11*time.Second, 1)

	if len(got.Finished) != 1 || got.Finished[0] != 0 {
		t.Errorf("Finished = %v, want job 0 still first", got.Finished)
	}
}

func TestAnUpdateThatChangesNothingIsNotCounted(t *testing.T) {
	q := queue.New(threeWaitingJobs(), slaLevels())
	q.Advance(time.Second, 0)

	applied := q.Reprioritise(time.Second, []orchestrator.PriorityUpdate{{At: time.Second, JobID: 1, Priority: 25}})

	if applied.Changed != 0 || applied.TooLate != 0 {
		t.Errorf("Applied = %+v, want nothing", applied)
	}
}

// A job decayed to a level with a day's deadline is not late there, and the
// SLA it was submitted under is still worth knowing about: it is what decay
// cost.
func TestBreachesAreAlsoCountedAgainstTheDeadlineEachJobWasSubmittedWith(t *testing.T) {
	q := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: domain.PriorityAssociate, Seconds: 10},
		// Long, and served first once job 0 is decayed below it.
		{ID: 1, SubmittedAt: 0, Priority: domain.PriorityPick, Seconds: 80},
	}, deadlines())

	q.Advance(10*time.Second, 0)
	q.Reprioritise(10*time.Second, []orchestrator.PriorityUpdate{
		{At: 10 * time.Second, JobID: 0, Priority: domain.PriorityFloor},
	})
	for at := 20 * time.Second; !q.Done(); at += 10 * time.Second {
		q.Advance(at, 1)
		if at > time.Hour {
			t.Fatal("the queue never drained")
		}
	}

	stats := q.Stats()
	if stats.Breached != 0 {
		t.Errorf("Breached = %d, want 0 against the levels the jobs ended at", stats.Breached)
	}
	if stats.BreachedAsSubmitted != 1 {
		t.Errorf("BreachedAsSubmitted = %d, want 1: job 0 waited past a minute", stats.BreachedAsSubmitted)
	}
}

// Updates by the thousand arrive every cycle once intent is on. Whatever they
// do, every level has to stay in deadline order, and no job may be lost or
// duplicated.
func TestManyUpdatesLeaveEveryJobOnceAndEveryLevelInOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 71))
	levels := []domain.Priority{400, 100, 50, 25, 0}

	for trial := 0; trial < 200; trial++ {
		var jobs []workload.Job
		n := 1 + r.IntN(150)
		for i := 0; i < n; i++ {
			jobs = append(jobs, workload.Job{
				SubmittedAt: time.Duration(r.IntN(300)) * time.Second,
				Priority:    levels[r.IntN(len(levels))],
				Seconds:     1 + r.Float64()*60,
			})
		}
		// Identity by position once ordered, as the workload assigns it.
		ordered := append([]workload.Job(nil), jobs...)
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].SubmittedAt < ordered[j].SubmittedAt })
		for i := range ordered {
			ordered[i].ID = domain.JobID(i)
		}
		q := queue.New(ordered, queue.Deadlines{Default: time.Duration(30+r.IntN(300)) * time.Second})

		at := time.Duration(0)
		for cycle := 0; cycle < 60; cycle++ {
			at += time.Duration(1+r.IntN(30)) * time.Second
			q.Advance(at, r.IntN(4))

			var updates []orchestrator.PriorityUpdate
			for k := r.IntN(40); k > 0; k-- {
				updates = append(updates, orchestrator.PriorityUpdate{
					At: at, JobID: domain.JobID(r.IntN(len(ordered))),
					Priority:        levels[r.IntN(len(levels))],
					BurstExempt:     r.IntN(2) == 0,
					RestartDeadline: r.IntN(2) == 0,
				})
			}
			q.Reprioritise(at, updates)

			if problem := q.CheckOrder(); problem != "" {
				t.Fatalf("trial %d cycle %d: %s", trial, cycle, problem)
			}
			waiting := 0
			for _, level := range q.Snapshot(at) {
				waiting += level.Depth
			}
			counted, exempt := q.SnapshotByBurst(at)
			split := 0
			for _, level := range counted {
				split += level.Depth
			}
			for _, level := range exempt {
				split += level.Depth
			}
			if split != waiting {
				t.Fatalf("trial %d cycle %d: %d waiting, but %d counted and exempt", trial, cycle, waiting, split)
			}
		}

		for cycle := 0; cycle < 5000 && !q.Done(); cycle++ {
			at += time.Minute
			q.Advance(at, 1000)
		}
		if stats := q.Stats(); stats.Completed != len(ordered) {
			t.Fatalf("trial %d: %d of %d jobs completed", trial, stats.Completed, len(ordered))
		}
	}
}

// A deadline is on the wait, not on the work: a long job picked up in time has
// not breached however long it then runs.
func TestALongJobPickedUpInTimeIsNotABreach(t *testing.T) {
	q := queue.New([]workload.Job{
		{ID: 0, SubmittedAt: 0, Priority: domain.PriorityAssociate, Seconds: 300},
	}, deadlines())

	breaches := 0
	for at := 15 * time.Second; !q.Done(); at += 15 * time.Second {
		breaches += q.Advance(at, 1).Breached
	}

	if breaches != 0 || q.Stats().Breached != 0 {
		t.Errorf("Breached = %d: a five-minute job started 15s after it arrived, against a one-minute deadline", breaches)
	}
}
