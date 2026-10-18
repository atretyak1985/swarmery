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
	"time"
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
	// Capped is true when MORE candidates were waiting than this pass could
	// take, i.e. the backlog is larger than one pass and the NEXT pass still
	// has work. It is derived from candidateProbe (one row wider than the cap),
	// NOT from "this pass processed exactly MaxSessionsPerPass": a backlog of
	// exactly the cap is fully drained, and claiming a remainder there sends an
	// operator back for a pass with nothing left to do. Callers log it so an
	// operator can tell "caught up" from "still catching up" without reading
	// the sessions table.
	Capped bool
	// Undatable* count candidate rows whose timestamp SQLite cannot parse
	// (date(x,'localtime') IS NULL — an empty string, whitespace, or garbage).
	// They are EXCLUDED from every rollup grain but still deleted, so without
	// these counts they would vanish silently: the NOT NULL constraint error
	// that used to surface an ingest regression is gone (see rollupInsert).
	// UndatableSessions is the candidate sessions whose own started_at is
	// unparseable — those contribute no `sessions` count to any day.
	UndatableTurns    int64
	UndatableEvents   int64
	UndatableSessions int64
	// WorktreeSweeps is journal rows dropped (internal/wtjanitor). Unlike every
	// other count here it is NOT session-derived: the janitor's log ages on the
	// same cutoff but has no candidate set, since nothing reads a row to make a
	// decision — losing old rows costs history, never correctness.
	WorktreeSweeps int64
	// LinkEvents is link_events rows dropped (migration 0107, the swarmery
	// link's dedup ledger). They age on the same cutoff as events, by their own
	// `at`, before the session gate: a mod-minted session may never get an
	// ended_at, and link_events has no FK, so a session-derived delete would
	// strand both. sessions.link_* keep the folded state; only the ledger goes.
	LinkEvents int64
	// LinkCommands is link_commands rows dropped (migration 0108, the link
	// control channel's queue). Same rule as LinkEvents, by created_at: a
	// command is expired (stop 30 s, prompt 10 min) long before any retention
	// cutoff, so only finished history goes.
	LinkCommands int64
	// TurnForecasts is turn_forecasts rows dropped (migration 0113, the link's
	// per-turn forecast vs actual). Same horizon and same reasoning as
	// LinkEvents, aged by the row's own started_at.
	TurnForecasts int64
	// LinkMessages is link_messages rows dropped (migration 0115, the link's
	// swarm messages). Same horizon and same reasoning as LinkEvents, aged by
	// the row's own at.
	LinkMessages int64
	// LinkClaims is link_claims rows dropped (migration 0115, the file-claims
	// registry). NOT on the cutoff: a claim is only interesting while live, so
	// a released one goes 24 h (ClaimRetention) after its release, and one
	// never released (a crashed session) 24 h after it was claimed.
	LinkClaims int64
	// SessionPlans is session_plan rows dropped (migration 0116, the link's
	// plan-in-the-terminal pick). Same horizon as LinkEvents, by the row's own
	// `at`, but only once its session has ENDED (ended_at set, the mod reported
	// session.end, or no sessions row is left): a long-lived terminal keeps its
	// pick however old the choice is.
	SessionPlans int64
	// VacuumErr carries a post-commit VACUUM failure (e.g. SQLITE_BUSY).
	// The destructive transaction has already committed by then, so the
	// prune itself succeeded — callers should report this as a warning
	// ("space not reclaimed"), never as a failed prune.
	VacuumErr error
}

// MaxSessionsPerPass bounds ONE prune pass. The store runs a single SQLite
// connection (internal/store.Open: db.SetMaxOpenConns(1)), so the destructive
// transaction below holds the only connection every HTTP handler and every
// ingest write queues on. Unbounded, the first pass over a real store is a
// multi-minute all-or-nothing transaction (observed: ~34k turns / 43k events /
// 4.5k file_changes on an 833 MB store, 78-98% CPU for ~5 minutes) — and
// killing it rolls everything back, so a restart loop makes zero forward
// progress while the dashboard is unresponsive. Capping the candidate set makes
// each pass a bounded slice that COMMITS; the next pass continues the backlog.
//
// 300 is the trade: large enough that a daily tick keeps up with normal volume,
// small enough that one transaction is sub-second on the connection everything
// else shares.
const MaxSessionsPerPass = 300

