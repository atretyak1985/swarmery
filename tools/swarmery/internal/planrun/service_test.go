package planrun

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// ── test doubles (shape mirrors phaserun's, which these were derived from) ──

// stubRunner records specs and returns a canned Run. When block is non-nil the
// Start call waits on it (so a test can observe the in-flight state before the
// run completes and releases the slot); a Cancel() unblocks it via ctx.
type stubRunner struct {
	mu       sync.Mutex
	specs    []RunSpec
	block    chan struct{}
	runFn    func(spec RunSpec) (*Run, error)
	startErr error
	// tickDB, when set, makes the DEFAULT stub run behave like a controller that
	// drove the plan to completion: before returning exit 0 it ticks every
	// acceptance checkbox in every phase doc of the fixture.
	//
	// It exists because "exit 0" stopped meaning "finished" (runcore.ClassifyEnd):
	// a run whose phase docs still show unticked criteria is now resumed and
	// finally stamped `partial`. That is the correct new behaviour and the wrong
	// shape for the tests here that use the spawn purely to reach the exit path
	// and assert worktree/slot/branch/stamp mechanics — those describe SUCCESSFUL
	// runs, so the stub is made to leave behind what a successful run leaves
	// behind rather than the gate being weakened. newTestService wires it.
	tickDB *sql.DB
}

func (s *stubRunner) Start(ctx context.Context, spec RunSpec) (*Run, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	block := s.block
	fn := s.runFn
	startErr := s.startErr
	tickDB := s.tickDB
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done(): // Cancel() aborts the run
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: -1}, nil
		}
	}
	if startErr != nil {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: -1}, startErr
	}
	if fn != nil {
		return fn(spec)
	}
	if tickDB != nil {
		tickEveryPhaseDoc(tickDB)
	}
	return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

// tickEveryPhaseDoc rewrites every phase doc in the fixture with all of its
// checkboxes ticked — what a controller that finished the plan leaves behind.
func tickEveryPhaseDoc(db *sql.DB) {
	rows, err := db.Query(`SELECT doc_path FROM epic_phases WHERE doc_path IS NOT NULL AND doc_path <> ''`)
	if err != nil {
		return
	}
	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			paths = append(paths, p)
		}
	}
	rows.Close()
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out := make([]string, 0, 8)
		for _, line := range strings.Split(string(body), "\n") {
			out = append(out, strings.Replace(line, "- [ ] ", "- [x] ", 1))
		}
		_ = os.WriteFile(p, []byte(strings.Join(out, "\n")), 0o644)
	}
}

// firstSpec is the ORIGINAL spawn's spec. Tests asserting what the run was
// STARTED with must use this: lastSpec returns the continuation's spec whenever
// the completion loop ran.
func (s *stubRunner) firstSpec() RunSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.specs) == 0 {
		return RunSpec{}
	}
	return s.specs[0]
}

func (s *stubRunner) specCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.specs)
}

func (s *stubRunner) lastSpec() RunSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.specs) == 0 {
		return RunSpec{}
	}
	return s.specs[len(s.specs)-1]
}

func (s *stubRunner) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.specs) }

// stubWt is a scripted WorktreeManager recording Acquire/Remove/reclaim calls.
type stubWt struct {
	mu           sync.Mutex
	acquired     []string // taskIDs handed to Acquire
	acquireRoots []string // repoRoots handed to Acquire — what proves WHERE a run went
	removed      []worktree.Acquired
	keepBranch   []bool
	acquireErr   error
	onRemove     func() // observed inside Remove, before it returns

	reclaimed    []string // branches handed to ReclaimEmptyBranch, in order
	reclaimAhead int      // commits-ahead ReclaimEmptyBranch reports (0 ⇒ reclaimed)
	reclaimErr   error
	deleted      []string // branches handed to DeleteBranch
	deleteRoots  []string // repoRoots handed to DeleteBranch
	deleteErr    error
	// branchMissing makes DeleteBranch report existed=false — the idempotent
	// "already gone" path worktree.DeleteBranch answers with (false, nil).
	branchMissing bool
}

func (w *stubWt) Acquire(repoRoot, projectSlug, taskID string) (worktree.Acquired, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.acquireErr != nil {
		return worktree.Acquired{}, w.acquireErr
	}
	w.acquired = append(w.acquired, taskID)
	w.acquireRoots = append(w.acquireRoots, repoRoot)
	return worktree.Acquired{
		Path:   "/wt/" + projectSlug + "/" + taskID,
		Branch: "swarm/" + taskID,
	}, nil
}

func (w *stubWt) Remove(repoRoot string, a worktree.Acquired, keepBranch bool) error {
	w.mu.Lock()
	w.removed = append(w.removed, a)
	w.keepBranch = append(w.keepBranch, keepBranch)
	hook := w.onRemove
	w.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (w *stubWt) ReclaimEmptyBranch(repoRoot, branch string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reclaimed = append(w.reclaimed, branch)
	if w.reclaimErr != nil {
		return 0, w.reclaimErr
	}
	return w.reclaimAhead, nil
}

func (w *stubWt) DeleteBranch(repoRoot, branch string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deleted = append(w.deleted, branch)
	w.deleteRoots = append(w.deleteRoots, repoRoot)
	if w.deleteErr != nil {
		return false, w.deleteErr
	}
	return !w.branchMissing, nil
}

// CommitsForTask satisfies dispatch.WorktreeManager. This package's tests do not
// exercise the dispatcher's progress high-water, so an empty history is the honest
// stub: no commits observed, no error.
func (w *stubWt) CommitsForTask(repoRoot, taskID string) ([]string, error) { return nil, nil }

func (w *stubWt) lastDeleteRoot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.deleteRoots) == 0 {
		return ""
	}
	return w.deleteRoots[len(w.deleteRoots)-1]
}

