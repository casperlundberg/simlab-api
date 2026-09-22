package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/observe"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// runGround is the ground a run's planner protects at a moment: for each
// protected unit, where it is or could be over the lookahead, as the knowledge
// asked for reads it. Served from the same views the run engine builds, so a
// page drawing it draws what intent decided from, rather than recomputing it
// its own way.
//
// The lookahead, who is protected and the knowledge are query parameters, not
// read from the run: a page shows intent as it stood at the moment on screen,
// which may be a change made mid-run. They are checked as an intent patch is,
// so a value the planner would refuse is refused here in the same words.
func (s *server) runGround(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	raw := query.Get("at_seconds")
	at, err := strconv.ParseFloat(raw, 64)
	if raw == "" || err != nil || at < 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at_seconds must be a number of seconds >= 0, got %q", raw))
		return
	}
	patch := map[string]any{}
	if v := query.Get("lookahead_seconds"); v != "" {
		seconds, err := strconv.ParseFloat(v, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("lookahead_seconds must be a number of seconds, got %q", v))
			return
		}
		patch["lookahead_seconds"] = seconds
	}
	if v := query.Get("protect"); v != "" {
		patch["protect"] = strings.Split(v, ",")
	}
	if v := query.Get("knowledge"); v != "" {
		patch["knowledge"] = v
	}
	encoded, _ := json.Marshal(patch)
	settings, err := domain.DefaultIntent().Patched(encoded)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	views, err := s.views.get(r.Context(), r.PathValue("id"), s.buildViews)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	moment := time.Duration(at * float64(time.Second))
	ground := views.For(settings.Knowledge).Reach(moment, settings.Lookahead, settings.Protects)
	if ground == nil {
		ground = []observe.Path{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"at_seconds": at, "knowledge": settings.Knowledge, "ground": ground,
	})
}

// buildViews reads a run's units and tunnels as recorded and builds its views
// the way the run engine did.
func (s *server) buildViews(ctx context.Context, runID string) (observe.Views, error) {
	layout, err := s.Store.RunLayout(ctx, runID)
	if err != nil {
		return observe.Views{}, err
	}
	entities, err := s.Store.Entities(ctx, runID)
	if err != nil {
		return observe.Views{}, err
	}
	return observe.ViewsOf(entities, layout.Tunnels, workload.WalkingSpeed), nil
}

// viewCache keeps the views of the last few runs asked about. Building one
// measures the distance between every pair of junctions in the mine, and a
// page playing a run back asks about the same run many times a second.
type viewCache struct {
	mu    sync.Mutex
	order []string
	views map[string]observe.Views
}

const viewsKept = 8

func newViewCache() *viewCache { return &viewCache{views: map[string]observe.Views{}} }

func (c *viewCache) get(ctx context.Context, runID string,
	build func(context.Context, string) (observe.Views, error)) (observe.Views, error) {
	c.mu.Lock()
	if v, ok := c.views[runID]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	// Built outside the lock: two requests for a new run may both build it,
	// which costs a little work, rather than every other run waiting on one.
	v, err := build(ctx, runID)
	if err != nil {
		return observe.Views{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.views[runID]; !ok {
		c.order = append(c.order, runID)
		if len(c.order) > viewsKept {
			delete(c.views, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.views[runID] = v
	return v, nil
}
