package domain_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func TestTheDefaultIntentDecaysOnlyAndIsValid(t *testing.T) {
	s := domain.DefaultIntent()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if s.Mode != domain.IntentDecay || s.Mode.Promotes() || !s.Mode.Decays() {
		t.Errorf("Mode = %q, want decay only", s.Mode)
	}
	if s.Knowledge != domain.KnowledgeEstimate {
		t.Errorf("Knowledge = %q, want the mine's estimates, never the truth, by default", s.Knowledge)
	}
	for _, kind := range []string{domain.EntityPerson, domain.EntityCrewedVehicle, domain.EntityAutonomousVehicle} {
		if !s.Protects(kind) {
			t.Errorf("the default does not protect %s", kind)
		}
	}
	// Restored work comes back already late; counted, each restore would be
	// a breach no capacity avoids, and the scaling decay was meant not to
	// cause.
	if !reflect.DeepEqual(s.BurstExempt, []domain.IntentClass{domain.ClassRestored}) {
		t.Errorf("BurstExempt = %v, want restored work exempt and nothing else", s.BurstExempt)
	}
}

func TestAnIntentPatchChangesOnlyWhatItNames(t *testing.T) {
	base := domain.DefaultIntent()

	got, err := base.Patched([]byte(`{"mode":"both","lookahead_seconds":90,"burst_exempt":["promoted"]}`))
	if err != nil {
		t.Fatalf("Patched() = %v", err)
	}

	want := domain.DefaultIntent()
	want.Mode = domain.IntentBoth
	want.Lookahead = 90 * time.Second
	want.BurstExempt = []domain.IntentClass{domain.ClassPromoted}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Patched() = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(base, domain.DefaultIntent()) {
		t.Errorf("the receiver changed to %+v", base)
	}
}

// A list in a patch is the new list. Merging would make removing a protected
// kind impossible.
func TestAListInAnIntentPatchReplacesTheList(t *testing.T) {
	got, err := domain.DefaultIntent().Patched([]byte(`{"protect":["person"]}`))
	if err != nil {
		t.Fatalf("Patched() = %v", err)
	}
	if !reflect.DeepEqual(got.Protect, []string{"person"}) {
		t.Errorf("Protect = %v, want only people", got.Protect)
	}
}

func TestAnIntentPatchThatIsRefusedChangesNothing(t *testing.T) {
	base := domain.DefaultIntent()
	for _, patch := range []string{
		`{"mode":"sideways"}`,
		`{"lookahead":300}`,
		`{"protect":["drone"]}`,
		`{"burst_exempt":["kept"]}`,
		`{"protect_level":"severe"}`,
		`{"decay_to":400,"promote_to":100}`,
		`{"deadline_from":"yesterday"}`,
		`{"lookahead_seconds":-1}`,
	} {
		got, err := base.Patched([]byte(patch))
		if err == nil {
			t.Errorf("Patched(%s) accepted, want it refused", patch)
		}
		if !reflect.DeepEqual(got, base) {
			t.Errorf("Patched(%s) returned %+v, want the settings unchanged", patch, got)
		}
	}
}

func TestIntentValidationNamesTheField(t *testing.T) {
	s := domain.DefaultIntent()
	s.Mode = "sideways"
	s.Margin = -3
	err := s.Validate()
	if err == nil {
		t.Fatal("Validate() = nil")
	}
	for _, want := range []string{`mode "sideways"`, "margin_m"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() = %q, want it to mention %s", err, want)
		}
	}
}

func TestIntentSettingsRoundTripWithTheLookaheadInSeconds(t *testing.T) {
	original := domain.DefaultIntent()
	original.Mode = domain.IntentPromote
	original.PreLocation = true
	original.BurstExempt = []domain.IntentClass{domain.ClassDecayed, domain.ClassPromoted}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if !strings.Contains(string(encoded), `"lookahead_seconds":300`) {
		t.Errorf("Marshal() = %s, want lookahead_seconds", encoded)
	}
	var back domain.IntentSettings
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if !reflect.DeepEqual(back, original) {
		t.Errorf("round trip = %+v, want %+v", back, original)
	}
}

func TestAnIntentTransitionRoundTripsInSeconds(t *testing.T) {
	original := domain.IntentTransition{
		At: 95 * time.Second, State: domain.EventDecayed, Basis: "location",
		Entity: "person-03", Distance: 412.5, Reach: 129,
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if !strings.Contains(string(encoded), `"at_seconds":95`) {
		t.Errorf("Marshal() = %s, want at_seconds", encoded)
	}
	var back domain.IntentTransition
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if back != original {
		t.Errorf("round trip = %+v, want %+v", back, original)
	}
}
