package workload

import "github.com/casperlundberg/simlab-api/internal/domain"

// Work is the workload as the mine sees it before any of it is done — the
// jobs' priorities and each event's triggers — for whatever orders the work
// and must not read where events really were.
func (w Workload) Work() domain.Work {
	sensors := make(map[string]domain.Point, len(w.Layout.Sensors))
	for _, s := range w.Layout.Sensors {
		sensors[s.ID] = s.At
	}
	out := domain.Work{
		Priorities: make([]domain.Priority, len(w.Jobs)),
		Events:     make([]domain.EventWork, len(w.Events)),
	}
	for i, job := range w.Jobs {
		out.Priorities[i] = job.Priority
	}
	for i, event := range w.Events {
		triggers := make([]domain.Point, 0, len(event.Picks))
		for _, pick := range event.Picks {
			if at, ok := sensors[pick.SensorID]; ok {
				triggers = append(triggers, at)
			}
		}
		out.Events[i] = domain.EventWork{Origin: event.Origin, FirstJob: event.FirstJob, Picks: len(event.Picks), Triggers: triggers}
	}
	return out
}
