package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// runRequest is what a caller sends to start a run.
type runRequest struct {
	Name       string `json:"name,omitempty"`
	Mode       string `json:"mode"`
	ScenarioID string `json:"scenario_id,omitempty"`

	// TargetID names the autoscaler target for a live run. A simulation run
	// leaves it empty and gets a target of its own, created and removed with
	// the run.
	TargetID string `json:"target_id,omitempty"`

	DecisionIntervalSeconds float64 `json:"decision_interval_seconds,omitempty"`
	TimeCompression         float64 `json:"time_compression,omitempty"`

	// Settings is the autoscaler settings patch this run is executed under. It
	// is how one scenario is replayed under different policies, which is the
	// comparison the whole app exists to make.
	Settings json.RawMessage `json:"settings,omitempty"`
}

// Defaults chosen so a caller can post {"mode":"simulation","scenario_id":...}
// and get a sensible run.
const (
	defaultDecisionInterval = 15 * time.Second
	defaultCompression      = 600
)

func (s *server) createRun(w http.ResponseWriter, r *http.Request) {
	var request runRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id, err := newID("run")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	created := domain.Run{
		ID:               id,
		Name:             request.Name,
		Mode:             domain.RunMode(request.Mode),
		ScenarioID:       request.ScenarioID,
		TargetID:         request.TargetID,
		Status:           domain.StatusPending,
		DecisionInterval: defaultDecisionInterval,
	}
	if request.DecisionIntervalSeconds > 0 {
		created.DecisionInterval = time.Duration(request.DecisionIntervalSeconds * float64(time.Second))
	}

	if created.Mode == domain.ModeSimulation {
		// The run's target is its own, created and removed with it. A shared
		// one would start each run holding the previous run's executors.
		created.TargetID = id
		created.SimulatedStart = time.Now().UTC().Truncate(time.Second)
		created.TimeCompression = defaultCompression
		if request.TimeCompression > 0 {
			created.TimeCompression = request.TimeCompression
		}
	}

	if err := created.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if created.Mode == domain.ModeSimulation {
		// Fail here, where the caller is looking, rather than inside a
		// background run they will have to go and read the status of.
		if _, err := s.Store.Scenario(r.Context(), created.ScenarioID); err != nil {
			writeStoreError(w, err)
			return
		}
	}

	if err := s.Store.SaveRun(r.Context(), created, request.Settings); err != nil {
		writeStoreError(w, err)
		return
	}

	spec, err := s.Manager.Prepare(r.Context(), created.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.Manager.Start(spec); err != nil {
		writeStoreError(w, err)
		return
	}

	stored, err := s.Store.Run(r.Context(), created.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, stored)
}

func (s *server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	runs, err := s.Store.Runs(r.Context(),
		domain.RunStatus(r.URL.Query().Get("status")), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Whether a run is in flight is a live fact the database cannot answer:
	// a process that was killed leaves a run stuck at "running" in the table.
	active := map[string]bool{}
	for _, id := range s.Manager.Active() {
		active[id] = true
	}

	out := make([]map[string]any, 0, len(runs))
	for _, stored := range runs {
		out = append(out, map[string]any{"run": stored, "active": active[stored.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (s *server) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	stored, err := s.Store.Run(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	body := map[string]any{"run": stored, "active": s.Manager.IsActive(id)}
	if metrics, err := s.Store.Metrics(r.Context(), id); err == nil {
		body["metrics"] = metrics
	}
	if settings, err := s.Store.RunSettings(r.Context(), id); err == nil && len(settings) > 0 {
		body["settings"] = json.RawMessage(settings)
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *server) deleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.Manager.IsActive(id) {
		// Deleting the rows underneath a running engine would have it fail on
		// its next write for a reason nobody could reconstruct.
		writeError(w, http.StatusConflict,
			"run "+id+" is still in flight; cancel it before deleting it")
		return
	}

	if err := s.Store.DeleteRun(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Manager.Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "cancelling"})
}

func (s *server) runCycles(w http.ResponseWriter, r *http.Request) {
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	cycles, err := s.Store.Cycles(r.Context(), r.PathValue("id"), from, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// next tells a client where to resume, which is what lets a page follow a
	// run without re-fetching a timeline that is already thousands of rows.
	next := from
	if len(cycles) > 0 {
		next = cycles[len(cycles)-1].Sequence
	}
	writeJSON(w, http.StatusOK, map[string]any{"cycles": cycles, "next": next})
}

// runLayout is the sensor array a run's virtual mine was replayed against.
func (s *server) runLayout(w http.ResponseWriter, r *http.Request) {
	layout, err := s.Store.RunLayout(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"layout": layout})
}

// runSeismicity is a run's events, paged the way its cycles are: a long
// scenario has tens of thousands, and a page following a live run should not
// refetch the ones it already has.
func (s *server) runSeismicity(w http.ResponseWriter, r *http.Request) {
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	events, err := s.Store.SeismicEvents(r.Context(), r.PathValue("id"), from, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	next := from
	if len(events) > 0 {
		next = events[len(events)-1].Sequence
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "next": next})
}

// checkGeometry refuses a scenario whose bursts happen outside its mine.
//
// The run would refuse it too, but only once started, as a failed run somebody
// has to go and read. A mine that does not exist is left for the store to
// report, as it always has been.
func (s *server) checkGeometry(ctx context.Context, scenario domain.Scenario) error {
	mine, err := s.Store.Mine(ctx, scenario.MineID)
	if err != nil {
		return nil
	}
	return workload.CheckGeometry(mine, scenario)
}

func (s *server) runMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.Store.Metrics(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (s *server) runEvents(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, r.PathValue("id"))
}

func (s *server) allEvents(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, "")
}

// stream serves server-sent events for a run, or for everything.
//
// SSE rather than websockets: this is one-directional, it survives proxies
// that mangle upgrades, and a browser reconnects on its own. The client that
// reconnects re-reads the run's cycles from the database, so a dropped
// connection costs nothing.
func (s *server) stream(w http.ResponseWriter, r *http.Request, runID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "this server cannot stream")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Nginx and friends buffer by default, which turns a live stream into one
	// long silence followed by everything at once.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	stream, unsubscribe := s.Hub.Subscribe(runID)
	defer unsubscribe()

	// An immediate comment gets the response committed, so a client knows the
	// stream is open before anything has happened.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// A heartbeat keeps intermediaries from closing an idle connection during
	// a quiet stretch of a long run.
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case event, open := <-stream:
			if !open {
				return
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, encoded)
			flusher.Flush()
		}
	}
}
