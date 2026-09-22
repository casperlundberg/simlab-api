package observe

import "github.com/casperlundberg/simlab-api/internal/domain"

// Views is a run's two readings of its units: what the mine's own systems
// read, and the simulator's truth. Knowledge chooses between them, and can
// change while a run is in flight, so both are built once and kept.
type Views struct {
	Mine   Whereabouts
	Oracle Whereabouts
}

// ViewsOf builds a run's views from its units and the tunnels they move
// through, with people walking no faster than walking metres a second. The
// run engine and the API both build them here, so what a page draws is what
// the planner decided from.
func ViewsOf(entities []domain.Entity, tunnels []domain.Tunnel, walking float64) Views {
	return Views{
		Mine:   NewPositioned(entities, tunnels, walking),
		Oracle: NewTracks(entities),
	}
}

// For is the view a knowledge decides from: the truth for the oracle arm, the
// mine's own reading for anything else — an unknown knowledge must never be
// handed foresight by accident.
func (v Views) For(knowledge domain.IntentKnowledge) Whereabouts {
	if knowledge == domain.KnowledgeTruth {
		return v.Oracle
	}
	return v.Mine
}
