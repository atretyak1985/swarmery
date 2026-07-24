package verify

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// ── test doubles ──

// stubRunner returns a canned verifier transcript and counts calls (to assert a
// cache hit spawns ZERO runs). outFn, when set, computes the Run per spec.
type stubRunner struct {
	mu    sync.Mutex
	calls int
	out   string      // canned stdout (parsed into a verdict)
	run   *Run        // full canned Run (overrides out when set)
	err   error       // canned start error
	outFn func(RunSpec) *Run
}

func (s *stubRunner) Run(_ context.Context, spec RunSpec) (*Run, error) {
	s.mu.Lock()
	s.calls++
	fn, canned, out, err := s.outFn, s.run, s.out, s.err
	s.mu.Unlock()
	if err != nil {
		return &Run{ExitCode: -1}, err
	}
	if fn != nil {
		return fn(spec), nil
	}
	if canned != nil {
		return canned, nil
	}
	return &Run{Output: out, ExitCode: 0}, nil
}

func (s *stubRunner) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

// stubTrees returns a scripted tree hash (and can force an error to simulate the
// worktree-vanished race).
type stubTrees struct {
	hash string
	err  error
}

func (t stubTrees) TreeHash(string) (string, error) { return t.hash, t.err }

// ── harness ──

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "verify.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func newTestService(t *testing.T, db *sql.DB, r Runner, trees Trees) *Service {
	t.Helper()
	s := NewService(db, Config{
		Enabled: true, Concurrency: 1, RunTimeout: time.Minute,
		RetryBudget: DefaultRetryBudget, StaleAfter: 2 * time.Hour,
	}, r, trees)
	s.Go = func(fn func()) { fn() } // inline spawn for deterministic auto-trigger tests
	var n int
	s.UUID = func() string { n++; return "vuuid-" + itoaTest(n) }
	return s
}

func itoaTest(n int) string { return string(rune('0' + n)) }

// taskOpts mutate the inserted task row.
type taskOpts struct {
	column     string
	source     string
	externalID string
	worktree   string
	fileScope  string
	model      string
	retryCount int
	paused     int
}

func insertTask(t *testing.T, db *sql.DB, o taskOpts) int64 {
	t.Helper()
	if o.column == "" {
		o.column = "in_review"
	}
	if o.source == "" {
		o.source = "queue"
	}
	if o.externalID == "" {
		o.externalID = "T-root1"
	}
	if o.worktree == "" {
		o.worktree = "/wt/p/" + o.externalID
	}
	if o.fileScope == "" {
		o.fileScope = "[]"
	}
	res, err := db.Exec(`
		INSERT INTO tasks(project_id, title, prompt, priority, status, created_at,
		                  source, external_id, board_column, model, file_scope,
		                  dependencies, worktree_path, branch, retry_count, paused)
		VALUES(1, ?, ?, 5, 'needs_review', '2026-07-24T00:00:00.000Z',
		       ?, ?, ?, ?, ?, '[]', ?, ?, ?, ?)`,
		"title "+o.externalID, "do the thing for "+o.externalID,
		o.source, o.externalID, o.column, nullStr(o.model), o.fileScope,
		o.worktree, "swarm/"+o.externalID, o.retryCount, o.paused)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func verdictOf(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT verify_verdict FROM tasks WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatalf("read verdict %d: %v", id, err)
	}
	return v.String
}

func intField(t *testing.T, db *sql.DB, id int64, col string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT `+col+` FROM tasks WHERE id=?`, id).Scan(&n); err != nil {
		t.Fatalf("read %s %d: %v", col, id, err)
	}
	return n
}

func countFixTasks(t *testing.T, db *sql.DB, rootExtID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM tasks WHERE source='verify-fix' AND external_id=?`, rootExtID).Scan(&n); err != nil {
		t.Fatalf("count fix tasks: %v", err)
	}
	return n
}