func (w *stubWt) lastAcquireRoot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.acquireRoots) == 0 {
		return ""
	}
	return w.acquireRoots[len(w.acquireRoots)-1]
}

func (w *stubWt) acquiredCount() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.acquired) }
func (w *stubWt) removedCount() int  { w.mu.Lock(); defer w.mu.Unlock(); return len(w.removed) }
func (w *stubWt) reclaimedList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.reclaimed...)
}
func (w *stubWt) deletedList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.deleted...)
}

// ── harness ──

// fixture builds a DB with one project, one workspace epic task registered as a
// plan artifact, a plan/ dir with README + two phase docs, and two phase rows
// (phase 2 depends on seq 1).
func fixture(t *testing.T) (*sql.DB, int64, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "planrun.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	planDir := filepath.Join(t.TempDir(), "ws", "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc1 := filepath.Join(planDir, "phase-1-schema.md")
	doc2 := filepath.Join(planDir, "phase-2-ui.md")
	mustWrite(t, filepath.Join(planDir, "README.md"), "# My Epic\n\nObjective: ship it.\n")
	mustWrite(t, doc1, "# Phase 1 — Schema\n\n- [ ] a\n- [ ] b\n")
	mustWrite(t, doc2, "# Phase 2 — UI\n\n- [ ] c\n")

	mustExec(t, db, `INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`)
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1,'My Epic','goal','running',
		'2026-07-27T00:00:00Z','2026-07-27T00:00:00Z','workspace','2026-07-27-my-epic')`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	mustExec(t, db, `INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at)
		VALUES (?, 'plan', ?, 'hash', '2026-07-27T00:00:00Z')`, taskID, planDir)

	mustExec(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done)
		VALUES (?, 1, 'Phase 1 — Schema', ?, '[]', 2, 0)`, taskID, doc1)
	mustExec(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done)
		VALUES (?, 2, 'Phase 2 — UI', ?, '[1]', 1, 0)`, taskID, doc2)
	return db, taskID, planDir
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

// newTestService builds a service with a SYNC Go seam (Start blocks until the
// run goroutine finishes) — deterministic end-state assertions. Tests that need
// an in-flight run override Go with nil (real goroutine) + a blocking runner.
func newTestService(db *sql.DB, r Runner, wt *stubWt) *Service {
	// The default stub run is a SUCCESSFUL run: it leaves the phase docs ticked.
	// See stubRunner.tickDB — exit 0 alone no longer proves completion.
	if sr, ok := r.(*stubRunner); ok && sr.tickDB == nil && sr.runFn == nil {
		sr.tickDB = db
	}
	s := NewService(db, r, wt)
	s.UUID = func() string { return "uuid-1" }
	s.Go = func(fn func()) { fn() }
	// Identity resolver: these fixtures use paths that are not checkouts (often not
	// even on disk), and what they exercise is the run lifecycle, not repo
	// resolution. Resolution has its own tests, which assert the argument the
	// worktree manager receives.
	s.RepoRoot = func(projectPath string, _ ...string) (string, error) { return projectPath, nil }
	return s
}

func planRow(t *testing.T, db *sql.DB, taskID int64) (state string, agent, uuid, startedAt, runErr sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT run_state, agent, run_session_uuid, run_started_at, run_error
		FROM plan_runs WHERE workspace_task_id=?`, taskID).Scan(&state, &agent, &uuid, &startedAt, &runErr); err != nil {
		t.Fatalf("plan_runs row: %v", err)
	}
	return
}

// waitFor polls until cond() or 2s. For in-flight (async) tests only.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// ── tests ──

