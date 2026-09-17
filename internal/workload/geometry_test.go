package workload_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// fingerprint is a digest of everything about a job stream that a run
// replays: identity, arrival, priority and duration, to the bit.
func fingerprint(jobs []workload.Job) string {
	digest := sha256.New()
	var word [8]byte
	for _, job := range jobs {
		for _, value := range []uint64{
			uint64(job.ID), uint64(job.SubmittedAt), uint64(job.Priority), math.Float64bits(job.Seconds),
		} {
			binary.LittleEndian.PutUint64(word[:], value)
			digest.Write(word[:])
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// verifyScenario is the one platform-deploy/verify.sh replays, and
// liveScenario is the one on the deployed instance with runs recorded against
// it.
func verifyScenario() (domain.Mine, domain.Scenario) {
	return domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 30, BackgroundRate: 90},
		domain.Scenario{
			ID: "rock-burst", MineID: "storhall", Duration: 2 * time.Hour, JobSeconds: 20, Seed: 20260910,
			PriorityMix: map[domain.Priority]float64{100: 1, 50: 1, 25: 2},
			Bursts:      []domain.Burst{{At: 30 * time.Minute, Magnitude: 45, AftershockDecay: 30 * time.Minute}},
		}
}

func liveScenario() (domain.Mine, domain.Scenario) {
	return domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 40, BackgroundRate: 60},
		domain.Scenario{
			ID: "storhall-rock-burst", MineID: "storhall", Duration: time.Hour, JobSeconds: 20, Seed: 42,
			PriorityMix: map[domain.Priority]float64{100: 1, 25: 3},
			Bursts:      []domain.Burst{{At: 10 * time.Minute, Magnitude: 30, AftershockDecay: 15 * time.Minute}},
		}
}

func build(t *testing.T, m domain.Mine, s domain.Scenario) workload.Workload {
	t.Helper()
	w, err := workload.Build(m, s)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return w
}

// The seismic geometry was added to scenarios that already had runs recorded
// against them. If it had moved a single job, a new run of an old scenario
// would no longer be comparable with the runs already stored, and nothing
// would say so. These digests were taken from the generator before geometry
// existed, and the geometry parameters are set here precisely to show they do
// not reach the jobs.
func TestGeometryChangesNotOneJobOfAnExistingScenario(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func() (domain.Mine, domain.Scenario)
		jobs  int
		want  string
	}{
		{"verify.sh", verifyScenario, 13071, "d7a5a37e2e978a10bfca13ff27fea4b98a70edcb5bf63fd28663ec7682eeb8cc"},
		{"deployed", liveScenario, 3500, "1a3d20cb3dceae1962f1379eab737aa175546dc0f48a40cf0835e19cb2d9f76d"},
	} {
		m, s := c.setup()
		s.PickJitter = 4 * time.Millisecond
		s.Bursts[0].Epicentre = &domain.Point{X: 300, Y: 300, Z: -900}

		jobs := build(t, m, s).Jobs
		if len(jobs) != c.jobs {
			t.Errorf("%s: %d jobs, want the %d it has always replayed", c.name, len(jobs), c.jobs)
		}
		if got := fingerprint(jobs); got != c.want {
			t.Errorf("%s: the job stream changed (digest %s)", c.name, got)
		}
	}
}

func TestBuildingTwiceGivesTheSameMine(t *testing.T) {
	m, s := liveScenario()
	s.PickJitter = 3 * time.Millisecond

	if first, second := build(t, m, s), build(t, m, s); !reflect.DeepEqual(first, second) {
		t.Error("two builds of one scenario differ; a run could not be replayed")
	}
}

// Every pick job is one sensor's detection of one event. That relation is what
// lets the mine work out which event a finished job has brought closer to a
// location.
func TestEveryJobIsOneSensorsPickOfOneEvent(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)

	known := map[string]bool{}
	for _, sensor := range w.Layout.Sensors {
		known[sensor.ID] = true
	}

	total := 0
	for i, event := range w.Events {
		if int(event.FirstJob) != total {
			t.Fatalf("event %d starts at job %d, want %d", i, event.FirstJob, total)
		}
		for k, pick := range event.Picks {
			job := w.Jobs[total+k]
			if job.Event != i {
				t.Fatalf("job %d says event %d, but event %d owns it", job.ID, job.Event, i)
			}
			if job.SubmittedAt != event.Origin {
				t.Fatalf("job %d arrives at %v, its event happened at %v", job.ID, job.SubmittedAt, event.Origin)
			}
			if !known[pick.SensorID] {
				t.Fatalf("event %d has a pick from %q, which is not in the layout", i, pick.SensorID)
			}
		}
		total += len(event.Picks)
	}
	if total != len(w.Jobs) {
		t.Errorf("events account for %d picks, but there are %d jobs", total, len(w.Jobs))
	}
}

// Which sensors detect an event is decided by distance, not at random: the
// count was always drawn per event, and the geometry only chooses which
// sensors those are. A far sensor detecting what a near one missed would put
// the lit sensors in the 3D view nowhere near the event.
func TestTheSensorsThatDetectAnEventAreTheNearestOnes(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)

	for i, event := range w.Events {
		detected := map[string]bool{}
		for _, pick := range event.Picks {
			detected[pick.SensorID] = true
		}
		furthestDetecting, nearestSilent := 0.0, math.Inf(1)
		for _, sensor := range w.Layout.Sensors {
			d := sensor.At.DistanceTo(event.Truth)
			if detected[sensor.ID] {
				furthestDetecting = math.Max(furthestDetecting, d)
			} else {
				nearestSilent = math.Min(nearestSilent, d)
			}
		}
		if furthestDetecting > nearestSilent {
			t.Fatalf("event %d: a sensor %.0f m away detected it while one %.0f m away did not",
				i, furthestDetecting, nearestSilent)
		}
	}
}

