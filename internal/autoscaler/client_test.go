package autoscaler_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/autoscaler"
	"github.com/casperlundberg/simlab-api/internal/autoscaler/astest"
)

func client(t *testing.T, token string) (*autoscaler.Client, *astest.Server) {
	t.Helper()

	fake := astest.New(t, token)
	c, err := autoscaler.New(fake.Start(), token, 5*time.Second)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return c, fake
}

func TestPlatformsAreReadBackWithTheirFields(t *testing.T) {
	c, _ := client(t, "")

	got, err := c.Platforms(context.Background())
	if err != nil {
		t.Fatalf("Platforms() = %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Platforms() returned nothing")
	}
	if got[0].Kind == "" || got[0].Summary == "" {
		t.Errorf("platform = %+v, want a kind and a summary", got[0])
	}
}

func TestATargetCanBeCreatedFetchedAndDeleted(t *testing.T) {
	c, fake := client(t, "")
	ctx := context.Background()

	if _, err := c.CreateTarget(ctx, autoscaler.Target{
		ID: "run-1", Kind: "simulation", Mode: "driven",
	}, json.RawMessage(`{"local_executor_cap": 7}`)); err != nil {
		t.Fatalf("CreateTarget() = %v", err)
	}
	if !fake.TargetExists("run-1") {
		t.Fatal("the target was not registered")
	}

	got, err := c.GetTarget(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetTarget() = %v", err)
	}
	if got.Target.Kind != "simulation" {
		t.Errorf("Kind = %q", got.Target.Kind)
	}

	if err := c.DeleteTarget(ctx, "run-1"); err != nil {
		t.Fatalf("DeleteTarget() = %v", err)
	}
	if fake.TargetExists("run-1") {
		t.Error("the target survived being deleted")
	}
}

func TestAMissingTargetIsRecognisableAsNotFound(t *testing.T) {
	c, _ := client(t, "")

	_, err := c.GetTarget(context.Background(), "nobody")
	if err == nil {
		t.Fatal("GetTarget() = nil error")
	}
	// Callers branch on this rather than matching message text.
	if !autoscaler.NotFound(err) {
		t.Errorf("NotFound(%v) = false, want true", err)
	}
}

func TestTheAutoscalersOwnExplanationIsPreserved(t *testing.T) {
	c, _ := client(t, "")

	_, err := c.GetTarget(context.Background(), "nobody")
	if err == nil || !strings.Contains(err.Error(), "no such target") {
		t.Errorf("GetTarget() = %v, want the service's own message", err)
	}
}

func TestTheBearerTokenIsSent(t *testing.T) {
	c, _ := client(t, "s3cr3t")

	if _, err := c.Platforms(context.Background()); err != nil {
		t.Errorf("Platforms() = %v, want the token to have been accepted", err)
	}
}

func TestAMissingTokenIsRejectedByTheService(t *testing.T) {
	fake := astest.New(t, "s3cr3t")
	c, err := autoscaler.New(fake.Start(), "", 5*time.Second)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	if _, err := c.Platforms(context.Background()); err == nil {
		t.Error("Platforms() = nil error with no token")
	}
}

func TestSettingsCanBeReadAndChanged(t *testing.T) {
	c, fake := client(t, "")
	ctx := context.Background()
	if _, err := c.CreateTarget(ctx, autoscaler.Target{ID: "run-1", Kind: "simulation"}, nil); err != nil {
		t.Fatalf("CreateTarget() = %v", err)
	}

	before, err := c.GetSettings(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetSettings() = %v", err)
	}

	after, err := c.ApplySettings(ctx, "run-1",
		json.RawMessage(`{"local_executor_cap": 42}`), &before.Version, "operator@example.org")
	if err != nil {
		t.Fatalf("ApplySettings() = %v", err)
	}
	if after.Version <= before.Version {
		t.Errorf("version went from %d to %d", before.Version, after.Version)
	}
	if fake.SettingsOf("run-1")["local_executor_cap"] != 42.0 {
		t.Errorf("local_executor_cap = %v, want 42", fake.SettingsOf("run-1")["local_executor_cap"])
	}
}

// The call a run is built out of.
func TestACycleReturnsADecisionAndWhatWasObserved(t *testing.T) {
	c, _ := client(t, "")
	ctx := context.Background()
	if _, err := c.CreateTarget(ctx, autoscaler.Target{ID: "run-1", Kind: "simulation"}, nil); err != nil {
		t.Fatalf("CreateTarget() = %v", err)
	}

	got, err := c.Cycle(ctx, "run-1", autoscaler.CycleRequest{
		At: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		Workload: &autoscaler.Workload{
			Queues:             map[string]autoscaler.QueueInfo{"100": {Depth: 300, OldestJobAgeSeconds: 40, ArrivalRate: 2}},
			ExecutorThroughput: 0.05,
		},
	})
	if err != nil {
		t.Fatalf("Cycle() = %v", err)
	}

	if got.Decision.Plan.LocalExecutors == 0 {
		t.Errorf("Plan = %+v, want capacity for 300 waiting jobs", got.Decision.Plan)
	}
	if got.Decision.Reason == "" {
		t.Error("Reason is empty")
	}
	if got.Applied == nil {
		t.Error("Applied is nil, want what the platform now holds")
	}
}

func TestACycleWithoutAWorkloadIsRefused(t *testing.T) {
	c, _ := client(t, "")
	ctx := context.Background()
	if _, err := c.CreateTarget(ctx, autoscaler.Target{ID: "run-1", Kind: "simulation"}, nil); err != nil {
		t.Fatalf("CreateTarget() = %v", err)
	}

	if _, err := c.Cycle(ctx, "run-1", autoscaler.CycleRequest{}); err == nil {
		t.Error("Cycle() = nil error with no workload")
	}
}

func TestAnUnreachableAutoscalerIsReportedWithItsAddress(t *testing.T) {
	c, err := autoscaler.New("http://127.0.0.1:1", "", time.Second)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	_, err = c.Platforms(context.Background())
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("Platforms() = %v, want the address it could not reach", err)
	}
}

func TestAnEmptyBaseURLIsRefusedAtConstruction(t *testing.T) {
	if _, err := autoscaler.New("", "", time.Second); err == nil {
		t.Error("New() = nil error with no base URL")
	}
}