func TestStartHappyPath(t *testing.T) {
	db, taskID, planDir := fixture(t)
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)
	var notified []int64
	s.Notify = func(id int64) { notified = append(notified, id) }

	uuid, err := s.Start(taskID, "tech-lead", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if uuid != "uuid-1" {
		t.Errorf("uuid = %q, want uuid-1", uuid)
	}

	// The sync Go seam means the run already finished: state is terminal.
	state, agent, gotUUID, startedAt, runErr := planRow(t, db, taskID)
	if state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
	if agent.String != "tech-lead" {
		t.Errorf("agent = %q, want tech-lead", agent.String)
	}
	if gotUUID.String != "uuid-1" || startedAt.String == "" {
		t.Errorf("uuid/startedAt = %q/%q, want both set", gotUUID.String, startedAt.String)
	}
	if runErr.Valid {
		t.Errorf("run_error = %q, want NULL", runErr.String)
	}

	// Worktree acquired under a plan-scoped id and removed keeping the branch.
	if wt.acquiredCount() != 1 || wt.removedCount() != 1 {
		t.Fatalf("worktree acquire/remove = %d/%d, want 1/1", wt.acquiredCount(), wt.removedCount())
	}
	if got := wt.acquired[0]; !strings.HasPrefix(got, "plan-") {
		t.Errorf("worktree task id = %q, want plan-<taskId>", got)
	}
	if !wt.keepBranch[0] {
		t.Error("keepBranch = false, want true (commits must stay reachable)")
	}

	// The spec carries the agent, the worktree cwd, and a prompt that points at
	// the run-plan skill and the plan dir rather than restating the procedure.
	spec := r.lastSpec()
	if spec.Agent != "tech-lead" {
		t.Errorf("spec.Agent = %q, want tech-lead", spec.Agent)
	}
	if spec.Cwd != wt.removed[0].Path {
		t.Errorf("spec.Cwd = %q, want the acquired worktree path", spec.Cwd)
	}
	for _, want := range []string{"run-plan", planDir, "Objective: ship it.", "Phase 1", "Phase 2"} {
		if !strings.Contains(spec.Prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}

	// plan_updated fires on both edges (start + exit) so the page refetches.
	if len(notified) != 2 {
		t.Errorf("notify calls = %d, want 2 (start + exit)", len(notified))
	}
}

func TestStartDefaultsAgent(t *testing.T) {
	db, taskID, _ := fixture(t)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().Agent; got != DefaultAgent() {
		t.Errorf("spec.Agent = %q, want the default %q", got, DefaultAgent())
	}
	_, agent, _, _, _ := planRow(t, db, taskID)
	if agent.String != DefaultAgent() {
		t.Errorf("stored agent = %q, want %q", agent.String, DefaultAgent())
	}
}

func TestStartAgentEnvOverride(t *testing.T) {
	t.Setenv(agentEnv, "custom-lead")
	if got := DefaultAgent(); got != "custom-lead" {
		t.Fatalf("DefaultAgent() = %q, want custom-lead", got)
	}
}

func TestStartGates(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(t *testing.T, db *sql.DB, taskID int64, planDir string)
		want    error
	}{
		{
			name: "paused plan",
			arrange: func(t *testing.T, db *sql.DB, taskID int64, _ string) {
				mustExec(t, db, `UPDATE tasks SET status='paused' WHERE id=?`, taskID)
			},
			want: ErrNotActive,
		},
		{
			name: "archived plan",
			arrange: func(t *testing.T, db *sql.DB, taskID int64, _ string) {
				mustExec(t, db, `UPDATE tasks SET archived_at='2026-07-27T00:00:00Z' WHERE id=?`, taskID)
			},
			want: ErrNotActive,
		},
		{
			name: "no phases",
			arrange: func(t *testing.T, db *sql.DB, taskID int64, _ string) {
				mustExec(t, db, `DELETE FROM epic_phases WHERE workspace_task_id=?`, taskID)
			},
			want: ErrNoPhases,
		},
		{
			name: "every phase already done",
			arrange: func(t *testing.T, db *sql.DB, taskID int64, _ string) {
				mustExec(t, db, `UPDATE epic_phases SET checkboxes_done = checkboxes_total
					WHERE workspace_task_id=?`, taskID)
			},
			want: ErrComplete,
		},
		{
			name: "a phase run is in flight",
			arrange: func(t *testing.T, db *sql.DB, taskID int64, _ string) {
				mustExec(t, db, `UPDATE epic_phases SET run_state='running'
					WHERE workspace_task_id=? AND seq=1`, taskID)
			},
			want: ErrPhaseRunning,
		},
		{
			name: "a plan run is already running",
			arrange: func(t *testing.T, db *sql.DB, taskID int64, _ string) {
				mustExec(t, db, `INSERT INTO plan_runs (workspace_task_id, run_state)
					VALUES (?, 'running')`, taskID)
			},
			want: ErrRunning,
		},
		{
			name: "project has no path",
			arrange: func(t *testing.T, db *sql.DB, _ int64, _ string) {
				mustExec(t, db, `UPDATE projects SET path='' WHERE id=1`)
			},
			want: ErrNoPath,
		},
		{
			name: "README missing",
			arrange: func(t *testing.T, _ *sql.DB, _ int64, planDir string) {
				os.Remove(filepath.Join(planDir, "README.md"))
			},
			want: ErrNoDoc,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, taskID, planDir := fixture(t)
			tc.arrange(t, db, taskID, planDir)
			r := &stubRunner{}
			wt := &stubWt{}
			s := newTestService(db, r, wt)

			_, err := s.Start(taskID, "", "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("Start error = %v, want %v", err, tc.want)
			}
			if r.count() != 0 {
				t.Error("a gated Start must not spawn")
			}
			if wt.acquiredCount() != 0 {
				t.Error("a gated Start must not acquire a worktree")
			}
		})
	}
}

func TestStartUnknownPlan(t *testing.T) {
	db, _, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.Start(9999, "", ""); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("Start error = %v, want ErrPlanNotFound", err)
	}
}