func cacheCount(t *testing.T, db *sql.DB, taskID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verification_cache WHERE task_id=?`, taskID).Scan(&n); err != nil {
		t.Fatalf("cache count: %v", err)
	}
	return n
}

// ── tests ──

func TestVerifyPass_StampsAndCaches(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "- all criteria met\nVERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-abc"})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "pass" {
		t.Fatalf("verdict = %q, want pass", got)
	}
	if cacheCount(t, db, id) != 1 {
		t.Fatal("pass verdict should write a cache row")
	}
	if r.count() != 1 {
		t.Fatalf("runner calls = %d, want 1", r.count())
	}
}

func TestVerifyCacheHit_SkipsSpawn(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-xyz"})
	id := insertTask(t, db, taskOpts{})

	// First run populates the cache (1 spawn).
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// Second run on the SAME tree hash → cache hit, ZERO additional spawns.
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Fatalf("runner calls = %d, want 1 (second run must be a cache hit)", r.count())
	}
	// The cache-hit run is recorded with detail='cache'.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM verification_runs WHERE task_id=? AND detail='cache'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("cache-hit run rows = %d, want 1", n)
	}
}

func TestVerifyInconclusive_NoFixNoCache(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "could not install deps\nVERDICT: INCONCLUSIVE"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-inc"})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Fatalf("verdict = %q, want inconclusive", got)
	}
	if cacheCount(t, db, id) != 0 {
		t.Fatal("inconclusive must NOT be cached")
	}
	if countFixTasks(t, db, "T-root1") != 0 {
		t.Fatal("inconclusive must spawn NO fix task")
	}
	// A re-verify of the same tree must RE-RUN (not a cache hit), because
	// inconclusive was never cached.
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 2 {
		t.Fatalf("runner calls = %d, want 2 (inconclusive is never cached → re-run)", r.count())
	}
}

func TestVerifyTimeout_Inconclusive(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: &Run{TimedOut: true, ExitCode: -1}}
	s := newTestService(t, db, r, stubTrees{hash: "tree-to"})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Fatalf("timeout verdict = %q, want inconclusive", got)
	}
	if countFixTasks(t, db, "T-root1") != 0 {
		t.Fatal("timeout must spawn no fix task")
	}
}

func TestVerifyWorktreeVanished_Inconclusive(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	// TreeHash errors → simulate the RemoveWorktreeFor race (worktree gone).
	s := newTestService(t, db, r, stubTrees{err: errTreeGone})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Fatalf("verdict = %q, want inconclusive (worktree gone → degrade, not fail)", got)
	}
	if r.count() != 0 {
		t.Fatal("must not spawn a verifier when the tree can't be read")
	}
}

func TestVerifyFail_CreatesOneFixTask(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "- endpoint returns 500\nVERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-f1"})
	id := insertTask(t, db, taskOpts{externalID: "T-root1"})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "fail" {
		t.Fatalf("verdict = %q, want fail", got)
	}
	if n := countFixTasks(t, db, "T-root1"); n != 1 {
		t.Fatalf("fix tasks = %d, want exactly 1", n)
	}
	// Root retry_count charged to 1.
	if rc := intField(t, db, id, "retry_count"); rc != 1 {
		t.Fatalf("root retry_count = %d, want 1", rc)
	}
	// The fix task carries the root external_id + failure reasons + same file scope.
	var prompt, scope string
	if err := db.QueryRow(
		`SELECT prompt, file_scope FROM tasks WHERE source='verify-fix' AND external_id='T-root1'`).
		Scan(&prompt, &scope); err != nil {
		t.Fatal(err)
	}
	if !contains(prompt, "## Verification failed") || !contains(prompt, "returns 500") {
		t.Fatalf("fix prompt missing failure section: %q", prompt)
	}
}

func TestVerifyFail_DedupsOpenFix(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-d"})
	id := insertTask(t, db, taskOpts{externalID: "T-root1"})

	// First fail creates a fix task.
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// Force a different tree so the second run is not a cache hit (we want to
	// exercise the dedup gate, not the cache).
	s.Trees = stubTrees{hash: "tree-d2"}
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if n := countFixTasks(t, db, "T-root1"); n != 1 {
		t.Fatalf("fix tasks = %d, want 1 (dedup: an open fix already exists)", n)
	}
}

// The runaway-spend guard (pre-mortem #4): a FIX task that itself fails charges
// the ROOT's budget, not its own. Budget = 3 (root retry_count < 3 → create a
// fix), so exactly 3 fix tasks are created across failures; the 4th failure
// (root retry_count already 3) pauses the chain — a bounded, non-runaway result.
func TestVerifyFail_RootChargedAndBudgetExhausts(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "h0"})

	root := insertTask(t, db, taskOpts{externalID: "T-root1", worktree: "/wt/p/root"})

	// Model the real lifecycle: the task under verification is in_review; after it
	// fails and spawns a successor fix, that prior attempt is superseded (the
	// dispatcher will have moved it on) so we archive it — a terminal state the
	// dedup gate ignores, leaving exactly one open fix at a time. Each failure
	// re-hashes the tree so the cache never short-circuits the run. verifyOne only
	// (re)opens a NON-terminal task, so archiving a concluded fix is durable.
	verifyOne := func(id int64, treeHash, wt string) {
		s.Trees = stubTrees{hash: treeHash}
		if col := boardColumn(t, db, id); col != "done" && col != "archived" {
			toReview(t, db, id, wt)
		}
		mustVerify(t, s, id)
	}
	supersede := func(id int64) { // the prior fix attempt concluded
		if _, err := db.Exec(`UPDATE tasks SET board_column='archived' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}

	// Failure 1 on the ROOT → fix#1, root.retry_count 0→1.
	verifyOne(root, "h0", "/wt/p/root")
	assertRetry(t, db, root, 1)
	fix1 := fixTaskID(t, db, "T-root1")
	// The fix task's OWN retry_count stays 0 — the budget is root-inherited.
	if intField(t, db, fix1, "retry_count") != 0 {
		t.Fatal("fix task's OWN retry_count must stay 0 (budget is root-inherited)")
	}

	// Failure 2 charged to the ROOT (fix#1 → external_id=root) → fix#2, rc 1→2.
	verifyOne(fix1, "h1", "/wt/p/fix1")
	assertRetry(t, db, root, 2)
	fix2 := newestFix(t, db, "T-root1", fix1)
	supersede(fix1)

	// Failure 3 → fix#3, rc 2→3.
	verifyOne(fix2, "h2", "/wt/p/fix2")
	assertRetry(t, db, root, 3)
	fix3 := newestFix(t, db, "T-root1", fix2)
	supersede(fix2)

	// Failure 4: root retry_count is already 3 (== budget) → NO 4th fix; pause the
	// chain (root + the failing fix) with the budget marker.
	verifyOne(fix3, "h3", "/wt/p/fix3")
	assertRetry(t, db, root, 3) // not charged further

	if intField(t, db, root, "paused") != 1 {
		t.Fatal("root must be paused at budget exhaustion")
	}
	if intField(t, db, fix3, "paused") != 1 {
		t.Fatal("the failing fix task must be paused at budget exhaustion")
	}
	var derr sql.NullString
	_ = db.QueryRow(`SELECT dispatch_error FROM tasks WHERE id=?`, root).Scan(&derr)
	if derr.String != "verify retry budget exhausted" {
		t.Fatalf("root dispatch_error = %q, want budget-exhausted marker", derr.String)
	}
	// Total fix tasks created = exactly 3 (bounded by the budget); the 4th failure
	// paused instead of spawning a runaway 4th fix.
	if n := countFixTasks(t, db, "T-root1"); n != 3 {
		t.Fatalf("fix tasks = %d, want 3 (budget bounds fix creation; the 4th failure pauses)", n)
	}
}

