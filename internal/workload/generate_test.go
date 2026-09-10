package workload_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

func mine() domain.Mine {
	return domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 40, BackgroundRate: 20}
}

func scenario() domain.Scenario {
	return domain.Scenario{
		ID: "quiet", MineID: "storhall", Duration: 6 * time.Hour, JobSeconds: 20,
		PriorityMix: map[domain.Priority]float64{
			domain.PriorityAssociate: 1,
			domain.PriorityLocate:    1,
			domain.PriorityPick:      2,
		},
		Seed: 42,
	}
}

func generate(t *testing.T, m domain.Mine, s domain.Scenario) []workload.Job {
	t.Helper()
	jobs, err := workload.Generate(m, s)
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	return jobs
}

func countBetween(jobs []workload.Job, from, to time.Duration) int {
	count := 0
	for _, job := range jobs {
		if job.SubmittedAt >= from && job.SubmittedAt < to {
			count++
		}
	}
	return count
}

// The property the whole comparison rests on: two runs of the same scenario
// must replay exactly the same jobs, or a difference in results cannot be
// attributed to the settings that changed.
func TestTheSameSeedProducesExactlyTheSameJobs(t *testing.T) {
	first := generate(t, mine(), scenario())
	second := generate(t, mine(), scenario())

	if len(first) != len(second) {
		t.Fatalf("job counts differ: %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("job %d differs: %+v and %+v", i, first[i], second[i])
		}
	}
}

func TestADifferentSeedProducesADifferentWorkload(t *testing.T) {
	other := scenario()
	other.Seed = 43

	if len(generate(t, mine(), scenario())) == len(generate(t, mine(), other)) {
		// Not impossible, but the two should differ somewhere; compare the
		// first arrival too so a coincidence in count does not pass.
		first := generate(t, mine(), scenario())
		second := generate(t, mine(), other)
		if len(first) > 0 && len(second) > 0 && first[0] == second[0] {
			t.Error("two seeds produced the same workload")
		}
	}
}

func TestJobsArriveInOrder(t *testing.T) {
	jobs := generate(t, mine(), scenario())

	for i := 1; i < len(jobs); i++ {
		if jobs[i].SubmittedAt < jobs[i-1].SubmittedAt {
			t.Fatalf("job %d arrives at %v, before job %d at %v",
				i, jobs[i].SubmittedAt, i-1, jobs[i-1].SubmittedAt)
		}
	}
}

func TestEveryJobFallsInsideTheScenario(t *testing.T) {
	s := scenario()
	for _, job := range generate(t, mine(), s) {
		if job.SubmittedAt < 0 || job.SubmittedAt >= s.Duration {
			t.Fatalf("job arrives at %v, outside the %v scenario", job.SubmittedAt, s.Duration)
		}
	}
}

// A busy mine is used here on purpose. These are counts of a random process,
// and at twenty events an hour the sampling noise alone is wider than the
// effect being measured — the test would fail perhaps one run in ten and teach
// everyone to re-run it.
func TestQuietMonitoringIsSteadyEnoughToProvisionFor(t *testing.T) {
	m := mine()
	m.BackgroundRate = 400
	jobs := generate(t, m, scenario())

	first := countBetween(jobs, 0, time.Hour)
	fourth := countBetween(jobs, 3*time.Hour, 4*time.Hour)
	if first == 0 || fourth == 0 {
		t.Fatalf("background produced no work: %d and %d jobs", first, fourth)
	}

	// Shifts make the day vary, but nothing like a burst does.
	ratio := float64(first) / float64(fourth)
	if ratio < 0.5 || ratio > 2 {
		t.Errorf("quiet hours differ by %.1fx (%d and %d jobs), want a steady baseline",
			ratio, first, fourth)
	}
}

// This is the event the whole system exists for: an hour of work arriving in
// minutes.
func TestABurstFloodsTheQueue(t *testing.T) {
	s := scenario()
	s.Bursts = []domain.Burst{{At: 2 * time.Hour, Magnitude: 50, AftershockDecay: time.Hour}}
	jobs := generate(t, mine(), s)

	quiet := countBetween(jobs, time.Hour, 2*time.Hour)
	burst := countBetween(jobs, 2*time.Hour, 2*time.Hour+10*time.Minute)

	if burst <= quiet {
		t.Errorf("the burst's first ten minutes produced %d jobs against %d in the "+
			"previous quiet hour; a magnitude-50 event should dwarf it", burst, quiet)
	}
}

// Aftershocks are what make the recovery interesting. Load does not return to
// background the moment the event is over.
func TestAftershocksDecayRatherThanStopping(t *testing.T) {
	s := scenario()
	s.Duration = 8 * time.Hour
	s.Bursts = []domain.Burst{{At: time.Hour, Magnitude: 60, AftershockDecay: 3 * time.Hour}}
	m := mine()
	m.BackgroundRate = 400
	jobs := generate(t, m, s)

	peak := countBetween(jobs, time.Hour, time.Hour+30*time.Minute)
	settling := countBetween(jobs, 2*time.Hour, 2*time.Hour+30*time.Minute)
	background := countBetween(jobs, 0, 30*time.Minute)

	if settling >= peak {
		t.Errorf("an hour after the event there were %d jobs against %d at the peak; "+
			"aftershocks should be decaying", settling, peak)
	}
	if settling <= background {
		t.Errorf("an hour after the event there were %d jobs against %d at background; "+
			"aftershocks should still be elevated", settling, background)
	}
}

