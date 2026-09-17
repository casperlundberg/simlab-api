package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/orchestrator"
)

// Restriction is what lets a result be claimed to hold on a real orchestrator.
// A simulator that can do more than ColonyOS produces results that do not
// transfer to it, and nothing in the numbers says which ones depended on the
// extra capability.

type fake struct {
	caps    orchestrator.Capabilities
	applied []orchestrator.PriorityUpdate
}

func (f *fake) Kind() string                            { return "fake" }
func (f *fake) Capabilities() orchestrator.Capabilities { return f.caps }
func (f *fake) Advance(time.Duration, int) orchestrator.Progress {
	return orchestrator.Progress{}
}
func (f *fake) Snapshot(time.Duration) map[domain.Priority]domain.QueueSnapshot { return nil }
func (f *fake) SnapshotByBurst(time.Duration) (counted, exempt map[domain.Priority]domain.QueueSnapshot) {
	return nil, nil
}
func (f *fake) Done() bool                { return true }
func (f *fake) Stats() orchestrator.Stats { return orchestrator.Stats{} }

func (f *fake) Reprioritise(_ time.Duration, updates []orchestrator.PriorityUpdate) orchestrator.Applied {
	f.applied = append(f.applied, updates...)
	return orchestrator.Applied{Changed: len(updates)}
}

func mutable() *fake {
	return &fake{caps: orchestrator.Capabilities{MutablePriority: true}}
}

func TestAnOrchestratorCannotBeRestrictedToACapabilityItLacks(t *testing.T) {
	immutable := &fake{caps: orchestrator.Capabilities{MutablePriority: false}}

	if _, err := orchestrator.Restrict(immutable, orchestrator.Capabilities{MutablePriority: true}); err == nil {
		t.Error("granting a capability the orchestrator does not have would produce a run " +
			"that could not be reproduced on the system being claimed about")
	}
}

func TestRestrictingAwayMutablePriorityRefusesUpdatesRatherThanDroppingThem(t *testing.T) {
	inner := mutable()
	restricted, err := orchestrator.Restrict(inner, orchestrator.Capabilities{MutablePriority: false})
	if err != nil {
		t.Fatalf("restricting: %v", err)
	}

	applied := restricted.Reprioritise(0, []orchestrator.PriorityUpdate{
		{JobID: 1, Priority: 100}, {JobID: 2, Priority: 100},
	})

	if applied.Changed != 0 {
		t.Errorf("expected nothing changed, got %d", applied.Changed)
	}
	if len(applied.Rejected) != 2 {
		t.Fatalf("expected both updates reported as rejected, got %d", len(applied.Rejected))
	}
	if !strings.Contains(applied.Rejected[0].Reason, "priority") {
		t.Errorf("the rejection should say what was refused, got %q", applied.Rejected[0].Reason)
	}
	if len(inner.applied) != 0 {
		t.Error("the update reached the inner orchestrator despite being restricted away")
	}
}

func TestAPriorityOutsideTheAdmittedLevelsIsRejected(t *testing.T) {
	inner := mutable()
	restricted, err := orchestrator.Restrict(inner, orchestrator.Capabilities{
		MutablePriority: true,
		Levels:          []domain.Priority{1, 50, 100},
	})
	if err != nil {
		t.Fatalf("restricting: %v", err)
	}

	applied := restricted.Reprioritise(0, []orchestrator.PriorityUpdate{
		{JobID: 1, Priority: 100}, // admitted
		{JobID: 2, Priority: 73},  // not one of the levels
	})

	if applied.Changed != 1 {
		t.Errorf("expected the admitted update to be applied, got %d", applied.Changed)
	}
	if len(applied.Rejected) != 1 || applied.Rejected[0].JobID != 2 {
		t.Errorf("expected job 2 rejected, got %+v", applied.Rejected)
	}
	if len(inner.applied) != 1 || inner.applied[0].JobID != 1 {
		t.Errorf("only the admitted update should reach the orchestrator, got %+v", inner.applied)
	}
}

func TestCapabilitiesWithNoLevelsAdmitAnyPriority(t *testing.T) {
	open := orchestrator.Capabilities{MutablePriority: true}
	for _, priority := range []domain.Priority{0, 25, 100, 9999} {
		if !open.Accepts(priority) {
			t.Errorf("priority %d should be admitted when no levels are declared", priority)
		}
	}
}

func TestARestrictedOrchestratorReportsTheNarrowedCapabilities(t *testing.T) {
	restricted, err := orchestrator.Restrict(mutable(), orchestrator.Capabilities{MutablePriority: false})
	if err != nil {
		t.Fatalf("restricting: %v", err)
	}
	if restricted.Capabilities().MutablePriority {
		t.Error("a restricted orchestrator must report what it will actually do, " +
			"or a run cannot tell whether its result transfers")
	}
}
