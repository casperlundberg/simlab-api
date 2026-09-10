package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/casperlundberg/simlab-api/internal/autoscaler"
)

// These endpoints stand in front of the autoscaler.
//
// The browser never talks to the autoscaler directly, and this is why: the
// autoscaler's API token unlocks every platform credential it holds —
// Kubernetes tokens, ColonyOS private keys, Docker certificates — and a token
// that reaches a browser is a token that has been disclosed. Keeping the
// relationship on this side means the client needs no credential at all.
//
// The responses are passed through as the autoscaler shaped them, so the
// client works against one contract rather than a Simlab-flavoured restatement
// of it that would drift.

func (s *server) listPlatforms(w http.ResponseWriter, r *http.Request) {
	platforms, err := s.Autoscaler.Platforms(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"platforms": platforms})
}

func (s *server) listTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.Autoscaler.ListTargets(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

func (s *server) getTarget(w http.ResponseWriter, r *http.Request) {
	target, err := s.Autoscaler.GetTarget(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, target)
}

// targetRequest registers a real target — a cluster, a ColonyOS colony, a
// Docker host — through Simlab.
type targetRequest struct {
	Target   autoscaler.Target `json:"target"`
	Settings json.RawMessage   `json:"settings,omitempty"`
}

func (s *server) createTarget(w http.ResponseWriter, r *http.Request) {
	var request targetRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	created, err := s.Autoscaler.CreateTarget(r.Context(), request.Target, request.Settings)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *server) deleteTarget(w http.ResponseWriter, r *http.Request) {
	if err := s.Autoscaler.DeleteTarget(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getTargetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Autoscaler.GetSettings(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// patchTargetSettings changes a live autoscaler's policy.
//
// This is the endpoint that makes the settings editor real rather than
// decorative: a change here reaches the running controller and is in force on
// its next decision cycle.
func (s *server) patchTargetSettings(w http.ResponseWriter, r *http.Request) {
	patch, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading the settings document: "+err.Error())
		return
	}

	var expected *int64
	if raw := r.URL.Query().Get("expected_version"); raw != "" {
		version, err := parseInt64(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "expected_version is not a version number")
			return
		}
		expected = &version
	}

	// Who made the change, carried through to the autoscaler's own change log
	// so it is attributable there rather than showing up as "simlab".
	actor := r.Header.Get("X-Simlab-Actor")
	if actor == "" {
		actor = "simlab"
	}

	updated, err := s.Autoscaler.ApplySettings(r.Context(), r.PathValue("id"),
		json.RawMessage(patch), expected, actor)
	if err != nil {
		if apiErr := asAutoscalerError(err); apiErr != nil && apiErr.Status == http.StatusConflict {
			writeError(w, http.StatusConflict, apiErr.Message)
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *server) getTargetStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.Autoscaler.TargetStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
