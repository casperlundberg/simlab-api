package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/intent"
)

// getRunIntent is a run's intent: what is in force now, and every change
// recorded with the cycle it took effect from.
//
// For a run in flight the settings are the live ones, which may be a version
// the run has not reached a cycle boundary to apply yet; changes lists only
// what has taken effect.
func (s *server) getRunIntent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	created, err := s.Store.RunIntent(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if created == nil {
		writeError(w, http.StatusNotFound, "run "+id+" has no intent: it is a live run, "+
			"or it was created before intent existed and reordered nothing")
		return
	}
	changes, err := s.Store.IntentChanges(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	settings, version := created.Settings, 1
	if len(changes) > 0 {
		last := changes[len(changes)-1]
		settings, version = last.Settings, last.Version
	}
	control, active := s.Manager.Intent(id)
	if active {
		settings, version, _ = control.Current()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":   id,
		"active":   active,
		"settings": settings,
		"version":  version,
		"schedule": created.Schedule,
		"changes":  changes,
	})
}

// patchRunIntent changes an in-flight run's intent, from its next cycle.
func (s *server) patchRunIntent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	control, active := s.Manager.Intent(id)
	if !active {
		writeError(w, http.StatusConflict, "run "+id+" is not in flight, so its intent cannot change; "+
			"a finished run's intent is part of its record")
		return
	}

	patch, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading the intent document: "+err.Error())
		return
	}
	var expected *int
	if raw := r.URL.Query().Get("expected_version"); raw != "" {
		version, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "expected_version is not a version number")
			return
		}
		expected = &version
	}

	settings, version, err := control.Patch(patch, expected, domain.IntentSourceOperator)
	switch {
	case errors.Is(err, intent.ErrVersionConflict):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": id, "active": true, "settings": settings, "version": version,
	})
}
