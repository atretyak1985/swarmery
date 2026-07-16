// Package prune implements retention (ops-hygiene wave): sessions that ended
// before a cutoff get their per-day, per-project aggregates written into
// daily_rollups, then their bulky child rows (events, file_changes, turns)
// deleted. Session HEADER rows are kept and flagged pruned=1 (migration 0014)
// so the sessions list and detail stay browsable; analytics unions the
// rollups back in for the days that lost raw rows.
//
// Rollup grain: (local day, project_id) with agent_id NULL. Day bucketing
// uses SQLite date(x,'localtime'), matching the Go time.Local folding in
// internal/api/analytics.go on the same host. Re-runs are safe: pruned=1
// sessions leave the candidate set, and every reader SUMs over daily_rollups,
// so accumulated rows per (day, project) stay correct (the table's PK does
// not dedupe NULL agent_id rows — SQLite treats NULLs as distinct).
//
// Known interaction: `swarmery backfill --rebuild-text` replays transcripts
// from byte 0 and re-inserts pruned turns/events (their dedup keys were
// deleted). That is an explicit repair tool; re-inserted rows of a pruned=1
// session are simply raw rows again — reset pruned=0 before re-pruning them.
package prune

import (
	"database/sql"
	"fmt"
)

// Stats reports one prune pass (the would-be numbers under DryRun).
type Stats struct {
	Cutoff      string
	DryRun      bool
	Sessions    int64 // sessions marked pruned
	Turns       int64
	Events      int64
	FileChanges int64
	RollupRows  int64 // daily_rollups rows inserted (0 under DryRun)
	// VacuumErr carries a post-commit VACUUM failure (e.g. SQLITE_BUSY).
	// The destructive transaction has already committed by then, so the
	// prune itself succeeded — callers should report this as a warning
	// ("space not reclaimed"), never as a failed prune.
	VacuumErr error
}

// candidateSet is the single source of truth for "what gets pruned": ended
// before the cutoff and not yet pruned. Every count, the rollup insert, and
// every delete embeds it, so they can never disagree.
const candidateSet = `SELECT id, project_id, started_at FROM sessions
	WHERE pruned = 0 AND ended_at IS NOT NULL AND ended_at < ?`

// rollupInsert aggregates the candidate sessions into daily_rollups
// (agent_id NULL — the per-project grain). Tokens/cost bucket by the turn's
// local day, tool_calls/errors by the event's local day, session counts by
// the session's local start day; the keys UNION merges the three grains.
const rollupInsert = `
WITH pruned AS (` + candidateSet + `),
tk AS (
	SELECT date(t.started_at, 'localtime') AS day, pr.project_id,
	       SUM(COALESCE(t.tokens_in, 0))  AS tokens_in,
	       SUM(COALESCE(t.tokens_out, 0)) AS tokens_out,
	       SUM(COALESCE(t.cost_usd, 0))   AS cost_usd
	  FROM turns t JOIN pruned pr ON pr.id = t.session_id
	 GROUP BY 1, 2
),
ev AS (
	SELECT date(e.ts, 'localtime') AS day, pr.project_id,
	       SUM(CASE WHEN e.type = 'tool_call' THEN 1 ELSE 0 END) AS tool_calls,
	       SUM(CASE WHEN e.type = 'error' OR e.status = 'error' THEN 1 ELSE 0 END) AS errors
	  FROM events e JOIN pruned pr ON pr.id = e.session_id
	 GROUP BY 1, 2
),
ss AS (
	SELECT date(started_at, 'localtime') AS day, project_id, COUNT(*) AS sessions
	  FROM pruned GROUP BY 1, 2
),
keys AS (
	SELECT day, project_id FROM tk
	UNION SELECT day, project_id FROM ev
	UNION SELECT day, project_id FROM ss
)
INSERT INTO daily_rollups
	(day, project_id, agent_id, sessions, tasks_done, tasks_reverted,
	 tool_calls, errors, tokens_in, tokens_out, cost_usd, wait_minutes)
SELECT k.day, k.project_id, NULL,
       COALESCE(ss.sessions, 0), 0, 0,
       COALESCE(ev.tool_calls, 0), COALESCE(ev.errors, 0),
       COALESCE(tk.tokens_in, 0), COALESCE(tk.tokens_out, 0),
       COALESCE(tk.cost_usd, 0), 0
  FROM keys k
  LEFT JOIN tk ON tk.day = k.day AND tk.project_id = k.project_id
  LEFT JOIN ev ON ev.day = k.day AND ev.project_id = k.project_id
  LEFT JOIN ss ON ss.day = k.day AND ss.project_id = k.project_id`