// candidateSet is the single source of truth for "what gets pruned": ended
// before the cutoff and not yet pruned, oldest first, capped at
// MaxSessionsPerPass. Every count, the rollup insert, and every delete embeds
// it, so they can never disagree — and because pruned stays 0 until the last
// step of the transaction, every embedding inside one pass sees the SAME rows.
// ORDER BY makes that set deterministic rather than storage-order dependent.
//
// It is a var only because the LIMIT is interpolated from the constant above
// (a bound parameter would change the argument count of every embedding query).
var candidateSet = fmt.Sprintf(`SELECT id, project_id, started_at FROM sessions
	WHERE pruned = 0 AND ended_at IS NOT NULL AND ended_at < ?
	ORDER BY ended_at, id LIMIT %d`, MaxSessionsPerPass)

// candidateProbe is candidateSet's gate, one row wider than the cap. Counting
// it answers BOTH questions in one query: how many sessions this pass takes
// (min(n, MaxSessionsPerPass)) and whether a GENUINE remainder is waiting
// (n > MaxSessionsPerPass).
//
// Counting the capped set alone cannot separate those. A backlog of exactly
// MaxSessionsPerPass counts identically to one of ten thousand, so the old
// `Sessions >= MaxSessionsPerPass` test reported a remainder at the exact-cap
// boundary where the backlog had in fact been fully drained — sending the
// operator back for a pass with nothing to do, and the daemon into logging
// "backlog remains" over an empty candidate set.
//
// It selects id only: nothing consumes the rows, the COUNT wrapping it is the
// whole point, and the LIMIT keeps the probe O(cap) on any backlog size.
var candidateProbe = fmt.Sprintf(`SELECT id FROM sessions
	WHERE pruned = 0 AND ended_at IS NOT NULL AND ended_at < ?
	ORDER BY ended_at, id LIMIT %d`, MaxSessionsPerPass+1)

// sessionPlanExpired is the WHERE clause (one ? = the cutoff) of a session_plan
// row the prune drops: its `at` (fixed-width ms UTC) is before the cutoff AND no
// still-running session carries its uuid — a sessions row with no ended_at whose
// mod has not reported session.end. A row with no sessions row at all is gone
// with it.
const sessionPlanExpired = `at < ? AND NOT EXISTS (
	SELECT 1 FROM sessions s
	 WHERE s.session_uuid = session_plan.session_id
	   AND s.ended_at IS NULL AND COALESCE(s.link_status, '') <> 'done')`

