package domain_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func TestTheDefaultEncountersAreUsable(t *testing.T) {
	if err := domain.DefaultEncounters().Validate(); err != nil {
		t.Errorf("Validate() = %v; the defaults must be a scenario that runs", err)
	}
}

func TestEncountersCrossTheWireInSecondsAndComeBackTheSame(t *testing.T) {
	s := domain.Scenario{ID: "s", MineID: "m", Name: "n", Duration: time.Hour, JobSeconds: 20,
		PriorityMix: map[domain.Priority]float64{100: 1}}
	e := domain.DefaultEncounters()
	s.Encounters = &e
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	for _, field := range []string{`"count":20`, `"magnitude":2.5`, `"lead_seconds":120`, `"level":"high"`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("the wire form %s does not carry %s", encoded, field)
		}
	}
	var back domain.Scenario
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if back.Encounters == nil || !reflect.DeepEqual(*back.Encounters, e) {
		t.Errorf("encounters came back %+v, want %+v", back.Encounters, e)
	}
}

// What a request leaves out takes the default, so a sweep can ask for one lead
// and mean everything else as it was; kinds given replaces the list whole,
// because whom encounters are for is a choice rather than an addition.
func TestEncountersTakeTheDefaultsTheyDoNotState(t *testing.T) {
	var got domain.EncounterSpec
	if err := json.Unmarshal([]byte(`{"lead_seconds": 45, "kinds": ["person"]}`), &got); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	want := domain.DefaultEncounters()
	want.Lead, want.Kinds = 45*time.Second, []string{"person"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read %+v, want %+v", got, want)
	}
}

func TestAScenarioWithEncountersItCouldNotScriptIsRefused(t *testing.T) {
	s := domain.Scenario{ID: "s", MineID: "m", Name: "n", Duration: time.Hour, JobSeconds: 20,
		PriorityMix: map[domain.Priority]float64{100: 1}}
	e := domain.DefaultEncounters()
	e.Lead = 0
	s.Encounters = &e
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "encounters.lead_seconds") {
		t.Errorf("Validate() = %v, want the scenario refused naming the field", err)
	}
}
