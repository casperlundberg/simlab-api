package usecase_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/usecase"
)

// A single drive a kilometre long, at one level. Sampled every 25 m it is 40
// stretches of tunnel to close or leave open.
func kilometre() []domain.Tunnel {
	return []domain.Tunnel{{ID: "d1", Kind: "drive", Path: []domain.Point{
		{X: 0, Y: 0, Z: -500}, {X: 1000, Y: 0, Z: -500},
	}}}
}

// An event of magnitude 1 reaches moderate ground motion 250 m away:
// 0.25·√(10^2) / 0.01.
const moderateRadius = 250.0

func at(x float64) domain.Point { return domain.Point{X: x, Y: 0, Z: -500} }

// located is a location as the mine would have recorded it: where it thinks
// the event was, and how far each level reaches from there, the mine's own
// allowance for error included.
func located(p domain.Point, reach float64) *domain.Location {
	return &domain.Location{At: p, Zones: map[string]float64{"moderate": reach}}
}

func when(d time.Duration) *time.Duration { return &d }

// span is a length of time, as a duration rather than the pointer the record
// holds.
func span(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func closure(t *testing.T, params string) usecase.Closure {
	t.Helper()
	c, err := usecase.NewClosure(json.RawMessage(params))
	if err != nil {
		t.Fatalf("NewClosure(%s) = %v", params, err)
	}
	return c
}

// oneEvent is a world of one event of magnitude 1 at the middle of the drive,
// happening at 10:00, and the record of what the mine made of it.
func oneEvent(r usecase.Record) (usecase.World, usecase.Record) {
	return usecase.World{
		Tunnels: kilometre(),
		Events:  []usecase.Event{{Origin: 10 * time.Hour, At: at(500), Magnitude: 1}},
	}, r
}

func TestAMineThatLocatedEveryEventExactlyClosesTheGroundThatWasDangerous(t *testing.T) {
	w, r := oneEvent(usecase.Record{
		Located:   []*time.Duration{when(10 * time.Hour)},
		Processed: []*time.Duration{when(10 * time.Hour)},
		First:     []*domain.Location{located(at(500), moderateRadius)},
		Final:     []*domain.Location{located(at(500), moderateRadius)},
	})
	m := closure(t, `{}`).Map(w, r)
	if m.Summary.CoveredShare != 1 {
		t.Errorf("covered %.3f of the dangerous ground, want all of it", m.Summary.CoveredShare)
	}
	if m.Summary.MissedMeterSeconds != 0 || m.Summary.FalseMeterSeconds != 0 {
		t.Errorf("missed %.0f and falsely closed %.0f metre-seconds; an exact location does neither",
			m.Summary.MissedMeterSeconds, m.Summary.FalseMeterSeconds)
	}
	// 500 m of drive within 250 m of the event, closed for the whole window.
	if want := 500.0 * 1800; math.Abs(m.Summary.HazardMeterSeconds-want) > 1 {
		t.Errorf("hazard = %.0f metre-seconds, want %.0f", m.Summary.HazardMeterSeconds, want)
	}
	if len(m.Events) != 1 || m.Events[0].CompleteAfter == nil || *m.Events[0].CompleteAfter != 0 {
		t.Errorf("events = %+v; the map covered the event the moment it was located", m.Events)
	}
}

func TestAnEstimateTooFarOutLeavesDangerousGroundOpen(t *testing.T) {
	// Located 100 m up the drive from where it was, with the same reach: the
	// dangerous ground runs 250–750 m, the closed ground 350–850 m.
	w, r := oneEvent(usecase.Record{
		Located:   []*time.Duration{when(10 * time.Hour)},
		Processed: []*time.Duration{when(10 * time.Hour)},
		First:     []*domain.Location{located(at(600), moderateRadius)},
		Final:     []*domain.Location{located(at(600), moderateRadius)},
	})
	m := closure(t, `{}`).Map(w, r)
	if want := 100.0 * 1800; math.Abs(m.Summary.MissedMeterSeconds-want) > 1 {
		t.Errorf("missed %.0f metre-seconds, want %.0f — the 100 m the estimate fell short of",
			m.Summary.MissedMeterSeconds, want)
	}
	if math.Abs(m.Summary.CoveredShare-0.8) > 0.01 {
		t.Errorf("covered %.3f of the dangerous ground, want 0.8", m.Summary.CoveredShare)
	}
	if m.Events[0].CompleteAfter != nil {
		t.Errorf("the map is reported complete after %v, but it never covered the last 100 m",
			*m.Events[0].CompleteAfter)
	}
	// And it closed 100 m beyond the event's reach that nothing endangered.
	if want := 100.0 * 1800; math.Abs(m.Summary.FalseMeterSeconds-want) > 1 {
		t.Errorf("falsely closed %.0f metre-seconds, want %.0f", m.Summary.FalseMeterSeconds, want)
	}
}

func TestGroundClosedForAnEventThatNeverReachedItIsCountedAgainstTheMap(t *testing.T) {
	w, r := oneEvent(usecase.Record{
		Located:   []*time.Duration{when(10 * time.Hour)},
		Processed: []*time.Duration{when(10 * time.Hour)},
		First:     []*domain.Location{located(at(500), 500)},
		Final:     []*domain.Location{located(at(500), 500)},
	})
	m := closure(t, `{}`).Map(w, r)
	if m.Summary.CoveredShare != 1 {
		t.Errorf("covered %.3f; a zone twice the size covers everything dangerous", m.Summary.CoveredShare)
	}
	// The whole kilometre closed, 500 m of it needlessly.
	if want := 500.0 * 1800; math.Abs(m.Summary.FalseMeterSeconds-want) > 1 {
		t.Errorf("falsely closed %.0f metre-seconds, want %.0f", m.Summary.FalseMeterSeconds, want)
	}
	if math.Abs(m.Summary.FalseShare-0.5) > 0.01 {
		t.Errorf("false share %.3f, want half of what was closed", m.Summary.FalseShare)
	}
	if math.Abs(m.Summary.PeakClosedShare-1) > 0.01 {
		t.Errorf("peak closed share %.3f, want the whole drive shut at once", m.Summary.PeakClosedShare)
	}
}

func TestAnEventNobodyLocatedClosesNothingAndIsNeverComplete(t *testing.T) {
	w, r := oneEvent(usecase.Record{
		Located: []*time.Duration{nil}, Processed: []*time.Duration{nil},
		First: []*domain.Location{nil}, Final: []*domain.Location{nil},
	})
	m := closure(t, `{}`).Map(w, r)
	if m.Summary.ClosedMeterSeconds != 0 || m.Summary.CoveredShare != 0 {
		t.Errorf("closed %.0f metre-seconds and covered %.3f with no location at all",
			m.Summary.ClosedMeterSeconds, m.Summary.CoveredShare)
	}
	if m.Summary.NeverComplete != 1 || m.Events[0].CompleteAfter != nil {
		t.Errorf("summary = %+v, events = %+v; the one event was never covered", m.Summary, m.Events)
	}
}

func TestTheMapClosesGroundOnlyOnceTheMineHasALocation(t *testing.T) {
	w, r := oneEvent(usecase.Record{
		Located:   []*time.Duration{when(10*time.Hour + span(60))},
		Processed: []*time.Duration{when(10*time.Hour + span(60))},
		First:     []*domain.Location{located(at(500), moderateRadius)},
		Final:     []*domain.Location{located(at(500), moderateRadius)},
	})
	m := closure(t, `{}`).Map(w, r)
	// 500 m dangerous for 1,800 s, closed for the last 1,740 of them.
	if want := 500.0 * 60; math.Abs(m.Summary.MissedMeterSeconds-want) > 1 {
		t.Errorf("missed %.0f metre-seconds, want %.0f — the minute before the location arrived",
			m.Summary.MissedMeterSeconds, want)
	}
	if m.Events[0].CompleteAfter == nil || *m.Events[0].CompleteAfter != span(60) {
		t.Errorf("complete after %v, want 60 s", m.Events[0].CompleteAfter)
	}
}

// A first location is a map of its own until a better one replaces it: the
// ground it closed is shut for those seconds and the ground it missed is open
// for them, however right the final location turns out to be.
func TestAFirstLocationDrawsTheMapUntilTheFinalOneReplacesIt(t *testing.T) {
	w, r := oneEvent(usecase.Record{
		Located:   []*time.Duration{when(10*time.Hour + span(30))},
		Processed: []*time.Duration{when(10*time.Hour + span(120))},
		First:     []*domain.Location{located(at(700), moderateRadius)},
		Final:     []*domain.Location{located(at(500), moderateRadius)},
	})
	m := closure(t, `{}`).Map(w, r)
	// Nothing closed for 30 s, then 200 m of the 500 dangerous metres left
	// open for 90 s until the final location arrived.
	if want := 500.0*30 + 200.0*90; math.Abs(m.Summary.MissedMeterSeconds-want) > 1 {
		t.Errorf("missed %.0f metre-seconds, want %.0f", m.Summary.MissedMeterSeconds, want)
	}
	// And 200 m shut for those 90 s that the event never reached.
	if want := 200.0 * 90; math.Abs(m.Summary.FalseMeterSeconds-want) > 1 {
		t.Errorf("falsely closed %.0f metre-seconds, want %.0f", m.Summary.FalseMeterSeconds, want)
	}
	if m.Events[0].CompleteAfter == nil || *m.Events[0].CompleteAfter != span(120) {
		t.Errorf("complete after %v, want 120 s — when the location good enough to cover it arrived",
			m.Events[0].CompleteAfter)
	}
}

// The point of case 2: zones from several events overlap, and a neighbour's
// zone can close ground before the event that made it dangerous is located at
// all. The map is judged as the mine would read it — a union — not event by
// event.
func TestANeighboursZoneCanCoverAnEventTheMineHasNotLocatedYet(t *testing.T) {
	w := usecase.World{Tunnels: kilometre(), Events: []usecase.Event{
		{Origin: 10 * time.Hour, At: at(500), Magnitude: 1},
		{Origin: 10*time.Hour + span(30), At: at(520), Magnitude: 1},
	}}
	r := usecase.Record{
		Located:   []*time.Duration{when(10 * time.Hour), when(10*time.Hour + span(900))},
		Processed: []*time.Duration{when(10 * time.Hour), when(10*time.Hour + span(900))},
		First:     []*domain.Location{located(at(500), 400), located(at(520), moderateRadius)},
		Final:     []*domain.Location{located(at(500), 400), located(at(520), moderateRadius)},
	}
	m := closure(t, `{}`).Map(w, r)
	if len(m.Events) != 2 {
		t.Fatalf("%d events in the map, want 2", len(m.Events))
	}
	if m.Events[1].CompleteAfter == nil || *m.Events[1].CompleteAfter != 0 {
		t.Errorf("the second event is covered after %v; the first event's wider zone already "+
			"closed its ground when it happened", m.Events[1].CompleteAfter)
	}
}

func TestClosingMoreThanTheMineWouldRecoversGroundAtAPrice(t *testing.T) {
	w, r := oneEvent(usecase.Record{
		Located:   []*time.Duration{when(10 * time.Hour)},
		Processed: []*time.Duration{when(10 * time.Hour)},
		First:     []*domain.Location{located(at(600), moderateRadius)},
		Final:     []*domain.Location{located(at(600), moderateRadius)},
	})
	tight := closure(t, `{}`).Map(w, r)
	wide := closure(t, `{"extra_allowance_meters": 100}`).Map(w, r)
	if wide.Summary.MissedMeterSeconds >= tight.Summary.MissedMeterSeconds {
		t.Errorf("a further 100 m of allowance missed %.0f metre-seconds against %.0f: it should "+
			"recover ground", wide.Summary.MissedMeterSeconds, tight.Summary.MissedMeterSeconds)
	}
	if wide.Summary.FalseMeterSeconds <= tight.Summary.FalseMeterSeconds {
		t.Errorf("a further 100 m of allowance falsely closed %.0f metre-seconds against %.0f: it "+
			"should cost ground", wide.Summary.FalseMeterSeconds, tight.Summary.FalseMeterSeconds)
	}
}

func TestTheSameRunGivesTheSameClosureMap(t *testing.T) {
	w, r := oneEvent(usecase.Record{
		Located:   []*time.Duration{when(10 * time.Hour)},
		Processed: []*time.Duration{when(10*time.Hour + span(120))},
		First:     []*domain.Location{located(at(400), 200)},
		Final:     []*domain.Location{located(at(500), moderateRadius)},
	})
	first := closure(t, `{}`).Map(w, r)
	if second := closure(t, `{}`).Map(w, r); first.Summary != second.Summary {
		t.Errorf("the same run mapped twice gave %+v and %+v", first.Summary, second.Summary)
	}
}

func TestAClosureAsksForParametersTheMeasureHas(t *testing.T) {
	for _, tc := range []struct{ params, want string }{
		{`{"levl": "high"}`, "levl"},
		{`{"level": "catastrophic"}`, "moderate, high, very-high"},
		{`{"window_seconds": 0}`, "window_seconds"},
		{`{"step_meters": 0}`, "step_meters"},
		{`{"extra_allowance_meters": -1}`, "extra_allowance_meters"},
	} {
		_, err := usecase.NewClosure(json.RawMessage(tc.params))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("NewClosure(%s) = %v, want it refused naming %q", tc.params, err, tc.want)
		}
	}
}
