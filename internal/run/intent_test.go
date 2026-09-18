package run_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/intent"
)

func intentSpec(patch string, schedule ...domain.IntentStep) *domain.RunIntent {
	settings, err := domain.DefaultIntent().Patched([]byte(patch))
	if err != nil {
		panic(err)
	}
	return &domain.RunIntent{Settings: settings, Schedule: schedule}
}

func TestDecayMovesWorkAndTheRunRecordsWhatItDid(t *testing.T) {
	h := newHarness(t)
	spec := simulationSpec()
	spec.Intent = intentSpec(`{}`)

	metrics, err := h.engine.Execute(context.Background(), spec)
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	if metrics.JobsReprioritised == 0 {
		t.Fatal("intent decayed nothing in a run with events all over the mine")
	}
	decayed := false
	for _, cycle := range h.recorder.cycles {
		if cycle.Intent == nil {
			t.Fatalf("cycle %d has no intent summary", cycle.Sequence)
		}
		if cycle.Intent.Decayed > 0 {
			decayed = true
			// Decayed work waits below every level anything was submitted at,
			// so that it queues behind the work of the events intent kept.
			if cycle.Queues[domain.PriorityBelowFloor].Depth != cycle.Intent.Decayed {
				t.Errorf("cycle %d: %d decayed jobs waiting, but P%d holds %d",
					cycle.Sequence, cycle.Intent.Decayed, domain.PriorityBelowFloor,
					cycle.Queues[domain.PriorityBelowFloor].Depth)
			}
			if _, submitted := cycle.SubmittedDepths[domain.PriorityBelowFloor]; submitted {
				t.Errorf("cycle %d: work was submitted below the floor, which is intent's own level", cycle.Sequence)
			}
		}
		if cycle.Intent.Promoted > 0 {
			t.Fatalf("cycle %d: %d promoted jobs under decay only", cycle.Sequence, cycle.Intent.Promoted)
		}
	}
	if !decayed {
		t.Error("no cycle had decayed work waiting")
	}

	judged := 0
	for _, event := range h.recorder.seismic {
		judged += len(event.Intent)
	}
	if judged == 0 {
		t.Error("no event records what intent made of it")
	}

	if len(h.recorder.intentChanges) != 1 {
		t.Fatalf("intent changes = %+v, want the initial settings only", h.recorder.intentChanges)
	}
	if first := h.recorder.intentChanges[0]; first.Version != 1 || first.Cycle != 1 || first.Source != domain.IntentSourceInitial {
		t.Errorf("first change = %+v, want version 1 in force from cycle 1", first)
	}
	if h.recorder.provenance == nil || h.recorder.provenance.Intent == nil ||
		h.recorder.provenance.Intent.Settings.Mode != domain.IntentDecay {
		t.Errorf("provenance = %+v, want the intent the run began with", h.recorder.provenance)
	}
}

// Decay can only relax: the SLA judged at the level a job ends at can only be
// kinder than the one it was submitted under.
func TestDecayedWorkBreachesNoMoreThanItWouldAsSubmitted(t *testing.T) {
	h := newHarness(t)
	spec := simulationSpec()
	spec.Settings = json.RawMessage(`{"local_executor_cap": 3, "cloud_executor_cap": 0}`)
	spec.Intent = intentSpec(`{}`)

	metrics, err := h.engine.Execute(context.Background(), spec)
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if metrics.SLABreachesAsSubmitted == 0 {
		t.Fatal("no breaches at all on three executors; the test needs a queue under pressure")
	}
	if metrics.SLABreaches > metrics.SLABreachesAsSubmitted {
		t.Errorf("%d breaches at the levels jobs ended at, %d as submitted", metrics.SLABreaches, metrics.SLABreachesAsSubmitted)
	}
}

func TestAPlannedChangeTakesEffectAtItsCycle(t *testing.T) {
	h := newHarness(t)
	spec := simulationSpec()
	spec.Intent = intentSpec(`{}`, domain.IntentStep{Cycle: 40, Settings: json.RawMessage(`{"mode":"off"}`)})

	if _, err := h.engine.Execute(context.Background(), spec); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	changes := h.recorder.intentChanges
	if len(changes) != 2 || changes[1].Cycle != 40 || changes[1].Version != 2 ||
		changes[1].Source != domain.IntentSourceSchedule || changes[1].Settings.Mode != domain.IntentOff {
		t.Fatalf("changes = %+v, want off from cycle 40 as version 2", changes)
	}
	for _, cycle := range h.recorder.cycles {
		want := 1
		if cycle.Sequence >= 40 {
			want = 2
		}
		if cycle.Intent.Version != want {
			t.Fatalf("cycle %d under intent version %d, want %d", cycle.Sequence, cycle.Intent.Version, want)
		}
		if cycle.Sequence >= 40 && (cycle.Intent.Decayed > 0) {
			t.Errorf("cycle %d: %d jobs still decayed after intent was switched off", cycle.Sequence, cycle.Intent.Decayed)
		}
	}
}

