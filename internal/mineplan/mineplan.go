// Package mineplan is the mine's development: where the tunnels are, how they
// connect, and where in them the sensors are installed.
//
// It exists because a mine is not a box of rock. Sensors can only be installed
// where someone can reach, which is in the excavations; seismicity clusters
// around the workings, because that is where mining changes the stress; and
// people and vehicles move along the tunnels, not through the rock. A model
// that ignored the development would place all three somewhere no real mine
// could have them.
//
// The plan is deliberately schematic — a shaft, a spiral ramp, and on each
// level a drive, crosscuts and an ore drive — and low resolution. What matters
// downstream is that the array is confined to excavations and that everything
// is connected, not the exact shape of any one drift.
//
// Pure functions. Randomness only from an injected source, so a mine's plan is
// the same every time it is drawn.
package mineplan

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// The kinds of opening a plan contains.
const (
	Shaft    = "shaft"
	Ramp     = "ramp"
	Drive    = "drive"
	Crosscut = "crosscut"
	OreDrive = "ore-drive"
	Access   = "access"
)

const (
	// levelSpacing is the vertical distance between levels. 150 m is within
	// the range sublevel and longhole stoping mines use, and gives a kilometre
	// of rock seven levels.
	levelSpacing = 150.0

	// rampGrade is the target decline gradient, one in seven: steep enough not
	// to waste development, shallow enough for a loaded truck.
	rampGrade = 1.0 / 7

	// crosscutSpacing is the distance along a drive between crosscuts, before
	// jitter.
	crosscutSpacing = 180.0
)

// Tunnels draws a mine's development inside an extent.
func Tunnels(extent domain.Extent, random *rand.Rand) []domain.Tunnel {
	spanX, spanY, spanZ := extent.Span()
	margin := math.Min(50, spanZ*0.05)

	spacing := levelSpacing
	count := int((spanZ-2*margin)/spacing) + 1
	if count < 2 {
		count, spacing = 2, spanZ-2*margin
	}
	levels := make([]float64, count)
	for i := range levels {
		levels[i] = extent.Max.Z - margin - float64(i)*spacing
	}

	shaftX, shaftY := extent.Min.X+0.1*spanX, extent.Min.Y+0.15*spanY

	// The ramp spirals round a square footprint. Each level's section goes
	// round a whole number of times, so it reaches every level at the same
	// corner and the level's access can meet it there.
	half := math.Min(60, 0.05*math.Min(spanX, spanY))
	rampX, rampY := extent.Min.X+0.22*spanX, extent.Min.Y+0.55*spanY
	corners := [4][2]float64{
		{rampX - half, rampY - half}, {rampX + half, rampY - half},
		{rampX + half, rampY + half}, {rampX - half, rampY + half},
	}
	edges := 4 * int(math.Max(1, math.Round(spacing/rampGrade/(8*half))))

	var tunnels []domain.Tunnel

	shaft := domain.Tunnel{ID: "shaft", Kind: Shaft, Path: []domain.Point{{X: shaftX, Y: shaftY, Z: extent.Max.Z}}}
	for _, z := range levels {
		shaft.Path = append(shaft.Path, domain.Point{X: shaftX, Y: shaftY, Z: z})
	}
	tunnels = append(tunnels, shaft)

	ramp := domain.Tunnel{ID: "ramp", Kind: Ramp, Path: []domain.Point{{X: corners[0][0], Y: corners[0][1], Z: levels[0]}}}
	for level := 1; level < len(levels); level++ {
		for edge := 1; edge <= edges; edge++ {
			corner := corners[edge%4]
			z := levels[level-1] - spacing*float64(edge)/float64(edges)
			if edge == edges {
				// Exactly the level's elevation, so the junction is the same
				// point to the last bit and the graph joins it up.
				z = levels[level]
			}
			ramp.Path = append(ramp.Path, domain.Point{X: corner[0], Y: corner[1], Z: z})
		}
	}
	tunnels = append(tunnels, ramp)

	driveY := extent.Min.Y + 0.3*spanY
	for _, z := range levels {
		name := fmt.Sprintf("L%.0f", math.Abs(z))
		oreY := extent.Min.Y + (0.68+0.08*random.Float64())*spanY
		endX := extent.Min.X + (0.75+0.2*random.Float64())*spanX

		var crosscuts []float64
		for x := corners[0][0] + 150 + 30*random.Float64(); x < endX-40; x += crosscutSpacing + 60*(random.Float64()-0.5) {
			// Not every crosscut is developed on every level.
			if random.Float64() < 0.85 {
				crosscuts = append(crosscuts, x)
			}
		}

		tunnels = append(tunnels,
			domain.Tunnel{ID: name + "-shaft-station", Kind: Access, Path: []domain.Point{
				{X: shaftX, Y: shaftY, Z: z}, {X: shaftX, Y: driveY, Z: z},
			}},
			domain.Tunnel{ID: name + "-ramp-access", Kind: Access, Path: []domain.Point{
				{X: corners[0][0], Y: corners[0][1], Z: z}, {X: corners[0][0], Y: driveY, Z: z},
			}},
		)

		// The drive has a vertex wherever something joins it, so the joins are
		// nodes of the network rather than points that merely look close.
		xs := append([]float64{shaftX, corners[0][0], endX}, crosscuts...)
		sort.Float64s(xs)
		drive := domain.Tunnel{ID: name + "-drive", Kind: Drive}
		for _, x := range xs {
			drive.Path = append(drive.Path, domain.Point{X: x, Y: driveY, Z: z})
		}
		tunnels = append(tunnels, drive)

		ore := domain.Tunnel{ID: name + "-ore-drive", Kind: OreDrive}
		for i, x := range crosscuts {
			tunnels = append(tunnels, domain.Tunnel{
				ID: fmt.Sprintf("%s-crosscut-%d", name, i+1), Kind: Crosscut,
				Path: []domain.Point{{X: x, Y: driveY, Z: z}, {X: x, Y: oreY, Z: z}},
			})
			ore.Path = append(ore.Path, domain.Point{X: x, Y: oreY, Z: z})
		}
		if len(ore.Path) >= 2 {
			tunnels = append(tunnels, ore)
		}
	}
	return tunnels
}