func TestStartWorktreeFailureReleasesSlot(t *testing.T) {
	db, taskID, _ := fixture(t)
	wt := &stubWt{acquireErr: errors.New("boom")}
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(taskID, "", ""); err == nil {
		t.Fatal("Start: want an error when Acquire fails")
	}
	// No row was stamped, and the single-flight slot is free for a retry.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("plan_runs rows = %d, want 0 (nothing stamped)", n)
	}
	inFlight := s.Slots.Count()
	if inFlight != 0 {
		t.Errorf("in-flight slots = %d, want 0", inFlight)
	}
}

func TestRunOutcomesAreStamped(t *testing.T) {
	cases := []struct {
		name      string
		runner    *stubRunner
		wantState string
		wantErr   string
	}{
		{
			name:      "nonzero exit",
			runner:    &stubRunner{runFn: func(RunSpec) (*Run, error) { return &Run{ExitCode: 2, Stderr: "kaboom"}, nil }},
			wantState: "failed",
			wantErr:   "kaboom",
		},
		{
			name:      "timeout",
			runner:    &stubRunner{runFn: func(RunSpec) (*Run, error) { return &Run{TimedOut: true, ExitCode: -1}, nil }},
			wantState: "failed",
			wantErr:   "timeout",
		},
		{
			name:      "could not start",
			runner:    &stubRunner{startErr: errors.New("no claude binary")},
			wantState: "failed",
			wantErr:   "no claude binary",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, taskID, _ := fixture(t)
			wt := &stubWt{}
			s := newTestService(db, tc.runner, wt)
			if _, err := s.Start(taskID, "", ""); err != nil {
				t.Fatalf("Start: %v", err)
			}
			state, _, _, _, runErr := planRow(t, db, taskID)
			if state != tc.wantState {
				t.Errorf("run_state = %q, want %q", state, tc.wantState)
			}
			if !strings.Contains(runErr.String, tc.wantErr) {
				t.Errorf("run_error = %q, want it to contain %q", runErr.String, tc.wantErr)
			}
			// The worktree is released whatever the outcome.
			if wt.removedCount() != 1 {
				t.Errorf("worktree removals = %d, want 1", wt.removedCount())
			}
		})
	}
}

func TestCancelStampsCancelled(t *testing.T) {
	db, taskID, _ := fixture(t)
	r := &stubRunner{block: make(chan struct{})}
	wt := &stubWt{}
	r.tickDB = db // a stub run that finished its plan — exit 0 alone no longer proves it
	s := NewService(db, r, wt) // real goroutine — the run must be observable in flight
	s.UUID = func() string { return "uuid-1" }
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }

	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool {
		state, _, _, _, _ := planRow(t, db, taskID)
		return state == "running"
	})
	// A second Start while one is in flight is refused.
	if _, err := s.Start(taskID, "", ""); !errors.Is(err, ErrRunning) {
		t.Errorf("second Start error = %v, want ErrRunning", err)
	}

	if !s.Cancel(taskID) {
		t.Fatal("Cancel returned false for an in-flight run")
	}
	waitFor(t, func() bool {
		state, _, _, _, _ := planRow(t, db, taskID)
		return state == "failed"
	})
	_, _, _, _, runErr := planRow(t, db, taskID)
	if runErr.String != "cancelled" {
		t.Errorf("run_error = %q, want cancelled", runErr.String)
	}
	if s.Cancel(taskID) {
		t.Error("Cancel on an idle plan returned true")
	}
}

func TestHealStale(t *testing.T) {
	db, taskID, _ := fixture(t)
	mustExec(t, db, `INSERT INTO plan_runs (workspace_task_id, run_state, run_session_uuid)
		VALUES (?, 'running', 'orphan')`, taskID)
	s := newTestService(db, &stubRunner{}, &stubWt{})

	if err := s.HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	state, _, _, _, runErr := planRow(t, db, taskID)
	if state != "failed" || runErr.String != "daemon restart" {
		t.Errorf("healed row = (%q, %q), want (failed, daemon restart)", state, runErr.String)
	}
}

func TestStartAfterFailedRunIsAllowed(t *testing.T) {
	db, taskID, _ := fixture(t)
	mustExec(t, db, `INSERT INTO plan_runs (workspace_task_id, run_state, run_error)
		VALUES (?, 'failed', 'earlier boom')`, taskID)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(taskID, "retry-agent", ""); err != nil {
		t.Fatalf("Start after a failed run: %v", err)
	}
	state, agent, _, _, runErr := planRow(t, db, taskID)
	if state != "done" || agent.String != "retry-agent" {
		t.Errorf("row = (%q, %q), want (done, retry-agent)", state, agent.String)
	}
	if runErr.Valid {
		t.Errorf("run_error = %q, want the earlier error cleared", runErr.String)
	}
}

// ── retry: leftover branch reclamation ──

// planBranch is the deterministic run branch for a plan — the same literal
// Start reclaims, Acquire derives (worktree.branchName), and DeleteRunBranch
// removes.
func planBranch(taskID int64) string { return "swarm/plan-" + strconv.FormatInt(taskID, 10) }