// rollupInsert aggregates the candidate sessions into daily_rollups
// (agent_id NULL — the per-project grain). Tokens/cost bucket by the turn's
// local day, tool_calls/errors by the event's local day, session counts by
// the session's local start day; the keys UNION merges the three grains.
//
// Undatable rows are EXCLUDED from every grain. SQLite's date() returns NULL
// for a timestamp it cannot parse — notably the empty string, which ingest has
// written for some 'unknown' events — and daily_rollups.day is NOT NULL, so a
// single such row used to abort the whole INSERT and therefore the whole prune
// ("NOT NULL constraint failed: daily_rollups.day"). Found on a real store: 4
// events out of 43k carried ts=”. A row with no parseable timestamp cannot be
// attributed to a day at all, so dropping it from the aggregate is the only
// meaningful option; it is still DELETED below like every other pruned row, so
// this costs a few counts in one day's rollup, never retention itself.
var rollupInsert = `
WITH pruned AS (` + candidateSet + `),
tk AS (
	SELECT date(t.started_at, 'localtime') AS day, pr.project_id,
	       SUM(COALESCE(t.tokens_in, 0))  AS tokens_in,
	       SUM(COALESCE(t.tokens_out, 0)) AS tokens_out,
	       SUM(COALESCE(t.cost_usd, 0))   AS cost_usd
	  FROM turns t JOIN pruned pr ON pr.id = t.session_id
	 WHERE date(t.started_at, 'localtime') IS NOT NULL
	 GROUP BY 1, 2
),
ev AS (
	SELECT date(e.ts, 'localtime') AS day, pr.project_id,
	       SUM(CASE WHEN e.type = 'tool_call' THEN 1 ELSE 0 END) AS tool_calls,
	       SUM(CASE WHEN e.type = 'error' OR e.status = 'error' THEN 1 ELSE 0 END) AS errors
	  FROM events e JOIN pruned pr ON pr.id = e.session_id
	 WHERE date(e.ts, 'localtime') IS NOT NULL
	 GROUP BY 1, 2
),
ss AS (
	SELECT date(started_at, 'localtime') AS day, project_id, COUNT(*) AS sessions
	  FROM pruned WHERE date(started_at, 'localtime') IS NOT NULL GROUP BY 1, 2
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

// Options tunes one prune pass. It exists to separate the two callers: the
// CLI (`swarmery prune`) runs against a quiesced DB and wants the freed pages
// back, while the daemon's daily tick runs under load and must NOT VACUUM its
// own WAL — VACUUM rewrites the whole database file and blocks every other
// writer for the duration of the rewrite.
type Options struct {
	// Vacuum reclaims freed pages after the destructive transaction commits.
	// A failure is carried on Stats.VacuumErr, never reported as a failed prune.
	Vacuum bool
	// Now is the clock the now-relative horizons (ClaimRetention) are measured
	// from; zero means time.Now(). Tests pin it.
	Now time.Time
}

// ClaimRetention is how long a released (or never-released) link_claims row
// is kept.
const ClaimRetention = 24 * time.Hour

// claimTSFormat is the fixed-width UTC form link_claims timestamps are stored
// in (internal/link tsFormat), so the TEXT comparison orders like time.
const claimTSFormat = "2006-01-02T15:04:05.000Z"

// staleClaims selects the link_claims rows older than ClaimRetention: released
// before the horizon, or never released and claimed before it.
const staleClaims = `FROM link_claims
	WHERE (released_at IS NOT NULL AND released_at < ?)
	   OR (released_at IS NULL AND claimed_at < ?)`

// Run executes one prune pass with the CLI's options (VACUUM enabled). It is
// the original entry point, kept so existing callers and tests are unchanged;
// RunWithOptions is the general form.
func Run(db *sql.DB, cutoff string, dryRun bool) (Stats, error) {
	return RunWithOptions(db, cutoff, dryRun, Options{Vacuum: true})
}

// RunWithOptions executes one prune pass. cutoff is an ISO-8601 UTC timestamp
// (the TEXT collation the schema stores). Everything except the final VACUUM
// runs in one transaction; dryRun stops after counting.
//
// A pass is BOUNDED at MaxSessionsPerPass sessions (see candidateSet), so a
// large backlog is drained over several passes rather than one transaction
// that monopolises the store's single connection. Stats.Capped reports that
// candidates remain BEYOND this pass's slice (see candidateProbe — a backlog
// of exactly the cap is drained and reports Capped false); run again, or wait
// for the next tick, to continue. DryRun counts the same bounded slice, i.e.
// what the NEXT real pass would do, not the whole backlog.
func RunWithOptions(db *sql.DB, cutoff string, dryRun bool, opts Options) (Stats, error) {
	st := Stats{Cutoff: cutoff, DryRun: dryRun}
	// The session count gates the destructive path; under DryRun the child
	// tables are pre-counted too. A real run reports Turns/Events/FileChanges
	// from RowsAffected() of the deletes inside the transaction instead, so
	// the printed numbers are exactly what was removed.
	//
	// One probe answers both "how many does this pass take" and "is anything
	// left over" — see candidateProbe for why counting the capped set alone
	// cannot tell a drained backlog of exactly the cap from a huge one.
	var waiting int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM (`+candidateProbe+`)`, cutoff).Scan(&waiting); err != nil {
		return st, fmt.Errorf("count candidates: %w", err)
	}
	st.Capped = waiting > MaxSessionsPerPass
	st.Sessions = min(waiting, MaxSessionsPerPass)
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	claimCut := now.UTC().Add(-ClaimRetention).Format(claimTSFormat)
	if dryRun {
		if err := db.QueryRow(`SELECT COUNT(*) `+staleClaims, claimCut, claimCut).Scan(&st.LinkClaims); err != nil {
			return st, fmt.Errorf("count candidates: %w", err)
		}
		counts := []struct {
			q   string
			dst *int64
		}{
			{`SELECT COUNT(*) FROM turns t JOIN (` + candidateSet + `) pr ON pr.id = t.session_id`, &st.Turns},
			{`SELECT COUNT(*) FROM events e JOIN (` + candidateSet + `) pr ON pr.id = e.session_id`, &st.Events},
			{`SELECT COUNT(*) FROM file_changes fc JOIN (` + candidateSet + `) pr ON pr.id = fc.session_id`, &st.FileChanges},
			{`SELECT COUNT(*) FROM worktree_sweeps WHERE ts < ?`, &st.WorktreeSweeps},
			{`SELECT COUNT(*) FROM link_events WHERE at < ?`, &st.LinkEvents},
			{`SELECT COUNT(*) FROM link_commands WHERE created_at < ?`, &st.LinkCommands},
			{`SELECT COUNT(*) FROM turn_forecasts WHERE started_at < ?`, &st.TurnForecasts},
			{`SELECT COUNT(*) FROM link_messages WHERE at < ?`, &st.LinkMessages},
			{`SELECT COUNT(*) FROM session_plan WHERE ` + sessionPlanExpired, &st.SessionPlans},
		}
		counts = append(counts, undatableCounts(&st)...)
		for _, c := range counts {
			if err := db.QueryRow(c.q, cutoff).Scan(c.dst); err != nil {
				return st, fmt.Errorf("count candidates: %w", err)
			}
		}
		return st, nil
	}
	// The worktree-janitor journal ages on the SAME cutoff but is not
	// session-derived, so it must be dropped before the candidate gate below:
	// leaving it inside the transaction would mean the journal is only ever
	// trimmed on days that also happen to have expiring sessions.
	if res, err := db.Exec(`DELETE FROM worktree_sweeps WHERE ts < ?`, cutoff); err == nil {
		st.WorktreeSweeps, _ = res.RowsAffected()
	} else {
		return st, fmt.Errorf("prune worktree_sweeps: %w", err)
	}
	// Same reasoning for the link ledger (see Stats.LinkEvents). at is stored
	// as fixed-width ms UTC, so the TEXT comparison against the cutoff orders
	// like time (served by idx_link_events_session_at only per session; the
	// table is small — a few rows per turn).
	if res, err := db.Exec(`DELETE FROM link_events WHERE at < ?`, cutoff); err == nil {
		st.LinkEvents, _ = res.RowsAffected()
	} else {
		return st, fmt.Errorf("prune link_events: %w", err)
	}
	// The command queue ages the same way (see Stats.LinkCommands); created_at
	// is fixed-width ms UTC too.
	if res, err := db.Exec(`DELETE FROM link_commands WHERE created_at < ?`, cutoff); err == nil {
		st.LinkCommands, _ = res.RowsAffected()
	} else {
		return st, fmt.Errorf("prune link_commands: %w", err)
	}
	// turn_forecasts age like the link ledger (see Stats.TurnForecasts);
	// started_at is the same fixed-width ms UTC form, served by
	// idx_turn_forecasts_started.
	if res, err := db.Exec(`DELETE FROM turn_forecasts WHERE started_at < ?`, cutoff); err == nil {
		st.TurnForecasts, _ = res.RowsAffected()
	} else {
		return st, fmt.Errorf("prune turn_forecasts: %w", err)
	}
	// Swarm messages age like the link ledger (see Stats.LinkMessages); at is
	// the same fixed-width ms UTC form, served by idx_link_messages_at.
	if res, err := db.Exec(`DELETE FROM link_messages WHERE at < ?`, cutoff); err == nil {
		st.LinkMessages, _ = res.RowsAffected()
	} else {
		return st, fmt.Errorf("prune link_messages: %w", err)
	}
	// Claims go on their own 24 h horizon, not the cutoff (see Stats.LinkClaims).
	if res, err := db.Exec(`DELETE `+staleClaims, claimCut, claimCut); err == nil {
		st.LinkClaims, _ = res.RowsAffected()
	} else {
		return st, fmt.Errorf("prune link_claims: %w", err)
	}
	// The terminal plan picks age like the link ledger, gated on their session
	// having ended (see Stats.SessionPlans and sessionPlanExpired).
	if res, err := db.Exec(`DELETE FROM session_plan WHERE `+sessionPlanExpired, cutoff); err == nil {
		st.SessionPlans, _ = res.RowsAffected()
	} else {
		return st, fmt.Errorf("prune session_plan: %w", err)
	}

	if st.Sessions == 0 {
		return st, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return st, err
	}
	defer tx.Rollback() // no-op after a successful Commit

	// Count the rows the rollup above is about to drop on the floor, in the
	// SAME transaction and against the SAME candidate set — the exact negation
	// of its `date(...) IS NOT NULL` guards. Without this the deletes below
	// still remove them and Stats would overstate what was preserved, so an
	// ingest regression writing ts='' at volume would be silent (it used to
	// announce itself by aborting the prune on daily_rollups.day NOT NULL).
	for _, c := range undatableCounts(&st) {
		if err := tx.QueryRow(c.q, cutoff).Scan(c.dst); err != nil {
			return st, fmt.Errorf("count undatable rows: %w", err)
		}
	}

	res, err := tx.Exec(rollupInsert, cutoff)
	if err != nil {
		return st, fmt.Errorf("rollup insert: %w", err)
	}
	st.RollupRows, _ = res.RowsAffected()

	// FK-safe order: permission_requests.event_id references events — keep
	// the approval history rows, drop the edge to the rows being deleted.
	// file_changes references events; both reference sessions (kept).
	//
	// FK-CHEAP as well: the store runs with foreign_keys=ON, so each deleted
	// event and turn is checked against every column that references it. With
	// an unindexed referencing column that check is a full scan of the child
	// table PER DELETED ROW — the first live pass sat in `DELETE FROM events`
	// for 11 hours on ~30k rows. Migration 0073 indexes those columns
	// (events.parent_event_id, events.turn_id, file_changes.event_id,
	// permission_requests.event_id); migrate_0073_test.go pins that every FK
	// child of events/turns stays indexed.
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
	if opts.Vacuum {
		if err := vacuum(db); err != nil {
			st.VacuumErr = fmt.Errorf("vacuum: %w", err)
		}
	}
	return st, nil
}

