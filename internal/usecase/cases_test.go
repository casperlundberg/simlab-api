package usecase_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
	"github.com/casperlundberg/simlab-api/internal/usecase"
)

// A drive along x from 0 to 600, with a crosscut leaving it at x=200, and one
// Nuttli 0 event at x=400 a minute into the run.
func drive(units ...domain.Entity) usecase.World {
	return usecase.World{
		Events: []usecase.Event{{Origin: time.Minute, At: domain.Point{X: 400}, Magnitude: 0}},
		Units:  units,
		Tunnels: []domain.Tunnel{
			{ID: "drive", Kind: mineplan.Drive, Path: []domain.Point{{}, {X: 200}, {X: 600}}},
			{ID: "crosscut", Kind: mineplan.Crosscut, Path: []domain.Point{{X: 200}, {X: 200, Y: 150}}},
		},
	}
}

// walker walks east at 1 m/s from x, stopping at each tunnel node on the way,
// as the generator's tracks do.
func walker(id, kind string, x float64) domain.Entity {
	var track []domain.Waypoint
	for _, stop := range []float64{x, 200, 600} {
		if stop < x {
			continue
		}
		track = append(track, domain.Waypoint{At: time.Duration(stop-x) * time.Second, Point: domain.Point{X: stop}})
	}
	return domain.Entity{ID: id, Kind: kind, Track: track}
}

// The moderate zone of a Nuttli 0 event, as the hazard model has it.
func moderate(t *testing.T) float64 {
	r, ok := hazard.Default.Zones(0, 0)[hazard.Moderate]
	if !ok {
		t.Fatal("a Nuttli 0 event has no moderate zone")
	}
	return r
}

func one(t *testing.T, kind string, params string, w usecase.World) []usecase.Opportunity {
	t.Helper()
	c, err := usecase.New(kind, json.RawMessage(params))
	if err != nil {
		t.Fatalf("New(%s) = %v", kind, err)
	}
	return c.Opportunities(w)
}

func secondsOf(d time.Duration) float64 { return d.Seconds() }

func TestAUnitHeadingIntoAZoneMustBeToldBeforeItEntersLessItsReaction(t *testing.T) {
	r := moderate(t)
	got := one(t, "turn-back", "", drive(walker("person-01", domain.EntityPerson, 0)))
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want one", len(got))
	}
	// It reaches the zone's edge at x = 400-r, at 1 m/s from x=0; the default
	// reaction is 30 s.
	if want := 400 - r - 30; math.Abs(secondsOf(got[0].Closes)-want) > 1e-6 || got[0].Opens != time.Minute {
		t.Errorf("decision opens %v and closes %v s, want at the event and at %.3f s", got[0].Opens, secondsOf(got[0].Closes), want)
	}
	if got[0].Need.Name() != "first-location" {
		t.Errorf("need = %s, want the event's first location", got[0].Need.Name())
	}
}

func TestAUnitAlreadyInsideTheZoneIsNotTurnedBackButLedOut(t *testing.T) {
	w := drive(walker("person-01", domain.EntityPerson, 360))
	if got := one(t, "turn-back", "", w); len(got) != 0 {
		t.Errorf("turn-back: %d decisions for someone inside the zone when it happened", len(got))
	}
	got := one(t, "way-out", "", w)
	if len(got) != 1 {
		t.Fatalf("way-out: %d decisions, want one", len(got))
	}
	// From x=360 at 1 m/s, at x=360+60=420 by the event; it leaves the zone at x = 400+r.
	r := moderate(t)
	if want := (400 + r - 360) - 30; math.Abs(secondsOf(got[0].Closes)-want) > 1e-6 {
		t.Errorf("way-out closes at %v s, want %.3f: when it would have left on its own, less its reaction",
			secondsOf(got[0].Closes), want)
	}
}

func TestARerouteMustBeDecidedBeforeTheLastJunctionOnTheWayIn(t *testing.T) {
	// Starting at x=0: passes the junction at x=200 at 200 s, enters the zone later.
	got := one(t, "reroute", "", drive(walker("person-01", domain.EntityPerson, 0)))
	if len(got) != 1 || math.Abs(secondsOf(got[0].Closes)-(200-30)) > 1e-6 {
		t.Fatalf("got %+v; want one decision closing 30 s before the junction at 200 s", got)
	}
	// Starting past the junction, there is no other path left to take.
	late := one(t, "reroute", "", drive(walker("person-01", domain.EntityPerson, 250)))
	if len(late) != 1 || late[0].Closes > late[0].Opens {
		t.Errorf("got %+v; with no junction left before the zone the decision cannot be won", late)
	}
}

