package usecase_test

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/usecase"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

func seconds(s float64) *time.Duration {
	d := time.Duration(s * float64(time.Second))
	return &d
}

func opportunity(event int, opens, closes float64) usecase.Opportunity {
	return usecase.Opportunity{
		Case: "test", Event: event, Entity: "person-01", Need: usecase.FirstLocation{Event: event},
		Opens: time.Duration(opens * float64(time.Second)), Closes: time.Duration(closes * float64(time.Second)),
	}
}

func TestADecisionIsInTimeWhenWhatItNeededArrivedBeforeItClosed(t *testing.T) {
	record := usecase.Record{Located: []*time.Duration{seconds(40), seconds(90), nil}}
	got := usecase.Score([]usecase.Opportunity{
		opportunity(0, 10, 60), // located at 40: in time, 20 s to spare
		opportunity(1, 10, 60), // located at 90: late
		opportunity(2, 10, 60), // never located
		opportunity(0, 10, 40), // located exactly as it closed: in time
	}, record)

	want := []struct {
		met    *time.Duration
		inTime bool
	}{{seconds(40), true}, {seconds(90), false}, {nil, false}, {seconds(40), true}}
	for i, w := range want {
		if !reflect.DeepEqual(got[i].MetAt, w.met) || got[i].InTime != w.inTime {
			t.Errorf("outcome %d = met %v, in time %v; want %v, %v", i, got[i].MetAt, got[i].InTime, w.met, w.inTime)
		}
	}
}

func TestASummarySaysHowManyDecisionsHadWhatTheyNeededInTime(t *testing.T) {
	record := usecase.Record{Located: []*time.Duration{seconds(20), seconds(50), seconds(200), nil}}
	summary := usecase.Summarise(usecase.Score([]usecase.Opportunity{
		opportunity(0, 10, 60),
		opportunity(1, 10, 60),
		opportunity(2, 10, 60),
		opportunity(3, 10, 60),
		opportunity(3, 10, 5), // closed before it opened
	}, record))

	if summary.Opportunities != 5 || summary.InTime != 2 || summary.Never != 2 || summary.Unwinnable != 1 {
		t.Errorf("summary = %+v; want 5 opportunities, 2 in time, 2 never met, 1 unwinnable", summary)
	}
	if math.Abs(summary.InTimeShare-0.4) > 1e-12 {
		t.Errorf("in-time share = %v, want 0.4", summary.InTimeShare)
	}
	// Latency over the three that were met: 10, 40 and 190 s.
	if summary.LatencyP50 != 40 || summary.LatencyP95 != 190 {
		t.Errorf("latency p50/p95 = %v/%v, want 40/190", summary.LatencyP50, summary.LatencyP95)
	}
	// Slack over the two in time: 40 and 10 s.
	if summary.SlackP50 != 10 {
		t.Errorf("slack p50 = %v, want 10", summary.SlackP50)
	}
}

