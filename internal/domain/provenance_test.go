package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func clean(version string) domain.Build {
	return domain.Build{Version: version, Commit: "9caaa12b934d2f03af94e79341e72d08bf230cf3", GoVersion: "go1.24.1"}
}

// A run can be rebuilt from its commits only if both services were built from
// commits, with nothing else in the tree. Anything less has to say so, or a
// result would claim a reproducibility it does not have.
func TestAProvenanceIsReproducibleOnlyFromCleanCommitsOfBothServices(t *testing.T) {
	scenario := validScenario()
	mine := validMine()
	autoscaler := clean("1.0.0")
	whole := domain.Provenance{
		SimlabAPI: clean("1.2.0"), Autoscaler: &autoscaler, Mine: &mine, Scenario: &scenario,
		Settings: json.RawMessage(`{"local_executor_cap":20}`),
	}
	if reasons := whole.NotReproducible(); len(reasons) != 0 {
		t.Errorf("a clean provenance is not reproducible: %v", reasons)
	}

	for name, broken := range map[string]func(*domain.Provenance){
		"simlab-api": func(p *domain.Provenance) { p.SimlabAPI.Modified = true },
		"autoscaler": func(p *domain.Provenance) { p.Autoscaler = nil },
		"commit":     func(p *domain.Provenance) { p.SimlabAPI.Commit = "" },
		"scenario":   func(p *domain.Provenance) { p.Scenario = nil },
		"settings":   func(p *domain.Provenance) { p.Settings = nil },
	} {
		p := whole
		a := *whole.Autoscaler
		p.Autoscaler = &a
		broken(&p)
		reasons := p.NotReproducible()
		if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "; "), name) {
			t.Errorf("breaking %s: reasons %v, want it named", name, reasons)
		}
	}
}

// The scenario inside a provenance is replayed from, so it has to come back
// exactly: seconds on the wire, like everywhere else.
func TestAProvenanceRoundTripsWithItsSnapshots(t *testing.T) {
	scenario := validScenario()
	scenario.PickJitter = 3 * time.Millisecond
	mine := validMine()
	original := domain.Provenance{
		RecordedAt: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
		SimlabAPI:  clean("1.2.0"), Mine: &mine, Scenario: &scenario,
		Settings: json.RawMessage(`{"cloud_executor_cap":40}`), SettingsVersion: 2,
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	for _, want := range []string{`"simlab_api":{"version":"1.2.0"`, `"autoscaler":null`, `"duration_seconds":21600`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("%s is missing %s", encoded, want)
		}
	}

	var back domain.Provenance
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if back.Scenario.PickJitter != scenario.PickJitter || back.Scenario.Seed != scenario.Seed ||
		back.Mine.Sensors != mine.Sensors || back.SettingsVersion != 2 || back.SimlabAPI.Commit != original.SimlabAPI.Commit {
		t.Errorf("round trip = %+v", back)
	}
}
