package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// ErrNotFound is returned, wrapped, when a row does not exist. It is the
// domain's own, so a caller can tell "no such thing" without knowing it came
// from Postgres.
var ErrNotFound = domain.ErrNotFound

// Store is Simlab's persistence.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects, verifies the connection, and migrates.
//
// Migrating on start is right for a service that owns its schema outright and
// deploys as one replica: the alternative is a separate step that can be
// forgotten, leaving the service running against a schema it does not expect.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("the database is not reachable: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// Ping reports whether the database is reachable, for the readiness probe.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// SaveMine inserts or replaces a mine.
func (s *Store) SaveMine(ctx context.Context, mine domain.Mine) error {
	if err := mine.Validate(); err != nil {
		return err
	}
	var layout any
	if mine.Layout != nil {
		encoded, err := json.Marshal(mine.Layout)
		if err != nil {
			return fmt.Errorf("encoding the layout of mine %q: %w", mine.ID, err)
		}
		layout = encoded
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO mines (id, name, sensors, background_rate, description, layout)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name, sensors = EXCLUDED.sensors,
			background_rate = EXCLUDED.background_rate, description = EXCLUDED.description,
			layout = EXCLUDED.layout`,
		mine.ID, mine.Name, mine.Sensors, mine.BackgroundRate, mine.Description, layout)
	if err != nil {
		return fmt.Errorf("saving mine %q: %w", mine.ID, err)
	}
	return nil
}

const mineColumns = `SELECT id, name, sensors, background_rate, description, layout, created_at FROM mines`

// Mine fetches one mine.
func (s *Store) Mine(ctx context.Context, id string) (domain.Mine, error) {
	mine, err := scanMine(s.pool.QueryRow(ctx, mineColumns+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Mine{}, fmt.Errorf("%w: mine %q", ErrNotFound, id)
	}
	if err != nil {
		return domain.Mine{}, fmt.Errorf("reading mine %q: %w", id, err)
	}
	return mine, nil
}

// Mines lists every mine, by name.
func (s *Store) Mines(ctx context.Context) ([]domain.Mine, error) {
	rows, err := s.pool.Query(ctx, mineColumns+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing mines: %w", err)
	}
	defer rows.Close()

	mines := []domain.Mine{}
	for rows.Next() {
		mine, err := scanMine(rows)
		if err != nil {
			return nil, fmt.Errorf("reading a mine: %w", err)
		}
		mines = append(mines, mine)
	}
	return mines, rows.Err()
}

func scanMine(row scannable) (domain.Mine, error) {
	var (
		mine   domain.Mine
		layout []byte
	)
	if err := row.Scan(&mine.ID, &mine.Name, &mine.Sensors, &mine.BackgroundRate,
		&mine.Description, &layout, &mine.CreatedAt); err != nil {
		return domain.Mine{}, err
	}
	if layout != nil {
		mine.Layout = &domain.Layout{}
		if err := json.Unmarshal(layout, mine.Layout); err != nil {
			return domain.Mine{}, fmt.Errorf("reading the layout of mine %q: %w", mine.ID, err)
		}
	}
	return mine, nil
}

// DeleteMine removes a mine and, by cascade, its scenarios.
func (s *Store) DeleteMine(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mines WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting mine %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: mine %q", ErrNotFound, id)
	}
	return nil
}

// SaveScenario inserts or replaces a scenario.
func (s *Store) SaveScenario(ctx context.Context, scenario domain.Scenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}

	mix, err := json.Marshal(priorityMixToWire(scenario.PriorityMix))
	if err != nil {
		return fmt.Errorf("encoding the priority mix: %w", err)
	}
	bursts, err := json.Marshal(burstsToWire(scenario.Bursts))
	if err != nil {
		return fmt.Errorf("encoding the bursts: %w", err)
	}

	var workforce any
	if scenario.Workforce != nil {
		encoded, err := json.Marshal(scenario.Workforce)
		if err != nil {
			return fmt.Errorf("encoding the workforce: %w", err)
		}
		workforce = encoded
	}
	var pipeline any
	if scenario.Pipeline != nil {
		encoded, err := json.Marshal(scenario.Pipeline)
		if err != nil {
			return fmt.Errorf("encoding the pipeline: %w", err)
		}
		pipeline = encoded
	}
	var activity any
	if scenario.Activity != nil {
		encoded, err := json.Marshal(scenario.Activity)
		if err != nil {
			return fmt.Errorf("encoding the activity: %w", err)
		}
		activity = encoded
	}
	var encounters any
	if scenario.Encounters != nil {
		encoded, err := json.Marshal(scenario.Encounters)
		if err != nil {
			return fmt.Errorf("encoding the encounters: %w", err)
		}
		encounters = encoded
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO scenarios (id, mine_id, name, duration_ms, job_seconds, seed,
			priority_mix, bursts, description, pick_jitter_ms, workforce, pipeline, activity, encounters)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (id) DO UPDATE SET
			mine_id = EXCLUDED.mine_id, name = EXCLUDED.name,
			duration_ms = EXCLUDED.duration_ms, job_seconds = EXCLUDED.job_seconds,
			seed = EXCLUDED.seed, priority_mix = EXCLUDED.priority_mix,
			bursts = EXCLUDED.bursts, description = EXCLUDED.description,
			pick_jitter_ms = EXCLUDED.pick_jitter_ms, workforce = EXCLUDED.workforce,
			pipeline = EXCLUDED.pipeline, activity = EXCLUDED.activity,
			encounters = EXCLUDED.encounters`,
		scenario.ID, scenario.MineID, scenario.Name, scenario.Duration.Milliseconds(),
		scenario.JobSeconds, scenario.Seed, mix, bursts, scenario.Description,
		float64(scenario.PickJitter)/float64(time.Millisecond), workforce, pipeline, activity, encounters)
	if err != nil {
		return fmt.Errorf("saving scenario %q: %w", scenario.ID, err)
	}
	return nil
}

