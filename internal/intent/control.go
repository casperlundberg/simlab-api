package intent

import (
	"errors"
	"fmt"
	"sync"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// ErrVersionConflict is a change made against a version that is no longer
// current.
var ErrVersionConflict = errors.New("intent settings have changed since that version")

// Control is a running run's intent settings, which an operator may change
// while it runs.
//
// A change takes effect at the start of the next cycle, never part-way through
// one: the run reads the settings once per cycle. Versions increase by one per
// accepted change, and a change can name the version it was made against, so
// two operators cannot silently overwrite each other.
type Control struct {
	mu       sync.Mutex
	settings domain.IntentSettings
	version  int
	source   string
}

// NewControl starts at version 1 with the settings a run was created with.
func NewControl(initial domain.IntentSettings) *Control {
	return &Control{settings: initial, version: 1, source: domain.IntentSourceInitial}
}

// Current is the settings in force for the next cycle, their version, and
// what set them.
func (c *Control) Current() (domain.IntentSettings, int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings, c.version, c.source
}

// Patch applies a change. expected, when not nil, is the version the change
// was made against.
func (c *Control) Patch(patch []byte, expected *int, source string) (domain.IntentSettings, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if expected != nil && *expected != c.version {
		return c.settings, c.version, fmt.Errorf("%w: it is at version %d, not %d",
			ErrVersionConflict, c.version, *expected)
	}
	next, err := c.settings.Patched(patch)
	if err != nil {
		return c.settings, c.version, err
	}
	c.settings, c.source = next, source
	c.version++
	return c.settings, c.version, nil
}

// Replay sets the settings a recorded change had, at the version it had. It is
// how a run is reproduced: an operator's hand edits become a schedule, and the
// replay has to carry the same version numbers or its record would differ from
// the original's for no reason in the results.
func (c *Control) Replay(settings domain.IntentSettings, version int, source string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := settings.Validate(); err != nil {
		return err
	}
	if version <= c.version {
		version = c.version + 1
	}
	c.settings, c.version, c.source = settings, version, source
	return nil
}