// TestStart_ReclaimsEmptyLeftoverBranch: every run's teardown removes the worktree
// with keepBranch=true, so swarm/plan-<taskID> outlives the run and the NEXT
// Acquire would hit ErrBranchBusy. An empty leftover is reclaimed automatically,
// before Acquire — without it no plan could ever be run twice.
func TestStart_ReclaimsEmptyLeftoverBranch(t *testing.T) {
	db, taskID, _ := fixture(t)
	wt := &stubWt{} // reclaimAhead=0 ⇒ the leftover was empty and got deleted
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := planBranch(taskID)
	if got := wt.reclaimedList(); len(got) != 1 || got[0] != want {
		t.Fatalf("reclaimed = %v, want exactly [%s]", got, want)
	}
	// Reclaim is a precondition of acquisition, not a replacement for it.
	if wt.acquiredCount() != 1 {
		t.Errorf("acquired = %v, want the run to proceed to Acquire after reclaim", wt.acquired)
	}
	if state, _, _, _, _ := planRow(t, db, taskID); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
}

// stubGit answers `symbolic-ref --short HEAD` so the service can NAME the branch a
// commits-ahead count was measured against.
type stubGit struct {
	head string
	err  error
	mu   sync.Mutex
	args []string
}

func (g *stubGit) Run(dir string, args ...string) (string, error) {
	g.mu.Lock()
	g.args = append(g.args, strings.Join(args, " "))
	g.mu.Unlock()
	if g.err != nil {
		return "", g.err
	}
	return g.head + "\n", nil
}

// TestStart_BranchDirty_RefusesAndReleasesSlot: a leftover branch holding commits is
// never destroyed to make room. Start refuses with a typed error naming the branch and
// the commit count (the api's 409 body / the UI's delete-or-merge prompt), and the
// single-flight slot must be released so the user's follow-up attempt is admitted.
func TestStart_BranchDirty_RefusesAndReleasesSlot(t *testing.T) {
	db, taskID, _ := fixture(t)
	wt := &stubWt{reclaimAhead: 3}
	s := newTestService(db, &stubRunner{}, wt)

	_, err := s.Start(taskID, "", "")
	if !errors.Is(err, ErrBranchDirty) {
		t.Fatalf("err = %v, want ErrBranchDirty", err)
	}
	var bde *BranchDirtyError
	if !errors.As(err, &bde) {
		t.Fatalf("err = %v, want a *BranchDirtyError", err)
	}
	if bde.Branch != planBranch(taskID) {
		t.Errorf("Branch = %q, want %q", bde.Branch, planBranch(taskID))
	}
	if bde.CommitsAhead != 3 {
		t.Errorf("CommitsAhead = %d, want 3", bde.CommitsAhead)
	}
	// No Git seam wired ⇒ the base cannot be named; the field is empty rather than
	// guessed, so the api never qualifies a 409 with a base it did not measure.
	if bde.Base != "" {
		t.Errorf("Base = %q, want empty without a Git seam", bde.Base)
	}
	// The dirty branch is untouched and no worktree was taken.
	if len(wt.deletedList()) != 0 {
		t.Errorf("deleted = %v, want the dirty branch left alone", wt.deletedList())
	}
	if wt.acquiredCount() != 0 {
		t.Errorf("acquired = %v, want no acquisition after a dirty-branch refusal", wt.acquired)
	}
	// Nothing was stamped: a refused admission must not look like a run.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("plan_runs rows = %d, want 0 after a refused admission", n)
	}
	// The refusal is not sticky: once the branch is resolved, a retry is admitted
	// (not rejected with ErrRunning by a leaked slot).
	inFlight := s.Slots.Count()
	if inFlight != 0 {
		t.Errorf("in-flight slots = %d, want 0 after a dirty-branch refusal", inFlight)
	}
	wt.mu.Lock()
	wt.reclaimAhead = 0
	wt.mu.Unlock()
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("retry after the branch was resolved: %v", err)
	}
}

// TestStart_BranchDirty_NamesTheBase: worktree.ReclaimEmptyBranch counts commits
// against the repo's CURRENT checkout (matching Acquire's start point), so the same
// branch is "3 commits ahead" of dev and "0 ahead" of a feature branch that already
// contains them. The 409 has to say which one it measured, or the user cannot tell a
// real conflict from base skew.
func TestStart_BranchDirty_NamesTheBase(t *testing.T) {
	db, taskID, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{reclaimAhead: 3})
	s.Git = &stubGit{head: "dev"}

	var bde *BranchDirtyError
	if _, err := s.Start(taskID, "", ""); !errors.As(err, &bde) {
		t.Fatalf("err = %v, want a *BranchDirtyError", err)
	}
	if bde.Base != "dev" {
		t.Errorf("Base = %q, want the checked-out branch dev", bde.Base)
	}
	// A detached HEAD (or any git failure) names nothing rather than guessing.
	s2 := newTestService(db, &stubRunner{}, &stubWt{reclaimAhead: 3})
	s2.Git = &stubGit{err: errors.New("detached HEAD")}
	if _, err := s2.Start(taskID, "", ""); !errors.As(err, &bde) {
		t.Fatalf("err = %v, want a *BranchDirtyError", err)
	}
	if bde.Base != "" {
		t.Errorf("Base = %q, want empty when git cannot name a branch", bde.Base)
	}
}

