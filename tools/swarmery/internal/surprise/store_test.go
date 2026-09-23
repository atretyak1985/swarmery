package surprise

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const runUUID = "run-uuid-1"

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "surprise.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seed inserts a project, an epic and one finished phase run, and returns the
// phase id. Forecasts and actuals are added per test.
func seed(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	mustExec(t, db, `INSERT INTO projects(id, path, slug, first_seen) VALUES(1, '/repo', 'p', '2026-01-01T00:00:00Z')`)
	taskID := mustExec(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1, 'The Plan', 'goal', 'running',
		'2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z', 'workspace', '2026-09-23-plan')`)
	return mustExec(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, run_state, run_session_uuid)
		VALUES (?, 1, 'Phase One', '/ws/plan/phase-1.md', '[]', 'done', ?)`, taskID, runUUID)
}

func addForecast(t *testing.T, db *sql.DB, phaseID int64, kind, areas, size, outcome string, conf any, postHoc int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO phase_forecasts
		(phase_id, kind, areas_json, size_band, duration_band, outcome, confidence, post_hoc, doc_hash)
		VALUES (?, ?, ?, ?, '30-90m', ?, ?, ?, 'h1')`, phaseID, kind, areas, size, outcome, conf, postHoc)
}

// addActuals records a run that touched internal/store and web/src, M-sized,
// 45 minutes, with the given outcome.
func addActuals(t *testing.T, db *sql.DB, phaseID int64, outcome, source string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO phase_actuals
		(phase_id, session_uuid, files_json, areas_json, area_depth, size_band, duration_s,
		 outcome, test_failures_unexpected, source, computed_at)
		VALUES (?, ?, '[{"path":"internal/store/a.go","added":10,"removed":0},{"path":"web/src/x.ts","added":5,"removed":1}]',
		        '["internal/store","web/src"]', 2, 'M', 2700, ?, 0, ?, '2026-09-23T10:00:00Z')
		ON CONFLICT(session_uuid) DO UPDATE SET outcome = excluded.outcome, source = excluded.source`,
		phaseID, runUUID, outcome, source)
}

func newScorer(db *sql.DB) *Scorer {
	s := NewScorer(db, DefaultConfig())
	s.Now = func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	return s
}

