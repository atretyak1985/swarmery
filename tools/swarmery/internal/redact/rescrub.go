package redact

// Background rescrub: masks secret-shaped values in rows stored before ingest
// redacted them.
//
// It is a worker the daemon starts (cmd/swarmery serve), never a migration and
// never part of store.Open — `swarmery backup` and every test open the store
// without running it. For each text-bearing table it reads a small batch of
// rows above its cursor, redacts them in memory, and in one short transaction
// UPDATEs only the rows whose text changed and advances the cursor
// (redaction_progress, migration 0121). It then sleeps, so the single writer
// connection is free for the ingest between batches. A restart resumes from
// the cursor; a newer RulesVersion restarts every table from rowid 0.
//
// Each UPDATE is guarded by the values it read (`col IS ?`): a row that ingest
// rewrote between the read and the write is left to ingest, which redacts on
// its own.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RulesVersion identifies the rule set a rescrub pass ran with. Bump it when
// the rules change so that stored rows are scanned again.
//
//	1  the 11 classes, PEM blocks, code fields literal-only
//	2  key-cued payload masking, Basic auth, --password, OpenAI and Stripe
//	   shapes, counts after a token-named key kept; link_events covered
const RulesVersion = 2

// Rescrub defaults: a batch is small enough to hold the writer for
// milliseconds, the pause long enough to let the ingest in between.
const (
	DefaultRescrubBatch = 500
	DefaultRescrubSleep = 200 * time.Millisecond
)

// RescrubConfig tunes the worker.
type RescrubConfig struct {
	Batch    int           // rows read per batch (SWARMERY_RESCRUB_BATCH)
	Sleep    time.Duration // pause between batches (SWARMERY_RESCRUB_SLEEP_MS)
	Disabled bool          // SWARMERY_RESCRUB=off
	// OnBatch, when set, is called after each committed batch with the
	// table, its new cursor and how many rows the batch rewrote.
	OnBatch func(table string, lastRowid int64, changed int)
}

// RescrubConfigFromEnv reads the SWARMERY_RESCRUB* knobs. A malformed value
// keeps the default and is reported in warnings.
func RescrubConfigFromEnv(getenv func(string) string) (cfg RescrubConfig, warnings []string) {
	cfg = RescrubConfig{Batch: DefaultRescrubBatch, Sleep: DefaultRescrubSleep}
	switch strings.ToLower(strings.TrimSpace(getenv("SWARMERY_RESCRUB"))) {
	case "off", "0", "false", "no", "disabled":
		cfg.Disabled = true
	}
	if v := strings.TrimSpace(getenv("SWARMERY_RESCRUB_BATCH")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Batch = n
		} else {
			warnings = append(warnings, fmt.Sprintf("ignoring SWARMERY_RESCRUB_BATCH=%q (want a positive integer)", v))
		}
	}
	if v := strings.TrimSpace(getenv("SWARMERY_RESCRUB_SLEEP_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Sleep = time.Duration(n) * time.Millisecond
		} else {
			warnings = append(warnings, fmt.Sprintf("ignoring SWARMERY_RESCRUB_SLEEP_MS=%q (want a non-negative integer)", v))
		}
	}
	return cfg, warnings
}

// target is one table the rescrub walks.
type target struct {
	table string
	// name keys the pass in redaction_progress and in RescrubStats; empty
	// means table. A pass over a subset of another target's rows has its own
	// name, so it keeps its own cursor (CountStored skips it: the rows are
	// counted once, under the table's own pass).
	name  string
	cols  []string // text columns it may rewrite
	ctx   []string // columns read to choose the redactor, never written
	where string   // extra row filter, ANDed with the cursor
	// fix returns the redacted cols and whether any changed.
	fix func(cols, ctx []sql.NullString) ([]sql.NullString, bool)
}

// key is the pass's progress key.
func (t target) key() string {
	if t.name != "" {
		return t.name
	}
	return t.table
}

// Targets lists the tables and columns the rescrub covers — the text the
// transcript ingest and the approvals path write.
func Targets() map[string][]string {
	out := map[string][]string{}
	for _, t := range targets {
		out[t.table] = append([]string(nil), t.cols...)
	}
	return out
}