func TestStart_ReclaimError_ReleasesSlot(t *testing.T) {
	db, taskID, _ := fixture(t)
	wt := &stubWt{reclaimErr: errors.New("could not lock ref")}
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(taskID, "", ""); err == nil || !strings.Contains(err.Error(), "could not lock ref") {
		t.Fatalf("err = %v, want the reclaim failure surfaced", err)
	}
	if wt.acquiredCount() != 0 {
		t.Error("Acquire ran despite a failed reclaim probe")
	}
	wt.mu.Lock()
	wt.reclaimErr = nil
	wt.mu.Unlock()
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("retry after a reclaim failure: %v", err)
	}
}

// ── crash leftover: the run's OWN worktree still on its OWN branch ──

// scriptedGit is a minimal scripted git boundary (the shape of
// worktree_test.go's stubGit): most-recently-registered matching prefix wins,
// unscripted verbs succeed with empty output.
type scriptedGit struct {
	mu      sync.Mutex
	calls   []string
	scripts []struct {
		toks   []string
		output string
	}
}

func (g *scriptedGit) on(verb, output string) *scriptedGit {
	g.scripts = append(g.scripts, struct {
		toks   []string
		output string
	}{strings.Fields(verb), output})
	return g
}

func (g *scriptedGit) Run(dir string, args ...string) (string, error) {
	g.mu.Lock()
	g.calls = append(g.calls, strings.Join(args, " "))
	g.mu.Unlock()
	for i := len(g.scripts) - 1; i >= 0; i-- {
		s := g.scripts[i]
		if len(args) < len(s.toks) {
			continue
		}
		match := true
		for j, tok := range s.toks {
			if args[j] != tok {
				match = false
				break
			}
		}
		if match {
			return s.output, nil
		}
	}
	return "", nil
}

func (g *scriptedGit) called(substr string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// TestStart_CrashLeftoverWorktreeIsReusedNotRefused drives a REAL
// worktree.Manager (scripted git) through the state a daemon crash leaves: the
// run's own worktree still registered at <Root>/<slug>/plan-<taskID>, still on
// swarm/plan-<taskID>. Read as a plain "the branch is checked out" conflict this
// is a permanent dead end — the branch cannot be reclaimed and Acquire is never
// reached — which is exactly the regression the phase-run review caught. It must
// instead fall through to Acquire's warm reuse (worktree invariant 4).
//
// It doubles as the proof that the swarm/ namespace guard admits swarm/plan-<id>
// unchanged: taskIDForBranch would otherwise reject it as ErrRefusedBranch before
// any of this ran.
func TestStart_CrashLeftoverWorktreeIsReusedNotRefused(t *testing.T) {
	db, taskID, _ := fixture(t)
	repoRoot := t.TempDir()
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repoRoot)

	root := filepath.Join(t.TempDir(), "wts")
	own := filepath.Join(root, "p", "plan-"+strconv.FormatInt(taskID, 10))
	branch := planBranch(taskID)

	git := (&scriptedGit{}).
		on("symbolic-ref --short HEAD", "main\n").
		on("rev-parse refs/heads/main", "aaaa1111\n").
		on("rev-parse --verify --quiet refs/heads/"+branch, "aaaa1111\n").
		on("worktree list --porcelain", "worktree "+own+"\nbranch refs/heads/"+branch+"\n\n")
	mgr := &worktree.Manager{Git: git, Root: root}

	r := &stubRunner{}
	r.tickDB = db // a stub run that finished its plan — exit 0 alone no longer proves it
	s := NewService(db, r, mgr)
	s.UUID = func() string { return "uuid-1" }
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	s.Go = func(fn func()) { fn() }

	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start over a crash leftover = %v, want the run admitted by warm reuse", err)
	}
	if got := r.lastSpec().Cwd; got != own {
		t.Errorf("run cwd = %q, want the warm-reused worktree %q", got, own)
	}
	if git.called("branch -D") {
		t.Error("a branch checked out in a live worktree must never be deleted")
	}
	if git.called("worktree add") {
		t.Error("warm reuse must not re-add the worktree")
	}
}

// ── teardown ordering ──

// TestRunTeardown_WorktreeRemovedBeforeSlotRelease: the single-flight slot is the
// LAST thing the teardown releases. stamp() has already opened the DB gate, so a
// slot released before the git shell-out lets a re-Start re-acquire the same
// deterministic worktree path — which this defer would then delete underneath it.
func TestRunTeardown_WorktreeRemovedBeforeSlotRelease(t *testing.T) {
	db, taskID, _ := fixture(t)
	wt := &stubWt{}
	var (
		s          *Service
		slotHeld   bool
		hookCalled bool
	)
	wt.onRemove = func() {
		hookCalled = true
		slotHeld = s.Slots.IsActive(s.slotKey(taskID))
	}
	s = newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hookCalled {
		t.Fatal("worktree was never removed")
	}
	if !slotHeld {
		t.Error("single-flight slot was already released when the worktree was removed — " +
			"a concurrent re-Start could reuse and then lose that worktree")
	}
	if s.Slots.IsActive(s.slotKey(taskID)) {
		t.Error("slot still held after the run finished")
	}
}

