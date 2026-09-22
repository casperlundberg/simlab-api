package workload_test

import (
	"testing"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/seismic"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// Work is the processing as the mine sees it before any of it is done, and
// nothing of the world beyond: no hypocentre, no magnitude.
func TestWorkIsTheJobsAndWhatTheMineHadOfEachEventAsItsSensorsTriggered(t *testing.T) {
	sensors := []domain.Sensor{{ID: "a", At: domain.Point{X: 1}}, {ID: "b", At: domain.Point{X: 2}}, {ID: "c", At: domain.Point{X: 3}}}
	w := workload.Workload{
		Layout: domain.Layout{Sensors: sensors},
		Jobs: []workload.Job{
			{ID: 0, Priority: 100, Event: 0}, {ID: 1, Priority: 50, Event: 0},
			{ID: 2, Priority: 25, Event: 1},
		},
		Events: []workload.Event{
			{Origin: 10 * time.Second, Truth: domain.Point{X: 99}, Magnitude: 2, FirstJob: 0,
				Picks: []seismic.Pick{{SensorID: "c"}, {SensorID: "a"}}},
			{Origin: 20 * time.Second, Truth: domain.Point{X: 98}, Magnitude: 1, FirstJob: 2,
				Picks: []seismic.Pick{{SensorID: "b"}}},
		},
	}
	got := w.Work()

	if want := []domain.Priority{100, 50, 25}; len(got.Priorities) != 3 || got.Priorities[0] != want[0] ||
		got.Priorities[1] != want[1] || got.Priorities[2] != want[2] {
		t.Errorf("priorities = %v, want %v", got.Priorities, want)
	}
	if len(got.Events) != 2 {
		t.Fatalf("%d events, want 2", len(got.Events))
	}
	first := got.Events[0]
	if first.Origin != 10*time.Second || first.FirstJob != 0 || first.Picks != 2 ||
		len(first.Triggers) != 2 || first.Triggers[0] != (domain.Point{X: 3}) || first.Triggers[1] != (domain.Point{X: 1}) {
		t.Errorf("first event = %+v; want its two sensors' positions in the order they triggered", first)
	}
	if got.Events[1].FirstJob != 2 || got.Events[1].Picks != 1 {
		t.Errorf("second event = %+v", got.Events[1])
	}
}