func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM phase_surprise`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScoreStoresTheRunsScore(t *testing.T) {
	db := openDB(t)
	p := seed(t, db)
	addForecast(t, db, p, "prior", `["internal/store"]`, "S", "done", 0.9, 0)
	addForecast(t, db, p, "posterior", `["internal/store","internal/api"]`, "M", "done", 0.7, 0)
	addActuals(t, db, p, "partial", "run-end")

	st, err := newScorer(db).Score(p, runUUID)
	if err != nil || st == nil {
		t.Fatalf("Score = %v, %v", st, err)
	}
	if st.Detail.ForecastKind != "posterior" {
		t.Errorf("scored the %q, want the non-post-hoc posterior", st.Detail.ForecastKind)
	}
	if st.Top != CompOutcomeMiss || st.Index <= 0 {
		t.Errorf("index %v top %q, want > 0 led by outcome_miss", st.Index, st.Top)
	}
	if st.Revision == nil || st.Revision.SizeShift == nil || *st.Revision.SizeShift != 1 {
		t.Errorf("revision = %+v, want the prior→posterior size shift of +1", st.Revision)
	}
	if st.ComputedAt != "2026-09-23T12:00:00Z" || st.ActualsSource != "run-end" {
		t.Errorf("computedAt %q source %q", st.ComputedAt, st.ActualsSource)
	}

	// Recompute is idempotent: one row per run.
	if _, err := newScorer(db).Score(p, runUUID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db); n != 1 {
		t.Errorf("%d rows after a recompute, want 1", n)
	}
}

// A post-hoc posterior is not scored against; the prior is, and the row says so.
func TestAPostHocPosteriorFallsBackToThePrior(t *testing.T) {
	db := openDB(t)
	p := seed(t, db)
	addForecast(t, db, p, "prior", `["internal/store"]`, "M", "done", nil, 0)
	addForecast(t, db, p, "posterior", `["web/src"]`, "XL", "blocked", nil, 1)
	addActuals(t, db, p, "completed", "run-end")
	st, err := newScorer(db).Score(p, runUUID)
	if err != nil || st == nil {
		t.Fatalf("Score = %v, %v", st, err)
	}
	if st.Detail.ForecastKind != "prior" || st.Revision != nil {
		t.Errorf("kind %q revision %v, want the prior and no revision", st.Detail.ForecastKind, st.Revision)
	}
}

// No forecast ⇒ no score row (never a zero), and a stale row is removed.
func TestNoForecastIsNoScore(t *testing.T) {
	db := openDB(t)
	p := seed(t, db)
	addActuals(t, db, p, "completed", "run-end")
	mustExec(t, db, `INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, computed_at)
		VALUES (?, ?, 0.9, 'stale')`, p, runUUID)

	st, err := newScorer(db).Score(p, runUUID)
	if err != nil || st != nil {
		t.Fatalf("Score = %v, %v; want (nil, nil)", st, err)
	}
	if n := countRows(t, db); n != 0 {
		t.Errorf("%d rows for a run with no forecast, want 0 — no score, not a zero score", n)
	}
}

func TestNoActualsIsNoScore(t *testing.T) {
	db := openDB(t)
	p := seed(t, db)
	addForecast(t, db, p, "prior", `["internal/store"]`, "M", "done", nil, 0)
	st, err := newScorer(db).Score(p, runUUID)
	if err != nil || st != nil || countRows(t, db) != 0 {
		t.Fatalf("Score = %v, %v (rows %d); want no score", st, err, countRows(t, db))
	}
}

// Attention fires once per run, at or above the threshold, never for a backfill.
func TestAttentionThresholdAndOnce(t *testing.T) {
	db := openDB(t)
	p := seed(t, db)
	// Posterior: done, S-sized, confident, naming an area the run never touched —
	// the run is partial, M, and also touched web/src.
	addForecast(t, db, p, "posterior", `["internal/cost"]`, "XS", "done", 1.0, 0)
	addActuals(t, db, p, "partial", "run-end")

	var got []Attention
	s := newScorer(db)
	s.Attention = func(a Attention) { got = append(got, a) }
	var changed []int64
	s.Changed = func(id int64) { changed = append(changed, id) }

	s.AfterActuals(p, runUUID, "run-end")
	s.AfterActuals(p, runUUID, "run-end-settled") // the settled recompute
	if len(got) != 1 {
		t.Fatalf("attention raised %d times, want exactly once per run", len(got))
	}
	a := got[0]
	if a.PhaseID != p || a.PhaseName != "Phase One" || a.PlanTitle != "The Plan" || a.Index < 0.6 || a.Top == "" {
		t.Errorf("attention = %+v", a)
	}
	if !strings.HasPrefix(a.Summary, "surprise ") {
		t.Errorf("summary %q", a.Summary)
	}
	if len(changed) != 2 {
		t.Errorf("Changed called %d times, want once per pass", len(changed))
	}
	st, _ := LoadBySession(db, runUUID)
	if st == nil || st.NotifiedAt == nil {
		t.Error("notified_at not recorded")
	}

	// Below the threshold: nothing.
	db2 := openDB(t)
	p2 := seed(t, db2)
	addForecast(t, db2, p2, "posterior", `["internal/store","web/src"]`, "M", "done", nil, 0)
	addActuals(t, db2, p2, "partial", "run-end") // outcome miss only: 0.25
	s2 := newScorer(db2)
	fired := false
	s2.Attention = func(Attention) { fired = true }
	s2.AfterActuals(p2, runUUID, "run-end")
	if fired {
		t.Error("attention raised below the 0.6 threshold")
	}

	// A backfill never raises attention, however surprising.
	db3 := openDB(t)
	p3 := seed(t, db3)
	addForecast(t, db3, p3, "posterior", `["internal/cost"]`, "XS", "done", 1.0, 0)
	addActuals(t, db3, p3, "partial", "backfill")
	s3 := newScorer(db3)
	s3.Attention = func(Attention) { fired = true }
	s3.AfterActuals(p3, runUUID, "backfill")
	if fired {
		t.Error("attention raised for a backfilled run")
	}
	if st, _ := LoadBySession(db3, runUUID); st == nil {
		t.Error("the backfill did not store a score")
	}
}

// Auto-verify is off by default; when enabled it answers true once, with a hint.
func TestAutoVerifyHint(t *testing.T) {
	db := openDB(t)
	p := seed(t, db)
	addForecast(t, db, p, "posterior", `["internal/cost"]`, "XS", "done", 1.0, 0)
	addActuals(t, db, p, "partial", "run-end")
	s := newScorer(db)
	s.AfterActuals(p, runUUID, "run-end")

	if _, ok := s.AutoVerifyHint(p, runUUID); ok {
		t.Fatal("auto-verify fired with the default configuration — it must be off unless enabled")
	}

	at := 0.5
	s.Cfg.AutoVerifyAt = &at
	hint, ok := s.AutoVerifyHint(p, runUUID)
	if !ok || !strings.Contains(hint, "diverged from its forecast") {
		t.Fatalf("AutoVerifyHint = (%q, %v), want a hint", hint, ok)
	}
	if _, ok := s.AutoVerifyHint(p, runUUID); ok {
		t.Error("auto-verify fired twice for one run")
	}
	if _, ok := s.AutoVerifyHint(p+1, runUUID); ok {
		t.Error("auto-verify answered for a phase the run does not belong to")
	}

	high := 0.99
	s.Cfg.AutoVerifyAt = &high
	mustExec(t, db, `UPDATE phase_surprise SET auto_verify_at = NULL`)
	if _, ok := s.AutoVerifyHint(p, runUUID); ok {
		t.Error("auto-verify fired below its threshold")
	}
}

func TestBackfillRescoresEveryRunWithActuals(t *testing.T) {
	db := openDB(t)
	p := seed(t, db)
	addForecast(t, db, p, "prior", `["internal/store"]`, "M", "done", nil, 0)
	addActuals(t, db, p, "completed", "backfill")
	// A second phase with actuals and no forecast.
	p2 := mustExec(t, db, `INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, depends_on, run_state, run_session_uuid)
		VALUES (1, 2, 'Two', '/ws/plan/phase-2.md', '[]', 'done', 'run-2')`)
	mustExec(t, db, `INSERT INTO phase_actuals (phase_id, session_uuid, outcome, computed_at)
		VALUES (?, 'run-2', 'completed', '2026-09-23T10:00:00Z')`, p2)

	st, err := newScorer(db).Backfill()
	if err != nil {
		t.Fatal(err)
	}
	if st.Scanned != 2 || st.Scored != 1 || st.Unscorable != 1 || st.Failed != 0 {
		t.Errorf("backfill stats = %+v, want 2 scanned, 1 scored, 1 unscorable", st)
	}
}

// A panic downstream (a nil DB here) is recovered: the hook is advisory.
func TestAfterActualsNeverPanics(t *testing.T) {
	s := &Scorer{}
	s.AfterActuals(1, "u", "run-end")
}