// TestStart_DBFailure_WorktreeRemovedBeforeSlotRelease: the admission INSERT is the
// write that closes the DB gate, so when it FAILS both gates are open at once. The
// teardown must therefore keep the same order as runAndHandle's defer — worktree
// first, slot last — or a concurrent Start warm-reuses the deterministic
// plan-<taskID> path this line is about to delete.
func TestStart_DBFailure_WorktreeRemovedBeforeSlotRelease(t *testing.T) {
	db, taskID, _ := fixture(t)
	// A BEFORE INSERT trigger fails the run_state='running' write while every
	// admission SELECT still succeeds — exactly the path under test.
	mustExec(t, db, `CREATE TRIGGER planrun_block_insert BEFORE INSERT ON plan_runs
		BEGIN SELECT RAISE(ABORT, 'insert blocked'); END`)

	wt := &stubWt{}
	var (
		s          *Service
		slotHeld   bool
		hookCalled bool
	)
	wt.onRemove = func() {
		hookCalled = true
		slotHeld = s.Slots.IsActive(s.slotKey(taskID))
	}
	s = newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(taskID, "", ""); err == nil {
		t.Fatal("Start = nil, want the failed admission INSERT surfaced")
	}
	if !hookCalled {
		t.Fatal("the acquired worktree was never removed after the failed INSERT")
	}
	if !slotHeld {
		t.Error("single-flight slot was already released when the worktree was removed — " +
			"a concurrent Start could warm-reuse the path this teardown then deletes")
	}
	if s.Slots.IsActive(s.slotKey(taskID)) {
		t.Error("slot still held after the failed admission")
	}
}

// ── explicit branch deletion ──

func TestDeleteRunBranch(t *testing.T) {
	db, taskID, _ := fixture(t)
	// existed now comes from the delete call itself (worktree.DeleteBranch reports
	// it), not from a local rev-parse probe — no Git seam needed.
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)

	branch, existed, err := s.DeleteRunBranch(taskID)
	if err != nil {
		t.Fatalf("DeleteRunBranch: %v", err)
	}
	want := planBranch(taskID)
	if branch != want {
		t.Errorf("branch = %q, want %q", branch, want)
	}
	if !existed {
		t.Error("existed = false for a branch the boundary reported deleted")
	}
	if got := wt.deletedList(); len(got) != 1 || got[0] != want {
		t.Errorf("deleted = %v, want [%s]", got, want)
	}
	// The duplicate probe is gone: the only source of `existed` is the boundary.
	if s.Git != nil {
		t.Fatal("test wired a Git seam it no longer needs")
	}
}

// A branch that was never there must not be reported as deleted — "deleted: true"
// on a no-op is the claim the caller turns into a cleared banner.
func TestDeleteRunBranch_MissingBranchReportsNotExisted(t *testing.T) {
	db, taskID, _ := fixture(t)
	wt := &stubWt{branchMissing: true} // worktree.DeleteBranch: (false, nil)
	s := newTestService(db, &stubRunner{}, wt)

	branch, existed, err := s.DeleteRunBranch(taskID)
	if err != nil {
		t.Fatalf("DeleteRunBranch: %v", err)
	}
	if branch != planBranch(taskID) {
		t.Errorf("branch = %q, want %q", branch, planBranch(taskID))
	}
	if existed {
		t.Error("existed = true for a branch that was never there")
	}
}

// TestDeleteRunBranch_ErrRunning: deleting the branch out from under a live run
// would strand its commits — refuse while the plan holds a single-flight slot.
func TestDeleteRunBranch_ErrRunning(t *testing.T) {
	db, taskID, _ := fixture(t)
	r := &stubRunner{block: make(chan struct{})}
	wt := &stubWt{}
	r.tickDB = db // a stub run that finished its plan — exit 0 alone no longer proves it
	s := NewService(db, r, wt) // real goroutine — run stays in flight
	s.UUID = func() string { return "uuid-1" }
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }

	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool {
		state, _, _, _, _ := planRow(t, db, taskID)
		return state == "running"
	})
	if _, _, err := s.DeleteRunBranch(taskID); !errors.Is(err, ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning while a run is in flight", err)
	}
	if got := wt.deletedList(); len(got) != 0 {
		t.Errorf("deleted = %v, want no deletion during a live run", got)
	}

	close(r.block)
	waitFor(t, func() bool {
		state, _, _, _, _ := planRow(t, db, taskID)
		return state == "done"
	})
	// Once the run is over the deletion is allowed.
	if _, _, err := s.DeleteRunBranch(taskID); err != nil {
		t.Fatalf("DeleteRunBranch after the run finished: %v", err)
	}
}

func TestDeleteRunBranch_UnknownPlan(t *testing.T) {
	db, _, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, _, err := s.DeleteRunBranch(9999); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("err = %v, want ErrPlanNotFound", err)
	}
}