// Sensors places count sensors along the tunnels, spread as far apart as the
// development allows.
//
// Farthest-point sampling: each sensor goes wherever is furthest from every
// sensor already placed. That reaches the top and bottom levels and both ends
// of the workings before it fills anything in, which is what an array
// designed to locate events across the whole mine does. The shaft is left out;
// nobody installs a geophone in the hoisting shaft.
func Sensors(tunnels []domain.Tunnel, count int, random *rand.Rand) []domain.Sensor {
	if count <= 0 {
		return []domain.Sensor{}
	}

	total := 0.0
	for _, tunnel := range tunnels {
		if tunnel.Kind != Shaft {
			total += length(tunnel.Path)
		}
	}
	step := math.Min(25, total/float64(3*count))
	var candidates []domain.Point
	for _, tunnel := range tunnels {
		if tunnel.Kind != Shaft {
			candidates = append(candidates, along(tunnel.Path, step)...)
		}
	}
	if len(candidates) == 0 {
		return []domain.Sensor{}
	}

	nearest := make([]float64, len(candidates))
	for i := range nearest {
		nearest[i] = math.Inf(1)
	}
	next := random.IntN(len(candidates))

	sensors := make([]domain.Sensor, 0, count)
	for len(sensors) < count && len(sensors) < len(candidates) {
		chosen := candidates[next]
		sensors = append(sensors, domain.Sensor{ID: fmt.Sprintf("s%02d", len(sensors)+1), At: chosen})

		furthest := -1.0
		for i, candidate := range candidates {
			if d := candidate.DistanceTo(chosen); d < nearest[i] {
				nearest[i] = d
			}
			// Strictly greater, so a tie goes to the earlier candidate and the
			// choice never depends on anything but the plan.
			if nearest[i] > furthest {
				furthest, next = nearest[i], i
			}
		}
	}
	return sensors
}

// NearWorkings is a point in the rock around the production workings: somewhere
// along a drive, crosscut or ore drive, displaced by a normal spread in each
// axis.
//
// Mining-induced seismicity concentrates where mining changes the stress, so
// this is where events are drawn when no epicentre is stated.
func NearWorkings(tunnels []domain.Tunnel, extent domain.Extent, spread float64, random *rand.Rand) domain.Point {
	var production []domain.Tunnel
	total := 0.0
	for _, tunnel := range tunnels {
		if tunnel.Kind == Drive || tunnel.Kind == Crosscut || tunnel.Kind == OreDrive {
			production = append(production, tunnel)
			total += length(tunnel.Path)
		}
	}
	if total == 0 {
		spanX, spanY, spanZ := extent.Span()
		return domain.Point{
			X: extent.Min.X + random.Float64()*spanX,
			Y: extent.Min.Y + random.Float64()*spanY,
			Z: extent.Min.Z + random.Float64()*spanZ,
		}
	}

	distance := random.Float64() * total
	var on domain.Point
	for _, tunnel := range production {
		l := length(tunnel.Path)
		if distance <= l {
			on = pointAlong(tunnel.Path, distance)
			break
		}
		distance -= l
	}
	return extent.Clamp(domain.Point{
		X: on.X + random.NormFloat64()*spread,
		Y: on.Y + random.NormFloat64()*spread,
		Z: on.Z + random.NormFloat64()*spread,
	})
}

// DistanceToTunnels is how far a point is from the nearest excavation.
func DistanceToTunnels(tunnels []domain.Tunnel, p domain.Point) float64 {
	best := math.Inf(1)
	for _, tunnel := range tunnels {
		for i := 1; i < len(tunnel.Path); i++ {
			best = math.Min(best, toSegment(p, tunnel.Path[i-1], tunnel.Path[i]))
		}
	}
	return best
}

func length(path []domain.Point) float64 {
	total := 0.0
	for i := 1; i < len(path); i++ {
		total += path[i-1].DistanceTo(path[i])
	}
	return total
}

// along is points every step metres along a path, including its vertices.
func along(path []domain.Point, step float64) []domain.Point {
	var out []domain.Point
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		d := a.DistanceTo(b)
		n := int(math.Max(1, math.Ceil(d/step)))
		for k := 0; k < n; k++ {
			out = append(out, lerp(a, b, float64(k)/float64(n)))
		}
	}
	if len(path) > 0 {
		out = append(out, path[len(path)-1])
	}
	return out
}

func pointAlong(path []domain.Point, distance float64) domain.Point {
	for i := 1; i < len(path); i++ {
		d := path[i-1].DistanceTo(path[i])
		if distance <= d && d > 0 {
			return lerp(path[i-1], path[i], distance/d)
		}
		distance -= d
	}
	return path[len(path)-1]
}

func lerp(a, b domain.Point, f float64) domain.Point {
	return domain.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f}
}

func toSegment(p, a, b domain.Point) float64 {
	abx, aby, abz := b.X-a.X, b.Y-a.Y, b.Z-a.Z
	squared := abx*abx + aby*aby + abz*abz
	if squared == 0 {
		return p.DistanceTo(a)
	}
	f := ((p.X-a.X)*abx + (p.Y-a.Y)*aby + (p.Z-a.Z)*abz) / squared
	f = math.Max(0, math.Min(1, f))
	return p.DistanceTo(lerp(a, b, f))
}