func TestAnEventWithNoAftershocksSubsidesImmediately(t *testing.T) {
	m := mine()
	m.BackgroundRate = 400 // enough events that the counts are not noise
	s := scenario()
	s.Bursts = []domain.Burst{{At: 2 * time.Hour, Magnitude: 50}}
	jobs := generate(t, m, s)

	after := countBetween(jobs, 3*time.Hour, 4*time.Hour)
	background := countBetween(jobs, time.Hour, 2*time.Hour)

	if float64(after) > 2*float64(background) {
		t.Errorf("an hour after a burst with no aftershock decay there were %d jobs "+
			"against %d at background", after, background)
	}
}

func TestThePriorityMixIsRespected(t *testing.T) {
	s := scenario()
	s.PriorityMix = map[domain.Priority]float64{
		domain.PriorityAssociate: 1,
		domain.PriorityPick:      3,
	}
	jobs := generate(t, mine(), s)

	counts := map[domain.Priority]int{}
	for _, job := range jobs {
		counts[job.Priority]++
	}
	if counts[domain.PriorityLocate] != 0 {
		t.Errorf("%d jobs at a priority the mix does not name", counts[domain.PriorityLocate])
	}

	total := counts[domain.PriorityAssociate] + counts[domain.PriorityPick]
	if total == 0 {
		t.Fatal("no jobs were generated")
	}
	share := float64(counts[domain.PriorityPick]) / float64(total)
	if math.Abs(share-0.75) > 0.05 {
		t.Errorf("P25 got %.0f%% of jobs, want about 75%%", share*100)
	}
}

func TestJobDurationsVaryAroundTheScenariosOwnFigure(t *testing.T) {
	jobs := generate(t, mine(), scenario())

	total, distinct := 0.0, map[float64]bool{}
	for _, job := range jobs {
		if job.Seconds <= 0 {
			t.Fatalf("a job takes %v seconds", job.Seconds)
		}
		total += job.Seconds
		distinct[job.Seconds] = true
	}

	mean := total / float64(len(jobs))
	if math.Abs(mean-20) > 4 {
		t.Errorf("mean job duration is %.1fs, want about 20s", mean)
	}
	// Every job taking exactly the nominal time would make the queue
	// unrealistically well-behaved.
	if len(distinct) < 10 {
		t.Errorf("only %d distinct durations; jobs should vary", len(distinct))
	}
}

func TestAMineWithMoreSensorsProducesMoreJobsPerEvent(t *testing.T) {
	small, large := mine(), mine()
	small.Sensors, large.Sensors = 10, 100

	fewer := len(generate(t, small, scenario()))
	more := len(generate(t, large, scenario()))

	// Each detecting sensor contributes a pick, so the array size sets how
	// much work one event becomes.
	if more <= fewer {
		t.Errorf("100 sensors produced %d jobs against %d from 10", more, fewer)
	}
}

func TestABurstStillArrivesWhenTheresNoBackgroundActivity(t *testing.T) {
	m := mine()
	m.BackgroundRate = 0
	s := scenario()
	s.Bursts = []domain.Burst{{At: time.Hour, Magnitude: 100, AftershockDecay: time.Hour}}

	jobs := generate(t, m, s)
	if len(jobs) == 0 {
		t.Error("a burst on a quiet mine produced nothing")
	}
	if countBetween(jobs, 0, time.Hour) != 0 {
		t.Error("jobs arrived before the event on a mine with no background activity")
	}
}

func TestAnInvalidScenarioIsRefused(t *testing.T) {
	s := scenario()
	s.JobSeconds = 0

	if _, err := workload.Generate(mine(), s); err == nil {
		t.Error("Generate() = nil error for an invalid scenario")
	}
}

func TestAnInvalidMineIsRefused(t *testing.T) {
	m := mine()
	m.Sensors = 0

	if _, err := workload.Generate(m, scenario()); err == nil {
		t.Error("Generate() = nil error for an invalid mine")
	}
}

// A scenario can be specified in a way that would generate hundreds of
// millions of jobs. Refusing is far better than exhausting memory while
// somebody waits for a page to load.
func TestAnAbsurdlyLargeWorkloadIsRefusedRatherThanAttempted(t *testing.T) {
	m := mine()
	m.Sensors = 100000
	m.BackgroundRate = 100000
	s := scenario()
	s.Duration = 30 * 24 * time.Hour

	_, err := workload.Generate(m, s)
	if err == nil {
		t.Fatal("Generate() = nil error for a workload of hundreds of millions of jobs")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "jobs") {
		t.Errorf("Generate() = %q, want it to explain the size problem", err)
	}
}
