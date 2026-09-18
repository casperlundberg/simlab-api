package pipeline_test

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/pipeline"
)

// ready is an observation with picks waiting to be associated and nothing else
// going on, which is the uninteresting case every trigger has to agree on.
func ready(count int, since time.Duration) pipeline.Observation {
	return pipeline.Observation{PicksReady: count, SinceLast: since}
}

func TestAFixedIntervalWaitsForItsIntervalToElapse(t *testing.T) {
	trigger := pipeline.FixedInterval{Every: 10 * time.Second}

	got := trigger.Decide(0, ready(5, 4*time.Second))

	if got.Associate {
		t.Errorf("swept after 4s of a 10s interval: %s", got.Reason)
	}
}

func TestAFixedIntervalSweepsOnceItsIntervalElapsed(t *testing.T) {
	trigger := pipeline.FixedInterval{Every: 10 * time.Second}

	got := trigger.Decide(0, ready(5, 10*time.Second))

	if !got.Associate {
		t.Errorf("did not sweep after a full interval: %s", got.Reason)
	}
}

// An empty sweep costs an executor a second and produces nothing. The real
// pipeline runs them anyway; nothing here has to.
func TestNoTriggerSweepsWithNothingReady(t *testing.T) {
	for _, trigger := range triggers() {
		t.Run(trigger.Name(), func(t *testing.T) {
			got := trigger.Decide(time.Hour, pipeline.Observation{
				PicksReady: 0, PicksPending: 3, SinceLast: time.Hour,
			})

			if got.Associate {
				t.Errorf("swept with nothing ready: %s", got.Reason)
			}
		})
	}
}

func TestWhenDrainedWaitsWhilePicksAreStillInFlight(t *testing.T) {
	trigger := pipeline.WhenDrained{Deadline: time.Minute}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 2, SinceLast: 5 * time.Second,
	})

	if got.Associate {
		t.Errorf("swept with 2 picks still in flight: %s", got.Reason)
	}
}

func TestWhenDrainedSweepsAsSoonAsTheBacklogIsClear(t *testing.T) {
	trigger := pipeline.WhenDrained{Deadline: time.Minute}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 0, SinceLast: time.Second,
	})

	if !got.Associate {
		t.Errorf("did not sweep with the backlog clear: %s", got.Reason)
	}
}

// A backlog that never drains would otherwise mean a sweep that never happens,
// which is the worst outcome for an operator waiting on a location.
func TestWhenDrainedSweepsAtItsDeadlineEvenWithABacklog(t *testing.T) {
	trigger := pipeline.WhenDrained{Deadline: time.Minute}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 99, SinceLast: time.Minute,
	})

	if !got.Associate {
		t.Errorf("did not sweep after waiting out its deadline: %s", got.Reason)
	}
}

// The arm the whole argument rests on: if ordering by intent works, the picks
// the mine values finish early and a sweep need not wait for the rest.
func TestJustInTimeSweepsOnceTheValuedPicksAreDoneEvenWithABacklog(t *testing.T) {
	trigger := pipeline.JustInTime{Deadline: time.Minute}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 40, HighValuePending: 0, SinceLast: time.Second,
	})

	if !got.Associate {
		t.Errorf("waited for picks the mine does not value: %s", got.Reason)
	}
}

func TestJustInTimeWaitsWhileTheValuedPicksAreInFlight(t *testing.T) {
	trigger := pipeline.JustInTime{Deadline: time.Minute}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 5, HighValuePending: 2, SinceLast: time.Second,
	})

	if got.Associate {
		t.Errorf("swept without the picks the mine values: %s", got.Reason)
	}
}

func TestJustInTimeGivesUpOnItsValuedPicksAtItsDeadline(t *testing.T) {
	trigger := pipeline.JustInTime{Deadline: time.Minute}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 5, HighValuePending: 2, SinceLast: time.Minute,
	})

	if !got.Associate {
		t.Errorf("waited past its deadline for valued picks: %s", got.Reason)
	}
}

// Without a floor it would sweep every cycle the queue happened to be empty,
// and every sweep costs an executor.
func TestAdaptiveHoldsItsMinimumSpacingEvenWithAClearBacklog(t *testing.T) {
	trigger := pipeline.Adaptive{Min: 10 * time.Second, Max: time.Minute}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 0, SinceLast: 2 * time.Second,
	})

	if got.Associate {
		t.Errorf("swept 2s after the last sweep: %s", got.Reason)
	}
}

func TestAdaptiveSweepsAtItsCeilingHoweverBigTheBacklog(t *testing.T) {
	trigger := pipeline.Adaptive{Min: 10 * time.Second, Max: time.Minute}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 500, DrainPerSecond: 0.1, SinceLast: time.Minute,
	})

	if !got.Associate {
		t.Errorf("did not sweep at its ceiling: %s", got.Reason)
	}
}

