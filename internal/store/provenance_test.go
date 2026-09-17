package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

func TestARunKeepsItsProvenanceAndSaysWhatBuiltIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}

	if p, err := s.RunProvenance(ctx, "run-1"); err != nil || p != nil {
		t.Fatalf("RunProvenance() before one is saved = %+v, %v; want none", p, err)
	}

	scenarioSnapshot, mineSnapshot := scenario(), mine()
	autoscaler := domain.Build{Version: "1.0.0", Commit: "def", GoVersion: "go1.24.1"}
	saved := domain.Provenance{
		RecordedAt: time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
		SimlabAPI:  domain.Build{Version: "1.2.0", Commit: "abc", GoVersion: "go1.24.1"},
		Autoscaler: &autoscaler, Scenario: &scenarioSnapshot, Mine: &mineSnapshot,
		Settings: json.RawMessage(`{"cloud_executor_cap": 40}`), SettingsVersion: 3,
	}
	if err := s.SaveProvenance(ctx, "run-1", saved); err != nil {
		t.Fatalf("SaveProvenance() = %v", err)
	}

	read, err := s.RunProvenance(ctx, "run-1")
	if err != nil || read == nil {
		t.Fatalf("RunProvenance() = %+v, %v", read, err)
	}
	if read.SimlabAPI != saved.SimlabAPI || *read.Autoscaler != autoscaler || read.SettingsVersion != 3 ||
		read.Scenario.Seed != scenarioSnapshot.Seed || read.Mine.ID != mineSnapshot.ID ||
		!read.RecordedAt.Equal(saved.RecordedAt) {
		t.Errorf("provenance changed in the database\n saved: %+v\n  read: %+v", saved, read)
	}

	// Listing runs says what built each, so runs can be told apart by version
	// without fetching every provenance.
	runs := mustRuns(t, s)
	if len(runs) != 1 || runs[0].BuiltWith == nil || runs[0].BuiltWith.SimlabAPI.Version != "1.2.0" ||
		runs[0].BuiltWith.Autoscaler == nil || runs[0].BuiltWith.Autoscaler.Commit != "def" {
		t.Errorf("Runs() built_with = %+v", runs[0].BuiltWith)
	}
	one, err := s.Run(ctx, "run-1")
	if err != nil || one.BuiltWith == nil {
		t.Errorf("Run() built_with = %+v, %v", one.BuiltWith, err)
	}
}

func TestARunRecordedBeforeProvenanceSaysNothingBuiltIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.SaveRun(ctx, simulationRun(), nil); err != nil {
		t.Fatalf("SaveRun() = %v", err)
	}
	if run, err := s.Run(ctx, "run-1"); err != nil || run.BuiltWith != nil {
		t.Errorf("Run() = %+v, %v; want no built_with", run.BuiltWith, err)
	}
}
