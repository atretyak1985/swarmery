package verify

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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
	specs []RunSpec // every spec handed to Run, for prompt assertions
	out   string    // canned stdout (parsed into a verdict)
	run   *Run      // full canned Run (overrides out when set)
	err   error     // canned start error
	outFn func(RunSpec) *Run
}

func (s *stubRunner) Run(_ context.Context, spec RunSpec) (*Run, error) {
	s.mu.Lock()
	s.calls++
	s.specs = append(s.specs, spec)
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

// lastPrompt is the prompt of the most recent spec (the rendered verifier
// prompt, which carries the "diff vs <base>" instruction).
func (s *stubRunner) lastPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.specs) == 0 {
		return ""
	}
	return s.specs[len(s.specs)-1].Prompt
}

// diffProbe records the bases DiffFileCount was asked about. stubTrees is used
// BY VALUE everywhere, so the recorder has to be behind a pointer to survive
// the copy — that is the whole reason it is a separate type.
type diffProbe struct {
	mu    sync.Mutex
	bases []string
}

func (p *diffProbe) record(base string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bases = append(p.bases, base)
}

func (p *diffProbe) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.bases...)
}

// stubTrees returns a scripted tree hash (and can force an error to simulate the
// worktree-vanished race).
type stubTrees struct {
	diffFiles int        // files reported by DiffFileCount
	diffErr   error      // when set, the diff size is UNREADABLE
	probe     *diffProbe // when set, records every base the gate asked about
	hash      string
	err       error
}

func (t stubTrees) TreeHash(string) (string, error) { return t.hash, t.err }

// DiffFileCount is the scope-gate signal. diffFiles defaults to 0, which keeps every
// pre-existing test under any bound — the gate must be invisible to them.
func (t stubTrees) DiffFileCount(worktreePath, base string) (int, error) {
	if t.probe != nil {
		t.probe.record(base)
	}
	return t.diffFiles, t.diffErr
}

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
	origin     string
	externalID string
	worktree   string
	fileScope  string
	model      string
	retryCount int // dispatch-owned budget (HealDeadProcess); verify must never read it
	// verifyRetryCount is the verify-owned fix-chain budget (0051). Split from
	// retryCount so a flaky-run heal cannot silently spend the fix budget.
	verifyRetryCount int
	paused           int
	playbook         string // selected recipe name (drives the verify knob via PlaybookVerify seam)
	// startPoint is the SHA admit() pinned the worktree to. Defaults to a
	// non-empty value because every post-0051 dispatched row HAS one; the
	// pre-0051 world is the exception and is opted into with legacyNoStartPoint.
	startPoint string
	// legacyNoStartPoint models a row dispatched before 0051: start_point is
	// NULL, so no honest diff base exists.
	legacyNoStartPoint bool
}

// defaultStartPoint is the harness's stand-in for Acquired.StartPoint. It is
// deliberately NOT a branch name: a test that passes only because the base
// happens to equal the branch would be exactly the bug 0051 fixes.
const defaultStartPoint = "base0000"

func insertTask(t *testing.T, db *sql.DB, o taskOpts) int64 {
	t.Helper()
	if o.column == "" {
		o.column = "in_review"
	}
	if o.source == "" {
		o.source = "queue"
	}
	if o.origin == "" {
		o.origin = "manual"
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
	if o.startPoint == "" {
		o.startPoint = defaultStartPoint
	}
	if o.legacyNoStartPoint {
		o.startPoint = ""
	}
	res, err := db.Exec(`
		INSERT INTO tasks(project_id, title, prompt, priority, status, created_at,
		                  source, origin, external_id, board_column, model, file_scope,
		                  dependencies, worktree_path, branch, retry_count, verify_retry_count,
		                  paused, playbook, start_point)
		VALUES(1, ?, ?, 5, 'needs_review', '2026-07-24T00:00:00.000Z',
		       ?, ?, ?, ?, ?, ?, '[]', ?, ?, ?, ?, ?, ?, ?)`,
		"title "+o.externalID, "do the thing for "+o.externalID,
		o.source, o.origin, o.externalID, o.column, nullStr(o.model), o.fileScope,
		o.worktree, "swarm/"+o.externalID, o.retryCount, o.verifyRetryCount,
		o.paused, nullStr(o.playbook), nullStr(o.startPoint))
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

func detailOf(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT verify_detail FROM tasks WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatalf("read detail %d: %v", id, err)
	}
	return v.String
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
		`SELECT COUNT(*) FROM tasks WHERE origin='verify-fix' AND external_id=?`, rootExtID).Scan(&n); err != nil {
		t.Fatalf("count fix tasks: %v", err)
	}
	return n
}

