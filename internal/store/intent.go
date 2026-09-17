package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// SaveRunIntent records the intent a run is created with.
func (s *Store) SaveRunIntent(ctx context.Context, runID string, intent domain.RunIntent) error {
	encoded, err := json.Marshal(intent)
	if err != nil {
		return fmt.Errorf("encoding the intent of run %q: %w", runID, err)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE runs SET intent = $2 WHERE id = $1`, runID, encoded)
	if err != nil {
		return fmt.Errorf("saving the intent of run %q: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %q", ErrNotFound, runID)
	}
	return nil
}

// RunIntent is the intent a run was created with, or nil for a run created
// before intent existed.
func (s *Store) RunIntent(ctx context.Context, runID string) (*domain.RunIntent, error) {
	var encoded []byte
	err := s.pool.QueryRow(ctx, `SELECT intent FROM runs WHERE id = $1`, runID).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: run %q", ErrNotFound, runID)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the intent of run %q: %w", runID, err)
	}
	if encoded == nil {
		return nil, nil
	}
	intent := &domain.RunIntent{}
	if err := json.Unmarshal(encoded, intent); err != nil {
		return nil, fmt.Errorf("reading the intent of run %q: %w", runID, err)
	}
	return intent, nil
}

// SaveIntentChange records intent as it is from a cycle of a run onwards.
func (s *Store) SaveIntentChange(ctx context.Context, runID string, change domain.IntentChange) error {
	encoded, err := json.Marshal(change.Settings)
	if err != nil {
		return fmt.Errorf("encoding intent version %d of run %q: %w", change.Version, runID, err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO run_intent_changes (run_id, version, cycle, source, settings, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (run_id, version) DO UPDATE SET
			cycle = EXCLUDED.cycle, source = EXCLUDED.source,
			settings = EXCLUDED.settings, recorded_at = EXCLUDED.recorded_at`,
		runID, change.Version, change.Cycle, change.Source, encoded, change.RecordedAt)
	if err != nil {
		return fmt.Errorf("saving intent version %d of run %q: %w", change.Version, runID, err)
	}
	return nil
}

// IntentChanges is every change of intent a run recorded, in version order.
func (s *Store) IntentChanges(ctx context.Context, runID string) ([]domain.IntentChange, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT version, cycle, source, settings, recorded_at
		FROM run_intent_changes WHERE run_id = $1 ORDER BY version`, runID)
	if err != nil {
		return nil, fmt.Errorf("reading the intent changes of run %q: %w", runID, err)
	}
	defer rows.Close()

	changes := []domain.IntentChange{}
	for rows.Next() {
		var (
			change   domain.IntentChange
			settings []byte
		)
		if err := rows.Scan(&change.Version, &change.Cycle, &change.Source, &settings, &change.RecordedAt); err != nil {
			return nil, fmt.Errorf("reading an intent change of run %q: %w", runID, err)
		}
		change.Settings = domain.DefaultIntent()
		if err := json.Unmarshal(settings, &change.Settings); err != nil {
			return nil, fmt.Errorf("reading intent version %d of run %q: %w", change.Version, runID, err)
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}
