// Package autoscaler is Simlab's client for the autoscaler service.
//
// The types here are Simlab's own, deliberately duplicated rather than shared
// through a common module. The two services are separately deployable and
// separately versioned, and a shared struct would make a field rename in one
// a compile error in the other — which is precisely the coupling that having
// an HTTP contract between them is supposed to avoid. What is shared is the
// contract in the autoscaler's openapi.yaml, and these types follow it.
package autoscaler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxResponseBytes bounds what a reply may be. A run reads thousands of these
// and an unbounded read is a memory exhaustion waiting to happen.
const maxResponseBytes = 8 << 20

// Error is a non-2xx reply, carrying the autoscaler's own explanation.
type Error struct {
	Status  int
	Message string
	Path    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("autoscaler %s returned %d: %s", e.Path, e.Status, e.Message)
}

// NotFound reports whether an error is the autoscaler saying a target does not
// exist, which a caller usually wants to handle rather than propagate.
func NotFound(err error) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusNotFound
	}
	return false
}

// Client talks to one autoscaler.
type Client struct {
	http    *http.Client
	baseURL string
	token   string
}

// New builds a client.
func New(baseURL, token string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("an autoscaler base URL is required")
	}
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("autoscaler base URL %q is not usable: %w", baseURL, err)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		http:    &http.Client{Timeout: timeout},
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
	}, nil
}

// QueueInfo is one priority level's aggregate state, on the wire.
type QueueInfo struct {
	Depth               int     `json:"depth"`
	OldestJobAgeSeconds float64 `json:"oldest_job_age_seconds"`
	ArrivalRate         float64 `json:"arrival_rate_per_second"`
}

// Workload is the queue handed to the autoscaler for a driven decision.
type Workload struct {
	Queues             map[string]QueueInfo `json:"queues"`
	ExecutorThroughput float64              `json:"executor_throughput_per_second"`
}

// Capacity is what the platform reports as running.
type Capacity struct {
	LocalReady   int `json:"local_ready"`
	CloudReady   int `json:"cloud_ready"`
	LocalPending int `json:"local_pending"`
	CloudPending int `json:"cloud_pending"`
}

// Plan is the capacity a decision asks for.
type Plan struct {
	LocalExecutors int `json:"local_executors"`
	CloudExecutors int `json:"cloud_executors"`
}

// Projection is what the autoscaler predicted under its chosen plan.
type Projection struct {
	BreachExpected      bool    `json:"breach_expected"`
	FirstBreachPriority int     `json:"first_breach_priority,omitempty"`
	FirstBreachSeconds  float64 `json:"first_breach_in_seconds,omitempty"`
	PeakQueueDepth      int     `json:"peak_queue_depth"`
	DrainedAtSeconds    float64 `json:"drained_at_seconds,omitempty"`
}

// Decision is one cycle's answer.
type Decision struct {
	At              time.Time  `json:"at"`
	Previous        Plan       `json:"previous"`
	Plan            Plan       `json:"plan"`
	Action          string     `json:"action"`
	Reason          string     `json:"reason"`
	Projection      Projection `json:"projection"`
	SettingsVersion int64      `json:"settings_version"`
	Constrained     bool       `json:"constrained"`
	Constraint      string     `json:"constraint,omitempty"`
}

// CycleRequest drives one decision.
type CycleRequest struct {
	At       time.Time `json:"at,omitempty"`
	Workload *Workload `json:"workload,omitempty"`
}

// CycleResult is what came back.
type CycleResult struct {
	Decision    Decision `json:"decision"`
	Observation struct {
		Capacity Capacity  `json:"capacity"`
		Workload *Workload `json:"workload,omitempty"`
	} `json:"observation"`
	Applied *struct {
		Applied Plan   `json:"applied"`
		Changed bool   `json:"changed"`
		Detail  string `json:"detail,omitempty"`
	} `json:"applied,omitempty"`
}

