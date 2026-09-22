package domain

import "time"

// Work is a run's processing as the mine sees it before any of it is done:
// every job's submitted priority, by job id, and for each event the work it
// produced and the order its sensors triggered in. Nothing of the world beyond
// that — where an event really was, or how large — which is what lets code that
// orders work take this without being able to read the truth.
type Work struct {
	Priorities []Priority
	Events     []EventWork
}

// EventWork is one event's share of the work.
type EventWork struct {
	Origin   time.Duration
	FirstJob JobID
	// Picks is how many jobs the event produced, from FirstJob on.
	Picks int
	// Triggers is where the detecting sensors are, first arrival first: what
	// a real system has of an event before any pick has been processed.
	Triggers []Point
}