func cacheCount(t *testing.T, db *sql.DB, taskID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM verification_cache WHERE target_key='task:'||?`, taskID).Scan(&n); err != nil {
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

// TestVerifyDaemonDeny_FromConfig: the spawned verifier — and the one resume that
// asks for a missing verdict line — is denied curl/wget/http to the daemon on the
// port from Config, and its prompt names that port. An unset port is the default.
func TestVerifyDaemonDeny_FromConfig(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{outFn: func(spec RunSpec) *Run {
		if spec.Resume {
			return &Run{Output: "VERDICT: PASS"}
		}
		return &Run{Output: "- checks ran, no verdict line"}
	}}
	s := newTestService(t, db, r, stubTrees{hash: "t-daemon-deny"})
	s.Cfg.DaemonPort = 8080
	id := insertTask(t, db, taskOpts{})
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	specs := append([]RunSpec(nil), r.specs...)
	r.mu.Unlock()
	if len(specs) != 2 {
		t.Fatalf("spawned %d times, want 2 (the run + the verdict resume)", len(specs))
	}
	want := strings.Join(DaemonDenyPatterns(8080), ",")
	for i, spec := range specs {
		if got := strings.Join(spec.DisallowedTools, ","); got != want {
			t.Errorf("spawn %d (resume=%v): DisallowedTools = %q, want %q", i, spec.Resume, got, want)
		}
	}
	if !strings.Contains(specs[0].Prompt, DaemonAPINotice(8080)) {
		t.Errorf("verifier prompt lacks the daemon-API rule for :8080:\n%s", specs[0].Prompt)
	}

	// Constructed without a port (every pre-existing caller): the default.
	if got := NewService(db, Config{}, r, stubTrees{}).Cfg.DaemonPort; got != DefaultDaemonPort {
		t.Errorf("NewService(Config{}).Cfg.DaemonPort = %d, want %d", got, DefaultDaemonPort)
	}
	if got := ConfigFromEnv().DaemonPort; got != DefaultDaemonPort {
		t.Errorf("ConfigFromEnv().DaemonPort = %d, want %d", got, DefaultDaemonPort)
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
	// Root verify budget charged to 1 (0051: retry_count stays dispatch-owned).
	if rc := intField(t, db, id, "verify_retry_count"); rc != 1 {
		t.Fatalf("root verify_retry_count = %d, want 1", rc)
	}
	// The fix task carries the root external_id + failure reasons + same file scope.
	var prompt, scope string
	if err := db.QueryRow(
		`SELECT prompt, file_scope FROM tasks WHERE origin='verify-fix' AND external_id='T-root1'`).
		Scan(&prompt, &scope); err != nil {
		t.Fatal(err)
	}
	if !contains(prompt, "## Verification failed") || !contains(prompt, "returns 500") {
		t.Fatalf("fix prompt missing failure section: %q", prompt)
	}
}

// TestVerifyFail_FixTaskIsReachable is the regression guard for the orphaned-fix
// bug: createFixTask used to write source='verify-fix', which matched NEITHER of
// the two predicates that decide whether a row exists for anyone — so every fix
// task was minted into 'todo' and then stranded, invisible and undispatchable.
//
// Both predicates are restated here VERBATIM rather than imported, because
// internal/verify cannot import internal/api or internal/dispatch (they import
// it). That duplication is the point: if either consumer ever narrows its filter
// again, this test is what fails, and it names the two call sites to re-check.
func TestVerifyFail_FixTaskIsReachable(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "- endpoint returns 500\nVERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-reach"})
	id := insertTask(t, db, taskOpts{externalID: "T-root1"})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if n := countFixTasks(t, db, "T-root1"); n != 1 {
		t.Fatalf("fix tasks = %d, want exactly 1", n)
	}

	// 1. api/tasks_board.go listBoardTasks: `WHERE t.source = 'queue'`.
	var onBoard int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM tasks
		 WHERE source = 'queue' AND origin = 'verify-fix' AND external_id = 'T-root1'`).
		Scan(&onBoard); err != nil {
		t.Fatal(err)
	}
	if onBoard != 1 {
		t.Fatalf("fix tasks visible to the board = %d, want 1 "+
			"(listBoardTasks selects source='queue'; a fix task must be a real board row)", onBoard)
	}

	// 2. dispatch/service.go candidates(): the exact eligibility predicate.
	var dispatchable int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM tasks
		 WHERE source='queue' AND board_column='todo'
		   AND paused=0 AND user_paused=0
		   AND origin='verify-fix' AND external_id='T-root1'`).
		Scan(&dispatchable); err != nil {
		t.Fatal(err)
	}
	if dispatchable != 1 {
		t.Fatalf("fix tasks eligible for dispatch = %d, want 1 "+
			"(candidates() selects source='queue' AND board_column='todo')", dispatchable)
	}
}

// TestResolveRootWalksOriginChain pins the fix-chain walk to `origin` after the
// marker moved off `source`: a fix task must still resolve to its root, so the
// retry budget is charged to the root and not reset by each new fix.
func TestResolveRootWalksOriginChain(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, stubTrees{hash: "tree-chain"})
	rootID := insertTask(t, db, taskOpts{externalID: "T-root1"})
	fixID := insertTask(t, db, taskOpts{
		origin: "verify-fix", externalID: "T-root1", worktree: "/wt/fix"})

	fix, err := s.loadTask(fixID)
	if err != nil {
		t.Fatal(err)
	}
	if fix.origin != "verify-fix" {
		t.Fatalf("loadTask origin = %q, want verify-fix", fix.origin)
	}
	root, err := s.resolveRoot(fix)
	if err != nil {
		t.Fatal(err)
	}
	if root.id != rootID {
		t.Fatalf("resolveRoot(fix %d) = task %d, want root %d", fixID, root.id, rootID)
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
// the ROOT's budget, not its own. Budget = 3 (root verify_retry_count < 3 →
// create a fix), so exactly 3 fix tasks are created across failures; the 4th
// failure (root verify_retry_count already 3) pauses the chain — a bounded,
// non-runaway result.
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

	// Failure 1 on the ROOT → fix#1, root.verify_retry_count 0→1.
	verifyOne(root, "h0", "/wt/p/root")
	assertRetry(t, db, root, 1)
	fix1 := fixTaskID(t, db, "T-root1")
	// The fix task's OWN budget stays 0 — the budget is root-inherited.
	if intField(t, db, fix1, "verify_retry_count") != 0 {
		t.Fatal("fix task's OWN verify_retry_count must stay 0 (budget is root-inherited)")
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

	// Failure 4: root verify_retry_count is already 3 (== budget) → NO 4th fix;
	// pause the chain (root + the failing fix) with the budget marker.
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
		`INSERT INTO verification_runs(target_key, task_id, status, started_at) VALUES('task:'||?, ?, 'running', ?)`, id, id, old); err != nil {
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
		`INSERT INTO verification_runs(target_key, task_id, status, started_at) VALUES('task:'||?, ?, 'running', ?)`, id, id, fresh); err != nil {
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
		`INSERT INTO verification_runs(target_key, task_id, status, started_at) VALUES('task:'||?, ?, 'running', ?)`, id, id, s.ts()); err != nil {
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
		`INSERT INTO verification_runs(target_key, task_id, status, started_at) VALUES('task:'||?, ?, 'running', ?)`, id, id, s.ts()); err != nil {
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

// assertRetry checks the VERIFY budget (0051). It also pins that the dispatch
// counter never moves as a side effect: the two budgets are separate, and the
// whole point of the split is that spending one cannot spend the other.
func assertRetry(t *testing.T, db *sql.DB, id int64, want int) {
	t.Helper()
	if got := intField(t, db, id, "verify_retry_count"); got != want {
		t.Fatalf("verify_retry_count(%d) = %d, want %d", id, got, want)
	}
	if got := intField(t, db, id, "retry_count"); got != 0 {
		t.Fatalf("retry_count(%d) = %d, want 0 — verification must never charge the dispatch budget", id, got)
	}
}

func fixTaskID(t *testing.T, db *sql.DB, rootExtID string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM tasks WHERE origin='verify-fix' AND external_id=? ORDER BY id DESC LIMIT 1`, rootExtID).
		Scan(&id); err != nil {
		t.Fatalf("fix task id: %v", err)
	}
	return id
}