// Scenario fetches one scenario.
func (s *Store) Scenario(ctx context.Context, id string) (domain.Scenario, error) {
	return s.scanScenario(s.pool.QueryRow(ctx, `
		SELECT id, mine_id, name, duration_ms, job_seconds, seed, priority_mix,
			bursts, description, pick_jitter_ms, workforce, pipeline, activity, encounters, created_at
		FROM scenarios WHERE id = $1`, id), id)
}

// Scenarios lists scenarios, optionally for one mine.
func (s *Store) Scenarios(ctx context.Context, mineID string) ([]domain.Scenario, error) {
	query := `
		SELECT id, mine_id, name, duration_ms, job_seconds, seed, priority_mix,
			bursts, description, pick_jitter_ms, workforce, pipeline, activity, encounters, created_at
		FROM scenarios`
	args := []any{}
	if mineID != "" {
		query += ` WHERE mine_id = $1`
		args = append(args, mineID)
	}
	query += ` ORDER BY name`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing scenarios: %w", err)
	}
	defer rows.Close()

	scenarios := []domain.Scenario{}
	for rows.Next() {
		scenario, err := s.scanScenarioRow(rows)
		if err != nil {
			return nil, err
		}
		scenarios = append(scenarios, scenario)
	}
	return scenarios, rows.Err()
}

// DeleteScenario removes a scenario. Runs that used it keep their results.
func (s *Store) DeleteScenario(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM scenarios WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting scenario %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: scenario %q", ErrNotFound, id)
	}
	return nil
}

type scannable interface {
	Scan(dest ...any) error
}

func (s *Store) scanScenario(row scannable, id string) (domain.Scenario, error) {
	scenario, err := s.scanScenarioRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Scenario{}, fmt.Errorf("%w: scenario %q", ErrNotFound, id)
	}
	return scenario, err
}

