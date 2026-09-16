package workload_test

import (
	"time"

	"testing"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// Job identity has to come from the seed, because the mine directs intent at a
// job by id and a comparison between two runs lines them up by it. An id that
// depended on anything else would make the same scenario produce jobs that
// cannot be matched between runs.

func TestTwoRunsOfOneScenarioGiveTheSameJobTheSameID(t *testing.T) {
	mine := domain.Mine{ID: "m", Name: "m", Sensors: 12, BackgroundRate: 90}
	scenario := domain.Scenario{
		ID: "s", MineID: "m", Name: "s", Duration: 30 * time.Minute,
		JobSeconds: 20, Seed: 7, PriorityMix: map[domain.Priority]float64{100: 1, 25: 2},
	}

	first, err := workload.Generate(mine, scenario)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	second, err := workload.Generate(mine, scenario)
	if err != nil {
		t.Fatalf("generating again: %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("the seed did not hold: %d jobs then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("job %d: id %d then %d", i, first[i].ID, second[i].ID)
		}
		if first[i].Priority != second[i].Priority || first[i].SubmittedAt != second[i].SubmittedAt {
			t.Fatalf("job %d differs between runs of the same seed", i)
		}
	}
}

func TestEveryJobHasADistinctID(t *testing.T) {
	mine := domain.Mine{ID: "m", Name: "m", Sensors: 12, BackgroundRate: 90}
	scenario := domain.Scenario{
		ID: "s", MineID: "m", Name: "s", Duration: 30 * time.Minute,
		JobSeconds: 20, Seed: 7, PriorityMix: map[domain.Priority]float64{100: 1},
	}

	jobs, err := workload.Generate(mine, scenario)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("no jobs generated, so this proves nothing")
	}

	seen := map[domain.JobID]bool{}
	for _, job := range jobs {
		if seen[job.ID] {
			t.Fatalf("id %d was used twice; intent would reach the wrong job", job.ID)
		}
		seen[job.ID] = true
	}
}
