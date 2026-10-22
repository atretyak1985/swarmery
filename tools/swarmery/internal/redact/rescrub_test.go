package redact

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func seededStore(t *testing.T, rows int) (*sql.DB, string, redacttest.SeedStats) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rescrub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := redacttest.Seed(db, rows)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if st.SecretRows == 0 {
		t.Fatal("seed wrote no secret rows")
	}
	return db, path, st
}

func leaks(t *testing.T, db *sql.DB) []string {
	t.Helper()
	l, err := redacttest.Leaks(db, redacttest.Samples())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

var fastCfg = RescrubConfig{Batch: 50}

func TestRescrubMasksStoredRows(t *testing.T) {
	db, _, seeded := seededStore(t, 600)
	if len(leaks(t, db)) == 0 {
		t.Fatal("seeded store holds no secrets — the test proves nothing")
	}
	stats, err := Rescrub(context.Background(), db, fastCfg)
	if err != nil {
		t.Fatalf("Rescrub: %v", err)
	}
	for _, l := range leaks(t, db) {
		t.Error(l)
	}
	changed := 0
	for _, tbl := range []string{"events", "turns", "file_changes", "sessions", "tasks"} {
		if !stats.Done[tbl] {
			t.Errorf("%s not done", tbl)
		}
		changed += stats.Changed[tbl]
	}
	if changed != seeded.SecretRows {
		t.Errorf("rows rewritten = %d, want exactly the %d secret rows", changed, seeded.SecretRows)
	}
	var done, total int
	if err := db.QueryRow(`SELECT SUM(done), COUNT(*) FROM redaction_progress WHERE rules_version = ?`, RulesVersion).
		Scan(&done, &total); err != nil {
		t.Fatal(err)
	}
	// One row per table, plus the full-text index purge.
	if want := len(targets) + 1; done != want || total != want {
		t.Errorf("progress done=%d of %d rows, want %d done", done, total, want)
	}
	if !stats.Done[ftsProgress] {
		t.Error("the full-text index purge did not finish")
	}

	// Code keeps its assignment: Edit payloads and diffs go through Literal.
	var payload, diff string
	if err := db.QueryRow(`SELECT payload FROM events WHERE tool_name = 'Edit' ORDER BY id LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Input struct {
			NewString string `json:"new_string"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Input.NewString, redacttest.SourceAssignment()) {
		t.Errorf("Edit new_string lost its source line: %q", p.Input.NewString)
	}
	if err := db.QueryRow(`SELECT diff FROM file_changes ORDER BY id DESC LIMIT 1`).Scan(&diff); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, strings.TrimSuffix(redacttest.SourceAssignment(), "\n")) {
		t.Errorf("diff lost its source line:\n%s", diff)
	}

	// A second run finds every table done and reads nothing.
	again, err := Rescrub(context.Background(), db, fastCfg)
	if err != nil {
		t.Fatal(err)
	}
	for tbl, n := range again.Scanned {
		if n != 0 {
			t.Errorf("second run read %d %s rows, want 0", n, tbl)
		}
	}
}

