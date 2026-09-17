package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func walker() domain.Entity {
	return domain.Entity{
		ID: "person-01", Kind: domain.EntityPerson,
		Track: []domain.Waypoint{
			{At: 0, Point: domain.Point{X: 0, Y: 0, Z: -500}},
			{At: 100 * time.Second, Point: domain.Point{X: 100, Y: 0, Z: -500}},
			{At: 400 * time.Second, Point: domain.Point{X: 100, Y: 0, Z: -500}},
			{At: 500 * time.Second, Point: domain.Point{X: 100, Y: 100, Z: -500}},
		},
	}
}

func TestAnEntityIsWhereItsTrackPutsItAtAnyMoment(t *testing.T) {
	e := walker()
	for _, c := range []struct {
		at   time.Duration
		want domain.Point
	}{
		{0, domain.Point{X: 0, Y: 0, Z: -500}},
		{50 * time.Second, domain.Point{X: 50, Y: 0, Z: -500}},
		{250 * time.Second, domain.Point{X: 100, Y: 0, Z: -500}}, // stopped at a face
		{450 * time.Second, domain.Point{X: 100, Y: 50, Z: -500}},
	} {
		if got := e.PositionAt(c.at); got != c.want {
			t.Errorf("at %v: %+v, want %+v", c.at, got, c.want)
		}
	}
}

// Before its first waypoint it has not started moving, and after its last it
// stays where it stopped: a run can go on long after its scenario's workforce
// was planned, and an entity should not vanish from it.
func TestAnEntityHoldsItsPositionOutsideItsTrack(t *testing.T) {
	e := walker()
	e.Track[0].At = 10 * time.Second
	if got := e.PositionAt(0); got != e.Track[0].Point {
		t.Errorf("before its track: %+v", got)
	}
	if got := e.PositionAt(10 * time.Hour); got != e.Track[3].Point {
		t.Errorf("after its track: %+v", got)
	}
}

// A day of a vehicle's track is thousands of waypoints, so each crosses the
// wire as [seconds, x, y, z] rather than as an object repeating its keys.
func TestATrackCrossesTheWireCompactly(t *testing.T) {
	encoded, err := json.Marshal(walker())
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if !strings.Contains(string(encoded), `"track":[[0,0,0,-500],[100,100,0,-500]`) {
		t.Errorf("track is not compact: %s", encoded)
	}

	var back domain.Entity
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if back.ID != "person-01" || back.Kind != domain.EntityPerson || len(back.Track) != 4 ||
		back.Track[3].At != 500*time.Second || back.Track[3].Point.Y != 100 {
		t.Errorf("round trip = %+v", back)
	}
}

func TestAWorkforceCountBelowZeroIsRefused(t *testing.T) {
	scenario := validScenario()
	scenario.Workforce = &domain.Workforce{People: -1}

	if err := scenario.Validate(); err == nil || !strings.Contains(err.Error(), "people") {
		t.Errorf("Validate() = %v, want people named", err)
	}
}

// A scenario that says nothing about its workforce gets the default one; one
// that states zero people means zero. The two must survive the wire apart.
func TestAStatedWorkforceOfNobodyIsNotTheDefault(t *testing.T) {
	scenario := validScenario()
	scenario.Workforce = &domain.Workforce{}

	encoded, _ := json.Marshal(scenario)
	var back domain.Scenario
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if back.Workforce == nil {
		t.Fatal("a stated empty workforce came back as the default")
	}

	encoded, _ = json.Marshal(validScenario())
	back = domain.Scenario{}
	_ = json.Unmarshal(encoded, &back)
	if back.Workforce != nil {
		t.Errorf("an unstated workforce came back stated: %+v", back.Workforce)
	}
}