func TestAnEventTooSmallToReachTheLevelMakesNoDecision(t *testing.T) {
	w := drive(walker("person-01", domain.EntityPerson, 0))
	w.Events[0].Magnitude = -3
	for _, kind := range usecase.Kinds() {
		if got := one(t, kind, `{"level":"very-high"}`, w); len(got) != 0 {
			t.Errorf("%s: %d decisions for an event that reaches no very high ground motion", kind, len(got))
		}
	}
}

func TestOnlyTheKindsOfUnitAskedAboutAreDecidedFor(t *testing.T) {
	w := drive(walker("person-01", domain.EntityPerson, 0), walker("autonomous-vehicle-01", domain.EntityAutonomousVehicle, 0))
	got := one(t, "turn-back", `{"kinds":["autonomous-vehicle"]}`, w)
	if len(got) != 1 || got[0].Entity != "autonomous-vehicle-01" {
		t.Errorf("got %+v; want the machine alone", got)
	}
}

func TestParametersThatCouldNotMeanAnythingAreRefusedByName(t *testing.T) {
	for _, kind := range usecase.Kinds() {
		for params, field := range map[string]string{
			`{"level":"apocalyptic"}`: "level",
			`{"kinds":["miner"]}`:     "kinds",
			`{"window_seconds":0}`:    "window_seconds",
			`{"reaction_seconds":-1}`: "reaction_seconds",
		} {
			if _, err := usecase.New(kind, json.RawMessage(params)); err == nil || !strings.Contains(err.Error(), field) {
				t.Errorf("%s %s: New() = %v; want %s named", kind, params, err, field)
			}
		}
	}
}

// ------------------------------------------------------------------ warning

func location(x float64, moderate float64) *domain.Location {
	return &domain.Location{At: domain.Point{X: x}, Zones: map[string]float64{"moderate": moderate}}
}

func TestAWarningIsTheFirstLocationWhoseZoneReachesWhereItMatters(t *testing.T) {
	need := usecase.Warning{Event: 0, At: domain.Point{X: 100}, Level: "moderate"}
	for _, tc := range []struct {
		name         string
		first, final *domain.Location
		want         *time.Duration
	}{
		{"the first location's zone reaches it", location(20, 90), location(0, 50), seconds(30)},
		{"only the final location's does", location(-200, 90), location(20, 90), seconds(80)},
		{"neither does", location(-200, 90), location(-150, 90), nil},
		{"no location at all", nil, nil, nil},
	} {
		record := usecase.Record{
			Located: []*time.Duration{seconds(30)}, Processed: []*time.Duration{seconds(80)},
			First: []*domain.Location{tc.first}, Final: []*domain.Location{tc.final},
		}
		met, ok := need.MetAt(record)
		var got *time.Duration
		if ok {
			got = &met
		}
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: met at %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestACaseCanWaitForAWarningRatherThanAnyLocation(t *testing.T) {
	for _, kind := range usecase.Kinds() {
		w := drive(walker("person-01", domain.EntityPerson, 0), walker("person-02", domain.EntityPerson, 360))
		for _, o := range one(t, kind, `{"need":"warning"}`, w) {
			if o.Need.Name() != "warning" {
				t.Errorf("%s: need %s, want warning", kind, o.Need.Name())
			}
		}
		if _, err := usecase.New(kind, json.RawMessage(`{"need":"clairvoyance"}`)); err == nil ||
			!strings.Contains(err.Error(), "need") {
			t.Errorf("%s: an unknown need was not refused by name: %v", kind, err)
		}
	}
}

// A case can ask only about the encounters a scenario scripted, so what they
// measure is the notice they were scripted with and not the rest of the day.
func TestACaseCanAskOnlyAboutTheEncountersThatWereScripted(t *testing.T) {
	w := drive(walker("person-01", domain.EntityPerson, 0))
	w.Events = append(w.Events, usecase.Event{
		Origin: time.Minute, At: domain.Point{X: 400}, Magnitude: 0, Activity: "encounter"})
	for _, tc := range []struct {
		params string
		want   int
	}{
		{"", 2},
		{`{"activity":"encounter"}`, 1},
		{`{"activity":"blast"}`, 0},
	} {
		for _, kind := range usecase.Kinds() {
			got := one(t, kind, tc.params, w)
			if kind == "way-out" {
				continue // nobody is inside this zone when it happens
			}
			if len(got) != tc.want {
				t.Errorf("%s with %q: %d decisions, want %d", kind, tc.params, len(got), tc.want)
			}
		}
	}
	if _, err := usecase.New("turn-back", json.RawMessage(`{"activity":"mining"}`)); err == nil ||
		!strings.Contains(err.Error(), "activity") {
		t.Errorf("New() = %v, want an activity nothing produces refused", err)
	}
}