func TestEveryEventHappensInsideTheMine(t *testing.T) {
	m, s := verifyScenario()
	w := build(t, m, s)

	if len(w.Events) == 0 {
		t.Fatal("no events, so this proves nothing")
	}
	for i, event := range w.Events {
		if !w.Layout.Extent.Contains(event.Truth) {
			t.Fatalf("event %d at %+v is outside the rock", i, event.Truth)
		}
	}
}

// A burst is one place in the rock failing, and its aftershocks come from
// around there rather than from anywhere in the mine.
func TestABurstsEventsClusterAroundItsEpicentre(t *testing.T) {
	m, s := verifyScenario()
	epicentre := domain.Point{X: 1200, Y: 250, Z: -1100}
	s.Bursts[0].Epicentre = &epicentre
	w := build(t, m, s)

	var burst, background []float64
	for _, event := range w.Events {
		d := event.Truth.DistanceTo(epicentre)
		if event.Burst != nil {
			burst = append(burst, d)
		} else {
			background = append(background, d)
		}
	}
	if len(burst) < 50 || len(background) < 50 {
		t.Fatalf("%d burst and %d background events; too few to compare", len(burst), len(background))
	}
	if median(burst) > 200 {
		t.Errorf("burst events sit a median %.0f m from the epicentre; they should cluster", median(burst))
	}
	if median(background) < 2*median(burst) {
		t.Errorf("background events sit a median %.0f m from the epicentre against %.0f m for the "+
			"burst's own; background should be spread through the mine", median(background), median(burst))
	}
}

func TestAnEpicentreOutsideTheMineIsRefusedByBurst(t *testing.T) {
	m, s := liveScenario()
	s.Bursts[0].Epicentre = &domain.Point{X: 0, Y: 0, Z: 500} // above ground

	_, err := workload.Build(m, s)
	if err == nil || !strings.Contains(err.Error(), "burst 0") || !strings.Contains(err.Error(), "outside") {
		t.Errorf("Build() = %v, want burst 0 named as outside the mine", err)
	}
}

// Sensors do not move between scenarios. Two scenarios on one mine that placed
// its array differently would be comparing two different mines.
func TestAMineHasOneArrayInEveryScenario(t *testing.T) {
	m, s := liveScenario()
	other := s
	other.Seed = 99

	if !reflect.DeepEqual(build(t, m, s).Layout, build(t, m, other).Layout) {
		t.Error("one mine got two different arrays from two scenarios")
	}

	elsewhere := m
	elsewhere.ID = "ravnfjell"
	if reflect.DeepEqual(build(t, m, s).Layout, build(t, elsewhere, s).Layout) {
		t.Error("two different mines got the identical array")
	}
}

func TestAStatedLayoutIsUsedAsGiven(t *testing.T) {
	m, s := liveScenario()
	stated := build(t, m, s).Layout
	stated.Sensors[0].At = domain.Point{X: 10, Y: 10, Z: -500}
	m.Layout = &stated

	if got := build(t, m, s).Layout; !reflect.DeepEqual(got, stated) {
		t.Error("a mine's stated layout was not the one used")
	}
}

// Pick jitter is the dial swept to ask how good a location has to be. Turning
// it must change what the sensors report and nothing else — not where events
// happen, and not the work they produce — or the sweep would be comparing
// different mines.
func TestPickJitterMovesArrivalsAndNothingElse(t *testing.T) {
	m, s := liveScenario()
	exact := build(t, m, s)
	s.PickJitter = 5 * time.Millisecond
	noisy := build(t, m, s)

	if fingerprint(exact.Jobs) != fingerprint(noisy.Jobs) {
		t.Error("pick jitter changed the jobs")
	}
	moved := false
	for i := range exact.Events {
		if exact.Events[i].Truth != noisy.Events[i].Truth {
			t.Fatalf("pick jitter moved event %d", i)
		}
		if !reflect.DeepEqual(exact.Events[i].Picks, noisy.Events[i].Picks) {
			moved = true
		}
	}
	if !moved {
		t.Error("5 ms of pick jitter changed no arrival at all")
	}
}

func TestExactPicksArriveInTravelTimeOrder(t *testing.T) {
	m, s := liveScenario()
	w := build(t, m, s)

	positions := map[string]domain.Point{}
	for _, sensor := range w.Layout.Sensors {
		positions[sensor.ID] = sensor.At
	}
	for i, event := range w.Events {
		distances := make([]float64, len(event.Picks))
		for k, pick := range event.Picks {
			distances[k] = positions[pick.SensorID].DistanceTo(event.Truth)
			want := event.Origin + w.Model.TravelTime(event.Truth, positions[pick.SensorID])
			if pick.At != want {
				t.Fatalf("event %d: %s picked at %v, want %v", i, pick.SensorID, pick.At, want)
			}
		}
		if !sort.Float64sAreSorted(distances) {
			t.Fatalf("event %d: picks are not in arrival order", i)
		}
	}
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2]
}
