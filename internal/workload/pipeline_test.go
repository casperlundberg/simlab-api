package workload_test

import (
	"testing"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// A scenario with a pipeline and one without must produce the same rock: the
// same events, at the same times and places, detected by the same sensors. That
// is what makes a comparison between them a test of the workflow rather than of
// two different days.
func TestAPipelineChangesTheWorkWithoutMovingTheEvents(t *testing.T) {
	m, s := liveScenario()
	plain := build(t, m, s)

	withPipeline := s
	spec := domain.DefaultPipeline()
	withPipeline.Pipeline = &spec
	piped := build(t, m, withPipeline)

	if len(piped.Events) != len(plain.Events) {
		t.Fatalf("a pipeline changed the event count: %d, want %d",
			len(piped.Events), len(plain.Events))
	}
	for i := range plain.Events {
		a, b := plain.Events[i], piped.Events[i]
		if a.Origin != b.Origin || a.Truth != b.Truth || a.Magnitude != b.Magnitude {
			t.Fatalf("event %d moved: %v/%v/%v became %v/%v/%v",
				i, a.Origin, a.Truth, a.Magnitude, b.Origin, b.Truth, b.Magnitude)
		}
		if len(a.Picks) != len(b.Picks) || a.FirstJob != b.FirstJob {
			t.Fatalf("event %d was detected differently: %d picks from job %d, want %d from %d",
				i, len(b.Picks), b.FirstJob, len(a.Picks), a.FirstJob)
		}
	}
	if len(piped.Jobs) != len(plain.Jobs) {
		t.Fatalf("a pipeline changed the pick count: %d, want %d", len(piped.Jobs), len(plain.Jobs))
	}
}

// With a pipeline every generated job is a pick, at the pick stage's own
// priority: the mix a scenario states is what an undifferentiated stream of
// jobs was submitted at, and says nothing about a workflow with three stages.
func TestWithAPipelineEveryGeneratedJobIsAPickAtTheStagesPriority(t *testing.T) {
	m, s := liveScenario()
	spec := domain.DefaultPipeline()
	spec.Pick.Priority = domain.PriorityRelocate
	s.Pipeline = &spec

	w := build(t, m, s)

	if len(w.Jobs) == 0 {
		t.Fatal("no jobs were generated")
	}
	for _, job := range w.Jobs {
		if job.Stage != workload.StagePick {
			t.Fatalf("job %d is a %s; the generator produces picks only", job.ID, job.Stage)
		}
		if job.Priority != domain.PriorityRelocate {
			t.Fatalf("job %d is at priority %d, want the pick stage's %d",
				job.ID, job.Priority, domain.PriorityRelocate)
		}
	}
}

// The pick stage states what a pick costs, and the spread around it is kept:
// a fleet sized for the mean of a long-tailed distribution is undersized for
// most of the work in the tail.
func TestWithAPipelinePicksCostWhatThePickStageSays(t *testing.T) {
	m, s := liveScenario()
	spec := domain.DefaultPipeline()
	spec.Pick.Seconds = 14.5
	s.Pipeline = &spec

	w := build(t, m, s)

	total, spread := 0.0, false
	for _, job := range w.Jobs {
		total += job.Seconds
		if job.Seconds != w.Jobs[0].Seconds {
			spread = true
		}
	}
	mean := total / float64(len(w.Jobs))
	if mean < 13 || mean > 16 {
		t.Errorf("picks average %.2fs, want about the stage's 14.5s", mean)
	}
	if !spread {
		t.Error("every pick took exactly the same time; the spread was lost")
	}
}
