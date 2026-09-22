package api

import (
	"context"
	"encoding/json"

	"github.com/casperlundberg/simlab-api/internal/autoscaler"
	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/intent"
	"github.com/casperlundberg/simlab-api/internal/run"
)

// What the HTTP interface asks of the rest of the service, declared here by
// the handlers that ask it. The Postgres store, the run manager and the
// autoscaler client are the implementations, wired in by internal/app; the
// handlers know them only as these.

// Mines are the mines runs are replayed in.
type Mines interface {
	Mines(ctx context.Context) ([]domain.Mine, error)
	Mine(ctx context.Context, id string) (domain.Mine, error)
	SaveMine(ctx context.Context, mine domain.Mine) error
	DeleteMine(ctx context.Context, id string) error
}

// Scenarios are the workloads replayed.
type Scenarios interface {
	Scenarios(ctx context.Context, mineID string) ([]domain.Scenario, error)
	Scenario(ctx context.Context, id string) (domain.Scenario, error)
	SaveScenario(ctx context.Context, scenario domain.Scenario) error
	DeleteScenario(ctx context.Context, id string) error
}

// Runs are the runs themselves: what each was asked to do.
type Runs interface {
	Runs(ctx context.Context, status domain.RunStatus, limit int) ([]domain.Run, error)
	Run(ctx context.Context, id string) (domain.Run, error)
	SaveRun(ctx context.Context, run domain.Run, settings json.RawMessage) error
	DeleteRun(ctx context.Context, id string) error
	RunSettings(ctx context.Context, id string) (json.RawMessage, error)
	RunIntent(ctx context.Context, runID string) (*domain.RunIntent, error)
	SaveRunIntent(ctx context.Context, runID string, intent domain.RunIntent) error
	IntentChanges(ctx context.Context, runID string) ([]domain.IntentChange, error)
	RunProvenance(ctx context.Context, runID string) (*domain.Provenance, error)
}

// Records are what a run recorded as it went.
type Records interface {
	Cycles(ctx context.Context, runID string, from, limit int) ([]domain.Cycle, error)
	Metrics(ctx context.Context, runID string) (domain.Metrics, error)
	RunLayout(ctx context.Context, runID string) (domain.Layout, error)
	SeismicEvents(ctx context.Context, runID string, from, limit int) ([]domain.SeismicEvent, error)
	Entities(ctx context.Context, runID string) ([]domain.Entity, error)
}

// Store is everything the handlers keep and read back, and whether it can
// be reached.
type Store interface {
	Mines
	Scenarios
	Runs
	Records
	Ping(ctx context.Context) error
}

// Manager is how runs are started, stopped and steered while in flight.
type Manager interface {
	Prepare(ctx context.Context, runID string) (run.Spec, error)
	Start(spec run.Spec) error
	Cancel(runID string) error
	Active() []string
	IsActive(runID string) bool
	Intent(runID string) (*intent.Control, bool)
}

// Autoscaler is the scaling controller, which the service stands in front of
// so the browser holds no credentials: its targets, their settings and status,
// and which build it is.
type Autoscaler interface {
	Version(ctx context.Context) (autoscaler.Build, error)
	Platforms(ctx context.Context) ([]autoscaler.PlatformSchema, error)
	ListTargets(ctx context.Context) ([]autoscaler.TargetSnapshot, error)
	GetTarget(ctx context.Context, id string) (autoscaler.TargetSnapshot, error)
	CreateTarget(ctx context.Context, target autoscaler.Target, settings json.RawMessage) (autoscaler.TargetSnapshot, error)
	DeleteTarget(ctx context.Context, id string) error
	GetSettings(ctx context.Context, id string) (autoscaler.SettingsSnapshot, error)
	ApplySettings(ctx context.Context, id string, patch json.RawMessage, expectedVersion *int64, actor string) (autoscaler.SettingsSnapshot, error)
	TargetStatus(ctx context.Context, id string) (autoscaler.Status, error)
}