// Run executes one prune pass. cutoff is an ISO-8601 UTC timestamp (the TEXT
// collation the schema stores). Everything except the final VACUUM runs in
// one transaction; dryRun stops after counting.
func Run(db *sql.DB, cutoff string, dryRun bool) (Stats, error) {
	st := Stats{Cutoff: cutoff, DryRun: dryRun}
	// The session count gates the destructive path; under DryRun the child
	// tables are pre-counted too. A real run reports Turns/Events/FileChanges
	// from RowsAffected() of the deletes inside the transaction instead, so
	// the printed numbers are exactly what was removed.
	if err := db.QueryRow(`SELECT COUNT(*) FROM (`+candidateSet+`)`, cutoff).Scan(&st.Sessions); err != nil {
		return st, fmt.Errorf("count candidates: %w", err)
	}
	if dryRun {
		counts := []struct {
			q   string
			dst *int64
		}{
			{`SELECT COUNT(*) FROM turns t JOIN (` + candidateSet + `) pr ON pr.id = t.session_id`, &st.Turns},
			{`SELECT COUNT(*) FROM events e JOIN (` + candidateSet + `) pr ON pr.id = e.session_id`, &st.Events},
			{`SELECT COUNT(*) FROM file_changes fc JOIN (` + candidateSet + `) pr ON pr.id = fc.session_id`, &st.FileChanges},
		}
		for _, c := range counts {
			if err := db.QueryRow(c.q, cutoff).Scan(c.dst); err != nil {
				return st, fmt.Errorf("count candidates: %w", err)
			}
		}
		return st, nil
	}
	if st.Sessions == 0 {
		return st, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return st, err
	}
	defer tx.Rollback() // no-op after a successful Commit

	res, err := tx.Exec(rollupInsert, cutoff)
	if err != nil {
		return st, fmt.Errorf("rollup insert: %w", err)
	}
	st.RollupRows, _ = res.RowsAffected()

	// FK-safe order: permission_requests.event_id references events — keep
	// the approval history rows, drop the edge to the rows being deleted.
	// file_changes references events; both reference sessions (kept).
	steps := []struct {
		q   string
		dst *int64 // reported count, from RowsAffected (nil = not reported)
	}{
		{`UPDATE permission_requests SET event_id = NULL
		  WHERE event_id IS NOT NULL AND session_id IN (SELECT id FROM (` + candidateSet + `))`, nil},
		{`DELETE FROM file_changes WHERE session_id IN (SELECT id FROM (` + candidateSet + `))`, &st.FileChanges},
		{`DELETE FROM events       WHERE session_id IN (SELECT id FROM (` + candidateSet + `))`, &st.Events},
		{`DELETE FROM turns        WHERE session_id IN (SELECT id FROM (` + candidateSet + `))`, &st.Turns},
		{`UPDATE sessions SET pruned = 1 WHERE id IN (SELECT id FROM (` + candidateSet + `))`, nil},
	}
	for _, s := range steps {
		res, err := tx.Exec(s.q, cutoff)
		if err != nil {
			return st, fmt.Errorf("prune step: %w", err)
		}
		if s.dst != nil {
			n, err := res.RowsAffected()
			if err != nil {
				return st, fmt.Errorf("prune step rows affected: %w", err)
			}
			*s.dst = n
		}
	}
	if err := tx.Commit(); err != nil {
		return st, err
	}

	// Reclaim the freed pages. VACUUM cannot run inside a transaction; the
	// store's single-connection pool serialises it against other writers.
	// The prune has already committed, so a busy VACUUM must not make the
	// pass look failed — carry it on the stats as a warning instead.
	if err := vacuum(db); err != nil {
		st.VacuumErr = fmt.Errorf("vacuum: %w", err)
	}
	return st, nil
}

// vacuum is a seam for tests: injecting a real SQLITE_BUSY needs a second
// connection racing the 5s busy_timeout, which would be slow and flaky.
var vacuum = func(db *sql.DB) error {
	_, err := db.Exec(`VACUUM`)
	return err
}
