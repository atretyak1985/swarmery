package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Optimize refreshes the query planner's statistics (sqlite_stat1). Without
// them the planner guesses between indexes, and on the live store it guessed
// the low-selectivity one: idx_events_status_ts over idx_events_session,
// idx_decisions_question over idx_decisions_subject — each such query holding
// the store's single connection for seconds while every HTTP handler queued
// (#584, #586).
//
// startup=true runs `PRAGMA optimize=0x10002`: 0x10000 makes it consider every
// table rather than only those this connection has queried, which a freshly
// opened connection has not. The periodic call (startup=false) is the plain
// form, which re-analyzes only tables whose row counts drifted. analysis_limit
// bounds each table's scan, so a cold run on a large store stays well under a
// second (measured 2026-10-19 on 1.35 GB: full ANALYZE 0.66 s, limit 400
// 0.11 s).
//
// The daemon calls it; store.Open does not, so CLI commands and backups never
// rewrite the statistics.
func Optimize(ctx context.Context, db *sql.DB, startup bool) error {
	// Both pragmas must run on one connection: analysis_limit is per-connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("optimize: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA analysis_limit=400`); err != nil {
		return fmt.Errorf("analysis_limit: %w", err)
	}
	q := `PRAGMA optimize`
	if startup {
		q = `PRAGMA optimize=0x10002`
	}
	if _, err := conn.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("optimize: %w", err)
	}
	return nil
}
