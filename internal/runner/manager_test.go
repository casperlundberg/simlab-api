package runner_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/run"
	"github.com/casperlundberg/simlab-api/internal/runner"
)

// The manager owns the goroutine a run executes on, so what it says about a
// run being in flight is the only reliable answer to "has this finished yet".
// These tests had nowhere to live while the manager took a concrete engine: to
// start a run at all you needed an autoscaler, a database and a workload, and
// none of that has anything to do with the questions below.
//
// No sleeping, and no timing assumptions. The stub executor blocks until a
// test releases it, so every transition happens because the test caused it.

// blocking is an executor that waits to be released, so a test can hold a run
// in flight for exactly as long as it wants to look at it.
type blocking struct {
	started  chan string
	release  chan struct{}
	failWith error
}

func newBlocking() *blocking {
	return &blocking{started: make(chan string, 8), release: make(chan struct{})}
}

func (b *blocking) Execute(ctx context.Context, spec run.Spec) (domain.Metrics, error) {
	b.started <- spec.Run.ID
	select {
	case <-b.release:
		return domain.Metrics{}, b.failWith
	case <-ctx.Done():
		// A cancelled run still returns; the engine records what it had.
		return domain.Metrics{}, ctx.Err()
	}
}

func spec(id string) run.Spec {
	return run.Spec{Run: domain.Run{ID: id, Mode: domain.ModeSimulation}}
}

// This is the property the API tests lean on. Start registers the run before
// it returns, so a caller that has been handed a 202 can rely on the run being
// in flight — there is no window to poll for, and a test that polls for one is
// describing a race that does not exist.
func TestARunIsInFlightAsSoonAsStartReturns(t *testing.T) {
	executor := newBlocking()
	manager := runner.New(nil, executor, discardLog())
	t.Cleanup(func() { close(executor.release); manager.Shutdown() })

	if err := manager.Start(spec("burst-1")); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	if !manager.IsActive("burst-1") {
		t.Error("IsActive() = false immediately after Start returned")
	}
	if got := manager.Active(); len(got) != 1 || got[0] != "burst-1" {
		t.Errorf("Active() = %v, want just burst-1", got)
	}
}

func TestStartingARunThatIsAlreadyInFlightIsRefused(t *testing.T) {
	executor := newBlocking()
	manager := runner.New(nil, executor, discardLog())
	t.Cleanup(func() { close(executor.release); manager.Shutdown() })

	if err := manager.Start(spec("burst-1")); err != nil {
		t.Fatalf("first Start() = %v", err)
	}

	err := manager.Start(spec("burst-1"))
	if !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Errorf("second Start() = %v, want ErrAlreadyRunning", err)
	}
	// And the first run is untouched by the refusal.
	if !manager.IsActive("burst-1") {
		t.Error("IsActive() = false after a refused second start")
	}
}

func TestDoneClosesWhenTheRunHasUnwound(t *testing.T) {
	executor := newBlocking()
	manager := runner.New(nil, executor, discardLog())
	t.Cleanup(manager.Shutdown)

	if err := manager.Start(spec("burst-1")); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	done := manager.Done("burst-1")

	select {
	case <-done:
		t.Fatal("Done() was closed while the run was still executing")
	default:
	}

	close(executor.release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done() did not close after the run finished")
	}

	// Closed means finished, not merely nearly finished: a caller woken by it
	// must not then find the run still listed as active.
	if manager.IsActive("burst-1") {
		t.Error("IsActive() = true after Done() closed")
	}
}

// Asking about a run that is not in flight has to answer immediately rather
// than block forever — a run that finished before anybody got round to waiting
// for it is the common case, not an error.
func TestDoneIsAlreadyClosedForARunThatIsNotInFlight(t *testing.T) {
	manager := runner.New(nil, newBlocking(), discardLog())

	select {
	case <-manager.Done("never-started"):
	case <-time.After(time.Second):
		t.Error("Done() on a run that was never started did not close")
	}
}

func TestCancellingARunStopsIt(t *testing.T) {
	executor := newBlocking()
	manager := runner.New(nil, executor, discardLog())
	t.Cleanup(func() { close(executor.release); manager.Shutdown() })

	if err := manager.Start(spec("burst-1")); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	done := manager.Done("burst-1")

	if err := manager.Cancel("burst-1"); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run was still in flight after being cancelled")
	}
}

func TestCancellingARunThatIsNotInFlightSaysSo(t *testing.T) {
	manager := runner.New(nil, newBlocking(), discardLog())

	err := manager.Cancel("never-started")
	if err == nil {
		t.Fatal("Cancel() on a run that is not in flight = nil, want an error")
	}
	// The error names the run, because the caller is usually a handler that
	// has to say which one.
	if !strings.Contains(err.Error(), "never-started") {
		t.Errorf("Cancel() error = %q, want it to name the run", err)
	}
}

// Shutdown stops rather than abandons, and waits. A run killed mid-cycle
// leaves an ephemeral autoscaler target behind, and those accumulate silently
// — so "the process is going away" must still mean every run unwound.
func TestShutdownStopsEveryRunAndWaitsForThem(t *testing.T) {
	executor := newBlocking()
	manager := runner.New(nil, executor, discardLog())

	for _, id := range []string{"burst-1", "burst-2", "burst-3"} {
		if err := manager.Start(spec(id)); err != nil {
			t.Fatalf("Start(%s) = %v", id, err)
		}
	}
	for range 3 {
		<-executor.started // all three are executing, not merely registered
	}

	manager.Shutdown()

	if got := manager.Active(); len(got) != 0 {
		t.Errorf("Active() = %v after Shutdown, want none", got)
	}
}

// A run whose execution failed is no longer in flight. The engine has already
// recorded the failure against the run; the manager's job is only to stop
// holding it.
func TestARunThatFailedIsNoLongerInFlight(t *testing.T) {
	executor := newBlocking()
	executor.failWith = errors.New("the autoscaler refused the target")
	manager := runner.New(nil, executor, discardLog())
	t.Cleanup(manager.Shutdown)

	if err := manager.Start(spec("burst-1")); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	done := manager.Done("burst-1")
	close(executor.release)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a failed run stayed in flight")
	}
	if manager.IsActive("burst-1") {
		t.Error("IsActive() = true for a run that failed")
	}
}

// discardLog keeps the manager's own logging out of the test output. It logs a
// line per run ending, which is right for a service and noise here.
func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