// CountStored sees every seeded secret class before a rescrub and nothing
// after it, and never reports a value.
func TestCountStoredBeforeAndAfter(t *testing.T) {
	db, _, _ := seededStore(t, 300)
	before, err := CountStored(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if before.Total() == 0 {
		t.Fatal("count before the rescrub is 0")
	}
	for _, s := range redacttest.Samples() {
		found := false
		for _, byClass := range before.Columns {
			if byClass[s.Class] > 0 {
				found = true
			}
		}
		if !found {
			t.Errorf("class %s not counted before the rescrub", s.Class)
		}
	}
	for _, line := range before.Lines() {
		for _, needle := range redacttest.Needles() {
			if strings.Contains(line, needle) {
				t.Fatalf("a count line holds a secret value")
			}
		}
	}
	if _, err := Rescrub(context.Background(), db, fastCfg); err != nil {
		t.Fatal(err)
	}
	after, err := CountStored(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if after.Total() != 0 {
		t.Errorf("count after the rescrub = %d, want 0: %v", after.Total(), after.Lines())
	}
}

// Rows without secrets are never UPDATEd: an AFTER UPDATE trigger counts the
// writes the rescrub issues per table.
func TestRescrubDoesNotRewriteCleanRows(t *testing.T) {
	db, _, _ := seededStore(t, 300)
	if _, err := db.Exec(`CREATE TABLE upd_log (tbl TEXT, rid INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"events", "turns", "file_changes", "sessions"} {
		if _, err := db.Exec(`CREATE TRIGGER upd_` + tbl + ` AFTER UPDATE ON ` + tbl +
			` BEGIN INSERT INTO upd_log VALUES ('` + tbl + `', new.rowid); END`); err != nil {
			t.Fatal(err)
		}
	}
	// What a rescrub must rewrite: the rows whose text holds a sample now.
	want := map[string]int{}
	for tbl, cols := range Targets() {
		if tbl == "tasks" {
			continue
		}
		var n int
		q := `SELECT COUNT(*) FROM ` + tbl + ` WHERE ` + cols[0] + ` LIKE '%' || ? || '%'`
		if err := db.QueryRow(q, redacttest.Samples()[0].Needle).Scan(&n); err != nil {
			t.Fatal(err)
		}
		want[tbl] = n
	}
	if _, err := Rescrub(context.Background(), db, fastCfg); err != nil {
		t.Fatal(err)
	}
	for tbl, n := range want {
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM upd_log WHERE tbl = ?`, tbl).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != n {
			t.Errorf("%s: %d UPDATEs, want %d (only the rows holding secrets)", tbl, got, n)
		}
	}
}

// A worker stopped mid-table resumes from its persisted cursor: the second run
// reads only the rows the first did not commit.
func TestRescrubResumesFromCursor(t *testing.T) {
	db, _, _ := seededStore(t, 400)
	var totalEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&totalEvents); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := RescrubConfig{Batch: 20, OnBatch: func(table string, _ int64, _ int) {
		if table == "events" {
			cancel()
		}
	}}
	first, err := Rescrub(ctx, db, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run: err = %v, want context.Canceled", err)
	}
	if first.Scanned["events"] != 20 {
		t.Fatalf("first run read %d events, want one batch of 20", first.Scanned["events"])
	}
	var last int64
	var done int
	if err := db.QueryRow(`SELECT last_rowid, done FROM redaction_progress WHERE table_name = 'events'`).
		Scan(&last, &done); err != nil {
		t.Fatal(err)
	}
	if last == 0 || done != 0 {
		t.Fatalf("cursor after the stop: last_rowid=%d done=%d, want >0 and 0", last, done)
	}

	second, err := Rescrub(context.Background(), db, RescrubConfig{Batch: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := second.Scanned["events"], totalEvents-20; got != want {
		t.Errorf("second run read %d events, want %d (resumed after the cursor)", got, want)
	}
	for _, l := range leaks(t, db) {
		t.Error(l)
	}
}

// The full-text purge finishes within its step budget while turns keep
// arriving: live ingest adds a segment per turn, so "merge until no work" is
// never the stop condition.
func TestRescrubFTSPurgeBoundedUnderConcurrentInserts(t *testing.T) {
	db, _, _ := seededStore(t, 2000)
	var sessionID int64
	if err := db.QueryRow(`SELECT id FROM sessions ORDER BY id LIMIT 1`).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	var pages int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM turns_fts_data`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	inserted := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				inserted <- n
				return
			default:
			}
			if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at, text)
				VALUES (?, ?, 'assistant', '2026-10-22T12:00:00Z', ?)`,
				sessionID, 100000+n, "fresh turn text number "+strings.Repeat("w", n%7)); err == nil {
				n++
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stats, err := Rescrub(ctx, db, RescrubConfig{Batch: 100, Sleep: 20 * time.Millisecond})
	close(stop)
	n := <-inserted
	if err != nil {
		t.Fatalf("Rescrub: %v", err)
	}
	if !stats.Done[ftsProgress] {
		t.Fatal("the full-text purge did not finish")
	}
	// The budget is computed when the purge starts; the index has grown since
	// the seed by the concurrent turns, so allow for that growth.
	if max := FTSStepBudget(pages + int64(n)*4); stats.FTSMergeSteps > max {
		t.Errorf("merge steps = %d, want ≤ %d (bounded by the index size)", stats.FTSMergeSteps, max)
	}
	if n == 0 {
		t.Error("no concurrent turn was inserted — the test did not exercise live ingest")
	}
	for _, l := range leaks(t, db) {
		t.Error(l)
	}
}

// A run whose turns pass rewrites nothing never merges the index.
func TestRescrubNoTurnChangesNoMerge(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "clean.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/p', 'p', '2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (1, 'u', '2026-10-22T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at, text) VALUES (1, ?, 'user', '2026-10-22T00:00:00Z', 'plain words')`, i); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := Rescrub(context.Background(), db, fastCfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Changed["turns"] != 0 {
		t.Fatalf("turns changed = %d, want 0", stats.Changed["turns"])
	}
	if stats.FTSMergeSteps != 0 {
		t.Errorf("merge steps = %d, want 0 when no turn was rewritten", stats.FTSMergeSteps)
	}
	if !stats.Done[ftsProgress] {
		t.Error("purge not recorded done")
	}
}

// A newer rule set restarts a finished table from rowid 0.
func TestRescrubRulesVersionRestarts(t *testing.T) {
	db, _, _ := seededStore(t, 120)
	if _, err := db.Exec(`INSERT INTO redaction_progress (table_name, last_rowid, done, rules_version)
		VALUES ('turns', 999999, 1, ?)`, RulesVersion-1); err != nil {
		t.Fatal(err)
	}
	stats, err := Rescrub(context.Background(), db, fastCfg)
	if err != nil {
		t.Fatal(err)
	}
	var turns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if stats.Scanned["turns"] != turns {
		t.Errorf("turns read = %d, want all %d after a rules-version change", stats.Scanned["turns"], turns)
	}
}

func TestRescrubOffSwitch(t *testing.T) {
	env := map[string]string{"SWARMERY_RESCRUB": "off", "SWARMERY_RESCRUB_BATCH": "7", "SWARMERY_RESCRUB_SLEEP_MS": "0"}
	cfg, warns := RescrubConfigFromEnv(func(k string) string { return env[k] })
	if !cfg.Disabled || cfg.Batch != 7 || cfg.Sleep != 0 || len(warns) != 0 {
		t.Fatalf("cfg = %+v warns = %v", cfg, warns)
	}
	def, _ := RescrubConfigFromEnv(func(string) string { return "" })
	if def.Disabled || def.Batch != DefaultRescrubBatch || def.Sleep != DefaultRescrubSleep {
		t.Errorf("defaults = %+v", def)
	}
	_, warns = RescrubConfigFromEnv(func(k string) string {
		return map[string]string{"SWARMERY_RESCRUB_BATCH": "-1", "SWARMERY_RESCRUB_SLEEP_MS": "x"}[k]
	})
	if len(warns) != 2 {
		t.Errorf("warnings = %v, want 2", warns)
	}

	db, _, _ := seededStore(t, 60)
	var logged []string
	RunRescrub(context.Background(), db, cfg, func(f string, a ...any) { logged = append(logged, f) })
	if len(leaks(t, db)) == 0 {
		t.Error("RunRescrub rewrote rows with SWARMERY_RESCRUB=off")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM redaction_progress`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(logged) != 1 {
		t.Errorf("off: progress rows=%d, log lines=%d, want 0 and 1", n, len(logged))
	}
}

// store.Open never runs the rescrub: a reopened store still holds the raw rows
// and no progress.
func TestStoreOpenDoesNotRescrub(t *testing.T) {
	db, path, _ := seededStore(t, 60)
	db.Close()
	again, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if len(leaks(t, again)) == 0 {
		t.Error("rows were rescrubbed by store.Open")
	}
	var n int
	if err := again.QueryRow(`SELECT COUNT(*) FROM redaction_progress`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("redaction_progress has %d rows after store.Open, want 0", n)
	}
}
