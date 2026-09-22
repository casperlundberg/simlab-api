package usecase

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
)

// zoneParams are what the cases about an event's zone share: which ground an
// event makes dangerous, for whom, for how long, and how long a unit needs to
// act on being told.
type zoneParams struct {
	// Level is the ground motion that marks the zone: the event's true zone
	// at this level, from its true magnitude. Moderate by default — the level
	// intent protects at, so a case asks about the ground intent acts on.
	Level string `json:"level"`

	// Kinds is who is decided for.
	Kinds []string `json:"kinds"`

	// WindowSeconds is how long after an event its zone still matters to
	// someone about to enter it: aftershocks, and rock the shaking loosened.
	// An assumption, and a sweep axis.
	WindowSeconds float64 `json:"window_seconds"`

	// ReactionSeconds is how long before the moment it matters a unit has to
	// be told to act on it: a vehicle braking and turning, a crew deciding.
	// Also an assumption.
	ReactionSeconds float64 `json:"reaction_seconds"`

	// Need is what the decision waits on: "first-location", any location of
	// the event, or "warning", a location whose own zone reaches the point
	// that matters — a location too far out would not have told the mine this
	// unit was in danger.
	Need string `json:"need"`

	level hazard.Level
}

func defaultZone() zoneParams {
	return zoneParams{
		Level:           hazard.Moderate.String(),
		Kinds:           []string{domain.EntityPerson, domain.EntityCrewedVehicle, domain.EntityAutonomousVehicle},
		WindowSeconds:   1800,
		ReactionSeconds: 30,
		Need:            "first-location",
	}
}

// read decodes a case's parameters over the defaults and checks them.
func readZone(kind string, params json.RawMessage) (zoneParams, error) {
	p := defaultZone()
	if err := decode(kind, params, &p); err != nil {
		return p, err
	}
	var problems []string
	found := false
	for _, l := range hazard.Levels {
		if l.String() == p.Level {
			p.level, found = l, true
		}
	}
	if !found {
		names := make([]string, len(hazard.Levels))
		for i, l := range hazard.Levels {
			names[i] = l.String()
		}
		problems = append(problems, fmt.Sprintf("level %q is not one of %s", p.Level, strings.Join(names, ", ")))
	}
	for _, k := range p.Kinds {
		switch k {
		case domain.EntityPerson, domain.EntityCrewedVehicle, domain.EntityAutonomousVehicle:
		default:
			problems = append(problems, fmt.Sprintf("kinds: %q is not a kind of unit; there are %s, %s and %s",
				k, domain.EntityPerson, domain.EntityCrewedVehicle, domain.EntityAutonomousVehicle))
		}
	}
	if p.WindowSeconds <= 0 {
		problems = append(problems, fmt.Sprintf("window_seconds must be > 0, got %v: a zone that matters for "+
			"no time at all asks no decision", p.WindowSeconds))
	}
	if p.Need != "first-location" && p.Need != "warning" {
		problems = append(problems, fmt.Sprintf("need %q is not one of first-location, warning", p.Need))
	}
	if p.ReactionSeconds < 0 {
		problems = append(problems, fmt.Sprintf("reaction_seconds must be >= 0, got %v", p.ReactionSeconds))
	}
	if len(problems) > 0 {
		return p, fmt.Errorf("parameters of %s: %s", kind, strings.Join(problems, "; "))
	}
	return p, nil
}

// need is what a decision about an event waits on, for a unit where it matters
// most: where it would enter the zone, or where it stands in it.
func (p zoneParams) need(event int, at domain.Point) Need {
	if p.Need == "warning" {
		return Warning{Event: event, At: at, Level: p.Level}
	}
	return FirstLocation{Event: event}
}

// radius is an event's true zone at the level, if its ground motion reaches
// that level at all outside its near field.
func (p zoneParams) radius(e Event) (float64, bool) {
	r, ok := hazard.Default.Zones(e.Magnitude, 0)[p.level]
	return r, ok
}

func (p zoneParams) window() time.Duration {
	return time.Duration(p.WindowSeconds * float64(time.Second))
}

func (p zoneParams) reaction() time.Duration {
	return time.Duration(p.ReactionSeconds * float64(time.Second))
}

