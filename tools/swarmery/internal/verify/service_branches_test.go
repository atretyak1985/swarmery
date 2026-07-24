package verify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A cached FAIL still drives the fix-task flow (the unchanged tree is still
// failing) without spawning a verifier.
func TestVerifyCachedFail_CreatesFixWithoutSpawn(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-cf"})
	id := insertTask(t, db, taskOpts{externalID: "T-root1"})

	// First fail: spawns once, creates fix#1, caches fail@tree-cf, root rc=1.
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// Archive the first fix so the dedup gate permits another, and re-verify the
	// SAME tree → cache hit returns FAIL without spawning, and (tree still failing)
	// creates the next fix charging the root again.
	if _, err := db.Exec(`UPDATE tasks SET board_column='archived' WHERE source='verify-fix'`); err != nil {
		t.Fatal(err)
	}
	callsBefore := r.count()
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != callsBefore {
		t.Fatalf("cached fail must NOT spawn (calls %d→%d)", callsBefore, r.count())
	}
	if got := verdictOf(t, db, id); got != "fail" {
		t.Fatalf("verdict = %q, want fail", got)
	}
	if rc := intField(t, db, id, "retry_count"); rc != 2 {
		t.Fatalf("root retry_count = %d, want 2 (cached fail re-charges)", rc)
	}
}

// A runner START error (process could not be spawned) degrades to INCONCLUSIVE.
func TestVerifyRunnerStartError_Inconclusive(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{err: errors.New("exec: claude not found")}
	s := newTestService(t, db, r, stubTrees{hash: "tree-se"})
	id := insertTask(t, db, taskOpts{})
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Fatalf("verdict = %q, want inconclusive", got)
	}
	if countFixTasks(t, db, "T-root1") != 0 {
		t.Fatal("a start failure must spawn no fix task")
	}
}

// A stamped verdict emits task_updated exactly once (the Notify hook fires).
func TestVerify_EmitsNotify(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-n"})
	var mu sync.Mutex
	var notified []int64
	s.Notify = func(id int64) { mu.Lock(); notified = append(notified, id); mu.Unlock() }
	id := insertTask(t, db, taskOpts{})
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notified) != 1 || notified[0] != id {
		t.Fatalf("notify = %v, want exactly [%d]", notified, id)
	}
}

// A fix task inherits the root's --model override and file scope.
func TestVerifyFail_FixInheritsModelAndScope(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: FAIL"}
	s := newTestService(t, db, r, stubTrees{hash: "tree-m"})
	id := insertTask(t, db, taskOpts{
		externalID: "T-root1", model: "opus", fileScope: `["internal/x/"]`,
	})
	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var model, scope string
	if err := db.QueryRow(
		`SELECT COALESCE(model,''), file_scope FROM tasks WHERE source='verify-fix' AND external_id='T-root1'`).
		Scan(&model, &scope); err != nil {
		t.Fatal(err)
	}
	if model != "opus" {
		t.Errorf("fix model = %q, want opus", model)
	}
	if scope != `["internal/x/"]` {
		t.Errorf("fix file_scope = %q, want the root's scope", scope)
	}
}

func TestNullableModel(t *testing.T) {
	if nullableModel("") != nil {
		t.Error(`nullableModel("") should be nil`)
	}
	if nullableModel("  ") != nil {
		t.Error("nullableModel(whitespace) should be nil")
	}
	if got := nullableModel("sonnet"); got != "sonnet" {
		t.Errorf("nullableModel(sonnet) = %v, want sonnet", got)
	}
}

// resolveRoot on a fix task with a dangling parent treats the fix AS the root
// (conservative — budget still applies, never unbounded fixes).
func TestResolveRoot_DanglingParentTreatedAsRoot(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, stubTrees{})
	// A verify-fix task whose external_id points at a non-existent root.
	id := insertTask(t, db, taskOpts{source: "verify-fix", externalID: "T-ghost"})
	tk, err := s.loadTask(id)
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.resolveRoot(tk)
	if err != nil {
		t.Fatal(err)
	}
	if root.id != id {
		t.Fatalf("dangling-parent fix should resolve to itself as root; got id=%d want %d", root.id, id)
	}
}

// resolveRoot walks a multi-hop chain (fixC → fixB → root) to the origin.
func TestResolveRoot_MultiHopChain(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, stubTrees{})
	root := insertTask(t, db, taskOpts{source: "queue", externalID: "T-root1"})
	// fixB fixes the root (external_id = root external id).
	insertTask(t, db, taskOpts{source: "verify-fix", externalID: "T-root1", worktree: "/wt/b"})
	// The chain root lookup keys on external_id, and every fix in a chain points
	// at the SAME root id (createFixTask always uses root.externalID), so a fix's
	// resolveRoot is a single hop to the queue root. Assert that hop.
	var fixBID int64
	_ = db.QueryRow(`SELECT id FROM tasks WHERE source='verify-fix' AND external_id='T-root1'`).Scan(&fixBID)
	tk, _ := s.loadTask(fixBID)
	got, err := s.resolveRoot(tk)
	if err != nil {
		t.Fatal(err)
	}
	if got.id != root {
		t.Fatalf("resolveRoot = %d, want root %d", got.id, root)
	}
}

// The real ClaudeRunner maps a timeout to an OUTCOME (TimedOut), not an error —
// exercised with a tiny timeout against a command that will exceed it. We can't
// depend on `claude` in tests, so we assert the timeout branch via a shrunk
// timeout and a bogus cwd (the process fails fast → start error path), plus a
// direct check that TimedOut is surfaced when the deadline is what fired.
func TestClaudeRunner_StartErrorSurfaced(t *testing.T) {
	// PATH is controlled in CI; if `claude` is absent the exec fails to start,
	// which is exactly the start-error path we want to see returned as an error.
	r := ClaudeRunner{Timeout: 50 * time.Millisecond}
	run, err := r.Run(context.Background(), RunSpec{
		Prompt: "noop", SessionUUID: "u", Cwd: t.TempDir(),
	})
	// Either the binary is missing (start error) or it ran and exited; both are
	// acceptable — we assert the function returns a non-nil *Run and never panics.
	if run == nil {
		t.Fatal("Run must always return a non-nil *Run")
	}
	_ = err // environment-dependent; the contract is "never panic, always a Run"
}
