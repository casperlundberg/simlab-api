package workload

import (
	"fmt"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
	"github.com/casperlundberg/simlab-api/internal/seismic"
)

// locationUncertainty is how far, in metres, the mine allows a location to be
// out when judging who it threatens. Re-entry practice restricts at least 50 m
// around an event, which Vallejos and McKinnon relate to location errors of up
// to 50 m; a first location from four picks can be further out than that, so
// this is a floor on caution rather than a bound on error.
const locationUncertainty = 50.0

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
		magnitude := event.Magnitude
		events[i] = domain.SeismicEvent{
			Sequence:        i + 1,
			Origin:          event.Origin,
			Burst:           event.Burst,
			Truth:           event.Truth,
			Magnitude:       &magnitude,
			Exposed:         exposure(w.Entities, event.Truth, magnitude, event.Origin, 0),
			Sensors:         sensors,
			PickProcessedAt: make([]*time.Duration, len(event.Picks)),
			Intent:          []domain.IntentTransition{},
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
	out := make([]domain.SeismicEvent, len(c.events))
	for i, event := range c.events {
		out[i] = snapshot(event)
	}
	return out
}

// snapshot copies an event's record, so a caller holding it does not see the
// catalogue's later updates to the pick times it shares.
func snapshot(event domain.SeismicEvent) domain.SeismicEvent {
	event.PickProcessedAt = append([]*time.Duration(nil), event.PickProcessedAt...)
	event.Intent = append([]domain.IntentTransition{}, event.Intent...)
	return event
}

// Location is the mine's first location for event i, or nil before it has
// one. This, and never the event's Truth, is what anything ordering work by
// location may read.
func (c *Catalogue) Location(i int) *domain.Location { return c.events[i].Located }

// Finished reports whether a job has been reported finished.
func (c *Catalogue) Finished(id domain.JobID) bool { return c.done[id] }

// Remaining is how many of event i's picks are still to be processed.
func (c *Catalogue) Remaining(i int) int { return len(c.workload.Events[i].Picks) - c.processed[i] }

// RecordIntent adds a change of intent to event i's record, and returns the
// record as it now stands.
func (c *Catalogue) RecordIntent(i int, transition domain.IntentTransition) domain.SeismicEvent {
	c.events[i].Intent = append(c.events[i].Intent, transition)
	return snapshot(c.events[i])
}

// Observe records that jobs finished by at, and returns every event one of
// them was a pick of, as it now stands.
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
		when := at
		c.events[event].PickProcessedAt[int(id)-int(c.workload.Events[event].FirstJob)] = &when
		if !seen[event] {
			seen[event] = true
			touched = append(touched, event)
		}
	}

	// Every event touched has changed: at the least, a pick of it has a
	// processed time it did not have before.
	changed := []domain.SeismicEvent{}
	for _, i := range touched {
		record := &c.events[i]
		picks := len(c.workload.Events[i].Picks)

		if record.LocatedAt == nil && c.processed[i] >= seismic.MinimumPicks {
			if location, ok := c.locate(i, at); ok {
				when := at
				record.LocatedAt, record.Located = &when, location
			}
		}
		if record.ProcessedAt == nil && c.processed[i] == picks {
			when := at
			record.ProcessedAt = &when
			if location, ok := c.locate(i, at); ok {
				record.Final = location
			}
		}
		changed = append(changed, snapshot(*record))
	}
	return changed, nil
}

// locate solves for event i from the picks processed so far, and judges who
// that location threatens at the moment it was solved.
func (c *Catalogue) locate(i int, at time.Duration) (*domain.Location, bool) {
	event := c.workload.Events[i]

	picks := make([]seismic.Pick, 0, len(event.Picks))
	readings := 0.0
	for k, pick := range event.Picks {
		if c.done[int(event.FirstJob)+k] {
			picks = append(picks, pick)
			readings += pick.Magnitude
		}
	}

	estimate, err := c.workload.Model.Locate(c.workload.Layout.Sensors, picks, c.workload.Layout.Extent)
	if err != nil {
		// Too few picks, or picks from sensors the layout does not know.
		// Either way there is nothing an operator could point at.
		return nil, false
	}
	magnitude := readings / float64(len(picks))

	zones := map[string]float64{}
	for level, radius := range hazard.Default.Zones(magnitude, locationUncertainty) {
		zones[level.String()] = radius
	}
	return &domain.Location{
		At:                 estimate.At,
		RMSResidualSeconds: estimate.RMSResidualSeconds,
		Picks:              estimate.Picks,
		Magnitude:          &magnitude,
		Zones:              zones,
		Exposed:            exposure(c.workload.Entities, estimate.At, magnitude, at, locationUncertainty),
	}, true
}

// exposure is who is within reach of an event at a moment: everyone whose
// predicted ground motion is at least moderate, worst first.
func exposure(entities []domain.Entity, from domain.Point, magnitude float64, at time.Duration,
	uncertainty float64) []domain.Exposure {
	out := []domain.Exposure{}
	for _, entity := range entities {
		distance := entity.PositionAt(at).DistanceTo(from)
		level := hazard.Default.LevelAt(magnitude, distance, uncertainty)
		if level == hazard.None {
			continue
		}
		out = append(out, domain.Exposure{
			Entity:   entity.ID,
			Level:    level.String(),
			PPV:      hazard.Default.PPV(magnitude, max(0, distance-uncertainty)),
			Distance: distance,
		})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].PPV != out[b].PPV {
			return out[a].PPV > out[b].PPV
		}
		return out[a].Entity < out[b].Entity
	})
	return out
}
