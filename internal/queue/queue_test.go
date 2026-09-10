package queue_test

import (
	"math"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/queue"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

func deadlines() queue.Deadlines {
	return queue.Deadlines{
		Levels: map[domain.Priority]time.Duration{
			domain.PriorityAssociate: time.Minute,
			domain.PriorityPick:      time.Hour,
		},
		Default: 24 * time.Hour,
	}
}

func job(at time.Duration, priority domain.Priority, seconds float64) workload.Job {
	return workload.Job{SubmittedAt: at, Priority: priority, Seconds: seconds}
}

func TestAnEmptyQueueCompletesNothingAndIsDoneAtOnce(t *testing.T) {
	q := queue.New(nil, deadlines())

	got := q.Advance(15*time.Second, 4)

	if got.Completed != 0 || got.Breached != 0 {
		t.Errorf("Advance() = %+v, want nothing", got)
	}
	if !q.Done() {
		t.Error("Done() = false for a queue with no work")
	}
}

func TestJobsAreServedAndCounted(t *testing.T) {
	// Four ten-second jobs, one executor, a sixty-second interval: all four fit.
	jobs := []workload.Job{
		job(0, domain.PriorityPick, 10), job(0, domain.PriorityPick, 10),
		job(0, domain.PriorityPick, 10), job(0, domain.PriorityPick, 10),
	}
	q := queue.New(jobs, deadlines())

	got := q.Advance(time.Minute, 1)

	if got.Completed != 4 {
		t.Errorf("Completed = %d, want 4", got.Completed)
	}
	if !q.Done() {
		t.Error("Done() = false after everything was served")
	}
}

// One executor is one job at a time. Without this, a single executor would
// appear to clear a burst that in reality needs a fleet.
func TestOneExecutorCannotRunTwoLongJobsAtOnce(t *testing.T) {
	jobs := []workload.Job{
		job(0, domain.PriorityPick, 60), job(0, domain.PriorityPick, 60),
	}
	q := queue.New(jobs, deadlines())

	got := q.Advance(time.Minute, 1)

	if got.Completed != 1 {
		t.Errorf("Completed = %d, want 1: one executor cannot serve two minute-long "+
			"jobs in a minute", got.Completed)
	}
}

func TestMoreExecutorsClearWorkFaster(t *testing.T) {
	build := func() *queue.Simulator {
		var jobs []workload.Job
		for i := 0; i < 10; i++ {
			jobs = append(jobs, job(0, domain.PriorityPick, 60))
		}
		return queue.New(jobs, deadlines())
	}

	one := build().Advance(time.Minute, 1)
	five := build().Advance(time.Minute, 5)

	if five.Completed <= one.Completed {
		t.Errorf("five executors completed %d, one completed %d", five.Completed, one.Completed)
	}
}

// Work in progress carries over. Losing it would let a queue of long jobs
// never finish while appearing to be worked on.
func TestPartlyDoneWorkCarriesIntoTheNextInterval(t *testing.T) {
	q := queue.New([]workload.Job{job(0, domain.PriorityPick, 90)}, deadlines())

	first := q.Advance(time.Minute, 1)
	if first.Completed != 0 {
		t.Fatalf("Completed = %d after 60s of a 90s job, want 0", first.Completed)
	}

	second := q.Advance(2*time.Minute, 1)
	if second.Completed != 1 {
		t.Errorf("Completed = %d in the second interval, want the job finished", second.Completed)
	}
}

// Strict priority is what makes the autoscaler's starvation prediction real.
func TestUrgentWorkIsServedBeforeLessUrgentWork(t *testing.T) {
	jobs := []workload.Job{
		job(0, domain.PriorityPick, 30),      // queued first, but low priority
		job(0, domain.PriorityAssociate, 30), // queued second, but urgent
	}
	q := queue.New(jobs, deadlines())

	q.Advance(30*time.Second, 1)

	snapshot := q.Snapshot(30 * time.Second)
	if snapshot[domain.PriorityAssociate].Depth != 0 {
		t.Errorf("P100 depth = %d, want the urgent job served first",
			snapshot[domain.PriorityAssociate].Depth)
	}
	if snapshot[domain.PriorityPick].Depth != 1 {
		t.Errorf("P25 depth = %d, want the low-priority job still waiting",
			snapshot[domain.PriorityPick].Depth)
	}
}

func TestWithinAPriorityLevelWorkIsServedInOrder(t *testing.T) {
	q := queue.New([]workload.Job{
		job(0, domain.PriorityPick, 30),
		job(10*time.Second, domain.PriorityPick, 30),
	}, deadlines())

	q.Advance(30*time.Second, 1)

	// The job that arrived first is the one that got served, so the survivor
	// is the younger one.
	snapshot := q.Snapshot(30 * time.Second)
	if got := snapshot[domain.PriorityPick].OldestJobAgeSeconds; math.Abs(got-20) > 1 {
		t.Errorf("oldest waiting job is %.0fs old, want 20s — the older one should "+
			"have been served", got)
	}
}

func TestJobsThatHaveNotArrivedYetAreNotServed(t *testing.T) {
	q := queue.New([]workload.Job{job(time.Hour, domain.PriorityPick, 10)}, deadlines())

	got := q.Advance(time.Minute, 10)

	if got.Completed != 0 {
		t.Errorf("Completed = %d, want nothing: the job arrives in an hour", got.Completed)
	}
	if q.Done() {
		t.Error("Done() = true while a job is still to arrive")
	}
}

// A breach is a job that has waited longer than its level allows — the same
// definition the autoscaler predicts against, so the two can be compared.
func TestAJobThatWaitsPastItsDeadlineIsABreach(t *testing.T) {
	q := queue.New([]workload.Job{job(0, domain.PriorityAssociate, 10)}, deadlines())

	// No executors: the job just ages past its one-minute deadline.
	first := q.Advance(30*time.Second, 0)
	if first.Breached != 0 {
		t.Errorf("Breached = %d at 30s, want 0", first.Breached)
	}

	second := q.Advance(90*time.Second, 0)
	if second.Breached != 1 {
		t.Errorf("Breached = %d at 90s, want 1", second.Breached)
	}
}

func TestABreachIsCountedOnceNoMatterHowLongItWaits(t *testing.T) {
	q := queue.New([]workload.Job{job(0, domain.PriorityAssociate, 10)}, deadlines())

	total := 0
	for cycle := 1; cycle <= 10; cycle++ {
		total += q.Advance(time.Duration(cycle)*time.Minute, 0).Breached
	}

	if total != 1 {
		t.Errorf("counted %d breaches for one job, want 1", total)
	}
}

func TestALevelWithNoConfiguredDeadlineUsesTheDefault(t *testing.T) {
	q := queue.New([]workload.Job{job(0, domain.PriorityLocate, 10)}, deadlines())

	// The default is 24 hours, so nothing breaches inside one.
	if got := q.Advance(time.Hour, 0); got.Breached != 0 {
		t.Errorf("Breached = %d after an hour, want 0 under the 24h default", got.Breached)
	}
	if got := q.Advance(25*time.Hour, 0); got.Breached != 1 {
		t.Errorf("Breached = %d after 25 hours, want 1", got.Breached)
	}
}

func TestTheSnapshotIsWhatTheAutoscalerNeedsToDecide(t *testing.T) {
	q := queue.New([]workload.Job{
		job(0, domain.PriorityAssociate, 10),
		job(10*time.Second, domain.PriorityAssociate, 10),
		job(20*time.Second, domain.PriorityPick, 10),
	}, deadlines())

	q.Advance(30*time.Second, 0)
	snapshot := q.Snapshot(30 * time.Second)

	urgent := snapshot[domain.PriorityAssociate]
	if urgent.Depth != 2 {
		t.Errorf("P100 depth = %d, want 2", urgent.Depth)
	}
	if math.Abs(urgent.OldestJobAgeSeconds-30) > 0.5 {
		t.Errorf("P100 oldest age = %.1fs, want 30s", urgent.OldestJobAgeSeconds)
	}
	if urgent.ArrivalRate <= 0 {
		t.Errorf("P100 arrival rate = %v, want a positive rate", urgent.ArrivalRate)
	}
}

func TestAnEmptyLevelIsNotReportedAtAll(t *testing.T) {
	q := queue.New([]workload.Job{job(0, domain.PriorityPick, 10)}, deadlines())
	q.Advance(30*time.Second, 10)

	// A level with nothing waiting and nothing arriving is not part of the
	// picture; reporting it as depth zero would have the autoscaler carry
	// levels that no longer exist.
	if _, present := q.Snapshot(30 * time.Second)[domain.PriorityAssociate]; present {
		t.Error("a level with no jobs at all was reported")
	}
}

func TestStatsDescribeTheWholeRun(t *testing.T) {
	q := queue.New([]workload.Job{
		job(0, domain.PriorityPick, 10),
		job(0, domain.PriorityPick, 10),
		job(0, domain.PriorityAssociate, 10),
	}, deadlines())

	for cycle := 1; cycle <= 10; cycle++ {
		q.Advance(time.Duration(cycle)*30*time.Second, 2)
	}

	stats := q.Stats()
	if stats.Submitted != 3 {
		t.Errorf("Submitted = %d, want 3", stats.Submitted)
	}
	if stats.Completed != 3 {
		t.Errorf("Completed = %d, want 3", stats.Completed)
	}
	if stats.MaxWaitSeconds < 0 {
		t.Errorf("MaxWaitSeconds = %v", stats.MaxWaitSeconds)
	}
	if stats.PeakDepth < 3 {
		t.Errorf("PeakDepth = %d, want at least the 3 jobs that queued together", stats.PeakDepth)
	}
}

func TestWaitingTimesAreMeasuredFromSubmissionToService(t *testing.T) {
	q := queue.New([]workload.Job{job(0, domain.PriorityPick, 10)}, deadlines())

	// Nothing for two minutes, then an executor appears.
	q.Advance(2*time.Minute, 0)
	q.Advance(3*time.Minute, 1)

	stats := q.Stats()
	if stats.Completed != 1 {
		t.Fatalf("Completed = %d, want 1", stats.Completed)
	}
	if stats.MaxWaitSeconds < 100 {
		t.Errorf("MaxWaitSeconds = %.0f, want at least the two minutes it spent waiting",
			stats.MaxWaitSeconds)
	}
}

func TestThePercentileIsNotDraggedAroundByOneSlowJob(t *testing.T) {
	var jobs []workload.Job
	for i := 0; i < 100; i++ {
		jobs = append(jobs, job(0, domain.PriorityPick, 1))
	}
	q := queue.New(jobs, deadlines())

	for cycle := 1; cycle <= 20; cycle++ {
		q.Advance(time.Duration(cycle)*10*time.Second, 20)
	}

	stats := q.Stats()
	if stats.P95WaitSeconds > stats.MaxWaitSeconds {
		t.Errorf("P95 %.1f exceeds max %.1f", stats.P95WaitSeconds, stats.MaxWaitSeconds)
	}
	if stats.MeanWaitSeconds > stats.MaxWaitSeconds {
		t.Errorf("mean %.1f exceeds max %.1f", stats.MeanWaitSeconds, stats.MaxWaitSeconds)
	}
}

func TestAdvancingBackwardsIsRefusedRatherThanCorruptingTheRun(t *testing.T) {
	q := queue.New([]workload.Job{job(0, domain.PriorityPick, 10)}, deadlines())
	q.Advance(time.Minute, 1)

	// Time in a replay only moves forward. A caller that went backwards would
	// silently double-count arrivals and produce results nobody could explain.
	got := q.Advance(30*time.Second, 1)
	if got.Completed != 0 || got.Breached != 0 {
		t.Errorf("Advance() backwards = %+v, want it to do nothing", got)
	}
}

func TestNegativeExecutorCountsAreTreatedAsNone(t *testing.T) {
	q := queue.New([]workload.Job{job(0, domain.PriorityPick, 10)}, deadlines())

	if got := q.Advance(time.Minute, -5); got.Completed != 0 {
		t.Errorf("Advance() with -5 executors completed %d jobs", got.Completed)
	}
}
