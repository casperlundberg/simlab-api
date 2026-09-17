package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/store"
)

func layout() domain.Layout {
	return domain.Layout{
		Extent: domain.Extent{
			Min: domain.Point{X: 0, Y: 0, Z: -1400},
			Max: domain.Point{X: 1600, Y: 1000, Z: -400},
		},
		Sensors: []domain.Sensor{
			{ID: "s01", At: domain.Point{X: 100.5, Y: 200.25, Z: -700}},
			{ID: "s02", At: domain.Point{X: 1500, Y: 900, Z: -1300.75}},
		},
	}
}

// A mine's stated layout, a scenario's pick jitter and a burst's epicentre all
// existed on the domain types before they existed in the database, and saving
// any of them silently kept everything else. Whole-struct comparison is what
// stops the next field doing the same.
func TestEveryFieldOfAMineSurvivesTheDatabase(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	stated := layout()
	saved := domain.Mine{
		ID: "storhall", Name: "Storhall", Sensors: 2, BackgroundRate: 12.5,
		Layout: &stated, Description: "stated array",
	}
	if err := s.SaveMine(ctx, saved); err != nil {
		t.Fatalf("SaveMine() = %v", err)
	}

	read, err := s.Mine(ctx, "storhall")
	if err != nil {
		t.Fatalf("Mine() = %v", err)
	}
	if read.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set by the database")
	}
	saved.CreatedAt = read.CreatedAt
	noZeroFields(t, "mine", saved)
	if !reflect.DeepEqual(saved, read) {
		t.Errorf("the mine changed in the database\n saved: %+v\n  read: %+v", saved, read)
	}

	listed, err := s.Mines(ctx)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], read) {
		t.Errorf("Mines() = %+v, %v; want the same mine Mine() returns", listed, err)
	}
}

func TestAMineWithoutALayoutComesBackWithoutOne(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SaveMine(ctx, mine()); err != nil {
		t.Fatalf("SaveMine() = %v", err)
	}

	read, err := s.Mine(ctx, "storhall")
	if err != nil {
		t.Fatalf("Mine() = %v", err)
	}
	if read.Layout != nil {
		t.Errorf("Layout = %+v; a mine that stated none should get one derived, not stored", read.Layout)
	}
}

func TestEveryFieldOfAScenarioSurvivesTheDatabase(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.SaveMine(ctx, mine()); err != nil {
		t.Fatalf("SaveMine() = %v", err)
	}

	mainMagnitude := 2.8
	saved := scenario()
	saved.PickJitter = 2500 * time.Microsecond
	saved.Workforce = &domain.Workforce{People: 3, CrewedVehicles: 2, AutonomousVehicles: 1}
	saved.Description = "with geometry"
	saved.Bursts = []domain.Burst{
		{At: time.Hour, Magnitude: 40, AftershockDecay: 3 * time.Hour,
			Epicentre: &domain.Point{X: 410.5, Y: 220, Z: -615.25}, MainMagnitude: &mainMagnitude},
		{At: 2 * time.Hour, Magnitude: 5},
	}
	if err := s.SaveScenario(ctx, saved); err != nil {
		t.Fatalf("SaveScenario() = %v", err)
	}

	read, err := s.Scenario(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Scenario() = %v", err)
	}
	saved.CreatedAt = read.CreatedAt
	noZeroFields(t, "scenario", saved)
	noZeroFields(t, "burst", saved.Bursts[0])
	if !reflect.DeepEqual(saved, read) {
		t.Errorf("the scenario changed in the database\n saved: %+v\n  read: %+v", saved, read)
	}
}

func TestARunKeepsTheLayoutItWasReplayedAgainst(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	if _, err := s.RunLayout(ctx, "run-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RunLayout() before one is saved = %v, want not found", err)
	}
	if err := s.SaveLayout(ctx, "run-1", layout()); err != nil {
		t.Fatalf("SaveLayout() = %v", err)
	}
	read, err := s.RunLayout(ctx, "run-1")
	if err != nil {
		t.Fatalf("RunLayout() = %v", err)
	}
	if !reflect.DeepEqual(read, layout()) {
		t.Errorf("RunLayout() = %+v, want %+v", read, layout())
	}
}

func TestLayoutOfARunThatDoesNotExistIsNotFound(t *testing.T) {
	s := open(t)
	if err := s.SaveLayout(context.Background(), "nope", layout()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SaveLayout() = %v, want not found", err)
	}
}

func locatedEvent() domain.SeismicEvent {
	burst := 1
	locatedAt, processedAt := 95500*time.Millisecond, 140*time.Second
	magnitude, estimated, final := 2.4, 2.25, 2.37
	return domain.SeismicEvent{
		RunID: "run-1", Sequence: 1, Origin: 80250 * time.Millisecond, Burst: &burst,
		Truth:     domain.Point{X: 800.5, Y: 500, Z: -900},
		Magnitude: &magnitude,
		Exposed:   []domain.Exposure{{Entity: "person-02", Level: "high", PPV: 0.31, Distance: 48.5}},
		Sensors:   []string{"s02", "s01"},
		LocatedAt: &locatedAt,
		Located: &domain.Location{
			At: domain.Point{X: 790, Y: 510.5, Z: -905}, RMSResidualSeconds: 0.0021, Picks: 4,
			Magnitude: &estimated, Zones: map[string]float64{"moderate": 900, "high": 140},
			Exposed: []domain.Exposure{{Entity: "person-02", Level: "moderate", PPV: 0.05, Distance: 60}},
		},
		ProcessedAt:     &processedAt,
		PickProcessedAt: []*time.Duration{&locatedAt, nil},
		Final: &domain.Location{
			At: domain.Point{X: 801, Y: 499, Z: -899.5}, RMSResidualSeconds: 0.0008, Picks: 9,
			Magnitude: &final, Zones: map[string]float64{"moderate": 950},
			Exposed: []domain.Exposure{},
		},
	}
}

