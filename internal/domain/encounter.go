package domain

import (
	"fmt"
	"strings"
	"time"
)

// EncounterSpec scripts encounters: events placed where a unit is about to be,
// so that the decision they ask for exists at a stated notice.
//
// The decisions a case finds in an ordinary day are the ones the day happens
// to give. At moderate ground motion a calibrated day gives hundreds, which is
// enough to measure; at high ground motion it gives a handful, because an
// event large enough to shake a drift that hard is rare — and those are the
// decisions that matter most. Waiting for them costs days of runs for a few
// decisions each.
//
// So they are scripted. Each encounter places one event of a stated magnitude
// where a unit's own track takes it, timed so the unit reaches the edge of the
// event's zone exactly Lead after the event happens. Nothing else about the
// world changes: the unit's track is the one the workforce already drew, the
// other events are the ones the day already had, and the scripted events draw
// from streams of their own.
//
// Lead is what makes it an experiment. A decision has Lead seconds from the
// event to be made, less the reaction time the case allows, so a sweep over
// Lead asks the question directly: how much notice does the processing need?
type EncounterSpec struct {
	// Count is how many encounters are scripted over the scenario. One that
	// cannot be placed — a unit that stands still, a track too short — is
	// left out rather than forced, so a run may have fewer.
	Count int

	// Magnitude is the size of each scripted event. It decides how far the
	// levels reach: at Nuttli 2.5 high ground motion reaches about 140 m,
	// moderate about 1.4 km.
	Magnitude float64

	// Lead is how long after the event the unit reaches the edge of its zone
	// at Level: the notice the decision has.
	Lead time.Duration

	// Level is the zone the unit is timed against. High by default — the
	// ground motion that makes an encounter worth scripting.
	Level string

	// Kinds is whom encounters are scripted for.
	Kinds []string
}

// DefaultEncounters is a starting point, not a finding: twenty encounters a
// day at Nuttli 2.5, each giving two minutes' notice of high ground motion.
func DefaultEncounters() EncounterSpec {
	return EncounterSpec{
		Count: 20, Magnitude: 2.5, Lead: 2 * time.Minute, Level: "high",
		Kinds: []string{EntityPerson, EntityCrewedVehicle, EntityAutonomousVehicle},
	}
}

// Validate rejects encounters that could not be scripted or replayed.
func (e EncounterSpec) Validate() error {
	var problems []string
	if e.Count < 1 {
		problems = append(problems, fmt.Sprintf("encounters.count must be >= 1, got %d", e.Count))
	}
	if e.Magnitude < -2 || e.Magnitude > 6 {
		problems = append(problems, fmt.Sprintf("encounters.magnitude must be in [-2, 6], got %v", e.Magnitude))
	}
	if e.Lead <= 0 {
		problems = append(problems, fmt.Sprintf("encounters.lead_seconds must be > 0, got %v: an encounter "+
			"nobody could be told of in time asks no question", e.Lead.Seconds()))
	}
	switch e.Level {
	case "moderate", "high", "very-high":
	default:
		problems = append(problems, fmt.Sprintf("encounters.level %q is not one of moderate, high, very-high", e.Level))
	}
	if len(e.Kinds) == 0 {
		problems = append(problems, "encounters.kinds is empty: there is nobody to script an encounter for")
	}
	for _, k := range e.Kinds {
		switch k {
		case EntityPerson, EntityCrewedVehicle, EntityAutonomousVehicle:
		default:
			problems = append(problems, fmt.Sprintf("encounters.kinds: %q is not a kind of unit; there are "+
				"%s, %s and %s", k, EntityPerson, EntityCrewedVehicle, EntityAutonomousVehicle))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("encounters cannot be scripted: %s", strings.Join(problems, "; "))
	}
	return nil
}
