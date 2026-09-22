// Package runner owns runs that are in flight: starting them, stopping them,
// and knowing which are still going.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/intent"
	"github.com/casperlundberg/simlab-api/internal/run"
)

// Store is what the manager reads to turn a run's id into what to execute:
// the run, its mine and scenario, and the settings and intent it was created
// with. The Postgres store is the implementation, wired in by internal/app.
type Store interface {
	Run(ctx context.Context, id string) (domain.Run, error)
	Mine(ctx context.Context, id string) (domain.Mine, error)
	Scenario(ctx context.Context, id string) (domain.Scenario, error)
	RunSettings(ctx context.Context, id string) (json.RawMessage, error)
	RunIntent(ctx context.Context, runID string) (*domain.RunIntent, error)
}

// ErrAlreadyRunning is returned when a run is asked to start twice.
var ErrAlreadyRunning = errors.New("this run is already in flight")

// Executor is the thing a manager runs in the background. *run.Engine is the
// one the service uses.
//
// It is an interface rather than the concrete engine because the manager's own
// behaviour is worth checking on its own: what counts as in flight, what a
// second Start does, when a run stops being active, whether Shutdown really
// waits. Answering those through a real engine means an autoscaler, a
// database and a replayed workload, and then the thing under test is the
// slowest part of the answer.
type Executor interface {
	Execute(ctx context.Context, spec run.Spec) (domain.Metrics, error)
}

// Manager starts runs and keeps track of them.
type Manager struct {
	store  Store
	engine Executor
	log    *slog.Logger

	mu     sync.Mutex
	active map[string]context.CancelFunc

	// done carries one channel per in-flight run, closed when it unwinds. See
	// Done for why the event stream cannot be used for this.
	done map[string]chan struct{}

	// intents is how an in-flight simulation's intent is changed while it
	// runs.
	intents map[string]*intent.Control

	// finished lets Shutdown wait for in-flight runs.
	wg sync.WaitGroup
}

// New builds a manager.
func New(store Store, engine Executor, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store: store, engine: engine, log: log,
		active:  map[string]context.CancelFunc{},
		done:    map[string]chan struct{}{},
		intents: map[string]*intent.Control{},
	}
}

// Start executes a run in the background and returns as soon as it is under
// way.
//
// Background because a run takes minutes and an HTTP request should not. The
// context deliberately does not come from the request: a run must survive the
// browser tab that started it being closed, which is the normal way somebody
// launches one and then goes to look at something else.
func (m *Manager) Start(spec run.Spec) error {
	m.mu.Lock()
	if _, running := m.active[spec.Run.ID]; running {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, spec.Run.ID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.active[spec.Run.ID] = cancel
	m.done[spec.Run.ID] = make(chan struct{})
	if spec.Intent != nil {
		if spec.Control == nil {
			spec.Control = intent.NewControl(spec.Intent.Settings)
		}
		m.intents[spec.Run.ID] = spec.Control
	}
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.active, spec.Run.ID)
			delete(m.intents, spec.Run.ID)
			if done, waiting := m.done[spec.Run.ID]; waiting {
				delete(m.done, spec.Run.ID)
				// Closed last, and while still holding the lock, so that
				// anything woken by it sees a manager that already agrees the
				// run is no longer active.
				close(done)
			}
			m.mu.Unlock()
			cancel()
		}()

		metrics, err := m.engine.Execute(ctx, spec)
		if err != nil {
			// Already recorded against the run by the engine; logged here so
			// it is visible without querying the database.
			m.log.Warn("run ended early", "run", spec.Run.ID, "error", err)
			return
		}
		m.log.Info("run completed", "run", spec.Run.ID,
			"cycles", metrics.Cycles, "breaches", metrics.SLABreaches,
			"cloud_executor_seconds", metrics.CloudExecutorSeconds)
	}()
	return nil
}

// Cancel stops a run. Whatever it recorded before stopping stands.
func (m *Manager) Cancel(runID string) error {
	m.mu.Lock()
	cancel, running := m.active[runID]
	m.mu.Unlock()

	if !running {
		return fmt.Errorf("run %q is not in flight", runID)
	}
	cancel()
	return nil
}

// Active is the runs currently in flight.
func (m *Manager) Active() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]string, 0, len(m.active))
	for id := range m.active {
		out = append(out, id)
	}
	return out
}

// IsActive reports whether one run is in flight.
func (m *Manager) IsActive(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, running := m.active[runID]
	return running
}

// Intent is how an in-flight run's intent is changed, or false when the run
// is not in flight or has no intent to change.
func (m *Manager) Intent(runID string) (*intent.Control, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	control, ok := m.intents[runID]
	return control, ok
}

// Done returns a channel that closes when this run is no longer in flight, or
// an already-closed one if it never was.
//
// Shutdown waits for every run to unwind; this is the same guarantee for one
// of them. It exists because the event stream cannot give it: Publish drops
// events for a subscriber that has fallen behind, deliberately, so that a run
// never slows down for something watching it. That makes the terminal event a
// notification and not a promise. This is the promise — and the run's status
// is then read back from the database, which is the record.
func (m *Manager) Done(runID string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()

	if done, waiting := m.done[runID]; waiting {
		return done
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

// Shutdown stops every run and waits for them to unwind.
//
// Stopping rather than abandoning matters: a run that is killed mid-cycle
// leaves an ephemeral autoscaler target behind, and those accumulate silently.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	for _, cancel := range m.active {
		cancel()
	}
	m.mu.Unlock()

	m.wg.Wait()
}

// Prepare assembles the spec for a run from what is stored.
func (m *Manager) Prepare(ctx context.Context, runID string) (run.Spec, error) {
	stored, err := m.store.Run(ctx, runID)
	if err != nil {
		return run.Spec{}, err
	}

	settings, err := m.store.RunSettings(ctx, runID)
	if err != nil {
		return run.Spec{}, err
	}

	spec := run.Spec{Run: stored, Settings: json.RawMessage(settings)}
	if stored.Mode != domain.ModeSimulation {
		return spec, nil
	}

	scenario, err := m.store.Scenario(ctx, stored.ScenarioID)
	if err != nil {
		return run.Spec{}, fmt.Errorf("run %q refers to a scenario that is gone: %w", runID, err)
	}
	mine, err := m.store.Mine(ctx, scenario.MineID)
	if err != nil {
		return run.Spec{}, fmt.Errorf("scenario %q refers to a mine that is gone: %w",
			scenario.ID, err)
	}

	runIntent, err := m.store.RunIntent(ctx, runID)
	if err != nil {
		return run.Spec{}, err
	}

	spec.Scenario = scenario
	spec.Mine = mine
	spec.Intent = runIntent
	return spec, nil
}
