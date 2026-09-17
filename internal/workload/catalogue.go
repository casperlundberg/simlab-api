package workload

import (
	"fmt"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/seismic"
)

// Catalogue is the mine keeping track of its own work: which picks have been
// processed, and so which events it can put a location on.
//
// It learns that work finished from the orchestrator, by job id, and from
// nothing else. In particular it never reads an event's Truth — a location is
// solved from processed picks, exactly as a real installation would have to,
// so the distance between the two is an honest measure of the estimate rather
// than something the estimate was allowed to see.
//
// Not safe for concurrent use; a run observes from one goroutine.
type Catalogue struct {
	workload Workload

	// done is per job, so a job reported twice is caught rather than counted
	// twice.
	done []bool

	processed []int
	events    []domain.SeismicEvent
}

// NewCatalogue starts a catalogue for a workload with nothing processed.
func NewCatalogue(w Workload) *Catalogue {
	events := make([]domain.SeismicEvent, len(w.Events))
	for i, event := range w.Events {
		sensors := make([]string, len(event.Picks))
		for k, pick := range event.Picks {
			sensors[k] = pick.SensorID
		}
		events[i] = domain.SeismicEvent{
			Sequence: i + 1,
			Origin:   event.Origin,
			Burst:    event.Burst,
			Truth:    event.Truth,
			Sensors:  sensors,
		}
	}
	return &Catalogue{
		workload:  w,
		done:      make([]bool, len(w.Jobs)),
		processed: make([]int, len(w.Events)),
		events:    events,
	}
}

// Events is every event as the mine currently knows it.
func (c *Catalogue) Events() []domain.SeismicEvent {
	return append([]domain.SeismicEvent(nil), c.events...)
}

// Observe records that jobs finished by at, and returns the events that were
// located or fully processed as a result.
//
// at is the time the mine learned of it. A run reports completions once per
// decision interval, so a location is timed to the interval in which its
// fourth pick finished rather than to the instant — the same resolution every
// other measurement of a run has.
func (c *Catalogue) Observe(at time.Duration, finished []domain.JobID) ([]domain.SeismicEvent, error) {
	touched := []int{}
	seen := map[int]bool{}

	for _, id := range finished {
		if id < 0 || int(id) >= len(c.workload.Jobs) {
			return nil, fmt.Errorf("job %d was reported finished, but the mine never submitted it "+
				"(it submitted jobs 0 to %d)", id, len(c.workload.Jobs)-1)
		}
		if c.done[id] {
			return nil, fmt.Errorf("job %d was reported finished twice", id)
		}
		c.done[id] = true

		event := c.workload.Jobs[id].Event
		c.processed[event]++
		if !seen[event] {
			seen[event] = true
			touched = append(touched, event)
		}
	}

	changed := []domain.SeismicEvent{}
	for _, i := range touched {
		record := &c.events[i]
		picks := len(c.workload.Events[i].Picks)
		moved := false

		if record.LocatedAt == nil && c.processed[i] >= seismic.MinimumPicks {
			if location, ok := c.locate(i); ok {
				when := at
				record.LocatedAt, record.Located = &when, location
				moved = true
			}
		}
		if record.ProcessedAt == nil && c.processed[i] == picks {
			when := at
			record.ProcessedAt = &when
			if location, ok := c.locate(i); ok {
				record.Final = location
			}
			moved = true
		}
		if moved {
			changed = append(changed, *record)
		}
	}
	return changed, nil
}

// locate solves for event i from the picks processed so far.
func (c *Catalogue) locate(i int) (*domain.Location, bool) {
	event := c.workload.Events[i]

	picks := make([]seismic.Pick, 0, len(event.Picks))
	for k, pick := range event.Picks {
		if c.done[int(event.FirstJob)+k] {
			picks = append(picks, pick)
		}
	}

	estimate, err := c.workload.Model.Locate(c.workload.Layout.Sensors, picks, c.workload.Layout.Extent)
	if err != nil {
		// Too few picks, or picks from sensors the layout does not know.
		// Either way there is nothing an operator could point at.
		return nil, false
	}
	return &domain.Location{
		At:                 estimate.At,
		RMSResidualSeconds: estimate.RMSResidualSeconds,
		Picks:              estimate.Picks,
	}, true
}
