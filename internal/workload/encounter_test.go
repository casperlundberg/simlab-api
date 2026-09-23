package workload_test

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// A calibrated day, with encounters scripted into it or not.
func day(t *testing.T, seed int64, encounters *domain.EncounterSpec) workload.Workload {
	t.Helper()
	w, err := workload.Build(
		domain.Mine{ID: "storhall", Name: "Storhall", Sensors: 30, BackgroundRate: 91},
		domain.Scenario{ID: "day", MineID: "storhall", Duration: 24 * time.Hour, JobSeconds: 28, Seed: seed,
			PriorityMix: map[domain.Priority]float64{100: 1, 50: 2, 0: 1}, Encounters: encounters})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return w
}

func unitNamed(w workload.Workload, id string) (domain.Entity, bool) {
	for _, e := range w.Entities {
		if e.ID == id {
			return e, true
		}
	}
	return domain.Entity{}, false
}

func encounters(w workload.Workload) []workload.Event {
	var out []workload.Event
	for _, e := range w.Events {
		if e.Activity == workload.Encounter {
			out = append(out, e)
		}
	}
	return out
}

// The point of scripting one: the unit reaches the edge of the zone a stated
// lead after the event, so a decision has exactly that long to be made.
func TestAScriptedEncounterGivesAUnitTheNoticeItWasScriptedFor(t *testing.T) {
	spec := domain.DefaultEncounters()
	spec.Count, spec.Lead = 10, 3*time.Minute
	w := day(t, 1, &spec)
	scripted := encounters(w)
	if len(scripted) < 5 {
		t.Fatalf("%d encounters of the 10 asked for; most of them should be placeable", len(scripted))
	}
	radius := hazard.Default.Zones(spec.Magnitude, 0)[hazard.High]
	for _, e := range scripted {
		if e.Magnitude != spec.Magnitude {
			t.Errorf("a scripted event of magnitude %v, want %v", e.Magnitude, spec.Magnitude)
		}
		// The event names the unit it was scripted for, and that unit gets the
		// notice. Others may walk into the same zone sooner or later; those
		// are decisions the day gave, not the one that was scripted.
		unit, ok := unitNamed(w, e.ScriptedFor)
		if !ok {
			t.Errorf("the event at %v was scripted for %q, which is nobody in the run", e.Origin, e.ScriptedFor)
			continue
		}
		if unit.PositionAt(e.Origin).DistanceTo(e.Truth) <= radius {
			t.Errorf("%s was already inside the zone of the event at %v scripted for it", unit.ID, e.Origin)
			continue
		}
		given := time.Duration(-1)
		for at := e.Origin; at < e.Origin+time.Hour; at += 5 * time.Second {
			if unit.PositionAt(at).DistanceTo(e.Truth) <= radius {
				given = at - e.Origin
				break
			}
		}
		// The track is walked in five-second steps, so the notice can be a
		// step out; it must not be a different notice.
		if off := given - spec.Lead; off < -10*time.Second || off > 10*time.Second {
			t.Errorf("the event at %v gave %s %v notice, want %v", e.Origin, unit.ID, given, spec.Lead)
		}
	}
}

// Scripting encounters adds events to a day; it does not make a different one.
func TestScriptingEncountersLeavesTheDayItWasScriptedIntoAsItWas(t *testing.T) {
	spec := domain.DefaultEncounters()
	plain, scripted := day(t, 2, nil), day(t, 2, &spec)
	if !reflect.DeepEqual(plain.Entities, scripted.Entities) {
		t.Error("scripting encounters moved the workforce; it is scripted around their tracks, not over them")
	}
	var kept []workload.Event
	for _, e := range scripted.Events {
		if e.Activity != workload.Encounter {
			kept = append(kept, e)
		}
	}
	if len(kept) != len(plain.Events) {
		t.Fatalf("%d of the day's own events with encounters scripted in, %d without", len(kept), len(plain.Events))
	}
	for i, e := range plain.Events {
		if kept[i].Origin != e.Origin || kept[i].Truth != e.Truth || kept[i].Magnitude != e.Magnitude ||
			len(kept[i].Picks) != len(e.Picks) {
			t.Fatalf("the day's event %d moved when encounters were scripted in: %+v, want %+v",
				i, kept[i], e)
		}
	}
}