func TestEveryFieldOfASeismicEventSurvivesTheDatabase(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	saved := locatedEvent()
	noZeroFields(t, "seismic event", saved)
	if err := s.SaveSeismicEvents(ctx, "run-1", []domain.SeismicEvent{saved}); err != nil {
		t.Fatalf("SaveSeismicEvents() = %v", err)
	}

	events, err := s.SeismicEvents(ctx, "run-1", 0, 100)
	if err != nil {
		t.Fatalf("SeismicEvents() = %v", err)
	}
	if len(events) != 1 || !reflect.DeepEqual(events[0], saved) {
		t.Errorf("the event changed in the database\n saved: %+v\n  read: %+v", saved, events)
	}
}

// Events are written once unlocated and again as the mine locates them. The
// second write has to replace the first, and an event not in the second write
// has to be left exactly as it was.
func TestSavingAnEventAgainRecordsItsLocationAndLeavesTheOthersAlone(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	first := domain.SeismicEvent{RunID: "run-1", Sequence: 1, Origin: time.Minute, Sensors: []string{"s01"}}
	second := domain.SeismicEvent{RunID: "run-1", Sequence: 2, Origin: 2 * time.Minute, Sensors: []string{"s02"}}
	if err := s.SaveSeismicEvents(ctx, "run-1", []domain.SeismicEvent{first, second}); err != nil {
		t.Fatalf("SaveSeismicEvents() = %v", err)
	}

	located := locatedEvent()
	located.Sequence = 2
	if err := s.SaveSeismicEvents(ctx, "run-1", []domain.SeismicEvent{located}); err != nil {
		t.Fatalf("SaveSeismicEvents() again = %v", err)
	}

	events, err := s.SeismicEvents(ctx, "run-1", 0, 100)
	if err != nil {
		t.Fatalf("SeismicEvents() = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("%d events, want 2", len(events))
	}
	if !reflect.DeepEqual(events[0], first) {
		t.Errorf("an event not written again changed: %+v", events[0])
	}
	if !reflect.DeepEqual(events[1], located) {
		t.Errorf("the second write did not replace the first: %+v", events[1])
	}
}

func TestEventsCanBeReadFromWhereAReaderLeftOff(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	var events []domain.SeismicEvent
	for i := 1; i <= 5; i++ {
		events = append(events, domain.SeismicEvent{
			RunID: "run-1", Sequence: i, Origin: time.Duration(i) * time.Second, Sensors: []string{"s01"},
		})
	}
	if err := s.SaveSeismicEvents(ctx, "run-1", events); err != nil {
		t.Fatalf("SaveSeismicEvents() = %v", err)
	}

	page, err := s.SeismicEvents(ctx, "run-1", 2, 2)
	if err != nil {
		t.Fatalf("SeismicEvents() = %v", err)
	}
	if len(page) != 2 || page[0].Sequence != 3 || page[1].Sequence != 4 {
		t.Errorf("from 2, limit 2 = %+v, want events 3 and 4", page)
	}
}

func TestDeletingARunTakesItsMineWithIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}
	if err := s.SaveSeismicEvents(ctx, "run-1", []domain.SeismicEvent{locatedEvent()}); err != nil {
		t.Fatalf("SaveSeismicEvents() = %v", err)
	}

	if err := s.DeleteRun(ctx, "run-1"); err != nil {
		t.Fatalf("DeleteRun() = %v", err)
	}
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() again = %v", err)
	}
	events, err := s.SeismicEvents(ctx, "run-1", 0, 100)
	if err != nil {
		t.Fatalf("SeismicEvents() = %v", err)
	}
	if len(events) != 0 {
		t.Errorf("a new run with a deleted run's id inherited %d of its events", len(events))
	}
}

func TestAScenarioThatStatesNoWorkforceComesBackWithoutOne(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)

	read, err := s.Scenario(ctx, scenario().ID)
	if err != nil {
		t.Fatalf("Scenario() = %v", err)
	}
	if read.Workforce != nil {
		t.Errorf("Workforce = %+v; unstated should stay unstated, so the default applies", read.Workforce)
	}
}

func TestARunKeepsThePeopleAndVehiclesItWasReplayedWith(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	saved := []domain.Entity{
		{ID: "person-01", Kind: domain.EntityPerson, Track: []domain.Waypoint{
			{At: 0, Point: domain.Point{X: 1.5, Y: 2, Z: -450}},
			{At: 90500 * time.Millisecond, Point: domain.Point{X: 91.5, Y: 2, Z: -450}},
		}},
		{ID: "autonomous-vehicle-01", Kind: domain.EntityAutonomousVehicle, Track: []domain.Waypoint{
			{At: 0, Point: domain.Point{X: 160, Y: 300, Z: -1350}},
		}},
	}
	if err := s.SaveEntities(ctx, "run-1", saved); err != nil {
		t.Fatalf("SaveEntities() = %v", err)
	}

	read, err := s.Entities(ctx, "run-1")
	if err != nil {
		t.Fatalf("Entities() = %v", err)
	}
	// Ordered by id, which is how they come back.
	want := []domain.Entity{saved[1], saved[0]}
	if !reflect.DeepEqual(read, want) {
		t.Errorf("entities changed in the database\n saved: %+v\n  read: %+v", want, read)
	}
}