func TestReap_StaleRunningToInconclusive(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, stubTrees{hash: "h"})
	id := insertTask(t, db, taskOpts{})

	// Insert a running row that started 3h ago (older than the 2h stale window).
	old := time.Now().Add(-3 * time.Hour).UTC().Format(tsFormat)
	if _, err := db.Exec(
		`INSERT INTO verification_runs(task_id, status, started_at) VALUES(?, 'running', ?)`, id, old); err != nil {
		t.Fatal(err)
	}
	n, err := s.Reap()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped = %d, want 1", n)
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Fatalf("reaped task verdict = %q, want inconclusive", got)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM verification_runs WHERE task_id=?`, id).Scan(&status)
	if status != "error" {
		t.Fatalf("reaped run status = %q, want error", status)
	}
}

func TestReap_LeavesFreshRunningAlone(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, stubTrees{hash: "h"})
	id := insertTask(t, db, taskOpts{})
	// A run that started 1 minute ago is well within the window.
	fresh := time.Now().Add(-time.Minute).UTC().Format(tsFormat)
	if _, err := db.Exec(
		`INSERT INTO verification_runs(task_id, status, started_at) VALUES(?, 'running', ?)`, id, fresh); err != nil {
		t.Fatal(err)
	}
	n, err := s.Reap()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reaped = %d, want 0 (fresh run must survive)", n)
	}
}

func TestHealStale_InterruptedRunToInconclusive(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, stubTrees{hash: "h"})
	id := insertTask(t, db, taskOpts{})
	// A running row from a "crashed" daemon (any age).
	if _, err := db.Exec(
		`INSERT INTO verification_runs(task_id, status, started_at) VALUES(?, 'running', ?)`, id, s.ts()); err != nil {
		t.Fatal(err)
	}
	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM verification_runs WHERE task_id=?`, id).Scan(&status)
	if status != "error" {
		t.Fatalf("healed run status = %q, want error", status)
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Fatalf("healed task verdict = %q, want inconclusive", got)
	}
}

