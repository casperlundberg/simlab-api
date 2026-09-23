package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/usecase"
)

// scoreUseCase scores a run for a use case: every decision the case would have
// had to make in it, and whether what each needed arrived in time.
//
// A POST because it takes a case and its parameters as a document, as intent
// patches do; it changes nothing. The score is computed from what the run
// stored — never recorded as a result of its own — so any case can be asked of
// any run, including one recorded before the case existed, and the same
// question always gets the same answer.
func (s *server) scoreUseCase(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind   string          `json:"kind"`
		Params json.RawMessage `json:"params"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Kind == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("kind is required: one of %s",
			strings.Join(usecase.Kinds(), ", ")))
		return
	}
	c, err := usecase.New(body.Kind, body.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id := r.PathValue("id")
	world, record, err := s.recorded(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	outcomes := usecase.Score(c.Opportunities(world), record)
	if outcomes == nil {
		outcomes = []usecase.Outcome{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kind": c.Kind(), "params": c.Params(), "summary": usecase.Summarise(outcomes), "outcomes": outcomes,
	})
}

// recorded is a run as it stored itself: its world and what the mine had when.
func (s *server) recorded(ctx context.Context, runID string) (usecase.World, usecase.Record, error) {
	// The layout first: it is what says whether the run exists at all.
	layout, err := s.Store.RunLayout(ctx, runID)
	if err != nil {
		return usecase.World{}, usecase.Record{}, err
	}
	entities, err := s.Store.Entities(ctx, runID)
	if err != nil {
		return usecase.World{}, usecase.Record{}, err
	}
	var events []domain.SeismicEvent
	for from := 0; ; {
		page, err := s.Store.SeismicEvents(ctx, runID, from, 20000)
		if err != nil {
			return usecase.World{}, usecase.Record{}, err
		}
		if len(page) == 0 {
			break
		}
		events = append(events, page...)
		from = page[len(page)-1].Sequence
	}
	world, record := usecase.FromRun(events, entities, layout.Tunnels)
	return world, record, nil
}

// closureMap draws a run's map of ground to keep out of, from the locations
// the mine had, and measures it against the ground the events really made
// dangerous: use case 2.
//
// A POST for the same reason scoreUseCase is one, and computed from what the
// run stored in the same way, so any recorded run can be asked.
func (s *server) closureMap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Params json.RawMessage `json:"params"`
	}
	if err := decode(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := usecase.NewClosure(body.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	world, record, err := s.recorded(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	drawn := c.Map(world, record)
	writeJSON(w, http.StatusOK, map[string]any{
		"params": c.Params(), "summary": drawn.Summary, "events": drawn.Events,
	})
}
