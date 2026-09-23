package workload

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/casperlundberg/simlab-api/internal/activity"
	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
)

// defaultWorkforce is who is underground when a scenario does not say: a shift
// of production crews, a few service and supervisor vehicles, and an
// autonomous haulage fleet.
var defaultWorkforce = domain.Workforce{People: 8, CrewedVehicles: 4, AutonomousVehicles: 3}

// Speeds, in metres per second.
//
// Walking underground is slow — uneven floor, poor light, and most walking is
// short moves around a working place — so 1 m/s rather than the 1.4 of a
// pavement. Vehicles keep to underground speed limits: 15 km/h for crewed
// light vehicles, and 12 km/h for a loaded autonomous hauler, which is held
// below what a driver would do.
const (
	WalkingSpeed    = 1.0
	crewedSpeed     = 15.0 / 3.6
	autonomousSpeed = 12.0 / 3.6
)

// trackTail is how far past the end of its scenario the workforce keeps
// moving. A run lasts until its queue drains, which with little capacity is
// many hours after the scenario ends.
const trackTail = 24 * time.Hour

// workforceStream seeds every entity's own generator. Own generators, so that
// adding one person moves no one else, and none of them moves a job.
const workforceStream = 0x94D049BB133111EB

// workforce draws where everyone goes during a scenario.
func workforce(layout domain.Layout, scenario domain.Scenario, plan *activity.Plan) []domain.Entity {
	crew := defaultWorkforce
	if scenario.Workforce != nil {
		crew = *scenario.Workforce
	}

	// Nobody travels the hoisting shaft: people ride a cage, which is not a
	// place anyone is exposed for long, and vehicles cannot use it at all.
	graph := mineplan.NewGraph(layout.Tunnels, mineplan.Walkable)
	if graph.Nodes() == 0 {
		return []domain.Entity{}
	}
	m := newMovement(graph, layout, scenario.Duration+trackTail)
	m.plan = plan

	var out []domain.Entity
	for i, spec := range []struct {
		kind  string
		count int
		move  func(*rand.Rand) []domain.Waypoint
	}{
		{domain.EntityPerson, crew.People, m.person},
		{domain.EntityCrewedVehicle, crew.CrewedVehicles, m.crewedVehicle},
		{domain.EntityAutonomousVehicle, crew.AutonomousVehicles, m.autonomousVehicle},
	} {
		for n := 0; n < spec.count; n++ {
			random := rand.New(rand.NewPCG(uint64(scenario.Seed)^workforceStream, uint64(i)<<32|uint64(n)))
			out = append(out, domain.Entity{
				ID:    fmt.Sprintf("%s-%02d", spec.kind, n+1),
				Kind:  spec.kind,
				Track: spec.move(random),
			})
		}
	}
	return out
}

// movement is what every entity's route is planned against.
type movement struct {
	graph *mineplan.Graph
	until time.Duration

	// workplaces are crosscut and ore drive nodes, where production happens;
	// byLevel groups them by elevation.
	workplaces []int
	byLevel    map[float64][]int

	// tip is where haulers unload: the bottom shaft station, where ore goes up.
	tip int

	// plan, in a worked mine, is which faces are worked when and when the
	// production areas are cleared for blasting; stations are where each
	// level's crews wait while they are. Nil plan: people move between
	// workplaces as they always have.
	plan     *activity.Plan
	stations map[float64]int

	routes map[int]mineplan.Routes
}

func newMovement(graph *mineplan.Graph, layout domain.Layout, until time.Duration) *movement {
	m := &movement{graph: graph, until: until, byLevel: map[float64][]int{}, routes: map[int]mineplan.Routes{},
		tip: -1, stations: map[float64]int{}}

	seen := map[int]bool{}
	deepest := math.Inf(1)
	for _, tunnel := range layout.Tunnels {
		if tunnel.Kind == mineplan.Crosscut || tunnel.Kind == mineplan.OreDrive {
			for _, p := range tunnel.Path {
				if node, ok := graph.NodeAt(p); ok && !seen[node] {
					seen[node] = true
					m.workplaces = append(m.workplaces, node)
					m.byLevel[p.Z] = append(m.byLevel[p.Z], node)
				}
			}
		}
		if tunnel.Kind == mineplan.Access && strings.HasSuffix(tunnel.ID, "-shaft-station") && len(tunnel.Path) > 1 {
			if node, ok := graph.NodeAt(tunnel.Path[0]); ok {
				m.stations[tunnel.Path[0].Z] = node
			}
			if end := tunnel.Path[len(tunnel.Path)-1]; end.Z < deepest {
				if node, ok := graph.NodeAt(end); ok {
					deepest, m.tip = end.Z, node
				}
			}
		}
	}
	// A stated layout need not follow the generated plan's naming; anything
	// connected is still somewhere to go.
	if len(m.workplaces) == 0 {
		for node := 0; node < graph.Nodes(); node++ {
			m.workplaces = append(m.workplaces, node)
			p := graph.Point(node)
			m.byLevel[p.Z] = append(m.byLevel[p.Z], node)
		}
	}
	sort.Ints(m.workplaces)
	if m.tip < 0 {
		m.tip = m.workplaces[0]
	}
	return m
}

// person walks between working places on one level, staying a while at each.
func (m *movement) person(random *rand.Rand) []domain.Waypoint {
	start := m.workplaces[random.IntN(len(m.workplaces))]
	if m.plan != nil {
		// A crew works a face for a good part of its shift — drilling a round,
		// charging, scaling — rather than hopping between faces.
		return m.workedCrew(random, WalkingSpeed, 45, 120)
	}
	return m.travel(start, WalkingSpeed, random, func(at int, _ time.Duration) (int, time.Duration) {
		level := m.byLevel[m.graph.Point(at).Z]
		// Now and then a person moves to another level; mostly they stay on
		// the one they were sent to.
		if len(level) < 2 || random.Float64() < 0.05 {
			level = m.workplaces
		}
		return level[random.IntN(len(level))], minutes(random, 5, 25)
	})
}

