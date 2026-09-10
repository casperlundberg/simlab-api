// Package events carries what is happening in a run to whoever is watching it.
package events

import (
	"sync"

	"github.com/casperlundberg/simlab-api/internal/run"
)

// bufferSize is how far behind a subscriber may fall before it starts losing
// events. A run at fifteen simulated seconds a cycle, replayed fast, can emit
// hundreds a second; a browser on a slow connection will not keep up with all
// of them, and the run must not slow down because of it.
const bufferSize = 256

// Hub fans run events out to subscribers.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[int]*subscriber
	next        int
}

type subscriber struct {
	// runID scopes a subscription to one run; empty means every run, which is
	// what a dashboard watching the whole system wants.
	runID  string
	events chan run.Event
}

// New builds an empty hub.
func New() *Hub {
	return &Hub{subscribers: map[int]*subscriber{}}
}

// Publish delivers an event to everyone watching, and never blocks.
//
// A run is the thing that matters here; a browser watching it is not. If a
// subscriber has fallen behind, its event is dropped rather than stalling the
// run — the client can always re-read the run's cycles from the database, and
// a run that ran slowly because someone left a tab open would be worthless as
// a measurement.
func (h *Hub) Publish(event run.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, s := range h.subscribers {
		if s.runID != "" && s.runID != event.RunID {
			continue
		}
		select {
		case s.events <- event:
		default:
		}
	}
}

// Subscribe returns a stream of events and a function that closes it. Pass an
// empty runID to watch every run.
func (h *Hub) Subscribe(runID string) (<-chan run.Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id := h.next
	h.next++
	s := &subscriber{runID: runID, events: make(chan run.Event, bufferSize)}
	h.subscribers[id] = s

	var once sync.Once
	return s.events, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if existing, ok := h.subscribers[id]; ok {
				delete(h.subscribers, id)
				close(existing.events)
			}
		})
	}
}

// Subscribers is how many streams are open, for the status endpoint.
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}
