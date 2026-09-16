package seismic_test

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/seismic"
)

// The point of solving for an epicentre rather than assuming it is that the
// estimate carries error, and the error is what the mine's intent has to cope
// with. A test suite that only checked clean recovery would be asserting the
// thing that makes the feature trivial.

func mine() domain.Extent {
	return domain.Extent{
		Min: domain.Point{X: 0, Y: 0, Z: -800},
		Max: domain.Point{X: 1200, Y: 900, Z: -200},
	}
}

func model() seismic.Model {
	return seismic.Model{PVelocitySeconds: 5800} // hard rock, metres per second
}

func layout(t *testing.T, count int) domain.Layout {
	t.Helper()
	return seismic.Layout(mine(), count, rand.New(rand.NewPCG(1, 2)))
}

func TestTravelTimeIsDistanceOverVelocity(t *testing.T) {
	m := model()
	from := domain.Point{X: 0, Y: 0, Z: 0}
	to := domain.Point{X: 5800, Y: 0, Z: 0} // exactly one second away

	got := m.TravelTime(from, to)
	if math.Abs(got.Seconds()-1) > 1e-9 {
		t.Errorf("5800 m at 5800 m/s should take one second, got %v", got)
	}
}

func TestAPickArrivesLaterAtAMoreDistantSensor(t *testing.T) {
	m := model()
	sensors := []domain.Sensor{
		{ID: "near", At: domain.Point{X: 100, Y: 0, Z: -500}},
		{ID: "far", At: domain.Point{X: 1100, Y: 0, Z: -500}},
	}
	event := seismic.Event{At: domain.Point{X: 0, Y: 0, Z: -500}, Origin: 10 * time.Second}

	picks := m.Picks(event, sensors, 0, nil)
	if len(picks) != 2 {
		t.Fatalf("expected a pick per sensor, got %d", len(picks))
	}
	if picks[0].SensorID != "near" {
		t.Errorf("picks should be ordered by arrival; first was %q", picks[0].SensorID)
	}
	if picks[0].At >= picks[1].At {
		t.Errorf("the near sensor should detect first: %v then %v", picks[0].At, picks[1].At)
	}
	// Nothing detects an event before it happens.
	if picks[0].At < event.Origin {
		t.Errorf("a pick at %v precedes the event at %v", picks[0].At, event.Origin)
	}
}

func TestTheSolverRecoversAKnownEpicentreFromCleanPicks(t *testing.T) {
	m := model()
	sensors := layout(t, 12).Sensors
	truth := domain.Point{X: 700, Y: 300, Z: -450}
	event := seismic.Event{At: truth, Origin: 30 * time.Second}

	estimate, err := m.Locate(sensors, m.Picks(event, sensors, 0, nil), mine())
	if err != nil {
		t.Fatalf("locating: %v", err)
	}

	// Noiseless picks and a dense array: the search should land within a few
	// metres. Anything worse means the search, not the physics, is the limit.
	if off := estimate.At.DistanceTo(truth); off > 10 {
		t.Errorf("clean picks should locate to within 10 m, got %.1f m (%+v)", off, estimate.At)
	}
	if off := math.Abs((estimate.Origin - event.Origin).Seconds()); off > 0.01 {
		t.Errorf("origin time off by %.3f s", off)
	}
}

func TestNoiseInThePicksBecomesErrorInTheEstimate(t *testing.T) {
	m := model()
	sensors := layout(t, 12).Sensors
	truth := domain.Point{X: 700, Y: 300, Z: -450}
	event := seismic.Event{At: truth, Origin: 30 * time.Second}

	clean, err := m.Locate(sensors, m.Picks(event, sensors, 0, nil), mine())
	if err != nil {
		t.Fatalf("locating cleanly: %v", err)
	}
	random := rand.New(rand.NewPCG(9, 9))
	noisy, err := m.Locate(sensors, m.Picks(event, sensors, 5*time.Millisecond, random), mine())
	if err != nil {
		t.Fatalf("locating with noise: %v", err)
	}

	cleanOff := clean.At.DistanceTo(truth)
	noisyOff := noisy.At.DistanceTo(truth)
	if noisyOff <= cleanOff {
		t.Errorf("noise should degrade the estimate: clean %.1f m, noisy %.1f m", cleanOff, noisyOff)
	}
	// It should still be usable. 5 ms of pick error is about 29 m of distance
	// at this velocity; an estimate hundreds of metres out would mean the
	// solver is unstable rather than merely uncertain.
	if noisyOff > 250 {
		t.Errorf("5 ms of pick noise put the estimate %.0f m out, which is not usable", noisyOff)
	}
	// And it should say it is less certain.
	if noisy.RMSResidualSeconds <= clean.RMSResidualSeconds {
		t.Error("the residual should report the noise the estimate could not explain")
	}
}

func TestLocatingInThreeDimensionsNeedsFourSensors(t *testing.T) {
	m := model()
	sensors := layout(t, 12).Sensors
	event := seismic.Event{At: domain.Point{X: 700, Y: 300, Z: -450}, Origin: time.Second}
	picks := m.Picks(event, sensors, 0, nil)

	// Three picks leave the origin time and three coordinates underdetermined.
	if _, err := m.Locate(sensors, picks[:3], mine()); err == nil {
		t.Error("three picks cannot fix four unknowns; the solver should say so rather than guess")
	}
	if _, err := m.Locate(sensors, picks[:4], mine()); err != nil {
		t.Errorf("four picks should be enough: %v", err)
	}
}

func TestAnEstimateIsNeverPlacedOutsideTheMine(t *testing.T) {
	m := model()
	sensors := layout(t, 8).Sensors
	// Picks that fit no location inside the extent: deliberately inconsistent,
	// which is what a bad detection looks like.
	picks := []seismic.Pick{
		{SensorID: sensors[0].ID, At: 1 * time.Second},
		{SensorID: sensors[1].ID, At: 30 * time.Second},
		{SensorID: sensors[2].ID, At: 2 * time.Second},
		{SensorID: sensors[3].ID, At: 45 * time.Second},
	}

	estimate, err := m.Locate(sensors, picks, mine())
	if err != nil {
		t.Fatalf("locating: %v", err)
	}
	if !mine().Contains(estimate.At) {
		t.Errorf("estimate %+v is outside the rock; no operator could act on it", estimate.At)
	}
	if estimate.RMSResidualSeconds < 1 {
		t.Error("inconsistent picks should leave a large residual, so the estimate can be distrusted")
	}
}

func TestALayoutIsTheSameEveryTimeForASeed(t *testing.T) {
	first := seismic.Layout(mine(), 16, rand.New(rand.NewPCG(4, 4)))
	second := seismic.Layout(mine(), 16, rand.New(rand.NewPCG(4, 4)))

	if len(first.Sensors) != 16 {
		t.Fatalf("expected 16 sensors, got %d", len(first.Sensors))
	}
	for i := range first.Sensors {
		if first.Sensors[i] != second.Sensors[i] {
			t.Fatalf("sensor %d differs between layouts of the same seed", i)
		}
	}
}

func TestEverySensorIsInsideTheMine(t *testing.T) {
	l := seismic.Layout(mine(), 24, rand.New(rand.NewPCG(5, 5)))
	for _, sensor := range l.Sensors {
		if !mine().Contains(sensor.At) {
			t.Errorf("sensor %s at %+v is outside the extent", sensor.ID, sensor.At)
		}
	}
}