// crewedVehicle drives anywhere in the mine, stopping briefly.
func (m *movement) crewedVehicle(random *rand.Rand) []domain.Waypoint {
	start := m.workplaces[random.IntN(len(m.workplaces))]
	if m.plan != nil {
		return m.workedCrew(random, crewedSpeed, 2, 6)
	}
	return m.travel(start, crewedSpeed, random, func(int, time.Duration) (int, time.Duration) {
		if random.Float64() < 0.3 {
			return m.graph.RandomNode(random), minutes(random, 1, 4)
		}
		return m.workplaces[random.IntN(len(m.workplaces))], minutes(random, 2, 6)
	})
}

// autonomousVehicle hauls: to a working place to load, to the tip to unload,
// and round again.
func (m *movement) autonomousVehicle(random *rand.Rand) []domain.Waypoint {
	return m.travel(m.tip, autonomousSpeed, random, func(at int, _ time.Duration) (int, time.Duration) {
		if at == m.tip {
			return m.workplaces[random.IntN(len(m.workplaces))], minutes(random, 1.5, 3)
		}
		return m.tip, minutes(random, 0.5, 1.5)
	})
}

// travel follows routes chosen by next until the track has run long enough.
// next is given where the entity is and says where it goes and how long it
// stays once there.
func (m *movement) travel(start int, speed float64, random *rand.Rand,
	next func(at int, now time.Duration) (int, time.Duration)) []domain.Waypoint {
	at := start
	now := time.Duration(0)
	track := []domain.Waypoint{waypoint(0, m.graph.Point(start))}

	for now < m.until {
		destination, stay := next(at, now)
		routes, ok := m.routes[at]
		if !ok {
			routes = m.graph.Routes(at)
			m.routes[at] = routes
		}
		path, _ := routes.To(destination)
		for i := 1; i < len(path); i++ {
			now += time.Duration(path[i-1].DistanceTo(path[i]) / speed * float64(time.Second))
			track = append(track, waypoint(now, path[i]))
		}
		if len(path) > 0 {
			at = destination
		}
		now += stay
		track = append(track, waypoint(now, m.graph.Point(at)))
	}
	return track
}

// trackSteps is how many steps a metre of track is rounded to.
const trackSteps = 10

// TrackResolution is what a waypoint is rounded to, in metres, in each axis. A
// point on a track can so lie up to half a step off the tunnel in each axis —
// at most TrackResolution·√3/2 — which anything comparing tracks with the
// tunnels themselves has to allow for.
const TrackResolution = 1.0 / trackSteps

// TrackTick is what a waypoint's time is rounded to. A unit can so reach a
// point up to half a tick early or late, which at walking pace is a few
// centimetres of ground.
const TrackTick = 100 * time.Millisecond

// waypoint rounds to a tenth of a second and a tenth of a metre: finer than
// anything the view or a safety distance can use, and it keeps a day of
// tracks a fraction of the size.
func waypoint(at time.Duration, p domain.Point) domain.Waypoint {
	round := func(v float64) float64 { return math.Round(v*trackSteps) / trackSteps }
	return domain.Waypoint{
		At:    at.Round(TrackTick),
		Point: domain.Point{X: round(p.X), Y: round(p.Y), Z: round(p.Z)},
	}
}

func minutes(random *rand.Rand, low, high float64) time.Duration {
	return time.Duration((low + random.Float64()*(high-low)) * float64(time.Minute))
}

// clearingLead is how far ahead a crew looks for the production areas being
// cleared: the longest it may walk to a face and stay there. A crew that would
// still be at a face when clearing begins goes to its shaft station instead.
const clearingLead = 45 * time.Minute

// workedCrew moves a crew in a worked mine: from face to face among those
// being worked, and to its level's shaft station while the production areas
// are cleared for blasting, until re-entry.
func (m *movement) workedCrew(random *rand.Rand, speed, low, high float64) []domain.Waypoint {
	faces := m.plan.ActiveAt(0)
	start := m.nodeAt(faces[random.IntN(len(faces))])
	return m.travel(start, speed, random, func(at int, now time.Duration) (int, time.Duration) {
		if cleared, reentry, ok := m.plan.Evacuation(now); ok && cleared <= now+clearingLead {
			return m.station(at), max(reentry-now, time.Minute)
		}
		faces := m.plan.ActiveAt(now)
		// A face on the crew's own level when one is worked: crossing levels
		// is a long walk down the ramp.
		var here []domain.Point
		for _, f := range faces {
			if f.Z == m.graph.Point(at).Z {
				here = append(here, f)
			}
		}
		if len(here) > 0 {
			faces = here
		}
		return m.nodeAt(faces[random.IntN(len(faces))]), minutes(random, low, high)
	})
}

// nodeAt is the node at a point of the plan, or the nearest one.
func (m *movement) nodeAt(p domain.Point) int {
	if node, ok := m.graph.NodeAt(p); ok {
		return node
	}
	best, nearest := math.Inf(1), m.tip
	for node := 0; node < m.graph.Nodes(); node++ {
		if d := m.graph.Point(node).DistanceTo(p); d < best {
			best, nearest = d, node
		}
	}
	return nearest
}

// station is the shaft station of the level a node is on, where its crews
// wait while blasting is under way; the bottom one on a level without.
func (m *movement) station(at int) int {
	if node, ok := m.stations[m.graph.Point(at).Z]; ok {
		return node
	}
	return m.tip
}
