package intent_test

import (
	"errors"
	"testing"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/intent"
)

func TestAControlStartsAtVersionOneWithTheInitialSettings(t *testing.T) {
	c := intent.NewControl(domain.DefaultIntent())

	settings, version, source := c.Current()
	if version != 1 || source != domain.IntentSourceInitial || settings.Mode != domain.IntentDecay {
		t.Errorf("Current() = %+v, %d, %q", settings, version, source)
	}
}

func TestAChangeAgainstAStaleVersionIsRefusedAndChangesNothing(t *testing.T) {
	c := intent.NewControl(domain.DefaultIntent())
	one := 1
	if _, version, err := c.Patch([]byte(`{"mode":"both"}`), &one, domain.IntentSourceOperator); err != nil || version != 2 {
		t.Fatalf("first Patch() = %d, %v", version, err)
	}

	_, version, err := c.Patch([]byte(`{"mode":"off"}`), &one, domain.IntentSourceOperator)

	if !errors.Is(err, intent.ErrVersionConflict) {
		t.Errorf("Patch() against version 1 at version 2 = %v, want a conflict", err)
	}
	if settings, _, _ := c.Current(); settings.Mode != domain.IntentBoth || version != 2 {
		t.Errorf("after a refused change: mode %q, version %d", settings.Mode, version)
	}
}

func TestAnInvalidChangeIsRefusedAndChangesNothing(t *testing.T) {
	c := intent.NewControl(domain.DefaultIntent())

	if _, _, err := c.Patch([]byte(`{"mode":"sideways"}`), nil, domain.IntentSourceOperator); err == nil {
		t.Error("Patch() accepted an unknown mode")
	}
	if _, version, _ := c.Current(); version != 1 {
		t.Errorf("version = %d after a refused change, want 1", version)
	}
}

func TestAReplayedChangeKeepsItsVersion(t *testing.T) {
	c := intent.NewControl(domain.DefaultIntent())

	if err := c.Replay(domain.IntentOffSettings(), 4, domain.IntentSourceOperator); err != nil {
		t.Fatalf("Replay() = %v", err)
	}
	if settings, version, source := c.Current(); version != 4 || settings.Mode != domain.IntentOff || source != domain.IntentSourceOperator {
		t.Errorf("Current() = %q, %d, %q; want off at version 4 from the operator", settings.Mode, version, source)
	}
}
