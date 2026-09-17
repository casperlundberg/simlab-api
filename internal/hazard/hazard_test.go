package hazard_test

import (
	"math"
	"testing"

	"github.com/casperlundberg/simlab-api/internal/hazard"
)

// The numbers here are the handbook's, not the code's: a model that drifted
// from its source would still produce plausible ground motions, and nothing
// else in the system could tell.

// Kaiser & Cai (2013), after Kaiser & Maloney (1997): the far-field scaling
// law should not be applied closer than Rmin = 1.5 (M0/Δσ)^(1/3), "10 to 70 m
// for a stress drop of 1 MPa, and 5 to 30 m for a stress drop of 10 MPa" for
// events from mN = 1.5 to 4.0.
func TestTheNearFieldLimitMatchesTheHandbooksWorkedRange(t *testing.T) {
	for _, c := range []struct {
		stressDrop, magnitude, want float64
	}{
		{1, 1.5, 10}, {1, 4.0, 70}, {10, 1.5, 5}, {10, 4.0, 30},
	} {
		m := hazard.Model{C: 0.25, StressDrop: c.stressDrop}
		if got := m.NearField(c.magnitude); math.Abs(got-c.want)/c.want > 0.1 {
			t.Errorf("Δσ %v MPa, mN %v: Rmin %.1f m, the handbook gives about %v m", c.stressDrop, c.magnitude, got, c.want)
		}
	}
}

// ppv = C* √(10^(mN+1)) / R (CRBSHB 1996, Kaiser & Cai 2013 Eq. 6).
func TestPeakParticleVelocityFallsWithDistanceAndRisesWithMagnitude(t *testing.T) {
	m := hazard.Model{C: 0.25, StressDrop: 2.5}

	if got := m.PPV(3, 100); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("mN 3 at 100 m: %.4f m/s, want 0.25", got)
	}
	if ratio := m.PPV(3, 100) / m.PPV(3, 200); math.Abs(ratio-2) > 1e-9 {
		t.Errorf("doubling the distance divided ppv by %.3f, want 2 (1/R in the far field)", ratio)
	}
	if ratio := m.PPV(3, 200) / m.PPV(2, 200); math.Abs(ratio-math.Sqrt(10)) > 1e-9 {
		t.Errorf("one magnitude unit multiplied ppv by %.3f, want √10", ratio)
	}
}

// Closer than Rmin the law does not hold, and ground motion is worse than it
// would predict rather than better. Clamping to Rmin keeps the prediction at
// its largest there instead of letting it grow without bound or fall.
func TestInsideTheNearFieldGroundMotionIsHeldAtItsLimit(t *testing.T) {
	m := hazard.Model{C: 0.25, StressDrop: 2.5}
	limit := m.NearField(2)
	if got := m.PPV(2, 0); got != m.PPV(2, limit) {
		t.Errorf("at the source: %.3f m/s, at Rmin: %.3f m/s", got, m.PPV(2, limit))
	}
}

// CRBSHB Figure 3.2: low below 0.01 m/s, moderate above, high above 0.1,
// very high above 1 m/s.
func TestGroundMotionFallsIntoTheHandbooksLevels(t *testing.T) {
	for ppv, want := range map[float64]hazard.Level{
		0.005: hazard.None, 0.02: hazard.Moderate, 0.3: hazard.High, 1.5: hazard.VeryHigh,
	} {
		if got := hazard.LevelOf(ppv); got != want {
			t.Errorf("%.3f m/s is %v, want %v", ppv, got, want)
		}
	}
}

// A zone's radius and the level at a distance are two statements of one rule,
// and the view draws the first while the mine classifies with the second.
// They have to agree everywhere, including where the near field caps what an
// event can do.
func TestAZoneContainsExactlyThePlacesAtItsLevelOrWorse(t *testing.T) {
	m := hazard.Model{C: 0.25, StressDrop: 2.5}
	for _, magnitude := range []float64{-0.5, 0.5, 1.5, 2.5, 3.5} {
		zones := m.Zones(magnitude, 50)
		for d := 0.0; d < 3000; d += 7 {
			level := m.LevelAt(magnitude, d, 50)
			for _, l := range []hazard.Level{hazard.Moderate, hazard.High, hazard.VeryHigh} {
				radius, reached := zones[l]
				inside := reached && d <= radius
				if inside != (level >= l) {
					t.Fatalf("mN %.1f at %.0f m: level %v, but the %v zone (radius %.1f, reached %v) says otherwise",
						magnitude, d, level, l, radius, reached)
				}
			}
		}
	}
}

// Location error is added to the zone rather than ignored: someone 60 m from
// an estimate that may be 50 m out may be 10 m from the event.
func TestUncertaintyInTheLocationWidensTheZone(t *testing.T) {
	m := hazard.Model{C: 0.25, StressDrop: 2.5}
	exact, uncertain := m.Zones(2.5, 0), m.Zones(2.5, 50)
	if uncertain[hazard.High]-exact[hazard.High] != 50 {
		t.Errorf("50 m of location error widened the high zone from %.1f to %.1f m",
			exact[hazard.High], uncertain[hazard.High])
	}
}