func TestVerifySingleFlight(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{out: "VERDICT: PASS"}, stubTrees{hash: "h"})
	id := insertTask(t, db, taskOpts{})
	// Pre-seed a running row → the next VerifyTask must bounce with ErrAlreadyRunning.
	if _, err := db.Exec(
		`INSERT INTO verification_runs(task_id, status, started_at) VALUES(?, 'running', ?)`, id, s.ts()); err != nil {
		t.Fatal(err)
	}
	err := s.VerifyTask(context.Background(), id)
	if err != ErrAlreadyRunning {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestVerifyNoWorktree(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, stubTrees{hash: "h"})
	// Insert with an explicit empty worktree.
	res, err := db.Exec(`
		INSERT INTO tasks(project_id, title, prompt, priority, status, created_at,
		                  source, external_id, board_column, file_scope, dependencies)
		VALUES(1,'t','p',5,'needs_review','2026-07-24T00:00:00.000Z','queue','T-noWt','in_review','[]','[]')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := s.VerifyTask(context.Background(), id); err != ErrNoWorktree {
		t.Fatalf("err = %v, want ErrNoWorktree", err)
	}
}

func TestPokeDisabledKillSwitch(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "h"})
	s.Cfg.Enabled = false // SWARMERY_AUTOVERIFY=0
	id := insertTask(t, db, taskOpts{})
	s.Poke(id) // inline Go seam; must be a no-op when disabled
	if r.count() != 0 {
		t.Fatal("Poke must not run the verifier when auto-verify is disabled")
	}
	if verdictOf(t, db, id) != "" {
		t.Fatal("disabled Poke must not stamp a verdict")
	}
}

func TestPokeEnabledRuns(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "h"})
	id := insertTask(t, db, taskOpts{})
	s.Poke(id) // inline Go seam runs VerifyTask synchronously here
	if verdictOf(t, db, id) != "pass" {
		t.Fatal("enabled Poke should stamp the verdict")
	}
}

// ── small helpers for the budget test ──

var errTreeGone = &treeErr{}

type treeErr struct{}

func (*treeErr) Error() string { return "worktree gone" }

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func mustVerify(t *testing.T, s *Service, id int64) {
	t.Helper()
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatalf("verify %d: %v", id, err)
	}
}

func assertRetry(t *testing.T, db *sql.DB, id int64, want int) {
	t.Helper()
	if got := intField(t, db, id, "retry_count"); got != want {
		t.Fatalf("retry_count(%d) = %d, want %d", id, got, want)
	}
}

func fixTaskID(t *testing.T, db *sql.DB, rootExtID string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM tasks WHERE source='verify-fix' AND external_id=? ORDER BY id DESC LIMIT 1`, rootExtID).
		Scan(&id); err != nil {
		t.Fatalf("fix task id: %v", err)
	}
	return id
}

func newestFix(t *testing.T, db *sql.DB, rootExtID string, notID int64) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM tasks WHERE source='verify-fix' AND external_id=? AND id<>? ORDER BY id DESC LIMIT 1`,
		rootExtID, notID).Scan(&id); err != nil {
		t.Fatalf("newest fix id: %v", err)
	}
	return id
}

// toReview moves a task to in_review with a worktree so it can be verified.
func toReview(t *testing.T, db *sql.DB, id int64, wt string) {
	t.Helper()
	if _, err := db.Exec(
		`UPDATE tasks SET board_column='in_review', worktree_path=? WHERE id=?`, wt, id); err != nil {
		t.Fatalf("toReview %d: %v", id, err)
	}
}

func boardColumn(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var c string
	if err := db.QueryRow(`SELECT board_column FROM tasks WHERE id=?`, id).Scan(&c); err != nil {
		t.Fatalf("read board_column %d: %v", id, err)
	}
	return c
}
