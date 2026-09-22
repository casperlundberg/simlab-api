package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func TestTheDefaultActivityIsUsable(t *testing.T) {
	if err := domain.DefaultActivity().Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestAnActivityThatCouldNotBeReplayedIsRefusedNamingTheField(t *testing.T) {
	for field, change := range map[string]func(*domain.ActivitySpec){
		"activity.areas":                    func(a *domain.ActivitySpec) { a.Areas = 0 },
		"activity.rotate_seconds":           func(a *domain.ActivitySpec) { a.Rotate = 0 },
		"activity.mix":                      func(a *domain.ActivitySpec) { a.Mix = domain.ActivityMix{} },
		"activity.blasting.start_seconds":   func(a *domain.ActivitySpec) { a.Blasting.Start = 25 * time.Hour },
		"activity.blasting.every_seconds":   func(a *domain.ActivitySpec) { a.Blasting.Every = 0 },
		"activity.blasting.omori_p":         func(a *domain.ActivitySpec) { a.Blasting.OmoriP = 0 },
		"activity.blasting.omori_c_seconds": func(a *domain.ActivitySpec) { a.Blasting.OmoriC = 0 },
		"activity.blasting.length_seconds":  func(a *domain.ActivitySpec) { a.Blasting.Length = 0 },
		"activity.spread_m":                 func(a *domain.ActivitySpec) { a.Spread = 0 },
	} {
		a := domain.DefaultActivity()
		change(&a)
		if err := a.Validate(); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: Validate() = %v; want it named", field, err)
		}
	}
}

func TestOneBlastADayNeedsNoInterval(t *testing.T) {
	a := domain.DefaultActivity()
	a.Blasting.Window, a.Blasting.Every = 0, 0
	if err := a.Validate(); err != nil {
		t.Errorf("Validate() = %v; a window of zero is one blast a day", err)
	}
}

func TestAnActivityCrossesTheWireInSecondsAndComesBackTheSame(t *testing.T) {
	s := domain.Scenario{ID: "s", MineID: "m", Name: "n", Duration: time.Hour, JobSeconds: 20,
		PriorityMix: map[domain.Priority]float64{100: 1}}
	a := domain.DefaultActivity()
	s.Activity = &a
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	for _, field := range []string{`"rotate_seconds":43200`, `"start_seconds":4500`, `"every_seconds":600`,
		`"omori_c_seconds":300`, `"length_seconds":43200`, `"spread_m":75`, `"areas":4`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("the wire form %s does not carry %s", encoded, field)
		}
	}
	var back domain.Scenario
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if back.Activity == nil || *back.Activity != a {
		t.Errorf("activity came back %+v, want %+v", back.Activity, a)
	}
}

func TestAScenarioWithAnUnusableActivityIsRefused(t *testing.T) {
	s := domain.Scenario{ID: "s", MineID: "m", Name: "n", Duration: time.Hour, JobSeconds: 20,
		PriorityMix: map[domain.Priority]float64{100: 1}}
	a := domain.DefaultActivity()
	a.Areas = 0
	s.Activity = &a
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "activity.areas") {
		t.Errorf("Validate() = %v; want the activity's problem named", err)
	}
}

// Shares mean something only together: a mix that names the blast share alone
// is all blasts, not a blast share added to the default's others.
func TestAMixGivenReplacesTheDefaultsShares(t *testing.T) {
	var a domain.ActivitySpec
	if err := json.Unmarshal([]byte(`{"mix":{"blast":1},"blasting":{"start_seconds":50400}}`), &a); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if a.Mix != (domain.ActivityMix{Blast: 1}) {
		t.Errorf("mix = %+v, want blasts alone", a.Mix)
	}
	if a.Blasting.Start != 14*time.Hour || a.Blasting.Every != 10*time.Minute || a.Areas != 4 {
		t.Errorf("activity = %+v; a start alone should keep the rest of the defaults", a)
	}
}