// units is the units decided for, by id, so decisions come out in unit order.
func (p zoneParams) units(w World) []domain.Entity {
	var out []domain.Entity
	for _, u := range w.Units {
		for _, k := range p.Kinds {
			if u.Kind == k && len(u.Track) > 0 {
				out = append(out, u)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// entering calls visit for every unit that enters an event's zone within the
// window after it, having been outside it when it happened.
func (p zoneParams) entering(w World, visit func(event int, unit domain.Entity, entry time.Duration)) {
	units := p.units(w)
	for i, e := range w.Events {
		r, ok := p.radius(e)
		if !ok {
			continue
		}
		for _, u := range units {
			if entry, ok := firstEntry(u.Track, e.At, r, e.Origin, e.Origin+p.window()); ok {
				visit(i, u, entry)
			}
		}
	}
}

// ------------------------------------------------------------------ turn-back

// TurnBack is use case 1: a unit heading into an event's zone is turned back
// before it enters. The decision waits on what need says — by default the
// event's first location — and stops being useful a reaction time before the
// unit would have entered. Case 10,
// protecting machines, is this with kinds set to the machines.
type TurnBack struct{ p zoneParams }

func newTurnBack(params json.RawMessage) (Case, error) {
	p, err := readZone("turn-back", params)
	return TurnBack{p}, err
}

func (TurnBack) Kind() string  { return "turn-back" }
func (c TurnBack) Params() any { return c.p }

func (c TurnBack) Opportunities(w World) []Opportunity {
	var out []Opportunity
	c.p.entering(w, func(event int, unit domain.Entity, entry time.Duration) {
		out = append(out, Opportunity{Case: c.Kind(), Event: event, Entity: unit.ID,
			Need: c.p.need(event, unit.PositionAt(entry)), Opens: w.Events[event].Origin, Closes: entry - c.p.reaction()})
	})
	return out
}

// -------------------------------------------------------------------- way-out

// WayOut is use case 3: a unit inside an event's zone when it happens is led
// out by the safest way. The decision waits on what need says, and is useful
// until a reaction time before the unit would have left on its own —
// or, if it would not have, until the window ends.
type WayOut struct{ p zoneParams }

func newWayOut(params json.RawMessage) (Case, error) {
	p, err := readZone("way-out", params)
	return WayOut{p}, err
}

func (WayOut) Kind() string  { return "way-out" }
func (c WayOut) Params() any { return c.p }

func (c WayOut) Opportunities(w World) []Opportunity {
	var out []Opportunity
	units := c.p.units(w)
	for i, e := range w.Events {
		r, ok := c.p.radius(e)
		if !ok {
			continue
		}
		until := e.Origin + c.p.window()
		for _, u := range units {
			if !inside(u.Track, e.At, r, e.Origin) {
				continue
			}
			leaves, ok := firstExit(u.Track, e.At, r, e.Origin, until)
			if !ok {
				leaves = until
			}
			out = append(out, Opportunity{Case: c.Kind(), Event: i, Entity: u.ID,
				Need: c.p.need(i, u.PositionAt(e.Origin)), Opens: e.Origin, Closes: leaves - c.p.reaction()})
		}
	}
	return out
}

// -------------------------------------------------------------------- reroute

// Reroute is use case 5: a unit whose way runs into an event's zone waits or
// takes another path. That is only possible up to the last junction before the
// zone, so what the decision waits on must arrive a reaction time before the
// unit passes it. A unit that has already passed its last junction
// when the event happens has no other path, and the decision cannot be won.
type Reroute struct{ p zoneParams }

func newReroute(params json.RawMessage) (Case, error) {
	p, err := readZone("reroute", params)
	return Reroute{p}, err
}

func (Reroute) Kind() string  { return "reroute" }
func (c Reroute) Params() any { return c.p }

// junctionTolerance is how close a waypoint must be to a junction to be at it:
// tracks are rounded to a tenth of a metre, the tunnels are not.
const junctionTolerance = 0.5

func (c Reroute) Opportunities(w World) []Opportunity {
	junctions := junctionsOf(w.Tunnels)
	var out []Opportunity
	c.p.entering(w, func(event int, unit domain.Entity, entry time.Duration) {
		origin := w.Events[event].Origin
		last, found := origin, false
		for _, wp := range unit.Track {
			if wp.At < origin || wp.At >= entry {
				continue
			}
			for _, j := range junctions {
				if wp.Point.DistanceTo(j) <= junctionTolerance {
					last, found = wp.At, true
					break
				}
			}
		}
		closes := origin
		if found {
			closes = last - c.p.reaction()
		}
		out = append(out, Opportunity{Case: c.Kind(), Event: event, Entity: unit.ID,
			Need: c.p.need(event, unit.PositionAt(entry)), Opens: origin, Closes: closes})
	})
	return out
}

// junctionsOf is every point where three or more ways meet, on the tunnels a
// unit can travel.
func junctionsOf(tunnels []domain.Tunnel) []domain.Point {
	graph := mineplan.NewGraph(tunnels, mineplan.Walkable)
	var out []domain.Point
	for node := 0; node < graph.Nodes(); node++ {
		if graph.Degree(node) >= 3 {
			out = append(out, graph.Point(node))
		}
	}
	return out
}
