package intent

import (
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// Skipping a judgement is an optimisation, and an optimisation that changes a
// decision is a defect. A whole day of a busy mine is planned twice — once
// judging every open event every cycle, once skipping the ones that cannot
// have changed — and every update and every transition must match.
//
// It matters because the skip rests on an argument rather than a test: nothing
// protected moves faster than the quickest vehicle, so until the time since a
// judgement is enough to cover its slack, no boundary can have been crossed.
// If that argument is ever wrong, this is what says so.
func TestSkippingSettledEventsDecidesExactlyWhatJudgingEverythingDoes(t *testing.T) {
	mine := domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 24, BackgroundRate: 90}
	scenario := domain.Scenario{
		ID: "day", MineID: "storhall", Name: "A day", Duration: 6 * time.Hour,
		JobSeconds: 28, PickJitter: 5 * time.Millisecond, Seed: 4242,
		PriorityMix: map[domain.Priority]float64{100: 22, 50: 51, 0: 27},
		Bursts:      []domain.Burst{{At: time.Hour, Magnitude: 30, AftershockDecay: time.Hour}},
	}
	built, err := workload.Build(mine, scenario)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	settings := domain.DefaultIntent()
	settings.Mode = domain.IntentBoth
	quick, thorough := New(built, workload.NewCatalogue(built), settings), New(built, workload.NewCatalogue(built), settings)
	thorough.alwaysJudge = true

	// The two catalogues are told about the same work at the same moments, so
	// both planners see the same locations at the same cycles.
	finish := func(c *workload.Catalogue, at time.Duration, ids []domain.JobID) {
		if _, err := c.Observe(at, ids); err != nil {
			t.Fatalf("Observe() = %v", err)
		}
	}
	done := 0
	changed := 0

	for cycle := 1; cycle <= 1000; cycle++ {
		at := time.Duration(cycle) * 15 * time.Second
		// Serve a slice of the log in order, as a constrained fleet would.
		var ids []domain.JobID
		for ; done < len(built.Jobs) && len(ids) < 9; done++ {
			if built.Jobs[done].SubmittedAt > at {
				break
			}
			ids = append(ids, built.Jobs[done].ID)
		}
		if len(ids) > 0 {
			finish(quick.catalogue, at, ids)
			finish(thorough.catalogue, at, ids)
		}

		// A change of settings part-way through: every judgement and request
		// has to be made again on both sides.
		if cycle == 400 {
			next := settings
			next.Lookahead = 0
			next.Mode = domain.IntentDecay
			quick.Configure(next)
			thorough.Configure(next)
		}

		a, b := quick.Plan(at), thorough.Plan(at)
		if len(a.Updates) != len(b.Updates) || len(a.Transitions) != len(b.Transitions) {
			t.Fatalf("cycle %d: skipping gave %d updates and %d transitions, judging everything gave %d and %d",
				cycle, len(a.Updates), len(a.Transitions), len(b.Updates), len(b.Transitions))
		}
		for k := range a.Updates {
			if a.Updates[k] != b.Updates[k] {
				t.Fatalf("cycle %d update %d: %+v, judging everything %+v", cycle, k, a.Updates[k], b.Updates[k])
			}
		}
		for k := range a.Transitions {
			if a.Transitions[k] != b.Transitions[k] {
				t.Fatalf("cycle %d transition %d: %+v, judging everything %+v",
					cycle, k, a.Transitions[k], b.Transitions[k])
			}
		}
		changed += len(a.Updates)
	}

	if changed == 0 {
		t.Fatal("nothing was ever moved, so this compared two planners doing nothing")
	}
	t.Logf("%d updates over 1000 cycles", changed)
}
