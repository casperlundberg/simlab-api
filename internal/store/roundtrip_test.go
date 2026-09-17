package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Round-trip tests that walk the whole struct rather than naming fields.
//
// The named-field style these sit beside is what let a field go unpersisted in
// the sibling service for as long as it did: every example happened to leave
// the dropped field at its default, so nothing noticed. These insist the
// fixture has no zero field, then compare everything — so a field added to a
// stored type fails here rather than going quietly missing from a run's
// results.

// noZeroFields fails when any exported field is still at its zero value, which
// would mean the comparison below could not tell that field from a dropped one.
func noZeroFields(t *testing.T, what string, v any) {
	t.Helper()
	value := reflect.ValueOf(v)
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		if value.Field(i).IsZero() {
			t.Fatalf("the %s fixture leaves %s at its zero value, so this test could "+
				"not tell whether that field is stored at all", what, field.Name)
		}
	}
}

func TestEveryFieldOfACycleSurvivesTheDatabase(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	// Microseconds, because that is the resolution Postgres keeps: comparing
	// nanoseconds would fail on the column type rather than on a lost field.
	cycle := domain.Cycle{
		RunID:    "run-1",
		Sequence: 7,
		At:       time.Now().UTC().Truncate(time.Microsecond),
		Queues: map[domain.Priority]domain.QueueSnapshot{
			100: {Depth: 42, OldestJobAgeSeconds: 31.5, ArrivalRate: 2.25},
		},
		SubmittedDepths: map[domain.Priority]int{100: 30, 25: 12},
		LocalReady:      3,
		CloudReady:      4,
		LocalPending:    5,
		CloudPending:    6,
		Action:          "scale_up",
		PlanLocal:       8,
		PlanCloud:       9,
		Reason:          "P100 breaches in 15s at the current 0 executors",
		Constraint:      "per-cycle step limit",
		SettingsVersion: 11,
		BreachExpected:  true,
		Completed:       12,
		Breached:        13,
	}
	noZeroFields(t, "cycle", cycle)

	if err := s.SaveCycle(ctx, cycle); err != nil {
		t.Fatalf("SaveCycle() = %v", err)
	}
	cycles, err := s.Cycles(ctx, "run-1", 0, 100)
	if err != nil {
		t.Fatalf("Cycles() = %v", err)
	}
	if len(cycles) != 1 {
		t.Fatalf("Cycles() returned %d rows, want 1", len(cycles))
	}
	// Postgres hands timestamps back in the session's own zone, so the same
	// instant comes home with a different time.Location and compares unequal.
	// Normalising is right here — the point of this test is a dropped field,
	// not the zone a driver chose.
	read := cycles[0]
	if !read.At.Equal(cycle.At) {
		t.Errorf("At = %v, want the instant %v", read.At, cycle.At)
	}
	read.At = cycle.At

	if !reflect.DeepEqual(cycle, read) {
		t.Errorf("the cycle changed in the database\n saved: %+v\n  read: %+v", cycle, read)
	}
}

func TestEveryFieldOfTheMetricsSurvivesTheDatabase(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	metrics := domain.Metrics{
		RunID:                "run-1",
		JobsSubmitted:        13071,
		JobsCompleted:        13070,
		SLABreaches:          1489,
		BreachRate:           0.114,
		MeanWaitSeconds:      12.5,
		P95WaitSeconds:       98.25,
		MaxWaitSeconds:       301.75,
		PeakQueueDepth:       4096,
		Cycles:               240,
		ScalingActions:       42,
		LocalExecutorSeconds: 123456.5,
		CloudExecutorSeconds: 224280.0,
		PeakLocalExecutors:   50,
		PeakCloudExecutors:   200,
	}
	noZeroFields(t, "metrics", metrics)

	if err := s.SaveMetrics(ctx, metrics); err != nil {
		t.Fatalf("SaveMetrics() = %v", err)
	}
	read, err := s.Metrics(ctx, "run-1")
	if err != nil {
		t.Fatalf("Metrics() = %v", err)
	}
	if !reflect.DeepEqual(metrics, read) {
		t.Errorf("the metrics changed in the database\n saved: %+v\n  read: %+v", metrics, read)
	}
}
