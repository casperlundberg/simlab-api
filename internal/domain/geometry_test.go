package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func statedLayout() *domain.Layout {
	return &domain.Layout{
		Extent: domain.Extent{
			Min: domain.Point{X: 0, Y: 0, Z: -900},
			Max: domain.Point{X: 1000, Y: 800, Z: -300},
		},
		Sensors: []domain.Sensor{
			{ID: "n1", At: domain.Point{X: 100, Y: 100, Z: -400}},
			{ID: "n2", At: domain.Point{X: 900, Y: 100, Z: -800}},
			{ID: "n3", At: domain.Point{X: 500, Y: 700, Z: -600}},
			{ID: "n4", At: domain.Point{X: 200, Y: 600, Z: -350}},
		},
	}
}

func mineWithLayout() domain.Mine {
	return domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 4, BackgroundRate: 12, Layout: statedLayout()}
}

func TestAMineMayStateItsOwnSensorArray(t *testing.T) {
	if err := mineWithLayout().Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

// Sensors is what sets how much work an event becomes, and the layout is what
// the picks come from. Two numbers for one array that disagree would make the
// workload and the geometry describe different mines.
func TestALayoutThatDisagreesWithTheSensorCountIsRefused(t *testing.T) {
	mine := mineWithLayout()
	mine.Sensors = 40

	err := mine.Validate()
	if err == nil || !strings.Contains(err.Error(), "4 sensors") || !strings.Contains(err.Error(), "40") {
		t.Errorf("Validate() = %v, want both counts named", err)
	}
}

func TestASensorOutsideTheMineIsRefusedByName(t *testing.T) {
	mine := mineWithLayout()
	mine.Layout.Sensors[2].At.Z = 50 // above the surface the extent ends at

	if err := mine.Validate(); err == nil || !strings.Contains(err.Error(), "n3") {
		t.Errorf("Validate() = %v, want sensor n3 named", err)
	}
}

// A flat extent cannot contain an epicentre, and the solver would search a
// plane while reporting a volume.
func TestAnExtentWithNoVolumeIsRefused(t *testing.T) {
	mine := mineWithLayout()
	mine.Layout.Extent.Max.Z = mine.Layout.Extent.Min.Z

	if err := mine.Validate(); err == nil || !strings.Contains(err.Error(), "Z") {
		t.Errorf("Validate() = %v, want the flat axis named", err)
	}
}

// A pick names its sensor by id, so two sensors sharing one would put a
// detection at whichever position happened to be read last.
func TestTwoSensorsSharingAnIDAreRefused(t *testing.T) {
	mine := mineWithLayout()
	mine.Layout.Sensors[3].ID = "n1"

	if err := mine.Validate(); err == nil || !strings.Contains(err.Error(), `"n1"`) {
		t.Errorf("Validate() = %v, want the repeated id named", err)
	}
}

func TestANegativePickJitterIsRefused(t *testing.T) {
	scenario := validScenario()
	scenario.PickJitter = -time.Millisecond

	if err := scenario.Validate(); err == nil || !strings.Contains(err.Error(), "pick_jitter") {
		t.Errorf("Validate() = %v, want pick_jitter named", err)
	}
}

// Both fields existed on the domain type and neither reached the wire, so a
// client setting them was answered with a scenario that had silently dropped
// them. What goes in has to come back out.
func TestPickJitterAndABurstEpicentreSurviveTheWire(t *testing.T) {
	original := validScenario()
	original.PickJitter = 2500 * time.Microsecond
	original.Bursts[0].Epicentre = &domain.Point{X: 410, Y: 220, Z: -615}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if !strings.Contains(string(encoded), `"pick_jitter_seconds":0.0025`) {
		t.Errorf("pick jitter is not in seconds on the wire: %s", encoded)
	}

	var back domain.Scenario
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if back.PickJitter != original.PickJitter {
		t.Errorf("PickJitter round trip = %v, want %v", back.PickJitter, original.PickJitter)
	}
	if got := back.Bursts[0].Epicentre; got == nil || *got != *original.Bursts[0].Epicentre {
		t.Errorf("epicentre round trip = %v, want %v", got, original.Bursts[0].Epicentre)
	}
}

func TestABurstWithoutAnEpicentreSaysNothingAboutOne(t *testing.T) {
	encoded, err := json.Marshal(validScenario())
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if strings.Contains(string(encoded), "epicentre") {
		t.Errorf("an unset epicentre should be absent, not a point at the origin: %s", encoded)
	}
}

// The chart of queue composition by submitted priority has to tell an empty
// queue apart from a cycle recorded before submitted priority was tracked.
// Both would otherwise read as nothing waiting.
func TestACycleSaysWhetherSubmittedPrioritiesWereRecorded(t *testing.T) {
	notRecorded, _ := json.Marshal(domain.Cycle{})
	if !strings.Contains(string(notRecorded), `"depth_by_submitted_priority":null`) {
		t.Errorf("an unrecorded composition should be null: %s", notRecorded)
	}

	empty, _ := json.Marshal(domain.Cycle{SubmittedDepths: map[domain.Priority]int{}})
	if !strings.Contains(string(empty), `"depth_by_submitted_priority":{}`) {
		t.Errorf("an empty queue should be an empty object: %s", empty)
	}
}

func TestASeismicEventCrossesTheWireInSeconds(t *testing.T) {
	burst := 0
	locatedAt, processedAt := 95*time.Second, 140*time.Second
	event := domain.SeismicEvent{
		RunID: "run-1", Sequence: 3, Origin: 80 * time.Second, Burst: &burst,
		Truth:           domain.Point{X: 1, Y: 2, Z: -3},
		Sensors:         []string{"s01", "s04"},
		LocatedAt:       &locatedAt,
		Located:         &domain.Location{At: domain.Point{X: 4, Y: 5, Z: -6}, RMSResidualSeconds: 0.002, Picks: 4},
		ProcessedAt:     &processedAt,
		PickProcessedAt: []*time.Duration{&locatedAt, nil},
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	for _, want := range []string{
		`"origin_seconds":80`, `"located_at_seconds":95`, `"processed_at_seconds":140`,
		`"burst":0`, `"final":null`, `"picks_processed_at_seconds":[95,null]`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("%s is missing %s", encoded, want)
		}
	}

	var back domain.SeismicEvent
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if back.Origin != event.Origin || *back.LocatedAt != locatedAt || *back.ProcessedAt != processedAt ||
		*back.Burst != 0 || back.Final != nil || back.Located.Picks != 4 ||
		len(back.PickProcessedAt) != 2 || *back.PickProcessedAt[0] != locatedAt || back.PickProcessedAt[1] != nil {
		t.Errorf("round trip = %+v, want %+v", back, event)
	}
}

// Background activity belongs to no burst, and has to say so rather than
// claim to be part of the first one.
func TestABackgroundEventBelongsToNoBurst(t *testing.T) {
	encoded, _ := json.Marshal(domain.SeismicEvent{Sequence: 1})
	if !strings.Contains(string(encoded), `"burst":null`) || !strings.Contains(string(encoded), `"located_at_seconds":null`) ||
		!strings.Contains(string(encoded), `"picks_processed_at_seconds":null`) {
		t.Errorf("unset burst and location times should be null: %s", encoded)
	}
}

func TestATunnelOutsideTheMineIsRefusedByName(t *testing.T) {
	mine := mineWithLayout()
	mine.Layout.Tunnels = []domain.Tunnel{
		{ID: "L600-drive", Kind: "drive", Path: []domain.Point{{X: 10, Y: 10, Z: -600}, {X: 990, Y: 10, Z: -600}}},
		{ID: "adit", Kind: "access", Path: []domain.Point{{X: 10, Y: 10, Z: -600}, {X: 10, Y: 10, Z: 20}}},
	}

	err := mine.Validate()
	if err == nil || !strings.Contains(err.Error(), `"adit"`) || strings.Contains(err.Error(), "L600") {
		t.Errorf("Validate() = %v, want only tunnel adit named", err)
	}
}

func TestATunnelOfOnePointIsRefused(t *testing.T) {
	mine := mineWithLayout()
	mine.Layout.Tunnels = []domain.Tunnel{{ID: "stub", Kind: "drive", Path: []domain.Point{{X: 10, Y: 10, Z: -600}}}}

	if err := mine.Validate(); err == nil || !strings.Contains(err.Error(), `"stub"`) {
		t.Errorf("Validate() = %v, want tunnel stub named", err)
	}
}