func newestFix(t *testing.T, db *sql.DB, rootExtID string, notID int64) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM tasks WHERE origin='verify-fix' AND external_id=? AND id<>? ORDER BY id DESC LIMIT 1`,
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

// ── scope gate (phase 5, close-the-run-loops) ──

func TestScopeGateRefusesOversizedDiffWithoutSpawning(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-big", diffFiles: 200})
	s.Cfg.MaxDiffFiles = 40
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 0 {
		t.Errorf("runner calls = %d, want 0 — the point of the gate is NOT spending the session", r.count())
	}
	// INCONCLUSIVE, never FAIL: a large change is not a failing change, and a fail
	// would spawn fix tasks against work nobody graded.
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Errorf("verdict = %q, want inconclusive", got)
	}
	d := detailOf(t, db, id)
	for _, want := range []string{"200", "40", "SWARMERY_VERIFY_MAX_DIFF_FILES"} {
		if !strings.Contains(d, want) {
			t.Errorf("verify_detail = %q, want it to name %q", d, want)
		}
	}
}

func TestScopeGateAllowsDiffUnderTheBound(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-small", diffFiles: 39})
	s.Cfg.MaxDiffFiles = 40
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Errorf("runner calls = %d, want 1 — a change under the bound verifies normally", r.count())
	}
	if got := verdictOf(t, db, id); got != "pass" {
		t.Errorf("verdict = %q, want pass", got)
	}
}

func TestScopeGateDisabledByZero(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-huge", diffFiles: 100000})
	s.Cfg.MaxDiffFiles = 0
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Errorf("runner calls = %d, want 1 — 0 must disable the bound entirely", r.count())
	}
}

// ── diff base: the persisted start point, never the branch (0051) ──

// The load-bearing test for this whole phase. Verification used to diff
// tk.branch against tk.branch — always zero files, so the scope gate could
// never fire and the prompt told the model to "diff vs <its own branch>". Both
// consumers must now see the SHA admit() pinned the worktree to.
func TestVerifyDiffsAgainstPersistedStartPoint(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	probe := &diffProbe{}
	s := newTestService(t, db, r, stubTrees{hash: "tree-sp", diffFiles: 3, probe: probe})
	s.Cfg.MaxDiffFiles = 40
	id := insertTask(t, db, taskOpts{externalID: "T-root1", startPoint: "cafebabe"})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	bases := probe.seen()
	if len(bases) != 1 || bases[0] != "cafebabe" {
		t.Fatalf("DiffFileCount bases = %v, want exactly [cafebabe] — the persisted start point", bases)
	}
	prompt := r.lastPrompt()
	if !strings.Contains(prompt, "diff vs cafebabe") {
		t.Errorf("prompt does not name the start point as the diff base:\n%s", prompt)
	}
	if strings.Contains(prompt, "diff vs swarm/T-root1") {
		t.Error("prompt still points the model at the task's own branch (a self-diff, always empty)")
	}
}

// With an honest base, the gate can finally fire: a genuinely oversized diff is
// refused BEFORE a session is spent on it.
func TestScopeGateFiresAgainstStartPoint(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	probe := &diffProbe{}
	s := newTestService(t, db, r, stubTrees{hash: "tree-spbig", diffFiles: 200, probe: probe})
	s.Cfg.MaxDiffFiles = 40
	id := insertTask(t, db, taskOpts{startPoint: "cafebabe"})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 0 {
		t.Errorf("runner calls = %d, want 0 — the gate must refuse before spawning", r.count())
	}
	if bases := probe.seen(); len(bases) != 1 || bases[0] != "cafebabe" {
		t.Errorf("DiffFileCount bases = %v, want [cafebabe]", bases)
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Errorf("verdict = %q, want inconclusive", got)
	}
}

// A row dispatched before 0051 has no start_point. There is no honest base, so
// the gate is SKIPPED entirely rather than run against a base we would have to
// invent: an unmeasurable diff is not evidence of a huge one, and gating on the
// branch (the old behavior) is a guaranteed-zero measurement dressed up as a
// check. The prompt falls back to the branch as before.
func TestVerifyLegacyRowSkipsGateAndFallsBackToBranch(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	probe := &diffProbe{}
	// diffFiles is far over the bound: if the gate ran at all, this task would
	// be refused, and the runner-call assertion below would catch it.
	s := newTestService(t, db, r, stubTrees{hash: "tree-legacy", diffFiles: 5000, probe: probe})
	s.Cfg.MaxDiffFiles = 40
	id := insertTask(t, db, taskOpts{externalID: "T-root1", legacyNoStartPoint: true})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if bases := probe.seen(); len(bases) != 0 {
		t.Errorf("DiffFileCount was called with %v; a legacy row must skip the gate entirely", bases)
	}
	if r.count() != 1 {
		t.Fatalf("runner calls = %d, want 1 — a legacy row still verifies", r.count())
	}
	if got := verdictOf(t, db, id); got != "pass" {
		t.Errorf("verdict = %q, want pass", got)
	}
	if prompt := r.lastPrompt(); !strings.Contains(prompt, "diff vs swarm/T-root1") {
		t.Errorf("legacy prompt should fall back to the branch:\n%s", prompt)
	}
}

// ── retry budgets are separate (0051) ──

// handleFail charges the VERIFY budget. retry_count belongs to the dispatcher's
// no-progress heal; sharing one counter meant a flaky run could silently eat
// the fix budget (and vice versa).
func TestVerifyFail_ChargesVerifyBudgetOnly(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-vb"})
	// A task that already burned two DISPATCH retries.
	id := insertTask(t, db, taskOpts{externalID: "T-root1", retryCount: 2})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := intField(t, db, id, "verify_retry_count"); got != 1 {
		t.Errorf("verify_retry_count = %d, want 1", got)
	}
	if got := intField(t, db, id, "retry_count"); got != 2 {
		t.Errorf("retry_count = %d, want 2 untouched — it is dispatch-owned", got)
	}
	if n := countFixTasks(t, db, "T-root1"); n != 1 {
		t.Errorf("fix tasks = %d, want 1 — spent dispatch retries must not deny a fix", n)
	}
}

// The budget READ is the other half of the split: a root whose dispatch retries
// are exhausted still has its full verify budget.
func TestVerifyFail_DispatchRetriesDoNotExhaustVerifyBudget(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-vb2"})
	id := insertTask(t, db, taskOpts{externalID: "T-root1", retryCount: 99})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if intField(t, db, id, "paused") != 0 {
		t.Fatal("a root with spent DISPATCH retries must not be paused by the VERIFY budget")
	}
	if n := countFixTasks(t, db, "T-root1"); n != 1 {
		t.Fatalf("fix tasks = %d, want 1", n)
	}
}

// …and the verify budget still bounds itself: at the budget, the chain pauses.
func TestVerifyFail_VerifyBudgetExhaustionPauses(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-vb3"})
	id := insertTask(t, db, taskOpts{
		externalID: "T-root1", verifyRetryCount: DefaultRetryBudget,
	})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if intField(t, db, id, "paused") != 1 {
		t.Fatal("at the verify budget the chain must pause")
	}
	if n := countFixTasks(t, db, "T-root1"); n != 0 {
		t.Fatalf("fix tasks = %d, want 0 at budget exhaustion", n)
	}
}

// An unreadable diff is not evidence of a large one. Refusing on it would deny
// verification for a repo state we simply could not measure — the same rule the
// dispatcher's progress signal follows.
func TestScopeGateSkippedWhenDiffUnreadable(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r,
		stubTrees{hash: "tree-err", diffErr: errors.New("fatal: bad revision")})
	s.Cfg.MaxDiffFiles = 40
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Errorf("runner calls = %d, want 1 — an unmeasurable diff must not block verification", r.count())
	}
	if got := verdictOf(t, db, id); got != "pass" {
		t.Errorf("verdict = %q, want pass", got)
	}
}
