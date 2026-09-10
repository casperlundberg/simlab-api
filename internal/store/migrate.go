// Package store persists Simlab's mines, scenarios, runs and results in
// Postgres.
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate brings a database up to date, applying whatever it has not seen.
//
// Migrations are plain SQL files applied in filename order, tracked in a table
// of their own. That is deliberately less than a migration framework offers:
// this service owns its schema outright, the files are append-only, and a
// dependency that has to be kept in step with a Postgres version is a cost
// with nothing to show for it here.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("preparing the migration record: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		var applied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("checking whether %s has been applied: %w", name, err)
		}
		if applied {
			continue
		}

		statements, err := fs.ReadFile(migrationFiles, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}

		// Each migration runs in its own transaction, so a failure leaves the
		// database on the last complete schema rather than halfway into one.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("starting %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(statements)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("applying %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("recording %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing %s: %w", name, err)
		}
	}
	return nil
}

func migrationNames() ([]string, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	// Filename order is apply order, which is why they are numbered.
	sort.Strings(names)
	return names, nil
}