func TestDeleteRunBranch_NoProjectPath(t *testing.T) {
	db, taskID, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path='' WHERE id=1`)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, _, err := s.DeleteRunBranch(taskID); !errors.Is(err, ErrNoPath) {
		t.Fatalf("err = %v, want ErrNoPath", err)
	}
}

func TestDeleteRunBranch_PropagatesFailure(t *testing.T) {
	db, taskID, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{deleteErr: errors.New("checked out")})
	if _, _, err := s.DeleteRunBranch(taskID); err == nil {
		t.Fatal("DeleteRunBranch = nil, want the git refusal surfaced")
	}
}

// ── spec-coverage gate ──

// TestStart_SpecUncovered_Refuses: plan/spec.md declares SC-1..3, the phase docs
// cover only 1 and 2 ⇒ Start refuses with a typed error naming SC-3, before any
// state exists (no spawn, no worktree, no plan_runs row, slot free). Once the
// missing Covers line is added the very next Start is admitted — the gate reads
// the files live, not a stale scan.
func TestStart_SpecUncovered_Refuses(t *testing.T) {
	db, taskID, planDir := fixture(t)
	mustWrite(t, filepath.Join(planDir, "spec.md"),
		"# Spec\n\n## Acceptance criteria\n\n- [ ] **SC-1** — first\n- [ ] **SC-2** — second\n- [ ] **SC-3** — third\n")
	// Covers must live in the doc HEADER (ParseCovers scans the first lines and
	// stops at the first `## ` section) — both declared forms are exercised.
	mustWrite(t, filepath.Join(planDir, "phase-1-schema.md"),
		"# Phase 1 — Schema\n\n**Covers:** SC-1\n\n## Acceptance criteria\n- [ ] a\n- [ ] b\n")
	mustWrite(t, filepath.Join(planDir, "phase-2-ui.md"),
		"# Phase 2 — UI\n\n| **Covers** | SC-2 |\n\n## Acceptance criteria\n- [ ] c\n")
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)

	_, err := s.Start(taskID, "", "")
	if !errors.Is(err, ErrSpecUncovered) {
		t.Fatalf("Start error = %v, want ErrSpecUncovered", err)
	}
	var sue *SpecUncoveredError
	if !errors.As(err, &sue) {
		t.Fatalf("err = %v, want a *SpecUncoveredError", err)
	}
	if len(sue.Uncovered) != 1 || sue.Uncovered[0] != "SC-3" {
		t.Errorf("Uncovered = %v, want [SC-3]", sue.Uncovered)
	}
	if r.count() != 0 || wt.acquiredCount() != 0 {
		t.Error("a spec-gated Start must not spawn or acquire a worktree")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("plan_runs rows = %d, want 0 (a refusal leaves no state)", n)
	}
	inFlight := s.Slots.Count()
	if inFlight != 0 {
		t.Errorf("in-flight slots = %d, want 0 after a spec refusal", inFlight)
	}

	// Cover SC-3 and the retry is admitted.
	mustWrite(t, filepath.Join(planDir, "phase-2-ui.md"),
		"# Phase 2 — UI\n\n| **Covers** | SC-2, SC-3 |\n\n## Acceptance criteria\n- [ ] c\n")
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("retry after covering SC-3: %v", err)
	}
	if state, _, _, _, _ := planRow(t, db, taskID); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
}

// TestStart_SpecAllCovered_Proceeds: every declared criterion is covered ⇒ the
// gate is a pass-through and the run completes exactly like the happy path.
func TestStart_SpecAllCovered_Proceeds(t *testing.T) {
	db, taskID, planDir := fixture(t)
	mustWrite(t, filepath.Join(planDir, "spec.md"),
		"# Spec\n\n- [ ] **SC-1** — first\n- [x] **SC-2** — second\n")
	mustWrite(t, filepath.Join(planDir, "phase-1-schema.md"),
		"# Phase 1 — Schema\n\n**Covers:** SC-1, SC-2\n\n## Acceptance criteria\n- [ ] a\n- [ ] b\n")
	s := newTestService(db, &stubRunner{}, &stubWt{})

	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start with a fully covered spec: %v", err)
	}
	if state, _, _, _, _ := planRow(t, db, taskID); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
}

// TestStart_NoSpec_AdmissionUnchanged: a plan without spec.md — or with a
// spec.md that declares no SC criteria — is admission-identical to the
// pre-gate behavior.
func TestStart_NoSpec_AdmissionUnchanged(t *testing.T) {
	t.Run("no spec.md", func(t *testing.T) {
		db, taskID, _ := fixture(t)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(taskID, "", ""); err != nil {
			t.Fatalf("Start without spec.md: %v", err)
		}
		if state, _, _, _, _ := planRow(t, db, taskID); state != "done" {
			t.Errorf("run_state = %q, want done", state)
		}
	})
	t.Run("spec.md without criteria", func(t *testing.T) {
		db, taskID, planDir := fixture(t)
		mustWrite(t, filepath.Join(planDir, "spec.md"),
			"# Spec\n\nProse only — narrative checkboxes are not criteria:\n\n- [ ] plain box\n")
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(taskID, "", ""); err != nil {
			t.Fatalf("Start with a criterion-less spec.md: %v", err)
		}
	})
}

func TestPhaseRunStateIsNeverTouched(t *testing.T) {
	// A plan run is ONE session for the whole plan; claiming per-phase run rows
	// would make the two mechanisms lie about each other.
	db, taskID, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rows, err := db.Query(`SELECT run_state FROM epic_phases WHERE workspace_task_id=?`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "idle" {
			t.Errorf("phase run_state = %q, want idle (untouched by a plan run)", state)
		}
	}
}
