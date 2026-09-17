package domain

import (
	"encoding/json"
	"time"
)

// Build identifies the code a service was built from.
type Build struct {
	// Version is the semantic version: a release such as 1.3.0, or a
	// development build such as 1.3.1-dev.2+abc1234. Empty when the build was
	// not stamped.
	Version string `json:"version"`

	// Commit is the full hash built from. Empty when unknown.
	Commit string `json:"commit"`

	// Modified is whether the tree had changes the commit does not contain.
	Modified bool `json:"modified"`

	GoVersion string `json:"go_version"`

	// Platform is GOOS/GOARCH. Floating point can round differently across
	// architectures, so a replay that has to match to the bit has to know.
	Platform string `json:"platform"`
}

// Provenance is what a run was produced by and from: enough to rebuild the
// code and replay the run, and to say when that is not possible.
//
// Recorded as the run begins. The run row names its scenario by id, but a
// scenario can be edited or deleted after it was replayed, and the defaults a
// settings patch fills in change between autoscaler versions — so the inputs
// are copied here as they were, not referenced.
type Provenance struct {
	RecordedAt time.Time `json:"recorded_at"`

	// SimlabAPI is this service: the workload generator, the queue, the mine.
	SimlabAPI Build `json:"simlab_api"`

	// Autoscaler is the service that made every scaling decision. Nil when it
	// could not say, which an autoscaler built before /v1/version cannot.
	Autoscaler *Build `json:"autoscaler"`

	// Mine and Scenario are what a simulation replayed, as they were. Nil for
	// a live run, which replays nothing.
	Mine     *Mine     `json:"mine,omitempty"`
	Scenario *Scenario `json:"scenario,omitempty"`

	// Settings is the target's whole effective settings as the run began —
	// the patch the run asked for, applied over that autoscaler's defaults —
	// and SettingsVersion the version they were at.
	Settings        json.RawMessage `json:"settings,omitempty"`
	SettingsVersion int64           `json:"settings_version,omitempty"`

	// Intent is how the mine was to reorder its work, as the run began. Nil
	// for a run recorded before intent existed, which reordered nothing.
	// Changes made while it ran are recorded as they take effect, and
	// replaying them is part of reproducing it.
	Intent *RunIntent `json:"intent,omitempty"`
}

// NotReproducible is every reason a run cannot be rebuilt and replayed from
// its provenance alone. Empty means it can be.
func (p Provenance) NotReproducible() []string {
	var reasons []string
	for name, build := range map[string]*Build{"simlab-api": &p.SimlabAPI, "autoscaler": p.Autoscaler} {
		switch {
		case build == nil:
			reasons = append(reasons, name+" did not report its build")
		case build.Commit == "":
			reasons = append(reasons, name+" was built from no known commit")
		case build.Modified:
			reasons = append(reasons, name+" was built with changes its commit does not contain")
		}
	}
	if p.Scenario == nil || p.Mine == nil {
		reasons = append(reasons, "no scenario and mine were recorded to replay")
	}
	if len(p.Settings) == 0 {
		reasons = append(reasons, "no settings were recorded to replay under")
	}
	// Map order is random; the reasons are read by people and compared by
	// tests, so they come out in one order.
	sortStrings(reasons)
	return reasons
}

// BuiltWith is which builds produced a run, for telling runs apart by version
// without reading every provenance.
type BuiltWith struct {
	SimlabAPI  Build  `json:"simlab_api"`
	Autoscaler *Build `json:"autoscaler"`
}