var targets = []target{
	{table: "events", cols: []string{"payload"}, ctx: []string{"tool_name"}, fix: fixPayload},
	{table: "turns", cols: []string{"text"}, fix: fixWith(All)},
	{table: "file_changes", cols: []string{"diff"}, fix: fixWith(Literal)},
	{table: "sessions", cols: []string{"title"}, fix: fixWith(All)},
	// Only the cards ingest captured from a session: their text is transcript
	// text. A card the operator typed is theirs and is left as written.
	{table: "tasks", cols: []string{"title", "prompt", "origin_quote"}, where: "origin = 'session'", fix: fixWith(All)},
	// Approval requests (internal/approvals): the stored hook stdin, redacted
	// per tool like a tool-call payload.
	{table: "permission_requests", cols: []string{"request_json"}, ctx: []string{"tool_name"}, fix: fixPayload},
	// Their permission_request events, under a progress key of their own: a
	// store whose events pass finished before the approvals path masked new
	// requests still holds raw ones above that pass's cursor, and this
	// re-arms only them instead of a RulesVersion bump that would rescan
	// every table. Selected through permission_requests.event_id — a lookup
	// per request — rather than events.type, which could scan the whole
	// events table for each batch.
	{table: "events", name: PermissionEventsProgress, cols: []string{"payload"}, ctx: []string{"tool_name"},
		where: "rowid IN (SELECT event_id FROM permission_requests WHERE event_id IS NOT NULL)", fix: fixPayload},
	// swarmery-link mod events (internal/link): a JSON payload with no tool
	// context, redacted like a non-tool event.
	{table: "link_events", cols: []string{"payload"}, fix: fixWith(func(doc string) string {
		out, _ := PayloadJSON("", doc)
		return out
	})},
	// Swarm messages the sessions exchanged (internal/link swarm.go): text.
	{table: "link_messages", cols: []string{"body", "reason"}, fix: fixWith(All)},
}

// PermissionEventsProgress is the redaction_progress key of the pass over the
// permission_request events.
const PermissionEventsProgress = "events:permission_request"

func fixWith(fn func(string) string) func(cols, _ []sql.NullString) ([]sql.NullString, bool) {
	return func(cols, _ []sql.NullString) ([]sql.NullString, bool) {
		out := make([]sql.NullString, len(cols))
		changed := false
		for i, c := range cols {
			out[i] = c
			if !c.Valid {
				continue
			}
			if r := fn(c.String); r != c.String {
				out[i].String = r
				changed = true
			}
		}
		return out, changed
	}
}

func fixPayload(cols, ctx []sql.NullString) ([]sql.NullString, bool) {
	if !cols[0].Valid {
		return cols, false
	}
	doc, changed := PayloadJSON(ctx[0].String, cols[0].String)
	if !changed {
		return cols, false
	}
	return []sql.NullString{{String: doc, Valid: true}}, true
}

// RescrubStats summarizes one Rescrub call.
type RescrubStats struct {
	Scanned map[string]int // rows read per table
	Changed map[string]int // rows rewritten per table
	Done    map[string]bool
	// FTSMergeSteps counts the full-text merge steps that did work.
	FTSMergeSteps int64
}

