package workload

import (
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
)

// encounterStream seeds the scripted encounters. Its own stream, and drawn
// after the day's own events, so scripting encounters into a scenario moves
// neither the rock nor the workforce: the same seed gives the same day, with
// the scripted events added to it.
const encounterStream = 0xD1B54A32D192ED03

// encounterStep is how finely a track is walked when timing an encounter. A
// unit covers at most a few metres in it, which is far below the tens of
// metres a zone's edge is known to.
const encounterStep = 5 * time.Second

// encounterTries is how many moments are tried before an encounter is given
// up on. A unit at a face for an hour cannot be given one, and a run that
// silently forced it — moving the unit, or shrinking the notice — would report
// a decision nobody could have had.
const encounterTries = 40

// encounter is one scripted event: where and when it happens, and the unit it
// was scripted for.
type encounter struct {
	at    time.Duration
	truth domain.Point
	unit  string
}

// scripted places a scenario's encounters: for each, an event whose zone the
// chosen unit reaches exactly a lead after it happens, having been outside it
// when it happened.
//
// The unit's track is the one the workforce already drew; only the event is
// placed. In time order, so the events that follow stay in time order.
func scripted(spec domain.EncounterSpec, entities []domain.Entity, extent domain.Extent,
	duration time.Duration, random *rand.Rand) []encounter {
	units := ofKinds(entities, spec.Kinds)
	if len(units) == 0 {
		return nil
	}
	radius, ok := hazard.Default.Zones(spec.Magnitude, 0)[levelOf(spec.Level)]
	if !ok {
		// A magnitude too small to reach the level anywhere: there is no zone
		// to time anybody against.
		return nil
	}

	var out []encounter
	for n := 0; n < spec.Count; n++ {
		for try := 0; try < encounterTries; try++ {
			unit := units[random.IntN(len(units))]
			at := time.Duration(random.Float64() * float64(duration-spec.Lead))
			if at < 0 {
				break // a scenario shorter than the notice it asks for
			}
			if truth, ok := place(unit, at, spec.Lead, radius, extent); ok {
				out = append(out, encounter{at: at, truth: truth, unit: unit.ID})
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// place is where an event must happen for this unit to reach the edge of its
// zone exactly a lead after it, having been outside the zone until then.
//
// The unit enters the zone when it comes within radius of the event, so the
// event goes a zone's radius ahead of where the unit will be at that moment,
// along the way it is going: at the moment of entry it stands exactly on the
// edge, and a step later it is inside. In rock rather than in the drift, which
// is where hypocentres are.
//
// A unit barely moving then — at a face, or waiting out a blast — gives a
// direction that is noise or, standing still, none at all; it is refused
// rather than moved.
func place(unit domain.Entity, at, lead time.Duration, radius float64, extent domain.Extent) (domain.Point, bool) {
	entry := at + lead
	here, ahead := unit.PositionAt(entry), unit.PositionAt(entry+encounterStep)
	dx, dy, dz := ahead.X-here.X, ahead.Y-here.Y, ahead.Z-here.Z
	travelled := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if travelled < 1 {
		return domain.Point{}, false
	}
	truth := domain.Point{
		X: here.X + radius*dx/travelled,
		Y: here.Y + radius*dy/travelled,
		Z: here.Z + radius*dz/travelled,
	}
	if !extent.Contains(truth) {
		return domain.Point{}, false // beyond the mine: no sensor array around it
	}
	for t := at; t < entry; t += encounterStep {
		if unit.PositionAt(t).DistanceTo(truth) <= radius {
			// Already inside when the event happens: that is a way-out
			// decision, not the turn-back this was scripted for.
			return domain.Point{}, false
		}
	}
	return truth, true
}

// ofKinds is the units of the kinds asked for, in id order so the draw does
// not depend on how the workforce was built.
func ofKinds(entities []domain.Entity, kinds []string) []domain.Entity {
	var out []domain.Entity
	for _, e := range entities {
		for _, k := range kinds {
			if e.Kind == k && len(e.Track) > 0 {
				out = append(out, e)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func levelOf(name string) hazard.Level {
	for _, l := range hazard.Levels {
		if l.String() == name {
			return l
		}
	}
	return hazard.None
}