// undatableCounts returns the three "cannot be attributed to a day" queries
// bound to st's fields. Each takes the cutoff as its single argument (from the
// embedded candidateSet), so it runs identically on a *sql.DB (dry run) and on
// a *sql.Tx (the real pass).
func undatableCounts(st *Stats) []struct {
	q   string
	dst *int64
} {
	return []struct {
		q   string
		dst *int64
	}{
		{`SELECT COUNT(*) FROM turns t JOIN (` + candidateSet + `) pr ON pr.id = t.session_id
		   WHERE date(t.started_at, 'localtime') IS NULL`, &st.UndatableTurns},
		{`SELECT COUNT(*) FROM events e JOIN (` + candidateSet + `) pr ON pr.id = e.session_id
		   WHERE date(e.ts, 'localtime') IS NULL`, &st.UndatableEvents},
		{`SELECT COUNT(*) FROM (` + candidateSet + `) pr
		   WHERE date(pr.started_at, 'localtime') IS NULL`, &st.UndatableSessions},
	}
}

// vacuum is a seam for tests: injecting a real SQLITE_BUSY needs a second
// connection racing the 5s busy_timeout, which would be slow and flaky.
var vacuum = func(db *sql.DB) error {
	_, err := db.Exec(`VACUUM`)
	return err
}
