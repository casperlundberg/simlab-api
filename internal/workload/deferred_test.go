package workload_test

import (
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/seismic"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// A mine that runs a locate stage does not have a location the moment its
// fourth pick is processed: it has the *inputs* to one. The location exists
// when the locate job finishes, and the difference is the whole point of
// simulating the pipeline end to end — it is time an operator spends waiting.

func deferredCatalogue(t *testing.T) (*workload.Catalogue, workload.Workload, int) {
	t.Helper()
	m, s := liveScenario()
	w := build(t, m, s)
	return workload.NewCatalogue(w).Deferred(), w, eventWith(t, w, seismic.MinimumPicks)
}

func locate(t *testing.T, c *workload.Catalogue, at time.Duration, events []int) []domain.SeismicEvent {
	t.Helper()
	changed, err := c.Locate(at, events)
	if err != nil {
		t.Fatalf("Locate(%s, %v) = %v", at, events, err)
	}
	return changed
}

func TestWithALocateStageProcessingThePicksDoesNotLocateTheEvent(t *testing.T) {
	c, w, i := deferredCatalogue(t)

	observe(t, c, time.Minute, jobsOf(w.Events[i], 0, seismic.MinimumPicks))

	if got := c.Events()[i]; got.LocatedAt != nil {
		t.Errorf("event %d was located at %s with no locate having run", i, *got.LocatedAt)
	}
}

func TestWithALocateStageAnEventIsLocatedWhenItsLocateFinishes(t *testing.T) {
	c, w, i := deferredCatalogue(t)
	observe(t, c, time.Minute, jobsOf(w.Events[i], 0, seismic.MinimumPicks))

	changed := locate(t, c, 2*time.Minute, []int{i})

	if len(changed) != 1 {
		t.Fatalf("Locate returned %d events, want 1", len(changed))
	}
	got := c.Events()[i]
	if got.LocatedAt == nil {
		t.Fatal("the event was not located by its own locate job")
	}
	if *got.LocatedAt != 2*time.Minute {
		t.Errorf("LocatedAt = %s, want when the locate finished (2m)", *got.LocatedAt)
	}
	if got.Located == nil {
		t.Error("the event was marked located with no location on it")
	}
}

// The locate solves from whatever picks are processed. Running one before
// there are four cannot produce a location, and must not claim to.
func TestALocateWithTooFewPicksProducesNoLocation(t *testing.T) {
	c, w, i := deferredCatalogue(t)
	observe(t, c, time.Minute, jobsOf(w.Events[i], 0, seismic.MinimumPicks-1))

	locate(t, c, 2*time.Minute, []int{i})

	if got := c.Events()[i]; got.LocatedAt != nil {
		t.Errorf("event %d claims a location from %d picks", i, seismic.MinimumPicks-1)
	}
}

// The first location is the one an operator acts on, so it keeps the time the
// first locate produced it even as later locates sharpen the answer.
func TestALaterLocateSharpensTheLocationWithoutMovingWhenItWasFirstKnown(t *testing.T) {
	c, w, i := deferredCatalogue(t)
	picks := len(w.Events[i].Picks)
	if picks <= seismic.MinimumPicks {
		t.Skipf("event %d has only %d picks, so there is no later locate to run", i, picks)
	}
	observe(t, c, time.Minute, jobsOf(w.Events[i], 0, seismic.MinimumPicks))
	locate(t, c, 2*time.Minute, []int{i})
	first := *c.Events()[i].LocatedAt

	observe(t, c, 3*time.Minute, jobsOf(w.Events[i], seismic.MinimumPicks, picks))
	locate(t, c, 4*time.Minute, []int{i})

	got := c.Events()[i]
	if *got.LocatedAt != first {
		t.Errorf("LocatedAt moved to %s; the first location is when the operator first knew", *got.LocatedAt)
	}
	if got.Final == nil {
		t.Error("no final location after every pick was processed and a locate ran")
	}
}

// Every pick being processed is not the end of the work: the location an
// operator finally acts on comes from a locate that ran after them.
func TestTheFinalLocationWaitsForALocateAfterTheLastPick(t *testing.T) {
	c, w, i := deferredCatalogue(t)
	picks := len(w.Events[i].Picks)

	observe(t, c, time.Minute, jobsOf(w.Events[i], 0, picks))

	if got := c.Events()[i]; got.Final != nil {
		t.Error("a final location appeared with no locate having run")
	}
	locate(t, c, 2*time.Minute, []int{i})
	if got := c.Events()[i]; got.Final == nil {
		t.Error("no final location after a locate ran on every pick")
	}
}

func TestLocatingAnEventTheMineDoesNotHaveIsRefused(t *testing.T) {
	c, _, _ := deferredCatalogue(t)

	if _, err := c.Locate(time.Minute, []int{9999}); err == nil {
		t.Error("locating an event the mine never detected was accepted")
	}
}

// Without a locate stage the catalogue keeps deciding for itself, which is what
// every scenario recorded before the pipeline existed relies on.
func TestWithoutALocateStageAnEventIsStillLocatedByItsFourthPick(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)
	c := workload.NewCatalogue(w)
	i := eventWith(t, w, seismic.MinimumPicks)

	observe(t, c, time.Minute, jobsOf(w.Events[i], 0, seismic.MinimumPicks))

	if got := c.Events()[i]; got.LocatedAt == nil {
		t.Error("an event with four picks processed was not located")
	}
}
