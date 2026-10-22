package store

import (
	"database/sql"
	"errors"
	"time"
)

// ReviewFindingsMigration is the file that introduced review_findings; its
// applied_at is D21's cutoff — a phase Done before it without a `kind: review`
// block is a "legacy review" (wsingest/review_blocks.go).
const ReviewFindingsMigration = "0122_review_findings.sql"

// MigrationAppliedAt is when the migration file name was applied to this
// database, read from schema_migrations.applied_at. ok is false when the
// migration is not recorded (a database built by MigrateUpTo below it) — the
// caller decides what "no cutoff" means; err is only a read failure.
func MigrationAppliedAt(q Querier, name string) (at time.Time, ok bool, err error) {
	var raw string
	switch err := q.QueryRow(`SELECT applied_at FROM schema_migrations WHERE name = ?`, name).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, err
	}
	at, err = time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false, err
	}
	return at.UTC(), true, nil
}