func TestASummaryOfNothingSaysSoRatherThanDividingByZero(t *testing.T) {
	summary := usecase.Summarise(nil)
	if summary.Opportunities != 0 || !math.IsNaN(summary.InTimeShare) {
		t.Errorf("summary = %+v; with no opportunities the share is not a number, not 0 or 1", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil || !strings.Contains(string(encoded), `"in_time_share":null`) {
		t.Errorf("json = %s, %v; want in_time_share null", encoded, err)
	}
}

func TestAnUnknownUseCaseIsRefusedNamingTheOnesThereAre(t *testing.T) {
	_, err := usecase.New("fortune-telling", nil)
	if err == nil || !strings.Contains(err.Error(), "turn-back") {
		t.Errorf("New() = %v; want a refusal that lists the known cases", err)
	}
}

func TestAParameterACaseDoesNotHaveIsRefusedByName(t *testing.T) {
	for _, kind := range usecase.Kinds() {
		_, err := usecase.New(kind, json.RawMessage(`{"crystal_ball": true}`))
		if err == nil || !strings.Contains(err.Error(), "crystal_ball") {
			t.Errorf("%s: New() = %v; want the unknown parameter named", kind, err)
		}
	}
}

// world is a calibrated-sized day as the generator makes it, recorded as a run
// would record it: every event located a minute after it happened.
func world(t *testing.T) (usecase.World, usecase.Record) {
	t.Helper()
	w, err := workload.Build(
		domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 30, BackgroundRate: 91},
		domain.Scenario{ID: "day", MineID: "storhall", Duration: 3 * time.Hour, JobSeconds: 28, Seed: 17,
			PriorityMix: map[domain.Priority]float64{100: 1, 50: 2, 0: 1},
			Bursts:      []domain.Burst{{At: time.Hour, Magnitude: 30, AftershockDecay: 20 * time.Minute}}})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	var events []domain.SeismicEvent
	for i, e := range w.Events {
		m := e.Magnitude
		located := e.Origin + time.Minute
		events = append(events, domain.SeismicEvent{Sequence: i + 1, Origin: e.Origin, Truth: e.Truth,
			Magnitude: &m, LocatedAt: &located, ProcessedAt: &located})
	}
	return usecase.FromRun(events, w.Entities, w.Layout.Tunnels)
}

// Every case keeps the same promises, so the scoring, the API and the sweeps
// can take any of them.
func TestEveryCaseListsItsDecisionsDeterministicallyAndOnlyAboutWhatHappened(t *testing.T) {
	w, record := world(t)
	units := map[string]bool{}
	for _, u := range w.Units {
		units[u.ID] = true
	}
	for _, kind := range usecase.Kinds() {
		c, err := usecase.New(kind, nil)
		if err != nil {
			t.Fatalf("%s: New() = %v", kind, err)
		}
		first, again := c.Opportunities(w), c.Opportunities(w)
		if !reflect.DeepEqual(first, again) {
			t.Errorf("%s: the same world gave different decisions", kind)
		}
		if len(first) == 0 {
			t.Errorf("%s: three hours of a mine at 91 events an hour, with a burst, gave no decision to make", kind)
		}
		for i, o := range first {
			if o.Case != kind || o.Event < 0 || o.Event >= len(w.Events) || (o.Entity != "" && !units[o.Entity]) {
				t.Fatalf("%s: decision %d = %+v names something the world does not have", kind, i, o)
			}
			if o.Opens < w.Events[o.Event].Origin {
				t.Fatalf("%s: decision %d opens at %v, before its event happened at %v", kind, i, o.Opens, w.Events[o.Event].Origin)
			}
			if i > 0 && less(o, first[i-1]) {
				t.Fatalf("%s: decisions are not in event, then unit order at %d", kind, i)
			}
		}
		if outcomes := usecase.Score(first, record); len(outcomes) != len(first) {
			t.Errorf("%s: %d outcomes for %d decisions", kind, len(outcomes), len(first))
		}
	}
}

func less(a, b usecase.Opportunity) bool {
	if a.Event != b.Event {
		return a.Event < b.Event
	}
	return a.Entity < b.Entity
}

func TestARunsRecordSaysWhenEachEventWasFirstAndFinallyLocated(t *testing.T) {
	m := 1.5
	events := []domain.SeismicEvent{
		{Sequence: 1, Origin: 10 * time.Second, Truth: domain.Point{X: 1}, Magnitude: &m,
			LocatedAt: seconds(30), ProcessedAt: seconds(80)},
		{Sequence: 2, Origin: 20 * time.Second, Truth: domain.Point{X: 2}, Magnitude: &m},
	}
	w, record := usecase.FromRun(events, nil, nil)
	if len(w.Events) != 2 || w.Events[1].Origin != 20*time.Second || w.Events[0].Magnitude != 1.5 {
		t.Errorf("world events = %+v", w.Events)
	}
	if !reflect.DeepEqual(record.Located, []*time.Duration{seconds(30), nil}) ||
		!reflect.DeepEqual(record.Processed, []*time.Duration{seconds(80), nil}) {
		t.Errorf("record = %+v", record)
	}
}