// The tradeoff it exists for: waiting is only worth it while waiting is still
// buying more data before the ceiling.
func TestAdaptiveWaitsWhenTheBacklogWillClearBeforeItsCeiling(t *testing.T) {
	trigger := pipeline.Adaptive{Min: time.Second, Max: time.Minute}

	// 10 pending at 2/s clears in 5s, well inside the ceiling.
	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 10, DrainPerSecond: 2, SinceLast: 10 * time.Second,
	})

	if got.Associate {
		t.Errorf("swept although waiting would have bought more data: %s", got.Reason)
	}
}

func TestAdaptiveSweepsEarlyWhenTheBacklogWouldNotClearInTime(t *testing.T) {
	trigger := pipeline.Adaptive{Min: time.Second, Max: time.Minute}

	// 100 pending at 1/s needs 100s; the ceiling is 60s away at most.
	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 100, DrainPerSecond: 1, SinceLast: 10 * time.Second,
	})

	if !got.Associate {
		t.Errorf("waited for a backlog that cannot clear in time: %s", got.Reason)
	}
}

func TestAdaptiveWaitsWhenNothingIsCompletingAndThereIsNoRateToPredictFrom(t *testing.T) {
	trigger := pipeline.Adaptive{Min: time.Second, Max: time.Hour}

	got := trigger.Decide(0, pipeline.Observation{
		PicksReady: 4, PicksPending: 10, DrainPerSecond: 0, SinceLast: 10 * time.Second,
	})

	if got.Associate {
		t.Errorf("predicted a drain from no observed rate: %s", got.Reason)
	}
}

func TestUnderPressureSweepsWhenTheBacklogPassesItsThreshold(t *testing.T) {
	// The inner trigger would wait another 50 seconds.
	trigger := pipeline.UnderPressure{
		Trigger: pipeline.FixedInterval{Every: time.Minute},
		Backlog: 20,
	}

	got := trigger.Decide(0, ready(25, 10*time.Second))

	if !got.Associate {
		t.Errorf("held the interval with 25 ready past a threshold of 20: %s", got.Reason)
	}
}

func TestUnderPressureStillHoldsItsFloorOnceOverTheThreshold(t *testing.T) {
	trigger := pipeline.UnderPressure{
		Trigger: pipeline.FixedInterval{Every: time.Minute},
		Backlog: 20,
		Floor:   5 * time.Second,
	}

	got := trigger.Decide(0, ready(25, time.Second))

	if got.Associate {
		t.Errorf("swept 1s after the last sweep, inside its 5s floor: %s", got.Reason)
	}
}

// It is a layer, not a replacement: it may only ever bring a sweep forward. A
// wrapper that could also delay one would make a pipeline less responsive than
// the one it was layered onto, which no operator asked for.
func TestUnderPressureNeverSweepsLessOftenThanTheTriggerItWraps(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))

	for i := 0; i < 2000; i++ {
		inner := pipeline.FixedInterval{Every: time.Duration(random.IntN(120)) * time.Second}
		wrapper := pipeline.UnderPressure{
			Trigger: inner,
			Backlog: random.IntN(50),
			Floor:   time.Duration(random.IntN(30)) * time.Second,
		}
		o := pipeline.Observation{
			PicksReady:   random.IntN(100),
			PicksPending: random.IntN(100),
			SinceLast:    time.Duration(random.IntN(200)) * time.Second,
		}

		if inner.Decide(0, o).Associate && !wrapper.Decide(0, o).Associate {
			t.Fatalf("wrapping %+v suppressed a sweep the inner trigger wanted, at %+v", wrapper, o)
		}
	}
}

// The reason is not decoration: the argument for ordering by intent is that an
// operator can be told why the system did what it did.
func TestEveryDecisionSaysWhy(t *testing.T) {
	random := rand.New(rand.NewPCG(3, 4))

	for _, trigger := range triggers() {
		for i := 0; i < 500; i++ {
			o := pipeline.Observation{
				PicksReady:       random.IntN(50),
				PicksPending:     random.IntN(50),
				HighValuePending: random.IntN(10),
				DrainPerSecond:   random.Float64() * 3,
				SinceLast:        time.Duration(random.IntN(200)) * time.Second,
			}

			got := trigger.Decide(time.Duration(i)*time.Second, o)

			if strings.TrimSpace(got.Reason) == "" {
				t.Fatalf("%s decided %v at %+v with no reason", trigger.Name(), got.Associate, o)
			}
		}
	}
}

func triggers() []pipeline.Trigger {
	return []pipeline.Trigger{
		pipeline.FixedInterval{Every: 10 * time.Second},
		pipeline.WhenDrained{Deadline: time.Minute},
		pipeline.JustInTime{Deadline: time.Minute},
		pipeline.Adaptive{Min: 10 * time.Second, Max: time.Minute},
		pipeline.UnderPressure{
			Trigger: pipeline.FixedInterval{Every: 10 * time.Second},
			Backlog: 20, Floor: 5 * time.Second,
		},
	}
}
