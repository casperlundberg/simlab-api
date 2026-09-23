package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// SaveLayout records the sensor array a run was replayed against.
func (s *Store) SaveLayout(ctx context.Context, runID string, layout domain.Layout) error {
	encoded, err := json.Marshal(layout)
	if err != nil {
		return fmt.Errorf("encoding the layout of run %q: %w", runID, err)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE runs SET layout = $2 WHERE id = $1`, runID, encoded)
	if err != nil {
		return fmt.Errorf("saving the layout of run %q: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %q", ErrNotFound, runID)
	}
	return nil
}

// RunLayout is the sensor array a run was replayed against.
//
// Not found covers two cases a caller should explain differently only if it
// has to: no such run, and a run with no mine recorded — a live run, or one
// recorded before mines were modelled.
func (s *Store) RunLayout(ctx context.Context, runID string) (domain.Layout, error) {
	var encoded []byte
	err := s.pool.QueryRow(ctx, `SELECT layout FROM runs WHERE id = $1`, runID).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Layout{}, fmt.Errorf("%w: run %q", ErrNotFound, runID)
	}
	if err != nil {
		return domain.Layout{}, fmt.Errorf("reading the layout of run %q: %w", runID, err)
	}
	if encoded == nil {
		return domain.Layout{}, fmt.Errorf("%w: run %q has no virtual mine recorded; it is a live run, "+
			"or it was recorded before mines were modelled", ErrNotFound, runID)
	}

	var layout domain.Layout
	if err := json.Unmarshal(encoded, &layout); err != nil {
		return domain.Layout{}, fmt.Errorf("reading the layout of run %q: %w", runID, err)
	}
	return layout, nil
}

// seismicBatch bounds one round trip. A run's first write is every event it
// will have, which for a long scenario is tens of thousands of rows.
const seismicBatch = 1000

// SaveSeismicEvents records events for a run, replacing any earlier record of
// the same sequence numbers and leaving every other event as it was.
func (s *Store) SaveSeismicEvents(ctx context.Context, runID string, events []domain.SeismicEvent) error {
	for from := 0; from < len(events); from += seismicBatch {
		to := min(from+seismicBatch, len(events))

		batch := &pgx.Batch{}
		for _, event := range events[from:to] {
			truth, err := json.Marshal(event.Truth)
			if err != nil {
				return fmt.Errorf("encoding event %d of run %q: %w", event.Sequence, runID, err)
			}
			sensors := event.Sensors
			if sensors == nil {
				sensors = []string{}
			}
			located, err := encodeLocation(event.Located)
			if err != nil {
				return fmt.Errorf("encoding the first location of event %d of run %q: %w", event.Sequence, runID, err)
			}
			final, err := encodeLocation(event.Final)
			if err != nil {
				return fmt.Errorf("encoding the final location of event %d of run %q: %w", event.Sequence, runID, err)
			}
			var exposed any
			if event.Exposed != nil {
				encoded, err := json.Marshal(event.Exposed)
				if err != nil {
					return fmt.Errorf("encoding who event %d of run %q exposed: %w", event.Sequence, runID, err)
				}
				exposed = encoded
			}
			var intent any
			if event.Intent != nil {
				encoded, err := json.Marshal(event.Intent)
				if err != nil {
					return fmt.Errorf("encoding the intent about event %d of run %q: %w", event.Sequence, runID, err)
				}
				intent = encoded
			}
			batch.Queue(`
				INSERT INTO run_seismic_events (run_id, sequence, origin_ms, burst, truth, sensors,
					located_at_ms, located, processed_at_ms, final, picks_processed_at_ms,
					magnitude, exposed, intent, activity, scripted_for)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
				ON CONFLICT (run_id, sequence) DO UPDATE SET
					origin_ms = EXCLUDED.origin_ms, burst = EXCLUDED.burst,
					truth = EXCLUDED.truth, sensors = EXCLUDED.sensors,
					located_at_ms = EXCLUDED.located_at_ms, located = EXCLUDED.located,
					processed_at_ms = EXCLUDED.processed_at_ms, final = EXCLUDED.final,
					picks_processed_at_ms = EXCLUDED.picks_processed_at_ms,
					magnitude = EXCLUDED.magnitude, exposed = EXCLUDED.exposed,
					intent = EXCLUDED.intent, activity = EXCLUDED.activity,
					scripted_for = EXCLUDED.scripted_for`,
				runID, event.Sequence, event.Origin.Milliseconds(), event.Burst, truth, sensors,
				nullableMs(event.LocatedAt), located, nullableMs(event.ProcessedAt), final,
				pickTimes(event.PickProcessedAt), event.Magnitude, exposed, intent, event.Activity, event.ScriptedFor)
		}

		if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("saving the seismic events of run %q: %w", runID, err)
		}
	}
	return nil
}

// SeismicEvents reads a run's events in order, after sequence from.
func (s *Store) SeismicEvents(ctx context.Context, runID string, from, limit int) ([]domain.SeismicEvent, error) {
	if limit <= 0 || limit > 20000 {
		limit = 5000
	}

	rows, err := s.pool.Query(ctx, `
		SELECT run_id, sequence, origin_ms, burst, truth, sensors,
			located_at_ms, located, processed_at_ms, final, picks_processed_at_ms,
			magnitude, exposed, intent, activity, scripted_for
		FROM run_seismic_events
		WHERE run_id = $1 AND sequence > $2
		ORDER BY sequence
		LIMIT $3`, runID, from, limit)
	if err != nil {
		return nil, fmt.Errorf("reading the seismic events of run %q: %w", runID, err)
	}
	defer rows.Close()

	events := []domain.SeismicEvent{}
	for rows.Next() {
		var (
			event               domain.SeismicEvent
			originMs            int64
			truth               []byte
			locatedAt, finished *int64
			located, final      []byte
			picks               []*int64
			exposed             []byte
			intent              []byte
		)
		if err := rows.Scan(&event.RunID, &event.Sequence, &originMs, &event.Burst, &truth,
			&event.Sensors, &locatedAt, &located, &finished, &final, &picks,
			&event.Magnitude, &exposed, &intent, &event.Activity, &event.ScriptedFor); err != nil {
			return nil, fmt.Errorf("reading a seismic event of run %q: %w", runID, err)
		}

		event.Origin = time.Duration(originMs) * time.Millisecond
		event.LocatedAt = durationFromMs(locatedAt)
		event.ProcessedAt = durationFromMs(finished)
		if picks != nil {
			event.PickProcessedAt = make([]*time.Duration, len(picks))
			for i, ms := range picks {
				event.PickProcessedAt[i] = durationFromMs(ms)
			}
		}
		if err := json.Unmarshal(truth, &event.Truth); err != nil {
			return nil, fmt.Errorf("reading where event %d of run %q was: %w", event.Sequence, runID, err)
		}
		if exposed != nil {
			if err := json.Unmarshal(exposed, &event.Exposed); err != nil {
				return nil, fmt.Errorf("reading who event %d exposed: %w", event.Sequence, err)
			}
		}
		if intent != nil {
			if err := json.Unmarshal(intent, &event.Intent); err != nil {
				return nil, fmt.Errorf("reading the intent about event %d: %w", event.Sequence, err)
			}
		}
		if event.Located, err = locationFrom(located); err != nil {
			return nil, fmt.Errorf("reading the first location of event %d: %w", event.Sequence, err)
		}
		if event.Final, err = locationFrom(final); err != nil {
			return nil, fmt.Errorf("reading the final location of event %d: %w", event.Sequence, err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func nullableMs(d *time.Duration) any {
	if d == nil {
		return nil
	}
	return d.Milliseconds()
}

// pickTimes is a list of optional times as a BIGINT[] with NULL elements, or
// SQL NULL for a list that was never recorded.
func pickTimes(times []*time.Duration) any {
	if times == nil {
		return nil
	}
	out := make([]*int64, len(times))
	for i, at := range times {
		if at != nil {
			ms := at.Milliseconds()
			out[i] = &ms
		}
	}
	return out
}

func durationFromMs(ms *int64) *time.Duration {
	if ms == nil {
		return nil
	}
	d := time.Duration(*ms) * time.Millisecond
	return &d
}

// encodeLocation is a location as JSONB, or nil for SQL NULL. It can fail: a
// solver that produced a NaN residual would otherwise be written as nothing.
func encodeLocation(location *domain.Location) (any, error) {
	if location == nil {
		return nil, nil
	}
	return json.Marshal(location)
}

func locationFrom(encoded []byte) (*domain.Location, error) {
	if encoded == nil {
		return nil, nil
	}
	location := &domain.Location{}
	if err := json.Unmarshal(encoded, location); err != nil {
		return nil, err
	}
	return location, nil
}

// SaveEntities records the people and vehicles a run was replayed with,
// replacing any recorded for it before.
func (s *Store) SaveEntities(ctx context.Context, runID string, entities []domain.Entity) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("saving the entities of run %q: %w", runID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM run_entities WHERE run_id = $1`, runID); err != nil {
		return fmt.Errorf("clearing the entities of run %q: %w", runID, err)
	}
	batch := &pgx.Batch{}
	for _, entity := range entities {
		track, err := json.Marshal(entity.Track)
		if err != nil {
			return fmt.Errorf("encoding the track of %s: %w", entity.ID, err)
		}
		batch.Queue(`INSERT INTO run_entities (run_id, id, kind, track) VALUES ($1, $2, $3, $4)`,
			runID, entity.ID, entity.Kind, track)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("saving the entities of run %q: %w", runID, err)
	}
	return tx.Commit(ctx)
}

// Entities is the people and vehicles a run was replayed with, by id.
func (s *Store) Entities(ctx context.Context, runID string) ([]domain.Entity, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, track FROM run_entities WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("reading the entities of run %q: %w", runID, err)
	}
	defer rows.Close()

	entities := []domain.Entity{}
	for rows.Next() {
		var (
			entity domain.Entity
			track  []byte
		)
		if err := rows.Scan(&entity.ID, &entity.Kind, &track); err != nil {
			return nil, fmt.Errorf("reading an entity of run %q: %w", runID, err)
		}
		if err := json.Unmarshal(track, &entity.Track); err != nil {
			return nil, fmt.Errorf("reading the track of %s: %w", entity.ID, err)
		}
		entities = append(entities, entity)
	}
	return entities, rows.Err()
}