// Rescrub runs every table to done, or until ctx is cancelled (then it
// returns ctx.Err() with the progress made so far persisted). A table whose
// progress row is done under the current RulesVersion is skipped.
func Rescrub(ctx context.Context, db *sql.DB, cfg RescrubConfig) (RescrubStats, error) {
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultRescrubBatch
	}
	stats := RescrubStats{Scanned: map[string]int{}, Changed: map[string]int{}, Done: map[string]bool{}}
	for _, t := range targets {
		if err := rescrubTable(ctx, db, cfg, t, &stats); err != nil {
			return stats, err
		}
	}
	if err := purgeFTS(ctx, db, cfg, &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

// ftsProgress is the redaction_progress row of the full-text index purge.
const ftsProgress = "turns_fts"

// ftsMergePages bounds one incremental merge step, in index pages written.
const ftsMergePages = 200

// owePurge records (or re-opens) the full-text purge for the current rules,
// inside the transaction that rewrote turns — so a run stopped between the
// turns pass and the purge still owes it on the next start.
func owePurge(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO redaction_progress (table_name, last_rowid, done, rules_version) VALUES (?, 0, 0, ?)
		 ON CONFLICT(table_name) DO UPDATE SET last_rowid = 0, done = 0, rules_version = excluded.rules_version`,
		ftsProgress, RulesVersion)
	return err
}

// purgeFTS drops the old terms of rewritten turns from the full-text index.
// turns_fts (migration 0012) is external-content: the UPDATE trigger deletes a
// row's old terms, but FTS5 records a deletion as a marker and keeps the
// original entry in its segment until the segments are merged.
//
// It runs only when the turns pass rewrote a row (owePurge left an open
// progress row); otherwise it records done without touching the index. The
// purge merges the index in small steps — FTS5's 'merge' command with a
// negative page count, which merges every segment — each in its own short
// transaction with the batch pause in between. The number of steps is bounded
// by the index size at the start: live ingest keeps adding small segments, so
// "until a step does no work" might never come. Once the budget is spent (or a
// step does no work) the purge is done, whatever segments arrived meanwhile —
// those hold only text ingest has already redacted.
func purgeFTS(ctx context.Context, db *sql.DB, cfg RescrubConfig, stats *RescrubStats) error {
	var exists int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, ftsProgress).Scan(&exists); err != nil {
		return fmt.Errorf("rescrub fts: %w", err)
	}
	if exists == 0 {
		return nil
	}
	var done int
	err := db.QueryRowContext(ctx,
		`SELECT done FROM redaction_progress WHERE table_name = ?`, ftsProgress).Scan(&done)
	switch {
	case err == sql.ErrNoRows:
		// The turns pass rewrote nothing: no old terms to purge.
		if _, err := db.ExecContext(ctx,
			`INSERT INTO redaction_progress (table_name, last_rowid, done, rules_version) VALUES (?, 0, 1, ?)`,
			ftsProgress, RulesVersion); err != nil {
			return fmt.Errorf("rescrub fts: mark done: %w", err)
		}
		stats.Done[ftsProgress] = true
		return nil
	case err != nil:
		return fmt.Errorf("rescrub fts: progress: %w", err)
	case done == 1:
		stats.Done[ftsProgress] = true
		return nil
	}
	var pages int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turns_fts_data`).Scan(&pages); err != nil {
		return fmt.Errorf("rescrub fts: size: %w", err)
	}
	budget := FTSStepBudget(pages)
	for step := int64(1); ; step++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		worked, err := ftsMergeStep(ctx, db)
		if err != nil {
			return fmt.Errorf("rescrub fts: merge: %w", err)
		}
		if worked {
			stats.FTSMergeSteps++
		}
		if !worked || step >= budget {
			if _, err := db.ExecContext(ctx,
				`UPDATE redaction_progress SET done = 1, last_rowid = ? WHERE table_name = ?`, step, ftsProgress); err != nil {
				return fmt.Errorf("rescrub fts: mark done: %w", err)
			}
			stats.Done[ftsProgress] = true
			return nil
		}
		if cfg.OnBatch != nil {
			cfg.OnBatch(ftsProgress, step, 0)
		}
		if cfg.Sleep > 0 {
			timer := time.NewTimer(cfg.Sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

// FTSStepBudget is the most merge steps a purge takes for an index of pages
// turns_fts_data rows: enough to rewrite the starting index twice at
// ftsMergePages pages a step, and at least 4.
func FTSStepBudget(pages int64) int64 {
	b := 2*pages/ftsMergePages + 2
	if b < 4 {
		b = 4
	}
	return b
}

// ftsMergeStep runs one bounded merge and reports whether it did any work —
// per the FTS5 documentation, total_changes() grows by 2 or more when it did.
// One transaction, so the three statements share a connection.
func ftsMergeStep(ctx context.Context, db *sql.DB) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	var before, after int64
	if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO turns_fts(turns_fts, rank) VALUES ('merge', ?)`, -ftsMergePages); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&after); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return after-before >= 2, nil
}

func rescrubTable(ctx context.Context, db *sql.DB, cfg RescrubConfig, t target, stats *RescrubStats) error {
	key := t.key()
	last, done, err := loadProgress(ctx, db, key)
	if err != nil {
		return err
	}
	if done {
		stats.Done[key] = true
		return nil
	}
	sel := "SELECT rowid, " + strings.Join(append(append([]string{}, t.cols...), t.ctx...), ", ") +
		" FROM " + t.table + " WHERE rowid > ?"
	if t.where != "" {
		sel += " AND " + t.where
	}
	sel += " ORDER BY rowid LIMIT ?"
	upd := updateSQL(t)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := readBatch(ctx, db, sel, last, cfg.Batch, len(t.cols)+len(t.ctx))
		if err != nil {
			return fmt.Errorf("rescrub %s: read: %w", key, err)
		}
		if len(rows) == 0 {
			if _, err := db.ExecContext(ctx,
				`UPDATE redaction_progress SET done = 1 WHERE table_name = ?`, key); err != nil {
				return fmt.Errorf("rescrub %s: mark done: %w", key, err)
			}
			stats.Done[key] = true
			return nil
		}
		stats.Scanned[key] += len(rows)
		next := rows[len(rows)-1].rowid

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("rescrub %s: begin: %w", t.table, err)
		}
		changed := 0
		for _, r := range rows {
			cols, ctxCols := r.vals[:len(t.cols)], r.vals[len(t.cols):]
			fixed, ok := t.fix(cols, ctxCols)
			if !ok {
				continue
			}
			args := make([]any, 0, 2*len(cols)+1)
			for _, v := range fixed {
				args = append(args, v)
			}
			args = append(args, r.rowid)
			for _, v := range cols {
				args = append(args, v)
			}
			res, err := tx.ExecContext(ctx, upd, args...)
			if err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("rescrub %s: update rowid %d: %w", t.table, r.rowid, err)
			}
			if n, _ := res.RowsAffected(); n > 0 {
				changed++
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE redaction_progress SET last_rowid = ? WHERE table_name = ?`, next, key); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("rescrub %s: cursor: %w", key, err)
		}
		// A rewritten turn leaves its old terms in the full-text index until
		// the index is merged: record, with the batch, that a purge is owed.
		if t.table == "turns" && changed > 0 {
			if err := owePurge(ctx, tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("rescrub %s: fts purge marker: %w", t.table, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("rescrub %s: commit: %w", t.table, err)
		}
		last = next
		stats.Changed[key] += changed
		if cfg.OnBatch != nil {
			cfg.OnBatch(key, last, changed)
		}
		if cfg.Sleep > 0 {
			timer := time.NewTimer(cfg.Sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

// updateSQL sets every rewritable column, guarded by the values read.
func updateSQL(t target) string {
	set := make([]string, len(t.cols))
	guard := make([]string, len(t.cols))
	for i, c := range t.cols {
		set[i] = c + " = ?"
		guard[i] = c + " IS ?"
	}
	return "UPDATE " + t.table + " SET " + strings.Join(set, ", ") +
		" WHERE rowid = ? AND " + strings.Join(guard, " AND ")
}

type batchRow struct {
	rowid int64
	vals  []sql.NullString
}

// readBatch collects the whole batch and closes the cursor before any write:
// the store's writer is a single connection.
func readBatch(ctx context.Context, db *sql.DB, q string, after int64, limit, ncols int) ([]batchRow, error) {
	rows, err := db.QueryContext(ctx, q, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []batchRow
	for rows.Next() {
		r := batchRow{vals: make([]sql.NullString, ncols)}
		dest := make([]any, ncols+1)
		dest[0] = &r.rowid
		for i := range r.vals {
			dest[i+1] = &r.vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadProgress returns the table's cursor, creating (or resetting, when it was
// written under another RulesVersion) its progress row.
func loadProgress(ctx context.Context, db *sql.DB, table string) (last int64, done bool, err error) {
	var version int
	var doneN int
	err = db.QueryRowContext(ctx,
		`SELECT last_rowid, done, rules_version FROM redaction_progress WHERE table_name = ?`, table).
		Scan(&last, &doneN, &version)
	switch {
	case err == sql.ErrNoRows || (err == nil && version != RulesVersion):
		_, err = db.ExecContext(ctx,
			`INSERT INTO redaction_progress (table_name, last_rowid, done, rules_version) VALUES (?, 0, 0, ?)
			 ON CONFLICT(table_name) DO UPDATE SET last_rowid = 0, done = 0, rules_version = excluded.rules_version`,
			table, RulesVersion)
		if err != nil {
			return 0, false, fmt.Errorf("rescrub %s: progress: %w", table, err)
		}
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("rescrub %s: progress: %w", table, err)
	}
	return last, doneN == 1, nil
}

// RunRescrub is the daemon's entry point: it honours Disabled, runs Rescrub,
// and logs the outcome through logf. It returns when every table is done or
// ctx is cancelled.
func RunRescrub(ctx context.Context, db *sql.DB, cfg RescrubConfig, logf func(string, ...any)) {
	if cfg.Disabled {
		logf("swarmery rescrub disabled (SWARMERY_RESCRUB=off)")
		return
	}
	start := time.Now()
	stats, err := Rescrub(ctx, db, cfg)
	scanned, changed := 0, 0
	for _, n := range stats.Scanned {
		scanned += n
	}
	for _, n := range stats.Changed {
		changed += n
	}
	switch {
	case err == context.Canceled || err == context.DeadlineExceeded:
		logf("swarmery rescrub paused at shutdown: %d rows read, %d rewritten in %s (resumes on next start)",
			scanned, changed, time.Since(start).Round(time.Millisecond))
	case err != nil:
		logf("warning: swarmery rescrub stopped: %v (%d rows read, %d rewritten)", err, scanned, changed)
	default:
		logf("swarmery rescrub done: %d rows read, %d rewritten in %s", scanned, changed, time.Since(start).Round(time.Millisecond))
	}
}