func (s *Store) scanScenarioRow(row scannable) (domain.Scenario, error) {
	var (
		scenario   domain.Scenario
		durationMs int64
		mix        []byte
		bursts     []byte
		jitterMs   float64
		workforce  []byte
		pipeline   []byte
		activity   []byte
		encounters []byte
	)
	if err := row.Scan(&scenario.ID, &scenario.MineID, &scenario.Name, &durationMs,
		&scenario.JobSeconds, &scenario.Seed, &mix, &bursts,
		&scenario.Description, &jitterMs, &workforce, &pipeline, &activity, &encounters,
		&scenario.CreatedAt); err != nil {
		return domain.Scenario{}, err
	}
	if encounters != nil {
		scenario.Encounters = &domain.EncounterSpec{}
		if err := json.Unmarshal(encounters, scenario.Encounters); err != nil {
			return domain.Scenario{}, fmt.Errorf("reading the encounters of %q: %w", scenario.ID, err)
		}
	}
	if activity != nil {
		scenario.Activity = &domain.ActivitySpec{}
		if err := json.Unmarshal(activity, scenario.Activity); err != nil {
			return domain.Scenario{}, fmt.Errorf("reading the activity of %q: %w", scenario.ID, err)
		}
	}
	if pipeline != nil {
		scenario.Pipeline = &domain.PipelineSpec{}
		if err := json.Unmarshal(pipeline, scenario.Pipeline); err != nil {
			return domain.Scenario{}, fmt.Errorf("reading the pipeline of %q: %w", scenario.ID, err)
		}
	}
	if workforce != nil {
		scenario.Workforce = &domain.Workforce{}
		if err := json.Unmarshal(workforce, scenario.Workforce); err != nil {
			return domain.Scenario{}, fmt.Errorf("reading the workforce of %q: %w", scenario.ID, err)
		}
	}

	scenario.Duration = time.Duration(durationMs) * time.Millisecond
	scenario.PickJitter = time.Duration(math.Round(jitterMs * float64(time.Millisecond)))

	var wireMix map[string]float64
	if err := json.Unmarshal(mix, &wireMix); err != nil {
		return domain.Scenario{}, fmt.Errorf("reading the priority mix of %q: %w", scenario.ID, err)
	}
	scenario.PriorityMix = map[domain.Priority]float64{}
	for key, weight := range wireMix {
		priority, err := strconv.Atoi(key)
		if err != nil {
			return domain.Scenario{}, fmt.Errorf("scenario %q has %q in its priority mix, "+
				"which is not a priority level", scenario.ID, key)
		}
		scenario.PriorityMix[domain.Priority(priority)] = weight
	}

	var wireBursts []burstWire
	if err := json.Unmarshal(bursts, &wireBursts); err != nil {
		return domain.Scenario{}, fmt.Errorf("reading the bursts of %q: %w", scenario.ID, err)
	}
	for _, burst := range wireBursts {
		scenario.Bursts = append(scenario.Bursts, domain.Burst{
			At:              time.Duration(burst.AtMs) * time.Millisecond,
			Magnitude:       burst.Magnitude,
			AftershockDecay: time.Duration(burst.DecayMs) * time.Millisecond,
			Epicentre:       burst.Epicentre,
			MainMagnitude:   burst.MainMagnitude,
		})
	}
	return scenario, nil
}

// burstWire is how a burst is stored. Durations are milliseconds, because
// Postgres has no duration type and a number nobody has to guess the unit of
// is worth the explicit suffix.
type burstWire struct {
	AtMs      int64   `json:"at_ms"`
	Magnitude float64 `json:"magnitude"`
	DecayMs   int64   `json:"aftershock_decay_ms"`

	Epicentre     *domain.Point `json:"epicentre,omitempty"`
	MainMagnitude *float64      `json:"main_magnitude,omitempty"`
}

func burstsToWire(bursts []domain.Burst) []burstWire {
	out := make([]burstWire, 0, len(bursts))
	for _, burst := range bursts {
		out = append(out, burstWire{
			AtMs:          burst.At.Milliseconds(),
			Magnitude:     burst.Magnitude,
			DecayMs:       burst.AftershockDecay.Milliseconds(),
			Epicentre:     burst.Epicentre,
			MainMagnitude: burst.MainMagnitude,
		})
	}
	return out
}

func priorityMixToWire(mix map[domain.Priority]float64) map[string]float64 {
	out := make(map[string]float64, len(mix))
	for priority, weight := range mix {
		out[strconv.Itoa(int(priority))] = weight
	}
	return out
}
