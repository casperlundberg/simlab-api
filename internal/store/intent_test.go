package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func TestARunKeepsTheIntentItWasCreatedWith(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}
	if got, err := s.RunIntent(ctx, "run-1"); err != nil || got != nil {
		t.Fatalf("RunIntent() before one is saved = %+v, %v; want none, as for a run that predates intent", got, err)
	}

	settings := domain.DefaultIntent()
	settings.Mode = domain.IntentBoth
	saved := domain.RunIntent{Settings: settings, Schedule: []domain.IntentStep{
		{Cycle: 40, Settings: json.RawMessage(`{"mode":"off"}`), Version: 2, Source: "operator"},
	}}
	if err := s.SaveRunIntent(ctx, "run-1", saved); err != nil {
		t.Fatalf("SaveRunIntent() = %v", err)
	}
	read, err := s.RunIntent(ctx, "run-1")
	if err != nil || read == nil {
		t.Fatalf("RunIntent() = %+v, %v", read, err)
	}
	if !reflect.DeepEqual(read.Settings, saved.Settings) || len(read.Schedule) != 1 ||
		read.Schedule[0].Cycle != 40 || read.Schedule[0].Version != 2 || read.Schedule[0].Source != "operator" {
		t.Errorf("intent changed in the database\n saved: %+v\n  read: %+v", saved, read)
	}
}

func TestEveryIntentChangeSurvivesTheDatabaseInVersionOrder(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	off := domain.IntentOffSettings()
	off.BurstExempt = []domain.IntentClass{domain.ClassDecayed}
	saved := []domain.IntentChange{
		{Version: 1, Cycle: 1, Source: "initial", Settings: domain.DefaultIntent(),
			RecordedAt: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)},
		{Version: 3, Cycle: 31, Source: "operator", Settings: off,
			RecordedAt: time.Date(2026, 9, 17, 10, 5, 0, 0, time.UTC)},
	}
	for i := len(saved) - 1; i >= 0; i-- {
		if err := s.SaveIntentChange(ctx, "run-1", saved[i]); err != nil {
			t.Fatalf("SaveIntentChange() = %v", err)
		}
	}

	read, err := s.IntentChanges(ctx, "run-1")
	if err != nil {
		t.Fatalf("IntentChanges() = %v", err)
	}
	if len(read) != len(saved) {
		t.Fatalf("IntentChanges() = %d changes, want %d", len(read), len(saved))
	}
	for i := range saved {
		if !read[i].RecordedAt.Equal(saved[i].RecordedAt) {
			t.Errorf("change %d recorded at %v, want %v", i, read[i].RecordedAt, saved[i].RecordedAt)
		}
		read[i].RecordedAt = saved[i].RecordedAt
		if !reflect.DeepEqual(read[i], saved[i]) {
			t.Errorf("change %d\n saved: %+v\n  read: %+v", i, saved[i], read[i])
		}
	}
}
