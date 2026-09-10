package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// SaveRun inserts or replaces a run.
func (s *Store) SaveRun(ctx context.Context, run domain.Run, settings json.RawMessage) error {
	if err := run.Validate(); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO runs (id, name, scenario_id, target_id, mode, status, simulated_start,
			time_compression, decision_interval_ms, settings, started_at, finished_at, error)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name, status = EXCLUDED.status,
			started_at = EXCLUDED.started_at, finished_at = EXCLUDED.finished_at,
			error = EXCLUDED.error`,
		run.ID, run.Name, run.ScenarioID, run.TargetID, run.Mode, run.Status,
		nullableTime(run.SimulatedStart), nullableFloat(run.TimeCompression),
		run.DecisionInterval.Milliseconds(), nullableJSON(settings),
		nullableTime(run.StartedAt), nullableTime(run.FinishedAt), run.Error)
	if err != nil {
		return fmt.Errorf("saving run %q: %w", run.ID, err)
	}
	return nil
}

// SetStatus records where a run has got to.
//
// A terminal status stamps the finish time in the same statement, so a run
// cannot end up completed with no record of when — which is the state that
// makes a list of runs impossible to sort sensibly.
func (s *Store) SetStatus(ctx context.Context, runID string, status domain.RunStatus, failure string) error {
	query := `UPDATE runs SET status = $2, error = $3 WHERE id = $1`
	if status == domain.StatusRunning {
		query = `UPDATE runs SET status = $2, error = $3, started_at = now() WHERE id = $1`
	}
	if status.Terminal() {
		query = `UPDATE runs SET status = $2, error = $3, finished_at = now() WHERE id = $1`
	}

	tag, err := s.pool.Exec(ctx, query, runID, status, failure)
	if err != nil {
		return fmt.Errorf("setting the status of run %q: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %q", ErrNotFound, runID)
	}
	return nil
}

// Run fetches one run.
func (s *Store) Run(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.scanRun(s.pool.QueryRow(ctx, runColumns+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Run{}, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	return run, err
}

// RunSettings is the settings a run was executed under, as stored.
func (s *Store) RunSettings(ctx context.Context, id string) (json.RawMessage, error) {
	var settings []byte
	err := s.pool.QueryRow(ctx, `SELECT settings FROM runs WHERE id = $1`, id).Scan(&settings)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the settings of run %q: %w", id, err)
	}
	return settings, nil
}

// Runs lists runs, newest first, optionally filtered by status.
func (s *Store) Runs(ctx context.Context, status domain.RunStatus, limit int) ([]domain.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := runColumns
	args := []any{}
	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ` + strconv.Itoa(limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing runs: %w", err)
	}
	defer rows.Close()

	runs := []domain.Run{}
	for rows.Next() {
		run, err := s.scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// DeleteRun removes a run and everything recorded for it.
func (s *Store) DeleteRun(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM runs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting run %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	return nil
}

const runColumns = `
	SELECT id, name, COALESCE(scenario_id, ''), target_id, mode, status,
		simulated_start, time_compression, decision_interval_ms,
		started_at, finished_at, error, created_at
	FROM runs`

func (s *Store) scanRun(row scannable) (domain.Run, error) {
	var (
		run            domain.Run
		simulatedStart *time.Time
		compression    *float64
		intervalMs     int64
		startedAt      *time.Time
		finishedAt     *time.Time
	)
	if err := row.Scan(&run.ID, &run.Name, &run.ScenarioID, &run.TargetID, &run.Mode,
		&run.Status, &simulatedStart, &compression, &intervalMs,
		&startedAt, &finishedAt, &run.Error, &run.CreatedAt); err != nil {
		return domain.Run{}, err
	}

	run.DecisionInterval = time.Duration(intervalMs) * time.Millisecond
	if simulatedStart != nil {
		run.SimulatedStart = *simulatedStart
	}
	if compression != nil {
		run.TimeCompression = *compression
	}
	if startedAt != nil {
		run.StartedAt = *startedAt
	}
	if finishedAt != nil {
		run.FinishedAt = *finishedAt
	}
	return run, nil
}

// SaveCycle records one decision.
//
// Upsert rather than insert: a run that is retried re-emits the same sequence
// numbers, and failing on the second attempt would make a retry impossible for
// the least interesting reason.
func (s *Store) SaveCycle(ctx context.Context, cycle domain.Cycle) error {
	queues, err := json.Marshal(queuesToWire(cycle.Queues))
	if err != nil {
		return fmt.Errorf("encoding the queues of cycle %d: %w", cycle.Sequence, err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO run_cycles (run_id, sequence, at, queues, local_ready, cloud_ready,
			local_pending, cloud_pending, action, plan_local, plan_cloud, reason,
			constraint_name, settings_version, breach_expected, completed, breached)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (run_id, sequence) DO UPDATE SET
			at = EXCLUDED.at, queues = EXCLUDED.queues,
			local_ready = EXCLUDED.local_ready, cloud_ready = EXCLUDED.cloud_ready,
			local_pending = EXCLUDED.local_pending, cloud_pending = EXCLUDED.cloud_pending,
			action = EXCLUDED.action, plan_local = EXCLUDED.plan_local,
			plan_cloud = EXCLUDED.plan_cloud, reason = EXCLUDED.reason,
			constraint_name = EXCLUDED.constraint_name,
			settings_version = EXCLUDED.settings_version,
			breach_expected = EXCLUDED.breach_expected,
			completed = EXCLUDED.completed, breached = EXCLUDED.breached`,
		cycle.RunID, cycle.Sequence, cycle.At, queues, cycle.LocalReady, cycle.CloudReady,
		cycle.LocalPending, cycle.CloudPending, cycle.Action, cycle.PlanLocal,
		cycle.PlanCloud, cycle.Reason, cycle.Constraint, cycle.SettingsVersion,
		cycle.BreachExpected, cycle.Completed, cycle.Breached)
	if err != nil {
		return fmt.Errorf("saving cycle %d of run %q: %w", cycle.Sequence, cycle.RunID, err)
	}
	return nil
}

// Cycles reads a run's timeline, in order.
func (s *Store) Cycles(ctx context.Context, runID string, from, limit int) ([]domain.Cycle, error) {
	if limit <= 0 || limit > 20000 {
		// A six-hour run at fifteen seconds is 1440 cycles, so a generous
		// default returns a whole ordinary run in one request while still
		// bounding what a pathological one can do to memory.
		limit = 5000
	}

	rows, err := s.pool.Query(ctx, `
		SELECT run_id, sequence, at, queues, local_ready, cloud_ready, local_pending,
			cloud_pending, action, plan_local, plan_cloud, reason, constraint_name,
			settings_version, breach_expected, completed, breached
		FROM run_cycles
		WHERE run_id = $1 AND sequence > $2
		ORDER BY sequence
		LIMIT $3`, runID, from, limit)
	if err != nil {
		return nil, fmt.Errorf("reading the cycles of run %q: %w", runID, err)
	}
	defer rows.Close()

	cycles := []domain.Cycle{}
	for rows.Next() {
		var (
			cycle  domain.Cycle
			queues []byte
		)
		if err := rows.Scan(&cycle.RunID, &cycle.Sequence, &cycle.At, &queues,
			&cycle.LocalReady, &cycle.CloudReady, &cycle.LocalPending, &cycle.CloudPending,
			&cycle.Action, &cycle.PlanLocal, &cycle.PlanCloud, &cycle.Reason,
			&cycle.Constraint, &cycle.SettingsVersion, &cycle.BreachExpected,
			&cycle.Completed, &cycle.Breached); err != nil {
			return nil, fmt.Errorf("reading a cycle of run %q: %w", runID, err)
		}

		var wireQueues map[string]domain.QueueSnapshot
		if err := json.Unmarshal(queues, &wireQueues); err != nil {
			return nil, fmt.Errorf("reading the queues of cycle %d: %w", cycle.Sequence, err)
		}
		cycle.Queues = map[domain.Priority]domain.QueueSnapshot{}
		for key, level := range wireQueues {
			priority, err := strconv.Atoi(key)
			if err != nil {
				return nil, fmt.Errorf("cycle %d has %q as a priority level", cycle.Sequence, key)
			}
			cycle.Queues[domain.Priority(priority)] = level
		}
		cycles = append(cycles, cycle)
	}
	return cycles, rows.Err()
}

// SaveMetrics records what a run amounted to.
func (s *Store) SaveMetrics(ctx context.Context, metrics domain.Metrics) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO run_metrics (run_id, jobs_submitted, jobs_completed, sla_breaches,
			breach_rate, mean_wait_seconds, p95_wait_seconds, max_wait_seconds,
			peak_queue_depth, local_executor_seconds, cloud_executor_seconds,
			peak_local_executors, peak_cloud_executors, scaling_actions, cycles)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (run_id) DO UPDATE SET
			jobs_submitted = EXCLUDED.jobs_submitted, jobs_completed = EXCLUDED.jobs_completed,
			sla_breaches = EXCLUDED.sla_breaches, breach_rate = EXCLUDED.breach_rate,
			mean_wait_seconds = EXCLUDED.mean_wait_seconds,
			p95_wait_seconds = EXCLUDED.p95_wait_seconds,
			max_wait_seconds = EXCLUDED.max_wait_seconds,
			peak_queue_depth = EXCLUDED.peak_queue_depth,
			local_executor_seconds = EXCLUDED.local_executor_seconds,
			cloud_executor_seconds = EXCLUDED.cloud_executor_seconds,
			peak_local_executors = EXCLUDED.peak_local_executors,
			peak_cloud_executors = EXCLUDED.peak_cloud_executors,
			scaling_actions = EXCLUDED.scaling_actions, cycles = EXCLUDED.cycles`,
		metrics.RunID, metrics.JobsSubmitted, metrics.JobsCompleted, metrics.SLABreaches,
		metrics.BreachRate, metrics.MeanWaitSeconds, metrics.P95WaitSeconds,
		metrics.MaxWaitSeconds, metrics.PeakQueueDepth, metrics.LocalExecutorSeconds,
		metrics.CloudExecutorSeconds, metrics.PeakLocalExecutors, metrics.PeakCloudExecutors,
		metrics.ScalingActions, metrics.Cycles)
	if err != nil {
		return fmt.Errorf("saving the metrics of run %q: %w", metrics.RunID, err)
	}
	return nil
}

// Metrics reads what a run amounted to.
func (s *Store) Metrics(ctx context.Context, runID string) (domain.Metrics, error) {
	var metrics domain.Metrics
	err := s.pool.QueryRow(ctx, `
		SELECT run_id, jobs_submitted, jobs_completed, sla_breaches, breach_rate,
			mean_wait_seconds, p95_wait_seconds, max_wait_seconds, peak_queue_depth,
			local_executor_seconds, cloud_executor_seconds, peak_local_executors,
			peak_cloud_executors, scaling_actions, cycles
		FROM run_metrics WHERE run_id = $1`, runID,
	).Scan(&metrics.RunID, &metrics.JobsSubmitted, &metrics.JobsCompleted,
		&metrics.SLABreaches, &metrics.BreachRate, &metrics.MeanWaitSeconds,
		&metrics.P95WaitSeconds, &metrics.MaxWaitSeconds, &metrics.PeakQueueDepth,
		&metrics.LocalExecutorSeconds, &metrics.CloudExecutorSeconds,
		&metrics.PeakLocalExecutors, &metrics.PeakCloudExecutors,
		&metrics.ScalingActions, &metrics.Cycles)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Metrics{}, fmt.Errorf("%w: metrics for run %q", ErrNotFound, runID)
	}
	if err != nil {
		return domain.Metrics{}, fmt.Errorf("reading the metrics of run %q: %w", runID, err)
	}
	return metrics, nil
}

func queuesToWire(queues map[domain.Priority]domain.QueueSnapshot) map[string]domain.QueueSnapshot {
	out := make(map[string]domain.QueueSnapshot, len(queues))
	for priority, level := range queues {
		out[strconv.Itoa(int(priority))] = level
	}
	return out
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func nullableFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}