// Exempt work reaches the autoscaler as exempt, which is the whole of what
// exemption asks of this service.
func TestWorkIntentExemptsIsSentToTheAutoscalerAsExempt(t *testing.T) {
	h := newHarness(t)
	spec := simulationSpec()
	spec.Intent = intentSpec(`{"burst_exempt":["decayed"]}`)

	if _, err := h.engine.Execute(context.Background(), spec); err != nil {
		t.Fatalf("Execute() = %v", err)
	}

	sent := 0
	for i, call := range h.fake.Cycles {
		sent += call.ExemptDepth
		if cycle := h.recorder.cycles[i]; call.ExemptDepth != cycle.Intent.Exempt {
			t.Fatalf("cycle %d: %d exempt jobs waiting, %d sent as exempt", cycle.Sequence, cycle.Intent.Exempt, call.ExemptDepth)
		}
	}
	if sent == 0 {
		t.Error("no exempt work was ever sent")
	}
}

// An operator's change while the run is in flight is in force from the next
// cycle, and recorded as such — and replaying the recorded changes as a
// schedule reproduces the run exactly.
func TestAnOperatorsChangeIsRecordedAndReplaysExactly(t *testing.T) {
	control := intent.NewControl(intentSpec(`{"mode":"both"}`).Settings)
	h := newHarness(t)
	paced := 0
	h.engine.WithClock(func(time.Duration) {
		paced++
		if paced == 30 {
			if _, _, err := control.Patch([]byte(`{"mode":"decay","burst_exempt":["decayed"]}`), nil, domain.IntentSourceOperator); err != nil {
				t.Errorf("Patch() = %v", err)
			}
			if _, _, err := control.Patch([]byte(`{"lookahead_seconds":60}`), nil, domain.IntentSourceOperator); err != nil {
				t.Errorf("Patch() = %v", err)
			}
		}
	}, func() time.Time { return start })
	spec := simulationSpec()
	spec.Run.TimeCompression = 1
	spec.Intent = intentSpec(`{"mode":"both"}`)
	spec.Control = control

	if _, err := h.engine.Execute(context.Background(), spec); err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	changes := h.recorder.intentChanges
	if len(changes) != 2 || changes[1].Cycle != 31 || changes[1].Version != 3 || changes[1].Source != domain.IntentSourceOperator {
		t.Fatalf("changes = %+v, want the operator's two edits in force together from cycle 31 as version 3", changes)
	}

	replay := newHarness(t)
	again := simulationSpec()
	again.Intent = &domain.RunIntent{Settings: changes[0].Settings}
	for _, change := range changes[1:] {
		settings, _ := json.Marshal(change.Settings)
		again.Intent.Schedule = append(again.Intent.Schedule, domain.IntentStep{
			Cycle: change.Cycle, Settings: settings, Version: change.Version, Source: change.Source,
		})
	}
	if _, err := replay.engine.Execute(context.Background(), again); err != nil {
		t.Fatalf("replay Execute() = %v", err)
	}

	if len(replay.recorder.cycles) != len(h.recorder.cycles) {
		t.Fatalf("replay has %d cycles, original %d", len(replay.recorder.cycles), len(h.recorder.cycles))
	}
	for i := range h.recorder.cycles {
		a, b := h.recorder.cycles[i], replay.recorder.cycles[i]
		if !reflect.DeepEqual(a.Intent, b.Intent) || !reflect.DeepEqual(a.Queues, b.Queues) || a.PlanCloud != b.PlanCloud {
			t.Fatalf("cycle %d differs:\noriginal %+v\nreplay   %+v", a.Sequence, a, b)
		}
	}
	for i, change := range replay.recorder.intentChanges {
		original := changes[i]
		if change.Version != original.Version || change.Cycle != original.Cycle || change.Source != original.Source ||
			!reflect.DeepEqual(change.Settings, original.Settings) {
			t.Errorf("replayed change %+v, original %+v", change, original)
		}
	}
}
