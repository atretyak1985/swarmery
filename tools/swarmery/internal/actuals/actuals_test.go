package actuals

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// ── fixture: a real temp git repo + a phase run row pointing at it ──

const (
	runUUID    = "run-uuid-1"
	runBranch  = "swarm/phase-1"
	runStarted = "2026-09-23T10:00:00Z"
	runEnded   = "2026-09-23T10:30:00Z"
)

type fixtureRepo struct {
	dir   string
	start string // the SHA the run branch was cut from
	git   worktree.ExecGit
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func (r fixtureRepo) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := r.git.Run(r.dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (r fixtureRepo) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newFixtureRepo builds a repo whose run branch changes, relative to its start
// point: README.md (+1 −1), internal/store/a.go (+30), assets/logo.png (binary),
// and the daemon's lent doc under .swarmery/ (must be excluded). After the branch
// is cut, main gains a commit of its own that must NOT count as the run's work.
func newFixtureRepo(t *testing.T) fixtureRepo {
	t.Helper()
	needGit(t)
	r := fixtureRepo{dir: t.TempDir()}
	r.run(t, "init", "-q", "-b", "main")
	r.run(t, "config", "user.email", "test@example.com")
	r.run(t, "config", "user.name", "Test")
	r.run(t, "config", "commit.gpgsign", "false")
	r.write(t, "README.md", "hello\n")
	r.run(t, "add", "-A")
	r.run(t, "commit", "-q", "-m", "init")
	r.start = strings.TrimSpace(r.run(t, "rev-parse", "HEAD"))

	r.run(t, "checkout", "-q", "-b", runBranch)
	r.write(t, "README.md", "hello, world\n")
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("// line\n")
	}
	r.write(t, "internal/store/a.go", b.String())
	r.write(t, "assets/logo.png", "\x89PNG\x00\x01\x02\x00binary")
	r.write(t, ".swarmery/plan/phase-1.md", "# lent doc\n")
	r.run(t, "add", "-A")
	r.run(t, "commit", "-q", "-m", "feat: the run's work")

	r.run(t, "checkout", "-q", "main")
	r.write(t, "unrelated/base.go", "package base\n")
	r.run(t, "add", "-A")
	r.run(t, "commit", "-q", "-m", "chore: base moved on")
	return r
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "actuals.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func exec1(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedPhase inserts a project at projectPath, a workspace epic and one FINISHED
// phase run (done, 0 → 3 of 3 criteria) on runBranch from start.
func seedPhase(t *testing.T, db *sql.DB, projectPath, start string) int64 {
	t.Helper()
	exec1(t, db, `INSERT INTO projects(id, path, slug, first_seen) VALUES(1, ?, 'p', '2026-01-01T00:00:00Z')`, projectPath)
	taskID := exec1(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1, 'Epic', 'goal', 'running',
		'2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z', 'workspace', '2026-09-23-epic')`)
	return exec1(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
		 run_state, run_session_uuid, run_started_at, run_ended_at, run_branch, run_start_point,
		 run_checkboxes_before, run_checkboxes_after)
		VALUES (?, 1, 'Phase 1', '/ws/plan/phase-1.md', '[]', 3, 3,
		        'done', ?, ?, ?, ?, ?, 0, 3)`,
		taskID, runUUID, runStarted, runEnded, runBranch, start)
}

// seedSession ingests the run's transcript facts: two priced turns that moved
// from opus 5.5 to an older sonnet (a fallback), three test runs — a failure
// inside the forecast's area, one outside it, and a pass.
func seedSession(t *testing.T, db *sql.DB) {
	t.Helper()
	sid := exec1(t, db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (1, ?, ?)`, runUUID, runStarted)
	exec1(t, db, `INSERT INTO turns (session_id, seq, role, started_at, cost_usd, model) VALUES (?, 1, 'assistant', ?, 0.5, 'claude-opus-5-5')`, sid, runStarted)
	exec1(t, db, `INSERT INTO turns (session_id, seq, role, started_at, cost_usd, model) VALUES (?, 2, 'assistant', ?, 0.25, 'claude-sonnet-4-5')`, sid, runStarted)
	exec1(t, db, `INSERT INTO turns (session_id, seq, role, started_at) VALUES (?, 3, 'user', ?)`, sid, runStarted)

	testRun := func(n int, cmd, output, status string, failed int) {
		parent := exec1(t, db, `INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
			VALUES (?, ?, 'tool_call', 'Bash', ?, ?, ?)`, sid, runStarted, status,
			mustJSON(t, map[string]any{"input": map[string]any{"command": cmd}, "result": map[string]any{"stdout": output}}),
			"call-"+string(rune('a'+n)))
		exec1(t, db, `INSERT INTO events (session_id, ts, type, tool_name, parent_event_id, status, payload, dedup_key)
			VALUES (?, ?, 'test_run', 'Bash', ?, ?, ?, ?)`, sid, runStarted, parent, status,
			mustJSON(t, map[string]any{"command": cmd, "failed": failed}), "test-"+string(rune('a'+n)))
	}
	testRun(0, "go test ./internal/store", "--- FAIL: TestA (0.00s)\nFAIL\tgithub.com/x/app/internal/store\t0.1s\nFAIL\n", "error", 1)
	testRun(1, "cd app && go test ./...", "ok  \tgithub.com/x/app/internal/store\nFAIL\tgithub.com/x/app/internal/cost\t0.2s\n", "error", 1)
	testRun(2, "go test ./internal/store", "ok  \tgithub.com/x/app/internal/store\t0.1s\n", "ok", 0)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func seedForecasts(t *testing.T, db *sql.DB, phaseID int64) {
	t.Helper()
	// A usable prior naming internal/store, and a POST-HOC posterior naming
	// internal/cost that must be ignored — post hoc is not a prediction.
	exec1(t, db, `INSERT INTO phase_forecasts (phase_id, kind, areas_json, risks_json) VALUES (?, 'prior', '["internal/store"]', '["migration order"]')`, phaseID)
	exec1(t, db, `INSERT INTO phase_forecasts (phase_id, kind, areas_json, post_hoc, post_hoc_reason) VALUES (?, 'posterior', '["internal/cost"]', 1, 'after-first-edit')`, phaseID)
}

func seedRunEvents(t *testing.T, db *sql.DB, phaseID int64) {
	t.Helper()
	for _, k := range []string{"continuation", "continuation", "done"} {
		exec1(t, db, `INSERT INTO run_events (engine, subject_id, session_uuid, kind, created_at) VALUES ('phaserun', ?, ?, ?, ?)`,
			phaseID, runUUID, k, runEnded)
	}
	// Another run's events on the same phase are not this run's.
	exec1(t, db, `INSERT INTO run_events (engine, subject_id, session_uuid, kind, created_at) VALUES ('phaserun', ?, 'other-run', 'continuation', ?)`,
		phaseID, runEnded)
}

func seedVerifications(t *testing.T, db *sql.DB, phaseID int64) {
	t.Helper()
	key := "phase:" + itoa(phaseID)
	// A PREVIOUS run's failing grade, then this run's pass.
	exec1(t, db, `INSERT INTO verification_runs (target_key, status, started_at) VALUES (?, 'fail', '2026-09-22T09:00:00.000Z')`, key)
	exec1(t, db, `INSERT INTO verification_runs (target_key, status, started_at) VALUES (?, 'pass', '2026-09-23T10:30:01.250Z')`, key)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func newRecorder(db *sql.DB) *Recorder {
	return &Recorder{
		DB:  db,
		Git: worktree.ExecGit{},
		Now: func() time.Time { return time.Date(2026, 9, 23, 11, 0, 0, 0, time.UTC) },
	}
}

func readRow(t *testing.T, db *sql.DB, uuid string) map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM phase_actuals WHERE session_uuid = ?`, uuid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		t.Fatalf("no phase_actuals row for %s", uuid)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for i, c := range cols {
		if b, ok := vals[i].([]byte); ok {
			vals[i] = string(b)
		}
		out[c] = vals[i]
	}
	return out
}

// ── the acceptance case: a fixture run produces a correct row ──

func TestRecordFixtureRun(t *testing.T) {
	repo := newFixtureRepo(t)
	db := openDB(t)
	phaseID := seedPhase(t, db, repo.dir, repo.start)
	seedSession(t, db)
	seedForecasts(t, db, phaseID)
	seedRunEvents(t, db, phaseID)
	seedVerifications(t, db, phaseID)

	a, err := newRecorder(db).Record(phaseID, runUUID, repo.dir, SourceRunEnd, true)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if a.DiffNote != "" {
		t.Fatalf("diff not measured: %s", a.DiffNote)
	}

	wantFiles := []gitstat.FileStat{
		{Path: "README.md", Added: 1, Removed: 1},
		{Path: "assets/logo.png", Binary: true},
		{Path: "internal/store/a.go", Added: 30},
	}
	if !reflect.DeepEqual(a.Files, wantFiles) {
		t.Errorf("files = %+v\nwant   %+v (the lent .swarmery doc and main's own commit must not count)", a.Files, wantFiles)
	}

	row := readRow(t, db, runUUID)
	want := map[string]any{
		"phase_id":                 phaseID,
		"run_state":                "done",
		"branch":                   runBranch,
		"start_point":              repo.start,
		"areas_json":               `[".","assets","internal/store"]`,
		"area_depth":               int64(2),
		"lines_added":              int64(31),
		"lines_removed":            int64(1),
		"size_band":                "S",
		"duration_s":               int64(1800),
		"cost_usd":                 0.75,
		"outcome":                  "completed",
		"verify_verdict":           "pass",
		"test_failures":            int64(2),
		"test_failures_unexpected": int64(1),
		"continuations":            int64(2),
		"model_fallback":           int64(1),
		"source":                   SourceRunEnd,
		"computed_at":              "2026-09-23T11:00:00Z",
	}
	for col, w := range want {
		if got := row[col]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s = %#v, want %#v", col, got, w)
		}
	}
	var files []gitstat.FileStat
	if err := json.Unmarshal([]byte(row["files_json"].(string)), &files); err != nil || !reflect.DeepEqual(files, wantFiles) {
		t.Errorf("files_json = %v (%v), want %+v", row["files_json"], err, wantFiles)
	}

	// A recompute replaces the run's row; it never appends a second one.
	if _, err := newRecorder(db).Record(phaseID, runUUID, repo.dir, SourceRunEndSettled, true); err != nil {
		t.Fatalf("second Record: %v", err)
	}
	var n int
	var source string
	if err := db.QueryRow(`SELECT COUNT(*), MAX(source) FROM phase_actuals`).Scan(&n, &source); err != nil {
		t.Fatal(err)
	}
	if n != 1 || source != SourceRunEndSettled {
		t.Errorf("after recompute: %d rows, source %q; want 1 row, %q", n, source, SourceRunEndSettled)
	}
}

// The area depth comes from the run repo's project.json when it declares one.
func TestRecordHonoursProjectAreaDepth(t *testing.T) {
	repo := newFixtureRepo(t)
	repo.write(t, ".claude/project.json", `{"name":"p","learning":{"areaDepth":1}}`)
	db := openDB(t)
	phaseID := seedPhase(t, db, repo.dir, repo.start)

	a, err := newRecorder(db).Compute(phaseID, runUUID, repo.dir, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if a.AreaDepth != 1 || !reflect.DeepEqual(a.Areas, []string{".", "assets", "internal"}) {
		t.Errorf("depth %d areas %q, want depth 1 areas [. assets internal]", a.AreaDepth, a.Areas)
	}
}

// Missing inputs are NULL, never fabricated: no ingested session, no forecast,
// no verification of this run, a branch that is gone, a restart-healed run.
func TestRecordMissingInputsAreNull(t *testing.T) {
	repo := newFixtureRepo(t)
	db := openDB(t)
	phaseID := seedPhase(t, db, repo.dir, repo.start)
	exec1(t, db, `UPDATE epic_phases SET run_branch = 'swarm/phase-gone', run_error = 'daemon restart', run_state = 'failed' WHERE id = ?`, phaseID)
	// Only a PREVIOUS run was graded.
	exec1(t, db, `INSERT INTO verification_runs (target_key, status, started_at) VALUES (?, 'fail', '2026-09-22T09:00:00.000Z')`, "phase:"+itoa(phaseID))

	a, err := newRecorder(db).Record(phaseID, runUUID, repo.dir, SourceRunEnd, true)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !strings.Contains(a.DiffNote, "no longer exists") {
		t.Errorf("DiffNote = %q, want the gone branch named", a.DiffNote)
	}
	row := readRow(t, db, runUUID)
	for _, col := range []string{"files_json", "areas_json", "lines_added", "lines_removed", "size_band",
		"duration_s", "cost_usd", "verify_verdict", "test_failures", "test_failures_unexpected", "model_fallback"} {
		if row[col] != nil {
			t.Errorf("%s = %#v, want NULL", col, row[col])
		}
	}
	// A run observed ending under this daemon records its continuations even when
	// there were none: 0 is a measurement here.
	if row["continuations"] != int64(0) {
		t.Errorf("continuations = %#v, want 0 for an observed run with no resumes", row["continuations"])
	}
	if row["outcome"] != "failed" {
		t.Errorf("outcome = %#v, want failed", row["outcome"])
	}
}

// A session that ran tests but has no forecast has failures, and no judgement
// about which were unexpected.
func TestUnexpectedFailuresNeedAForecast(t *testing.T) {
	repo := newFixtureRepo(t)
	db := openDB(t)
	phaseID := seedPhase(t, db, repo.dir, repo.start)
	seedSession(t, db)

	a, err := newRecorder(db).Compute(phaseID, runUUID, repo.dir, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if a.TestFailures == nil || *a.TestFailures != 2 {
		t.Errorf("test failures = %v, want 2", a.TestFailures)
	}
	if a.TestFailuresUnexpected != nil {
		t.Errorf("unexpected = %d, want NULL without a forecast", *a.TestFailuresUnexpected)
	}
}

func TestComputeRefusesMovedAndUnfinishedRuns(t *testing.T) {
	db := openDB(t)
	phaseID := seedPhase(t, db, "/nowhere", "abc")
	r := &Recorder{DB: db}

	if _, err := r.Compute(phaseID, "some-older-run", "", true); !errors.Is(err, ErrRunMoved) {
		t.Errorf("moved run: err = %v, want ErrRunMoved", err)
	}
	exec1(t, db, `UPDATE epic_phases SET run_state = 'running' WHERE id = ?`, phaseID)
	if _, err := r.Compute(phaseID, runUUID, "", true); !errors.Is(err, ErrNotFinished) {
		t.Errorf("running run: err = %v, want ErrNotFinished", err)
	}
	exec1(t, db, `UPDATE epic_phases SET run_session_uuid = NULL, run_state = 'idle' WHERE id = ?`, phaseID)
	if _, err := r.Compute(phaseID, "", "", true); !errors.Is(err, ErrNoRun) {
		t.Errorf("never-run phase: err = %v, want ErrNoRun", err)
	}
	if _, err := r.Compute(424242, "", "", true); !errors.Is(err, ErrNoRun) {
		t.Errorf("unknown phase: err = %v, want ErrNoRun", err)
	}
}

// Without a git boundary or a repository the rest of the row is still measured.
func TestComputeWithoutGit(t *testing.T) {
	db := openDB(t)
	phaseID := seedPhase(t, db, "/nowhere", "abc")
	seedSession(t, db)
	for _, tc := range []struct {
		name string
		r    *Recorder
		root string
		note string
	}{
		{"no git", &Recorder{DB: db}, "/nowhere", "no git boundary"},
		{"no repo", &Recorder{DB: db, Git: worktree.ExecGit{}}, "", "repository unknown"},
	} {
		a, err := tc.r.Compute(phaseID, runUUID, tc.root, true)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if a.Files != nil || !strings.Contains(a.DiffNote, tc.note) {
			t.Errorf("%s: files %v note %q", tc.name, a.Files, a.DiffNote)
		}
		if a.CostUSD == nil || *a.CostUSD != 0.75 {
			t.Errorf("%s: cost %v, want 0.75", tc.name, a.CostUSD)
		}
	}
	exec1(t, db, `UPDATE epic_phases SET run_start_point = NULL WHERE id = ?`, phaseID)
	a, _ := (&Recorder{DB: db, Git: worktree.ExecGit{}}).Compute(phaseID, runUUID, "/nowhere", true)
	if !strings.Contains(a.DiffNote, "no start point") {
		t.Errorf("no start point: note %q", a.DiffNote)
	}
	exec1(t, db, `UPDATE epic_phases SET run_branch = NULL WHERE id = ?`, phaseID)
	a, _ = (&Recorder{DB: db, Git: worktree.ExecGit{}}).Compute(phaseID, runUUID, "/nowhere", true)
	if !strings.Contains(a.DiffNote, "no run branch") {
		t.Errorf("no branch: note %q", a.DiffNote)
	}
}

// AfterRun records now, schedules the settled pass, and never lets a failure —
// or a panic — escape into the run's exit path.
func TestAfterRunSettlesAndIsAdvisory(t *testing.T) {
	repo := newFixtureRepo(t)
	db := openDB(t)
	phaseID := seedPhase(t, db, repo.dir, repo.start)

	var scheduled func()
	var delay time.Duration
	r := newRecorder(db)
	r.SettleDelay = time.Minute
	r.After = func(d time.Duration, f func()) { delay, scheduled = d, f }

	r.AfterRun(phaseID, runUUID, repo.dir)
	if got := readRow(t, db, runUUID)["source"]; got != SourceRunEnd {
		t.Fatalf("first pass source = %v, want %s", got, SourceRunEnd)
	}
	if scheduled == nil || delay != time.Minute {
		t.Fatalf("settled pass not scheduled (delay %s)", delay)
	}
	// The transcript arrives between the passes; the settled pass picks it up.
	seedSession(t, db)
	scheduled()
	row := readRow(t, db, runUUID)
	if row["source"] != SourceRunEndSettled || row["cost_usd"] != 0.75 {
		t.Errorf("settled pass: source %v cost %v, want %s / 0.75", row["source"], row["cost_usd"], SourceRunEndSettled)
	}

	// A newer run took the row before the settled pass: it must not be overwritten.
	exec1(t, db, `UPDATE epic_phases SET run_session_uuid = 'newer', run_state = 'running' WHERE id = ?`, phaseID)
	scheduled()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM phase_actuals WHERE session_uuid = 'newer'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("settled pass wrote a row for the newer run (n=%d, err=%v)", n, err)
	}

	// No DB at all panics inside Record; AfterRun must swallow it.
	(&Recorder{}).AfterRun(phaseID, runUUID, repo.dir)
	// A zero SettleDelay schedules nothing.
	called := false
	(&Recorder{DB: db, After: func(time.Duration, func()) { called = true }}).AfterRun(phaseID, runUUID, "")
	if called {
		t.Error("SettleDelay 0 still scheduled a settled pass")
	}
}

func TestNewRecorderDefaults(t *testing.T) {
	r := NewRecorder(nil, nil)
	if r.SettleDelay != DefaultSettleDelay {
		t.Errorf("SettleDelay = %s, want %s", r.SettleDelay, DefaultSettleDelay)
	}
	if r.now().IsZero() {
		t.Error("default clock returned the zero time")
	}
}