// The catalogue reads an event's picks as the jobs from FirstJob on, and the
// queue plays jobs in the order they were submitted. Merging scripted events
// into a day must leave both true.
func TestScriptedEventsJobsAreSubmittedInOrderAndStayWithTheirEvent(t *testing.T) {
	spec := domain.DefaultEncounters()
	w := day(t, 3, &spec)
	for i, job := range w.Jobs {
		if job.ID != domain.JobID(i) {
			t.Fatalf("job %d has id %d", i, job.ID)
		}
		if i > 0 && job.SubmittedAt < w.Jobs[i-1].SubmittedAt {
			t.Fatalf("job %d was submitted at %v, before job %d at %v", i, job.SubmittedAt, i-1, w.Jobs[i-1].SubmittedAt)
		}
	}
	for i, event := range w.Events {
		if i > 0 && event.Origin < w.Events[i-1].Origin {
			t.Fatalf("event %d happened at %v, before event %d at %v", i, event.Origin, i-1, w.Events[i-1].Origin)
		}
		for k := range event.Picks {
			job := w.Jobs[int(event.FirstJob)+k]
			if job.Event != i || job.SubmittedAt != event.Origin {
				t.Fatalf("event %d's pick %d is job %d, which belongs to event %d at %v",
					i, k, int(event.FirstJob)+k, job.Event, job.SubmittedAt)
			}
		}
	}
}

func TestTheSameSeedScriptsTheSameEncounters(t *testing.T) {
	spec := domain.DefaultEncounters()
	if !reflect.DeepEqual(encounters(day(t, 4, &spec)), encounters(day(t, 4, &spec))) {
		t.Error("the same seed scripted different encounters")
	}
	if reflect.DeepEqual(encounters(day(t, 4, &spec)), encounters(day(t, 5, &spec))) {
		t.Error("two seeds scripted the same encounters")
	}
}

// An encounter is scripted at a level: the magnitude has to reach it, and how
// far it reaches is what the unit is timed against.
func TestAnEncounterTooSmallToReachTheLevelIsNotScripted(t *testing.T) {
	spec := domain.DefaultEncounters()
	// At Nuttli 0 very high ground motion would only be reached inside the
	// near field, where the law is capped: there is no zone to be timed
	// against, so there is nothing to script.
	spec.Magnitude, spec.Level = 0, "very-high"
	if got := encounters(day(t, 6, &spec)); len(got) != 0 {
		t.Errorf("%d encounters scripted from an event that reaches very high ground motion nowhere", len(got))
	}
}

func TestEncountersAreRefusedWhenTheyCouldNotBeScripted(t *testing.T) {
	for _, tc := range []struct {
		change func(*domain.EncounterSpec)
		want   string
	}{
		{func(s *domain.EncounterSpec) { s.Count = 0 }, "encounters.count"},
		{func(s *domain.EncounterSpec) { s.Lead = 0 }, "encounters.lead_seconds"},
		{func(s *domain.EncounterSpec) { s.Level = "catastrophic" }, "encounters.level"},
		{func(s *domain.EncounterSpec) { s.Kinds = []string{"drone"} }, "encounters.kinds"},
		{func(s *domain.EncounterSpec) { s.Magnitude = 12 }, "encounters.magnitude"},
	} {
		spec := domain.DefaultEncounters()
		tc.change(&spec)
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate() = %v, want it refused naming %s", err, tc.want)
		}
	}
}

// A day with encounters has their jobs too, so its rate is a little higher.
func TestAScriptedEncounterIsRealWorkLikeAnyOtherEvent(t *testing.T) {
	spec := domain.DefaultEncounters()
	plain, scripted := day(t, 7, nil), day(t, 7, &spec)
	added := len(scripted.Jobs) - len(plain.Jobs)
	if want := len(encounters(scripted)) * 18; math.Abs(float64(added-want)) > float64(want)/2 {
		t.Errorf("%d jobs added by %d encounters, want about %d — each is picked up by most of the array",
			added, len(encounters(scripted)), want)
	}
}
