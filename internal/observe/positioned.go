package observe

import (
	"math"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
)

// Positioned is what a mine's own systems read: where every unit is now,
// exactly — everyone underground carries a device positioned over the mine's
// network, and a vehicle's positioning places it in its drift — and the route
// the fleet system has planned for each vehicle. Not where a person will walk:
// nobody plans that. A person's reach is every stretch of tunnel they could
// walk to by the end of the lookahead.
type Positioned struct {
	tracks  *Tracks
	reach   *mineplan.Reach
	walking float64
}

// NewPositioned reads the units through the mine's systems. People walk the
// tunnels mineplan.Walkable allows, at no more than walking metres a second.
func NewPositioned(entities []domain.Entity, tunnels []domain.Tunnel, walking float64) *Positioned {
	return &Positioned{
		tracks:  NewTracks(entities),
		reach:   mineplan.NewReach(mineplan.NewGraph(tunnels, mineplan.Walkable)),
		walking: walking,
	}
}

// Reach is each vehicle's planned route over the lookahead, and each person's
// walkable ground from where they are now.
func (p *Positioned) Reach(at, lookahead time.Duration, protects func(string) bool) []Path {
	var out []Path
	for _, entity := range p.tracks.entities {
		if !protects(entity.Kind) || len(entity.Track) == 0 {
			continue
		}
		if entity.Kind != domain.EntityPerson {
			out = append(out, route(entity, at, lookahead))
			continue
		}
		here := entity.PositionAt(at)
		segments := p.reach.Within(here, p.walking*lookahead.Seconds())
		if len(segments) == 0 {
			// No tunnels to say where they could go, so only where they are.
			out = append(out, Path{Entity: entity.ID, Points: []domain.Point{here}})
			continue
		}
		for _, s := range segments {
			out = append(out, Path{Entity: entity.ID, Points: []domain.Point{s.A, s.B}})
		}
	}
	return out
}

// Positions is where each protected unit was at a moment, exactly.
func (p *Positioned) Positions(at time.Duration, protects func(string) bool) []Position {
	return positions(p.tracks.entities, at, protects)
}

// Speed is the faster of the quickest unit and a person walking: a person's
// reach moves with them, and they walk no faster than that.
func (p *Positioned) Speed() float64 { return math.Max(p.tracks.speed, p.walking) }
