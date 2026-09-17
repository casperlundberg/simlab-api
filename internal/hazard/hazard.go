// Package hazard is how much a seismic event threatens a place, from its
// magnitude and how far the place is from it.
//
// The relations are the Canadian Rockburst Support Handbook's (Kaiser et al.
// 1996), as restated by Kaiser & Cai (2013, "Critical review of design
// principles for rock support in burst-prone ground", Ground Support 2013):
//
//	ppv = C* · √(10^(mN + 1)) / R
//
// peak particle velocity in m/s at hypocentral distance R in metres from an
// event of Nuttli magnitude mN — the form of McGarr's (1984) scaling law with
// the exponent fixed at a* = 0.5, which the handbook found holds for many
// mines. It is an upper bound for design, not a prediction of what one event
// will do: the source is taken as radiating in the worst direction.
//
// It answers the question the safety model asks — is closer more dangerous,
// and by how much — with a physical quantity: ground motion falls as 1/R, and
// each unit of magnitude multiplies it by √10, so the same motion reaches √10
// times further. The distance that matters is to the hypocentre, in three
// dimensions, not to the epicentre on a plan.
//
// Pure functions of their arguments.
package hazard

import "math"

// Model is the site's scaling law.
type Model struct {
	// C is C* in m²/s. The handbook recommends 0.2 to 0.3 for design at 90 to
	// 95 % confidence where stress drops are normal (below 2.5 MPa on
	// average), 0.1 for a 50 % fit, and 0.5 to 1.0 where they are very high.
	C float64

	// StressDrop is Δσ in MPa. It sets how close the far-field law can be
	// trusted, and nothing else here.
	StressDrop float64
}

// Default is the handbook's design relationship II: C* = 0.25 m²/s, the
// middle of its recommended range, with a 2.5 MPa stress drop.
var Default = Model{C: 0.25, StressDrop: 2.5}

// Level is how strong ground motion is, in the handbook's bands (Figure 3.2).
type Level int

const (
	None     Level = iota // below 0.01 m/s
	Moderate              // above 0.01 m/s
	High                  // above 0.1 m/s
	VeryHigh              // above 1 m/s
)

// thresholds is the ground motion, in m/s, at which each level begins.
var thresholds = map[Level]float64{Moderate: 0.01, High: 0.1, VeryHigh: 1}

// Levels is every level worth a zone, weakest first.
var Levels = []Level{Moderate, High, VeryHigh}

func (l Level) String() string {
	switch l {
	case Moderate:
		return "moderate"
	case High:
		return "high"
	case VeryHigh:
		return "very-high"
	default:
		return "none"
	}
}

// LevelOf is the level a ground motion falls in.
func LevelOf(ppv float64) Level {
	level := None
	for _, l := range Levels {
		if ppv >= thresholds[l] {
			level = l
		}
	}
	return level
}

// moment is the seismic moment in GN·m of an event of Nuttli magnitude mN,
// from the handbook's mN = log M0 − (1 ± 0.15).
func moment(magnitude float64) float64 {
	return math.Pow(10, magnitude+1)
}

// NearField is Rmin, the distance inside which the far-field law does not
// hold: 1.5 (M0/Δσ)^(1/3), with M0 in GN·m and Δσ in MPa (Kaiser & Maloney
// 1997).
func (m Model) NearField(magnitude float64) float64 {
	return 1.5 * math.Cbrt(moment(magnitude)/m.StressDrop)
}

// PPV is the design ground motion in m/s at a distance in metres.
//
// Closer than the near-field limit, ground motion is held at its value at the
// limit. The true near-field motion is worse than the law predicts, not
// better, so the cap is where a prediction stops being trustworthy rather
// than a claim that it stops rising; either way, a place that close is already
// at the event's worst level.
func (m Model) PPV(magnitude, distance float64) float64 {
	distance = math.Max(distance, m.NearField(magnitude))
	return m.C * math.Sqrt(moment(magnitude)) / distance
}

// LevelAt is the level at a place a distance from an estimated hypocentre,
// when the estimate may be out by uncertainty metres. The place is taken to be
// as close as the uncertainty allows.
func (m Model) LevelAt(magnitude, distance, uncertainty float64) Level {
	return LevelOf(m.PPV(magnitude, math.Max(0, distance-uncertainty)))
}

// Zones is, for each level an event can reach, how far from its estimated
// hypocentre that level extends. A level the event cannot reach anywhere — the
// cap at the near field keeps it below the threshold — has no zone.
func (m Model) Zones(magnitude, uncertainty float64) map[Level]float64 {
	zones := map[Level]float64{}
	near := m.NearField(magnitude)
	for _, l := range Levels {
		radius := m.C * math.Sqrt(moment(magnitude)) / thresholds[l]
		if radius < near {
			// The level would only be reached inside the near field, where
			// ground motion is held at its value at the limit — which is
			// below the threshold, or the radius would lie beyond the limit.
			continue
		}
		zones[l] = radius + uncertainty
	}
	return zones
}
