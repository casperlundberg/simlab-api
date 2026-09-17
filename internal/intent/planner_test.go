package intent_test

import (
	"math"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/hazard"
	"github.com/casperlundberg/simlab-api/internal/intent"
	"github.com/casperlundberg/simlab-api/internal/orchestrator"
	"github.com/casperlundberg/simlab-api/internal/seismic"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// A small mine with geometry that can be checked by hand: eight sensors at the
// corners of a box, one person standing in the middle of it, and three
// magnitude-1 events — one 20 m from the person, inside the high zone (25 m,
// or 75 m with the 50 m allowance an estimate gets); one 100 m away, inside the
// moderate zone (250 m, or 300 m) but outside the high one either way; and one
// about 600 m away, outside both.

var (
	person = domain.Point{X: 500, Y: 500, Z: -500}
	near   = domain.Point{X: 520, Y: 500, Z: -500}
	middle = domain.Point{X: 600, Y: 500, Z: -500}
	far    = domain.Point{X: 800, Y: 900, Z: -150}
)

const (
	nearEvent = iota
	middleEvent
	farEvent
)

func sensors() []domain.Sensor {
	var out []domain.Sensor
	for i, corner := range []domain.Point{
		{X: 100, Y: 100, Z: -900}, {X: 900, Y: 100, Z: -900}, {X: 100, Y: 900, Z: -900}, {X: 900, Y: 900, Z: -900},
		{X: 100, Y: 100, Z: -100}, {X: 900, Y: 100, Z: -100}, {X: 100, Y: 900, Z: -100}, {X: 900, Y: 900, Z: -100},
	} {
		out = append(out, domain.Sensor{ID: string(rune('a' + i)), At: corner})
	}
	return out
}

func standing(id, kind string, at domain.Point) domain.Entity {
	return domain.Entity{ID: id, Kind: kind, Track: []domain.Waypoint{{At: 0, Point: at}, {At: 24 * time.Hour, Point: at}}}
}

// mine builds the workload: every event is picked by all eight sensors, and
// every pick is a job at the given priority.
func mine(priority domain.Priority, entities ...domain.Entity) workload.Workload {
	layout := domain.Layout{
		Extent:  domain.Extent{Min: domain.Point{Z: -1000}, Max: domain.Point{X: 1000, Y: 1000}},
		Sensors: sensors(),
	}
	if entities == nil {
		entities = []domain.Entity{standing("person-01", domain.EntityPerson, person)}
	}
	w := workload.Workload{Layout: layout, Model: workload.Rock, Entities: entities}
	for i, at := range []domain.Point{near, middle, far} {
		origin := time.Duration(10*(i+1)) * time.Second
		picks := workload.Rock.Picks(seismic.Event{At: at, Origin: origin}, layout.Sensors, 0, nil)
		for k := range picks {
			picks[k].Magnitude = 1
		}
		event := workload.Event{Origin: origin, Truth: at, Magnitude: 1, Picks: picks, FirstJob: domain.JobID(len(w.Jobs))}
		for range picks {
			w.Jobs = append(w.Jobs, workload.Job{
				ID: domain.JobID(len(w.Jobs)), SubmittedAt: origin, Priority: priority, Seconds: 10, Event: i,
			})
		}
		w.Events = append(w.Events, event)
	}
	return w
}

// process reports the first n picks of an event finished, which locates it
// from four.
func process(t *testing.T, w workload.Workload, c *workload.Catalogue, at time.Duration, event, n int) {
	t.Helper()
	var ids []domain.JobID
	for k := 0; k < n; k++ {
		ids = append(ids, w.Events[event].FirstJob+domain.JobID(k))
	}
	if _, err := c.Observe(at, ids); err != nil {
		t.Fatalf("Observe() = %v", err)
	}
}

func settings(patch string) domain.IntentSettings {
	s, err := domain.DefaultIntent().Patched([]byte(patch))
	if err != nil {
		panic(err)
	}
	return s
}

// moved is each job an update moved, by id, and to where.
func moved(updates []orchestrator.PriorityUpdate) map[domain.JobID]orchestrator.PriorityUpdate {
	out := map[domain.JobID]orchestrator.PriorityUpdate{}
	for _, u := range updates {
		out[u.JobID] = u
	}
	return out
}

func jobsOf(w workload.Workload, event int) []domain.JobID {
	var out []domain.JobID
	for k := range w.Events[event].Picks {
		out = append(out, w.Events[event].FirstJob+domain.JobID(k))
	}
	return out
}

func stateOf(plan intent.Plan, event int) (domain.IntentTransition, bool) {
	for _, tr := range plan.Transitions {
		if tr.Event == event {
			return tr.IntentTransition, true
		}
	}
	return domain.IntentTransition{}, false
}

func TestTheHazardLevelsIntentNamesAreTheHazardModels(t *testing.T) {
	if len(domain.IntentLevels) != len(hazard.Levels) {
		t.Fatalf("intent names %v, hazard has %v", domain.IntentLevels, hazard.Levels)
	}
	for i, level := range hazard.Levels {
		if domain.IntentLevels[i] != level.String() {
			t.Errorf("intent level %d is %q, hazard's is %q", i, domain.IntentLevels[i], level)
		}
	}
}

func TestAnEventReachingNoProtectedPathIsDecayedOnceItIsLocated(t *testing.T) {
	w := mine(domain.PriorityAssociate)
	c := workload.NewCatalogue(w)
	p := intent.New(w, c, domain.DefaultIntent())

	if plan := p.Plan(40 * time.Second); len(plan.Updates) != 0 || len(plan.Transitions) != 0 {
		t.Fatalf("Plan() before any location = %+v, want nothing: there is nothing to judge from", plan)
	}

	for _, event := range []int{nearEvent, middleEvent, farEvent} {
		process(t, w, c, 50*time.Second, event, seismic.MinimumPicks)
	}
	plan := p.Plan(50 * time.Second)

	got := moved(plan.Updates)
	for k, id := range jobsOf(w, farEvent) {
		u, ok := got[id]
		switch {
		case k < seismic.MinimumPicks && ok:
			t.Errorf("job %d was already processed and was moved anyway", id)
		case k >= seismic.MinimumPicks && (!ok || u.Priority != domain.PriorityFloor):
			t.Errorf("job %d of the far event: update %+v, want it decayed to the floor", id, u)
		}
	}
	for _, event := range []int{nearEvent, middleEvent} {
		for _, id := range jobsOf(w, event) {
			if u, ok := got[id]; ok {
				t.Errorf("job %d of an event within reach of the person was moved: %+v", id, u)
			}
		}
	}
	if tr, ok := stateOf(plan, farEvent); !ok || tr.State != domain.EventDecayed || tr.Basis != intent.BasisLocation {
		t.Errorf("far event transition = %+v, want decayed from its location", tr)
	}
	if tr, ok := stateOf(plan, middleEvent); !ok || tr.State != domain.EventKept || tr.Entity != "person-01" {
		t.Errorf("middle event transition = %+v, want kept for person-01", tr)
	}
}

func TestAPlanRepeatedWithNothingChangedAsksForNothing(t *testing.T) {
	w := mine(domain.PriorityAssociate)
	c := workload.NewCatalogue(w)
	p := intent.New(w, c, domain.DefaultIntent())
	process(t, w, c, 50*time.Second, farEvent, seismic.MinimumPicks)
	p.Plan(50 * time.Second)

	if plan := p.Plan(65 * time.Second); len(plan.Updates) != 0 || len(plan.Transitions) != 0 {
		t.Errorf("second Plan() = %+v, want nothing: what is wanted has not changed", plan)
	}
}

// The mine's estimate is all a real installation has, so under it nothing may
// depend on where an event really was. Moving every event's truth must change
// nothing intent does.
func TestUnderEstimatesIntentNeverReadsTheTruth(t *testing.T) {
	plans := [2]intent.Plan{}
	for n := range plans {
		w := mine(domain.PriorityAssociate)
		if n == 1 {
			for i := range w.Events {
				w.Events[i].Truth = domain.Point{X: 1, Y: 1, Z: -999}
				w.Events[i].Magnitude = 3
			}
		}
		c := workload.NewCatalogue(w)
		// The picks were drawn from the original positions in both, so both
		// catalogues solve the same locations.
		p := intent.New(w, c, settings(`{"mode":"both","pre_location":true}`))
		p.Plan(35 * time.Second)
		for _, event := range []int{nearEvent, middleEvent, farEvent} {
			process(t, w, c, 50*time.Second, event, seismic.MinimumPicks)
		}
		plans[n] = p.Plan(50 * time.Second)
	}
	if len(plans[0].Updates) == 0 {
		t.Fatal("the plan moved nothing, so it cannot show that nothing it moved depended on the truth")
	}

	if len(plans[0].Updates) != len(plans[1].Updates) || len(plans[0].Transitions) != len(plans[1].Transitions) {
		t.Fatalf("plans differ with only the truth changed:\n%+v\n%+v", plans[0], plans[1])
	}
	for i := range plans[0].Updates {
		if plans[0].Updates[i] != plans[1].Updates[i] {
			t.Errorf("update %d differs with only the truth changed: %+v vs %+v", i, plans[0].Updates[i], plans[1].Updates[i])
		}
	}
}

// The oracle arm: the truth, from the moment an event happens, with no
// allowance for being wrong.
func TestUnderTruthIntentActsTheMomentAnEventHappens(t *testing.T) {
	w := mine(domain.PriorityAssociate)
	c := workload.NewCatalogue(w)
	p := intent.New(w, c, settings(`{"knowledge":"truth"}`))

	plan := p.Plan(30 * time.Second)

	if len(moved(plan.Updates)) != len(w.Events[farEvent].Picks) {
		t.Errorf("Plan() moved %d jobs, want every pick of the far event and nothing else: %+v",
			len(plan.Updates), plan.Updates)
	}
	if tr, _ := stateOf(plan, farEvent); tr.Basis != intent.BasisTruth {
		t.Errorf("far event transition = %+v, want judged from the truth", tr)
	}
}

func TestBeforeALocationTheFirstSensorToTriggerStandsInForOne(t *testing.T) {
	w := mine(domain.PriorityAssociate)
	c := workload.NewCatalogue(w)

	without := intent.New(w, c, domain.DefaultIntent()).Plan(30 * time.Second)
	with := intent.New(w, c, settings(`{"pre_location":true}`)).Plan(30 * time.Second)

	if len(without.Updates) != 0 {
		t.Errorf("without pre-location, Plan() = %+v before any pick is processed", without.Updates)
	}
	tr, ok := stateOf(with, farEvent)
	if !ok || tr.Basis != intent.BasisSensors {
		t.Fatalf("far event transition = %+v, want one judged from its sensors", tr)
	}
	// Eight sensors 800 m apart say very little about where an event is: the
	// fourth to trigger is 800 m from the first, and that is the allowance.
	// Intent that cannot rule a place out keeps what is there.
	first, fourth := domain.Point{X: 900, Y: 900, Z: -100}, domain.Point{X: 900, Y: 100, Z: -100}
	zone := hazard.Default.Zones(1.5, first.DistanceTo(fourth))[hazard.Moderate]
	if tr.State != domain.EventKept || math.Abs(tr.Reach-zone) > 1e-6 {
		t.Errorf("far event = %+v, want kept, with a reach of %v from the sensor spread", tr, zone)
	}
}

func TestSomethingTravellingTowardsAnEventProtectsIt(t *testing.T) {
	// A vehicle leaving the far event's corner of the mine now, and reaching
	// the person in the middle four minutes later.
	vehicle := domain.Entity{ID: "crewed-vehicle-01", Kind: domain.EntityCrewedVehicle, Track: []domain.Waypoint{
		{At: 0, Point: person}, {At: 50 * time.Second, Point: person},
		{At: 290 * time.Second, Point: domain.Point{X: 800, Y: 880, Z: -150}},
		{At: 24 * time.Hour, Point: domain.Point{X: 800, Y: 880, Z: -150}},
	}}

	for _, tc := range []struct {
		lookahead string
		want      string
	}{
		{`{"knowledge":"truth","lookahead_seconds":0}`, domain.EventDecayed},
		{`{"knowledge":"truth","lookahead_seconds":300}`, domain.EventKept},
	} {
		w := mine(domain.PriorityAssociate, vehicle)
		p := intent.New(w, workload.NewCatalogue(w), settings(tc.lookahead))
		tr, _ := stateOf(p.Plan(50*time.Second), farEvent)
		if tr.State != tc.want {
			t.Errorf("%s: far event = %+v, want %s", tc.lookahead, tr, tc.want)
		}
	}
}

func TestDecayedWorkIsRestoredWhenSomethingComesWithinReach(t *testing.T) {
	walker := domain.Entity{ID: "person-01", Kind: domain.EntityPerson, Track: []domain.Waypoint{
		{At: 0, Point: person}, {At: 60 * time.Second, Point: person},
		{At: 120 * time.Second, Point: far}, {At: 24 * time.Hour, Point: far},
	}}
	w := mine(domain.PriorityAssociate, walker)
	p := intent.New(w, workload.NewCatalogue(w), settings(`{"knowledge":"truth","lookahead_seconds":0}`))

	if tr, _ := stateOf(p.Plan(30*time.Second), farEvent); tr.State != domain.EventDecayed {
		t.Fatalf("far event at 30s = %+v, want decayed", tr)
	}
	plan := p.Plan(120 * time.Second)

	if tr, _ := stateOf(plan, farEvent); tr.State != domain.EventKept {
		t.Errorf("far event once the person is there = %+v, want kept", tr)
	}
	for _, id := range jobsOf(w, farEvent) {
		if u := moved(plan.Updates)[id]; u.Priority != domain.PriorityAssociate {
			t.Errorf("job %d = %+v, want restored to the priority it was submitted with", id, u)
		}
	}
}

func TestBothPromotesWhatPutsSomeoneAtHighRiskAndDecaysWhatReachesNoOne(t *testing.T) {
	w := mine(domain.PriorityLocate)
	p := intent.New(w, workload.NewCatalogue(w), settings(`{"mode":"both","knowledge":"truth"}`))

	plan := p.Plan(30 * time.Second)

	for event, want := range map[int]domain.Priority{
		nearEvent: domain.PriorityRelocate, middleEvent: domain.PriorityLocate, farEvent: domain.PriorityFloor,
	} {
		for _, id := range jobsOf(w, event) {
			got := domain.PriorityLocate
			if u, ok := moved(plan.Updates)[id]; ok {
				got = u.Priority
			}
			if got != want {
				t.Errorf("event %d job %d at %d, want %d", event, id, got, want)
			}
		}
	}
	if tr, _ := stateOf(plan, nearEvent); tr.State != domain.EventPromoted || math.Abs(tr.Distance-20) > 1e-9 {
		t.Errorf("near event = %+v, want promoted at 20 m", tr)
	}
}

func TestDecayNeverRaisesAJobAndPromotionNeverLowersOne(t *testing.T) {
	for _, tc := range []struct {
		submitted domain.Priority
		patch     string
		moves     int
	}{
		// Already above promote_to: the near event's work stays; the far
		// event's decays.
		{domain.PriorityRelocate, `{"mode":"both","knowledge":"truth","promote_to":100}`, farEvent},
		// Already below decay_to: the far event's work stays; the near
		// event's is promoted.
		{domain.PriorityPick, `{"mode":"both","knowledge":"truth","decay_to":50,"promote_to":100}`, nearEvent},
	} {
		w := mine(tc.submitted)
		plan := intent.New(w, workload.NewCatalogue(w), settings(tc.patch)).Plan(30 * time.Second)
		for _, u := range plan.Updates {
			if w.Jobs[u.JobID].Event != tc.moves {
				t.Errorf("%s: moved job %d of event %d to %d", tc.patch, u.JobID, w.Jobs[u.JobID].Event, u.Priority)
			}
		}
		if len(plan.Updates) != len(w.Events[tc.moves].Picks) {
			t.Errorf("%s: %d updates, want event %d's", tc.patch, len(plan.Updates), tc.moves)
		}
	}
}

func TestOnlyProtectedKindsProtect(t *testing.T) {
	w := mine(domain.PriorityAssociate, standing("autonomous-vehicle-01", domain.EntityAutonomousVehicle, person))
	p := intent.New(w, workload.NewCatalogue(w), settings(`{"knowledge":"truth","protect":["person"]}`))

	plan := p.Plan(30 * time.Second)

	if tr, _ := stateOf(plan, nearEvent); tr.State != domain.EventDecayed || tr.Entity != "" {
		t.Errorf("near event = %+v, want decayed: the only thing near it is not protected", tr)
	}
}

func TestUpdatesCarryExemptionAndTheDeadlineOrigin(t *testing.T) {
	w := mine(domain.PriorityLocate)
	p := intent.New(w, workload.NewCatalogue(w), settings(
		`{"mode":"both","knowledge":"truth","burst_exempt":["decayed"],"deadline_from":"change"}`))

	for _, u := range p.Plan(30 * time.Second).Updates {
		decayed := u.Priority == domain.PriorityFloor
		if u.BurstExempt != decayed {
			t.Errorf("update %+v: exempt = %v, want exemption for decayed work only", u, u.BurstExempt)
		}
		if !u.RestartDeadline {
			t.Errorf("update %+v does not restart the deadline", u)
		}
	}
}

// An operator can change exemption without changing anything's priority, and
// the queue has to hear about it.
func TestChangingOnlyExemptionUpdatesTheJobsItApplesTo(t *testing.T) {
	w := mine(domain.PriorityAssociate)
	p := intent.New(w, workload.NewCatalogue(w), settings(`{"knowledge":"truth"}`))
	p.Plan(30 * time.Second)

	p.Configure(settings(`{"knowledge":"truth","burst_exempt":["decayed"]}`))
	plan := p.Plan(45 * time.Second)

	if len(plan.Updates) != len(w.Events[farEvent].Picks) {
		t.Fatalf("Plan() = %d updates, want the far event's %d", len(plan.Updates), len(w.Events[farEvent].Picks))
	}
	for _, u := range plan.Updates {
		if !u.BurstExempt || u.Priority != domain.PriorityFloor {
			t.Errorf("update %+v, want the same decay, now exempt", u)
		}
	}
	if len(plan.Transitions) != 0 {
		t.Errorf("Transitions = %+v, want none: no event was judged differently", plan.Transitions)
	}
}

func TestSwitchingIntentOffPutsEverythingBack(t *testing.T) {
	w := mine(domain.PriorityAssociate)
	p := intent.New(w, workload.NewCatalogue(w), settings(`{"mode":"both","knowledge":"truth"}`))
	p.Plan(30 * time.Second)

	p.Configure(settings(`{"mode":"off"}`))
	plan := p.Plan(45 * time.Second)

	for _, u := range plan.Updates {
		if u.Priority != domain.PriorityAssociate || u.BurstExempt {
			t.Errorf("update %+v, want every job back as submitted", u)
		}
	}
	if len(plan.Updates) != len(w.Events[nearEvent].Picks)+len(w.Events[farEvent].Picks) {
		t.Errorf("%d updates, want every moved job restored", len(plan.Updates))
	}
	for _, event := range []int{nearEvent, farEvent} {
		if tr, ok := stateOf(plan, event); !ok || tr.State != domain.EventKept {
			t.Errorf("event %d = %+v, want a record of being let go", event, tr)
		}
	}
	if again := p.Plan(60 * time.Second); len(again.Updates) != 0 || len(again.Transitions) != 0 {
		t.Errorf("Plan() after restoring = %+v, want nothing", again)
	}
}

func TestIntentThatStartsOffRecordsNothing(t *testing.T) {
	w := mine(domain.PriorityAssociate)
	p := intent.New(w, workload.NewCatalogue(w), domain.IntentOffSettings())

	if plan := p.Plan(40 * time.Second); len(plan.Updates) != 0 || len(plan.Transitions) != 0 {
		t.Errorf("Plan() = %+v, want nothing at all with intent off", plan)
	}
}

func TestAPathIsWhereAnEntityIsAndWhereItIsGoing(t *testing.T) {
	entity := domain.Entity{ID: "crewed-vehicle-01", Kind: domain.EntityCrewedVehicle, Track: []domain.Waypoint{
		{At: 0, Point: domain.Point{}},
		{At: 100 * time.Second, Point: domain.Point{X: 100}},
		{At: 200 * time.Second, Point: domain.Point{X: 100, Y: 100}},
	}}
	s := settings(`{"lookahead_seconds":100}`)

	paths := intent.Paths([]domain.Entity{entity}, 50*time.Second, s)

	if len(paths) != 1 {
		t.Fatalf("Paths() = %+v, want one", paths)
	}
	want := []domain.Point{{X: 50}, {X: 100}, {X: 100, Y: 50}}
	if len(paths[0].Points) != len(want) {
		t.Fatalf("Points = %+v, want %+v", paths[0].Points, want)
	}
	for i := range want {
		if paths[0].Points[i] != want[i] {
			t.Errorf("Points[%d] = %+v, want %+v", i, paths[0].Points[i], want[i])
		}
	}
	// Level with the middle of the second leg, 30 m to the side of it.
	if d := paths[0].DistanceTo(domain.Point{X: 130, Y: 25}); math.Abs(d-30) > 1e-9 {
		t.Errorf("DistanceTo() = %v, want 30", d)
	}
	// Above the route, in three dimensions.
	if d := paths[0].DistanceTo(domain.Point{X: 80, Z: 40}); math.Abs(d-40) > 1e-9 {
		t.Errorf("DistanceTo() = %v, want 40", d)
	}
}

// Restored work comes back to its submitted level having waited all the while,
// and exempting it is how an operator keeps a restore from buying cloud.
func TestRestoredWorkCanBeExempt(t *testing.T) {
	walker := domain.Entity{ID: "person-01", Kind: domain.EntityPerson, Track: []domain.Waypoint{
		{At: 0, Point: person}, {At: 60 * time.Second, Point: person},
		{At: 120 * time.Second, Point: far}, {At: 24 * time.Hour, Point: far},
	}}
	w := mine(domain.PriorityAssociate, walker)
	p := intent.New(w, workload.NewCatalogue(w),
		settings(`{"knowledge":"truth","lookahead_seconds":0,"burst_exempt":["restored"]}`))

	for _, u := range p.Plan(30 * time.Second).Updates {
		if u.BurstExempt {
			t.Errorf("decayed job %d is exempt, but only restored work was named", u.JobID)
		}
	}
	restored := moved(p.Plan(120 * time.Second).Updates)
	for _, id := range jobsOf(w, farEvent) {
		if u, ok := restored[id]; !ok || u.Priority != domain.PriorityAssociate || !u.BurstExempt {
			t.Errorf("job %d: %+v, want restored and exempt", id, u)
		}
	}
}

// Without restore, decay is final: a pure relaxation.
func TestWithoutRestoreDecayIsFinal(t *testing.T) {
	walker := domain.Entity{ID: "person-01", Kind: domain.EntityPerson, Track: []domain.Waypoint{
		{At: 0, Point: person}, {At: 60 * time.Second, Point: person},
		{At: 120 * time.Second, Point: far}, {At: 24 * time.Hour, Point: far},
	}}
	w := mine(domain.PriorityAssociate, walker)
	p := intent.New(w, workload.NewCatalogue(w), settings(`{"knowledge":"truth","lookahead_seconds":0,"restore":false}`))
	p.Plan(30 * time.Second)

	plan := p.Plan(120 * time.Second)

	for _, id := range jobsOf(w, farEvent) {
		if u, ok := moved(plan.Updates)[id]; ok {
			t.Errorf("job %d moved to %+v once the person was there, want the decay to stand", id, u)
		}
	}
	if tr, _ := stateOf(plan, farEvent); tr.State != domain.EventKept {
		t.Errorf("far event = %+v: the judgement still changes, and is recorded, even when the work does not", tr)
	}

	p.Configure(settings(`{"mode":"off","restore":false}`))
	off := moved(p.Plan(135 * time.Second).Updates)
	if len(off) != len(w.Events[farEvent].Picks) {
		t.Errorf("switching intent off moved %d jobs, want the far event's %d decayed ones restored",
			len(off), len(w.Events[farEvent].Picks))
	}
	for id, u := range off {
		if u.Priority != domain.PriorityAssociate {
			t.Errorf("job %d = %+v, want restored", id, u)
		}
	}
}

// An event that shook someone matters after they have walked away: where they
// were when it happened is where to look for them, and the event's location
// is what an operator needs to do it.
func TestSomeoneAnEventReachedWhenItHappenedStillProtectsItAfterLeaving(t *testing.T) {
	leaver := domain.Entity{ID: "person-01", Kind: domain.EntityPerson, Track: []domain.Waypoint{
		{At: 0, Point: far}, {At: 40 * time.Second, Point: far},
		{At: 100 * time.Second, Point: person}, {At: 24 * time.Hour, Point: person},
	}}
	w := mine(domain.PriorityAssociate, leaver)
	p := intent.New(w, workload.NewCatalogue(w), settings(`{"knowledge":"truth","lookahead_seconds":0}`))

	if tr, _ := stateOf(p.Plan(30*time.Second), farEvent); tr.State != domain.EventKept {
		t.Fatalf("far event while the person stands on it = %+v, want kept", tr)
	}
	plan := p.Plan(120 * time.Second)

	for _, id := range jobsOf(w, farEvent) {
		if u, ok := moved(plan.Updates)[id]; ok {
			t.Errorf("job %d moved to %+v once the person had left, want it kept", id, u)
		}
	}
	if tr, ok := stateOf(plan, farEvent); ok {
		t.Errorf("far event judged afresh as %+v, want its judgement unchanged", tr)
	}
}

// Nor does it protect an event that happened before they got there.
func TestSomeoneWhoArrivesAndLeavesAgainDoesNotProtectWhatNobodyIsHeadingFor(t *testing.T) {
	visitor := domain.Entity{ID: "person-01", Kind: domain.EntityPerson, Track: []domain.Waypoint{
		{At: 0, Point: person}, {At: 50 * time.Second, Point: person},
		{At: 110 * time.Second, Point: far}, {At: 130 * time.Second, Point: far},
		{At: 190 * time.Second, Point: person}, {At: 24 * time.Hour, Point: person},
	}}
	w := mine(domain.PriorityAssociate, visitor)
	p := intent.New(w, workload.NewCatalogue(w), settings(`{"knowledge":"truth","lookahead_seconds":0}`))
	p.Plan(35 * time.Second)
	p.Plan(120 * time.Second)

	// The visit came after the event: the ground motion was over by then, so
	// it exposed nobody, and once they have gone nothing protects its work.
	if tr, _ := stateOf(p.Plan(300*time.Second), farEvent); tr.State != domain.EventDecayed {
		t.Errorf("far event after the visit = %+v, want decayed again", tr)
	}
}