// Target is a registered scalable workload.
type Target struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Kind        string            `json:"kind"`
	Mode        string            `json:"mode,omitempty"`
	Config      map[string]string `json:"config,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

// TargetSnapshot is a target as the autoscaler reports it.
type TargetSnapshot struct {
	Target   Target `json:"target"`
	Settings struct {
		Version  int64           `json:"version"`
		Settings json.RawMessage `json:"settings"`
	} `json:"settings"`
	LastError string `json:"last_error,omitempty"`
}

// SettingsSnapshot is one version of a target's settings.
type SettingsSnapshot struct {
	Version  int64           `json:"version"`
	Settings json.RawMessage `json:"settings"`
	Warnings []string        `json:"warnings,omitempty"`
}

// PlatformSchema describes what a platform needs, for the UI to render.
type PlatformSchema struct {
	Kind         string          `json:"kind"`
	Summary      string          `json:"summary"`
	SeesWorkload bool            `json:"sees_workload"`
	Config       []PlatformField `json:"config,omitempty"`
	Credentials  []PlatformField `json:"credentials,omitempty"`
}

// PlatformField is one setting a platform needs.
type PlatformField struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Example     string `json:"example,omitempty"`
	Required    bool   `json:"required"`
}

// Platforms lists what the autoscaler can scale.
func (c *Client) Platforms(ctx context.Context) ([]PlatformSchema, error) {
	var reply struct {
		Platforms []PlatformSchema `json:"platforms"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/platforms", nil, nil, &reply); err != nil {
		return nil, err
	}
	return reply.Platforms, nil
}

// ListTargets is every target the autoscaler knows about.
func (c *Client) ListTargets(ctx context.Context) ([]TargetSnapshot, error) {
	var reply struct {
		Targets []TargetSnapshot `json:"targets"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/targets", nil, nil, &reply); err != nil {
		return nil, err
	}
	return reply.Targets, nil
}

// GetTarget fetches one target.
func (c *Client) GetTarget(ctx context.Context, id string) (TargetSnapshot, error) {
	var reply TargetSnapshot
	err := c.do(ctx, http.MethodGet, "/v1/targets/"+url.PathEscape(id), nil, nil, &reply)
	return reply, err
}

// CreateTarget registers a target, with an optional settings patch.
func (c *Client) CreateTarget(ctx context.Context, target Target,
	settings json.RawMessage) (TargetSnapshot, error) {
	body := map[string]any{"target": target}
	if len(settings) > 0 {
		body["settings"] = settings
	}

	var reply TargetSnapshot
	err := c.do(ctx, http.MethodPost, "/v1/targets", nil, body, &reply)
	return reply, err
}

// DeleteTarget removes a target.
func (c *Client) DeleteTarget(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/targets/"+url.PathEscape(id), nil, nil, nil)
}

// GetSettings reads a target's settings.
func (c *Client) GetSettings(ctx context.Context, id string) (SettingsSnapshot, error) {
	var reply SettingsSnapshot
	err := c.do(ctx, http.MethodGet, "/v1/targets/"+url.PathEscape(id)+"/settings", nil, nil, &reply)
	return reply, err
}

// ApplySettings changes a target's settings while it runs.
func (c *Client) ApplySettings(ctx context.Context, id string, patch json.RawMessage,
	expectedVersion *int64, actor string) (SettingsSnapshot, error) {
	query := url.Values{}
	if expectedVersion != nil {
		query.Set("expected_version", strconv.FormatInt(*expectedVersion, 10))
	}

	var reply SettingsSnapshot
	err := c.doWithActor(ctx, http.MethodPatch,
		"/v1/targets/"+url.PathEscape(id)+"/settings", query, json.RawMessage(patch), &reply, actor)
	return reply, err
}

// Cycle asks for one decision. This is the call a run is built out of.
func (c *Client) Cycle(ctx context.Context, id string, request CycleRequest) (CycleResult, error) {
	var reply CycleResult
	err := c.do(ctx, http.MethodPost, "/v1/targets/"+url.PathEscape(id)+"/cycle",
		nil, request, &reply)
	return reply, err
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values,
	body any, out any) error {
	return c.doWithActor(ctx, method, path, query, body, out, "")
}

func (c *Client) doWithActor(ctx context.Context, method, path string, query url.Values,
	body any, out any, actor string) error {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request to %s: %w", path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("building the request to %s: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if actor != "" {
		// Recorded in the autoscaler's own change log, so a settings change
		// made through Simlab is attributable to whoever made it there.
		req.Header.Set("X-Autoscaler-Actor", actor)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling the autoscaler at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("reading the reply from %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Message: message(payload), Path: path}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decoding the reply from %s: %w", path, err)
	}
	return nil
}

// message pulls the autoscaler's own explanation out of an error reply. That
// message is written for an operator and is almost always the useful one.
func message(payload []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(payload, &body); err == nil && body.Error != "" {
		return body.Error
	}
	if len(payload) > 400 {
		return string(payload[:400])
	}
	return string(payload)
}
