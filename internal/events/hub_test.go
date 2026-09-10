package events_test

import (
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/events"
	"github.com/casperlundberg/simlab-api/internal/run"
)

func TestASubscriberSeesEventsForItsRun(t *testing.T) {
	hub := events.New()
	stream, unsubscribe := hub.Subscribe("run-1")
	defer unsubscribe()

	hub.Publish(run.Event{RunID: "run-1", Type: run.EventCycle})

	select {
	case got := <-stream:
		if got.RunID != "run-1" {
			t.Errorf("received an event for %q", got.RunID)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was delivered")
	}
}

func TestASubscriberDoesNotSeeAnotherRunsEvents(t *testing.T) {
	hub := events.New()
	stream, unsubscribe := hub.Subscribe("run-1")
	defer unsubscribe()

	hub.Publish(run.Event{RunID: "run-2", Type: run.EventCycle})

	select {
	case got := <-stream:
		t.Errorf("received %+v, which belongs to another run", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAnUnscopedSubscriberSeesEverything(t *testing.T) {
	hub := events.New()
	stream, unsubscribe := hub.Subscribe("")
	defer unsubscribe()

	hub.Publish(run.Event{RunID: "run-1"})
	hub.Publish(run.Event{RunID: "run-2"})

	for i := 0; i < 2; i++ {
		select {
		case <-stream:
		case <-time.After(time.Second):
			t.Fatalf("only %d of 2 events arrived", i)
		}
	}
}

func TestUnsubscribingClosesTheStream(t *testing.T) {
	hub := events.New()
	stream, unsubscribe := hub.Subscribe("run-1")

	unsubscribe()
	unsubscribe() // must be safe to call twice; a handler may unwind twice

	if _, open := <-stream; open {
		t.Error("the stream delivered a value after unsubscribing")
	}
	if hub.Subscribers() != 0 {
		t.Errorf("Subscribers() = %d after unsubscribing", hub.Subscribers())
	}
}

// A run is what matters; a browser watching it is not. A subscriber that
// stops reading must not slow the run down, or a measurement would depend on
// whether somebody left a tab open.
func TestASubscriberThatStopsReadingDoesNotStallTheRun(t *testing.T) {
	hub := events.New()
	_, unsubscribe := hub.Subscribe("run-1")
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100000; i++ {
			hub.Publish(run.Event{RunID: "run-1", Type: run.EventCycle})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked behind a subscriber that is not reading")
	}
}
