package phaserun

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// ── test doubles ──

// stubRunner records specs and returns a canned Run. When block is non-nil the
// Start call waits on it (so a test can observe the in-flight state before the
// run completes and releases the slot); a Cancel() unblocks it via ctx.
type stubRunner struct {
	mu       sync.Mutex
	specs    []RunSpec
	block    chan struct{}
	runFn    func(spec RunSpec) (*Run, error)
	startErr error
}

func (s *stubRunner) Start(ctx context.Context, spec RunSpec) (*Run, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	block := s.block
	fn := s.runFn
	startErr := s.startErr
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
	return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

func (s *stubRunner) lastSpec() RunSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.specs) == 0 {
		return RunSpec{}
	}
	return s.specs[len(s.specs)-1]
}

// stubWt is a scripted WorktreeManager recording Acquire/Remove/reclaim calls.
type stubWt struct {
	mu           sync.Mutex
	acquired     []string // taskIDs handed to Acquire
	acquireRoots []string // repoRoots handed to Acquire — what proves WHERE a run went
	removed      []worktree.Acquired
	keepBranch   []bool
	acquireErr   error
	onRemove     func() // observed inside Remove, before it returns
	// startPoint is the SHA Acquire reports pinning the worktree to. Deliberately
	// NOT a branch name: run_start_point exists so the verifier diffs
	// base...HEAD, and a test whose base happens to equal the branch would pass on
	// exactly the bug that column prevents. "" ⇒ stubStartPoint.
	startPoint string

	reclaimed    []string // branches handed to ReclaimEmptyBranch, in order
	reclaimAhead int      // commits-ahead ReclaimEmptyBranch reports (0 ⇒ reclaimed)
	reclaimErr   error
	// reclaimAheadBy overrides reclaimAhead per branch. Start reclaims TWO names when a
	// previous run's branch no longer matches the deterministic one, and the whole point
	// of that second call is that the two can answer differently.
	reclaimAheadBy map[string]int
	deleted        []string // branches handed to DeleteBranch
	deleteErr      error
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
	sp := w.startPoint
	if sp == "" {
		sp = stubStartPoint
	}
	return worktree.Acquired{
		Path:       "/wt/" + projectSlug + "/" + taskID,
		Branch:     "swarm/" + taskID,
		StartPoint: sp,
	}, nil
}

// stubStartPoint is the harness's stand-in for the SHA Acquire pins a worktree to.
const stubStartPoint = "base0ffee"

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
	if n, ok := w.reclaimAheadBy[branch]; ok {
		return n, nil
	}
	return w.reclaimAhead, nil
}

func (w *stubWt) DeleteBranch(repoRoot, branch string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deleted = append(w.deleted, branch)
	if w.deleteErr != nil {
		return false, w.deleteErr
	}
	return !w.branchMissing, nil
}

// CommitsForTask satisfies dispatch.WorktreeManager. This package's tests do not
// exercise the dispatcher's progress high-water, so an empty history is the honest
// stub: no commits observed, no error.
func (w *stubWt) CommitsForTask(repoRoot, taskID string) ([]string, error) { return nil, nil }

func (w *stubWt) acquiredCount() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.acquired) }
func (w *stubWt) removedCount() int  { w.mu.Lock(); defer w.mu.Unlock(); return len(w.removed) }
func (w *stubWt) reclaimedList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.reclaimed...)
}

// ── harness ──

// fixture builds a DB with one project, one workspace epic task, and two
// phases on disk: phase 1 (no deps) and phase 2 (depends on seq 1). Returns
// the db, the workspace task id, and the two phase ids.
func fixture(t *testing.T) (*sql.DB, int64, int64, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "phaserun.db"))
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
	os.WriteFile(doc1, []byte("# Phase 1 — Schema\n\n- [ ] a\n- [ ] b\n"), 0o644)
	os.WriteFile(doc2, []byte("# Phase 2 — UI\n\n- [ ] c\n"), 0o644)

	mustExec(t, db, `INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`)
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1,'My Epic','goal','running',
		'2026-07-27T00:00:00Z','2026-07-27T00:00:00Z','workspace','2026-07-27-my-epic')`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()

	r1, err := db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done)
		VALUES (?, 1, 'Phase 1 — Schema', ?, '[]', 2, 0)`, taskID, doc1)
	if err != nil {
		t.Fatal(err)
	}
	p1, _ := r1.LastInsertId()
	r2, err := db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done)
		VALUES (?, 2, 'Phase 2 — UI', ?, '[1]', 1, 0)`, taskID, doc2)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := r2.LastInsertId()
	return db, taskID, p1, p2
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

// testEpoch is where the injected test clock starts. Each clock read advances it
// by a second, so run_started_at and run_ended_at are exact, distinct, ORDERED
// values a test can assert literally — without the injection the timestamp
// assertions could only check "non-empty", which a garbage or wrongly-formatted
// value would also satisfy.
const testEpoch = "2026-07-28T12:00:00Z"

// testTime returns the timestamp the nth clock read produces, in the exact
// format the service persists.
func testTime(n int) string {
	base, err := time.Parse(time.RFC3339, testEpoch)
	if err != nil {
		panic(err)
	}
	return base.Add(time.Duration(n) * time.Second).UTC().Format(time.RFC3339)
}

// newTestService builds a service with a SYNC Go seam (Start blocks until the
// run goroutine finishes) — deterministic end-state assertions — and a pinned,
// monotonically advancing clock. Tests that need an in-flight run override Go
// with nil (real goroutine) + a blocking runner.
func newTestService(db *sql.DB, r Runner, wt *stubWt) *Service {
	s := NewService(db, r, wt)
	s.UUID = func() string { return "uuid-1" }
	s.Go = func(fn func()) { fn() }
	// Identity resolver: these fixtures use paths that are not checkouts. Repo
	// resolution has its own tests, which assert the argument the worktree manager
	// receives.
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	base, err := time.Parse(time.RFC3339, testEpoch)
	if err != nil {
		panic(err)
	}
	var (
		mu sync.Mutex
		n  int64
	)
	s.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t := base.Add(time.Duration(n) * time.Second)
		n++
		return t
	}
	return s
}

func phaseRow(t *testing.T, db *sql.DB, id int64) (state string, uuid, startedAt, runErr sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT run_state, run_session_uuid, run_started_at, run_error
		FROM epic_phases WHERE id=?`, id).Scan(&state, &uuid, &startedAt, &runErr); err != nil {
		t.Fatalf("phase row: %v", err)
	}
	return
}

// phaseOutcome reads the run-outcome measurement columns (migration 0041):
// the ticked-criteria baseline snapshotted at spawn and the terminal timestamp.
func phaseOutcome(t *testing.T, db *sql.DB, id int64) (before sql.NullInt64, endedAt sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT run_checkboxes_before, run_ended_at
		FROM epic_phases WHERE id=?`, id).Scan(&before, &endedAt); err != nil {
		t.Fatalf("phase outcome row: %v", err)
	}
	return
}

func taskCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
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
	t.Fatal("condition not reached in 2s")
}

// ── tests ──

func TestStart_HappyPath(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)
	var notified []int64
	s.Notify = func(id int64) { notified = append(notified, id) }
	before := taskCount(t, db)

	uuid, err := s.Start(p1, "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if uuid != "uuid-1" {
		t.Errorf("uuid = %q", uuid)
	}

	state, u, started, runErr := phaseRow(t, db, p1)
	if state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
	if u.String != "uuid-1" {
		t.Errorf("run_session_uuid = %q", u.String)
	}
	// Exact stamps from the injected clock — format and value, not just presence.
	if !started.Valid || started.String != testTime(0) {
		t.Errorf("run_started_at = %q (valid=%v), want %q", started.String, started.Valid, testTime(0))
	}
	if runErr.Valid {
		t.Errorf("run_error = %q, want NULL", runErr.String)
	}
	_, endedAt := phaseOutcome(t, db, p1)
	if !endedAt.Valid || endedAt.String != testTime(1) {
		t.Errorf("run_ended_at = %q (valid=%v), want %q", endedAt.String, endedAt.Valid, testTime(1))
	}
	// The interval must never run backwards — a duration derived from these two
	// columns has to be non-negative.
	if endedAt.String < started.String {
		t.Errorf("run_ended_at %q < run_started_at %q", endedAt.String, started.String)
	}

	// Worktree acquired under phase-<id> and removed with the branch kept.
	if wt.acquiredCount() != 1 || wt.acquired[0] != "phase-"+itoa64(p1) {
		t.Errorf("acquired = %v", wt.acquired)
	}
	if wt.removedCount() != 1 || !wt.keepBranch[0] {
		t.Errorf("removed = %v keepBranch = %v, want 1 removal with keepBranch=true", wt.removed, wt.keepBranch)
	}

	// The spawned run: cwd is the worktree, prompt embeds the phase doc.
	spec := r.lastSpec()
	if spec.Cwd != wt.removed[0].Path {
		t.Errorf("spec.Cwd = %q, want the acquired worktree %q", spec.Cwd, wt.removed[0].Path)
	}
	if !strings.Contains(spec.Prompt, "# Phase 1 — Schema") ||
		!strings.Contains(spec.Prompt, "phase-1-schema.md") {
		t.Errorf("prompt does not embed the phase doc:\n%s", spec.Prompt)
	}
	if spec.SessionUUID != "uuid-1" {
		t.Errorf("spec.SessionUUID = %q", spec.SessionUUID)
	}

	// Notify fired at both edges, keyed by the WORKSPACE task id.
	if len(notified) < 2 || notified[0] != taskID || notified[len(notified)-1] != taskID {
		t.Errorf("notified = %v, want ≥2 notifications for task %d", notified, taskID)
	}

	// No tasks rows minted anywhere in the flow.
	if after := taskCount(t, db); after != before {
		t.Errorf("tasks count changed %d → %d; phase runs must not create tasks", before, after)
	}
}

// TestStart_SnapshotsCheckboxesBefore: run_state records how the PROCESS ended,
// never whether work landed. The baseline snapshot taken at spawn is what makes
// the delta across a run measurable — a 'done' run with a zero delta produced
// nothing.
func TestStart_SnapshotsCheckboxesBefore(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET checkboxes_total=8, checkboxes_done=3 WHERE id=?`, p1)
	s := newTestService(db, &stubRunner{}, &stubWt{})

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before, _ := phaseOutcome(t, db, p1)
	if !before.Valid || before.Int64 != 3 {
		t.Errorf("run_checkboxes_before = %v (valid=%v), want 3 — the ticked count at spawn time",
			before.Int64, before.Valid)
	}
}

// phaseAfter reads run_checkboxes_after (migration 0042) — the right edge of the
// run's measurement interval, stamped at exit.
func phaseAfter(t *testing.T, db *sql.DB, id int64) sql.NullInt64 {
	t.Helper()
	var after sql.NullInt64
	if err := db.QueryRow(`SELECT run_checkboxes_after FROM epic_phases WHERE id=?`, id).
		Scan(&after); err != nil {
		t.Fatalf("phase after row: %v", err)
	}
	return after
}

// phaseDocPath reads a phase's doc_path — the file the run's exit stamp counts.
func phaseDocPath(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var p string
	if err := db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, id).Scan(&p); err != nil {
		t.Fatalf("phase doc_path: %v", err)
	}
	return p
}

func mustWriteDoc(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write doc %s: %v", path, err)
	}
}

// TestStamp_ClosesCheckboxInterval: the exit stamp pins the ticked count as it was
// when the run ended. Both the doc and checkboxes_done keep moving afterwards (the
// wsingest rescan and TickPhaseChecklist write the column, a human or the next run
// writes the doc) — the stamped edge must not follow either, or another writer's
// ticks get attributed to this run.
func TestStamp_ClosesCheckboxInterval(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)
	// The run itself ticks one of the doc's two boxes before it exits, and the
	// rescan happens to have caught up by then.
	r := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [ ] b\n")
		mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=1 WHERE id=?`, p1)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before, _ := phaseOutcome(t, db, p1)
	if !before.Valid || before.Int64 != 0 {
		t.Errorf("run_checkboxes_before = %v, want 0", before)
	}
	after := phaseAfter(t, db, p1)
	if !after.Valid || after.Int64 != 1 {
		t.Fatalf("run_checkboxes_after = %v, want 1 — the count at exit", after)
	}

	// Later writers move the live count AND the doc; the closed interval must not budge.
	mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
	mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=2 WHERE id=?`, p1)
	if got := phaseAfter(t, db, p1); !got.Valid || got.Int64 != 1 {
		t.Errorf("run_checkboxes_after = %v after a later tick, want a frozen 1", got)
	}
}

// TestStamp_CountsTheDocNotTheLaggingColumn is the race the stamp used to lose.
// checkboxes_done is owned by wsingest, which rescans on a 500 ms debounce and is
// triggered by nothing at run end; when the executor's LAST tick lands inside that
// window the column still holds the pre-tick count at the instant the run exits.
// Stamping the column then closes the interval at that stale value — and
// phasediag.OutcomeFromRow prefers the stamped edge over the live count forever, so
// a phase whose work actually landed is chipped 'no progress' permanently.
//
// The stamp must count the DOC, the artifact the executor actually wrote.
func TestStamp_CountsTheDocNotTheLaggingColumn(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)
	// The run ticks every criterion in the doc; the debounced rescan has not fired,
	// so checkboxes_done is still 0 when the process exits.
	r := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var (
		live   int
		total  int
		state  string
		before sql.NullInt64
		after  sql.NullInt64
	)
	if err := db.QueryRow(`SELECT run_state, checkboxes_total, checkboxes_done,
		run_checkboxes_before, run_checkboxes_after FROM epic_phases WHERE id=?`, p1).
		Scan(&state, &total, &live, &before, &after); err != nil {
		t.Fatalf("phase row: %v", err)
	}
	if live != 0 {
		t.Fatalf("setup: checkboxes_done = %d, want the stale 0 this test is about", live)
	}
	if !after.Valid || after.Int64 != 2 {
		t.Errorf("run_checkboxes_after = %v, want 2 — the ticked count in the doc, not the lagging column", after)
	}
	// The permanent damage the stale edge causes, asserted through the single
	// derivation both the chip and the modal go through.
	if got := phasediag.OutcomeFromRow(state, total, live, before, after); got != phasediag.OutcomeCompleted {
		t.Errorf("derived outcome = %q, want %q — the run ticked every criterion",
			got, phasediag.OutcomeCompleted)
	}
}

// TestStamp_UnreadableDocFallsBackToTheLiveColumn: the doc can be gone by the time
// a run exits (a workspace move, a plan rescan mid-run). The interval must still be
// closed — an open right edge would leave the outcome reading the live count forever
// — so the stamp degrades to checkboxes_done rather than skipping the write.
func TestStamp_UnreadableDocFallsBackToTheLiveColumn(t *testing.T) {
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)
	r := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		if err := os.Remove(doc); err != nil {
			t.Errorf("remove doc: %v", err)
		}
		mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=2 WHERE id=?`, p1)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := phaseAfter(t, db, p1); !got.Valid || got.Int64 != 2 {
		t.Errorf("run_checkboxes_after = %v, want the live count 2 — the interval must still close", got)
	}
}

// TestRunTeardown_WorktreeRemovedBeforeSlotRelease: the single-flight slot is the
// LAST thing the teardown releases. stamp() has already opened the DB gate, so a
// slot released before the git shell-out lets a re-Start re-acquire the same
// deterministic worktree path — which this defer would then delete underneath it.
func TestRunTeardown_WorktreeRemovedBeforeSlotRelease(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	var (
		s          *Service
		slotHeld   bool
		hookCalled bool
	)
	wt.onRemove = func() {
		hookCalled = true
		slotHeld = s.Slots.IsActive(s.slotKey(p1))
	}
	s = newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hookCalled {
		t.Fatal("worktree was never removed")
	}
	if !slotHeld {
		t.Error("single-flight slot was already released when the worktree was removed — " +
			"a concurrent re-Start could reuse and then lose that worktree")
	}
	if s.Slots.IsActive(s.slotKey(p1)) {
		t.Error("slot still held after the run finished")
	}
}

// TestStamp_RowVanished: an UPDATE that matches nothing is the exact shape the
// delete+reinsert defect took — it must be loud, not silent, and never panic.
func TestStamp_RowVanished(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	mustExec(t, db, `DELETE FROM epic_phases WHERE id=?`, p1)

	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	s.stamp(p1, "", "done", "") // must not panic
	if !strings.Contains(buf.String(), "row vanished mid-run") {
		t.Errorf("stamp against a deleted row logged %q, want a 'row vanished mid-run' error", buf.String())
	}
}

// TestStart_StampsRunEndedAt: every terminal transition records an end
// timestamp so a run's duration is derivable (only run_started_at was persisted
// before).
func TestStart_StampsRunEndedAt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		runFn func(spec RunSpec) (*Run, error)
		want  string
	}{
		{"completed", nil, "done"},
		{"failed", func(spec RunSpec) (*Run, error) {
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 3, Stderr: "boom"}, nil
		}, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, p1, _ := fixture(t)
			s := newTestService(db, &stubRunner{runFn: tc.runFn}, &stubWt{})
			if _, err := s.Start(p1, ""); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if state, _, _, _ := phaseRow(t, db, p1); state != tc.want {
				t.Fatalf("run_state = %q, want %q", state, tc.want)
			}
			_, _, startStamp, _ := phaseRow(t, db, p1)
			_, endedAt := phaseOutcome(t, db, p1)
			if !endedAt.Valid || endedAt.String != testTime(1) {
				t.Errorf("run_ended_at = %q (valid=%v), want %q on the terminal transition",
					endedAt.String, endedAt.Valid, testTime(1))
			}
			if endedAt.String < startStamp.String {
				t.Errorf("run_ended_at %q < run_started_at %q", endedAt.String, startStamp.String)
			}
		})
	}
}

// TestStart_ClearsPriorEndedAt: a re-run must not carry the previous run's end
// stamp while it is in flight.
func TestStart_ClearsPriorEndedAt(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{block: make(chan struct{})}
	s := NewService(db, r, &stubWt{}) // real goroutine — run stays in flight
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	s.UUID = func() string { return "uuid-1" }
	mustExec(t, db, `UPDATE epic_phases SET run_state='failed', run_ended_at='2026-01-01T00:00:00Z' WHERE id=?`, p1)

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "running"
	})
	if _, endedAt := phaseOutcome(t, db, p1); endedAt.Valid {
		t.Errorf("run_ended_at = %q while running, want NULL", endedAt.String)
	}
	close(r.block)
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "done"
	})
}

func TestStart_NonzeroExit_Failed(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 3, Stderr: "boom"}, nil
	}}
	wt := &stubWt{}
	s := newTestService(db, r, wt)

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "failed" {
		t.Errorf("run_state = %q, want failed", state)
	}
	if !strings.Contains(runErr.String, "boom") {
		t.Errorf("run_error = %q, want the stderr tail", runErr.String)
	}
	if wt.removedCount() != 1 {
		t.Error("worktree not removed on failure")
	}
}

func TestStart_Timeout_Failed(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{runFn: func(spec RunSpec) (*Run, error) {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: -1, TimedOut: true}, nil
	}}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "failed" || runErr.String != "timeout" {
		t.Errorf("state=%q run_error=%q, want failed/timeout", state, runErr.String)
	}
}

func TestStart_RunnerStartError_Failed(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{startErr: errors.New("fork: claude not found")}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start (admission) should succeed; spawn failure is stamped: %v", err)
	}
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "failed" || !strings.Contains(runErr.String, "claude not found") {
		t.Errorf("state=%q run_error=%q", state, runErr.String)
	}
}

func TestStart_DepsGate(t *testing.T) {
	t.Run("unmet dep blocks", func(t *testing.T) {
		db, _, _, p2 := fixture(t)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		_, err := s.Start(p2, "")
		if !errors.Is(err, ErrDepsUnmet) {
			t.Fatalf("err = %v, want ErrDepsUnmet", err)
		}
		var de *DepsUnmetError
		if !errors.As(err, &de) || len(de.Unmet) != 1 || de.Unmet[0] != 1 {
			t.Errorf("unmet = %+v, want [1]", de)
		}
	})

	// The live incident: a headless run that exits 0 without ticking anything
	// (failed precondition, refused work) is NOT a completed phase. run_state
	// answers "how did the process end", never "did work land" — and treating
	// 'done' as completion let phases start on top of an empty dependency.
	t.Run("run_state done with zero ticks does not satisfy", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases
			SET run_state='done', checkboxes_total=7, checkboxes_done=0 WHERE id=?`, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		_, err := s.Start(p2, "")
		if !errors.Is(err, ErrDepsUnmet) {
			t.Fatalf("err = %v, want ErrDepsUnmet (a 0/7 'done' run is not a completed phase)", err)
		}
		var de *DepsUnmetError
		if !errors.As(err, &de) || len(de.Unmet) != 1 || de.Unmet[0] != 1 {
			t.Errorf("unmet = %+v, want [1]", de)
		}
	})

	// The verification half of the same rule. A dependency that ASKED to be graded
	// and carries no verdict has not proven anything: the verifier may never have
	// started (the store holds a run that died on `fork/exec …/claude: no such file
	// or directory`), and a phase built on top of that inherits the uncertainty.
	t.Run("fully ticked but unverified dep does not satisfy", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases
			SET checkboxes_done=2, verify_mode='normal', verify_verdict=NULL WHERE id=?`, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		_, err := s.Start(p2, "")
		if !errors.Is(err, ErrDepsUnmet) {
			t.Fatalf("err = %v, want ErrDepsUnmet (a dep that asked to be graded and was not is unverified)", err)
		}
	})

	t.Run("fully ticked but inconclusive dep does not satisfy", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases
			SET checkboxes_done=2, verify_mode='strict', verify_verdict='inconclusive' WHERE id=?`, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); !errors.Is(err, ErrDepsUnmet) {
			t.Fatalf("err = %v, want ErrDepsUnmet", err)
		}
	})

	// …and the gate must stay off every plan that never asked for verification,
	// or this change stalls the fleet instead of protecting it.
	t.Run("verify off means the gate is invisible", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases
			SET checkboxes_done=2, verify_mode='off', verify_verdict=NULL WHERE id=?`, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); err != nil {
			t.Fatalf("Start: %v — a phase that never asked to be graded must not be gated", err)
		}
	})

	// A graded PASS satisfies, and a graded FAIL still satisfies: decision D5 keeps
	// a failing grade as completed work with a blocker beside it, and this gate is
	// about the ABSENCE of a grade.
	t.Run("graded dep satisfies, pass or fail", func(t *testing.T) {
		for _, verdict := range []string{"pass", "fail"} {
			db, _, p1, p2 := fixture(t)
			mustExec(t, db, `UPDATE epic_phases
				SET checkboxes_done=2, verify_mode='normal', verify_verdict=? WHERE id=?`, verdict, p1)
			s := newTestService(db, &stubRunner{}, &stubWt{})
			if _, err := s.Start(p2, ""); err != nil {
				t.Fatalf("verdict=%s: Start: %v", verdict, err)
			}
		}
	})

	t.Run("met via full checkboxes", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=2 WHERE id=?`, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); err != nil {
			t.Fatalf("Start: %v", err)
		}
	})

	// Guards the other direction of the gate: a FAILED run_state must not BLOCK a
	// dependency whose criteria are fully ticked. (This does not by itself prove
	// the new criteria-only predicate — the previous predicate accepted
	// total>0 && done==total too; the "done with zero ticks" case above is what
	// pins that.)
	t.Run("failed run_state does not block a fully ticked dep", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases
			SET run_state='failed', checkboxes_total=7, checkboxes_done=7 WHERE id=?`, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); err != nil {
			t.Fatalf("Start: %v", err)
		}
	})

	t.Run("zero checkboxes is not complete", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases SET checkboxes_total=0, checkboxes_done=0 WHERE id=?`, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); !errors.Is(err, ErrDepsUnmet) {
			t.Fatalf("err = %v, want ErrDepsUnmet (0/0 checkboxes must not satisfy)", err)
		}
	})

	t.Run("met via legacy done board task", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
			source, board_column) VALUES (1,'bt','p','done','2026-07-27T00:00:00Z','queue','done')`)
		if err != nil {
			t.Fatal(err)
		}
		btID, _ := res.LastInsertId()
		mustExec(t, db, `UPDATE epic_phases SET activated_board_task_id=? WHERE id=?`, btID, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); err != nil {
			t.Fatalf("Start: %v", err)
		}
	})

	t.Run("met via legacy archived board task", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
			source, board_column, archived_at) VALUES (1,'bt','p','done','2026-07-27T00:00:00Z','queue','todo','2026-07-27T01:00:00Z')`)
		if err != nil {
			t.Fatal(err)
		}
		btID, _ := res.LastInsertId()
		mustExec(t, db, `UPDATE epic_phases SET activated_board_task_id=? WHERE id=?`, btID, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); err != nil {
			t.Fatalf("Start: %v", err)
		}
	})

	t.Run("legacy board task not done blocks", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
			source, board_column) VALUES (1,'bt','p','running','2026-07-27T00:00:00Z','queue','in_progress')`)
		if err != nil {
			t.Fatal(err)
		}
		btID, _ := res.LastInsertId()
		mustExec(t, db, `UPDATE epic_phases SET activated_board_task_id=? WHERE id=?`, btID, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); !errors.Is(err, ErrDepsUnmet) {
			t.Fatalf("err = %v, want ErrDepsUnmet", err)
		}
	})

	t.Run("dep seq with no sibling row blocks", func(t *testing.T) {
		db, _, _, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases SET depends_on='[7]' WHERE id=?`, p2)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if _, err := s.Start(p2, ""); !errors.Is(err, ErrDepsUnmet) {
			t.Fatalf("err = %v, want ErrDepsUnmet", err)
		}
	})
}

func TestStart_DoubleStart_ErrRunning(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{block: make(chan struct{})}
	s := NewService(db, r, &stubWt{}) // real goroutine — run stays in flight
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	s.UUID = func() string { return "uuid-1" }

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "running"
	})
	if _, err := s.Start(p1, ""); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Start err = %v, want ErrRunning", err)
	}
	close(r.block)
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "done"
	})
}

func TestStart_RunningRowWithoutSlot_ErrRunning(t *testing.T) {
	// A row stuck 'running' (e.g. concurrent daemon) blocks even with an empty
	// in-memory map.
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running' WHERE id=?`, p1)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.Start(p1, ""); !errors.Is(err, ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning", err)
	}
}

func TestStart_UnknownPhase(t *testing.T) {
	db, _, _, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.Start(9999, ""); !errors.Is(err, ErrPhaseNotFound) {
		t.Fatalf("err = %v, want ErrPhaseNotFound", err)
	}
}

func TestStart_NoDoc(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET doc_path='/nope/missing.md' WHERE id=?`, p1)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.Start(p1, ""); !errors.Is(err, ErrNoDoc) {
		t.Fatalf("err = %v, want ErrNoDoc", err)
	}
}

func TestStart_NoProjectPath(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path='' WHERE id=1`)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.Start(p1, ""); !errors.Is(err, ErrNoPath) {
		t.Fatalf("err = %v, want ErrNoPath", err)
	}
}

func TestStart_AcquireFailure_StampsFailed(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{acquireErr: errors.New("branch busy")}
	s := newTestService(db, &stubRunner{}, wt)
	if _, err := s.Start(p1, ""); err == nil || !strings.Contains(err.Error(), "branch busy") {
		t.Fatalf("err = %v, want the acquire error surfaced", err)
	}
	// Slot released — a retry is admitted.
	wt.acquireErr = nil
	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("retry after acquire failure: %v", err)
	}
}

// TestStart_ResetsPriorCheckboxesAfter: opening the measurement interval must reset
// BOTH edges. A left-over run_checkboxes_after from the previous run makes the
// diagnosis of a RUNNING phase quote a right edge that belongs to a different run;
// stamp() heals it at exit, but a daemon that dies mid-run freezes the mismatch.
func TestStart_ResetsPriorCheckboxesAfter(t *testing.T) {
	db, _, p1, _ := fixture(t)
	// A completed previous run left both edges stamped.
	mustExec(t, db, `UPDATE epic_phases
		SET run_state='done', checkboxes_total=8, checkboxes_done=5,
		    run_checkboxes_before=1, run_checkboxes_after=5 WHERE id=?`, p1)

	r := &stubRunner{block: make(chan struct{})}
	s := NewService(db, r, &stubWt{}) // real goroutine — the run stays in flight
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	s.UUID = func() string { return "uuid-1" }

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "running"
	})
	if after := phaseAfter(t, db, p1); after.Valid {
		t.Errorf("run_checkboxes_after = %d while running, want NULL — the prior run's right edge must not survive into this one",
			after.Int64)
	}
	// The new baseline is this run's own left edge.
	if before, _ := phaseOutcome(t, db, p1); !before.Valid || before.Int64 != 5 {
		t.Errorf("run_checkboxes_before = %v, want 5 (the count at this spawn)", before)
	}

	close(r.block)
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "done"
	})
}

// TestStart_DBFailure_WorktreeRemovedBeforeSlotRelease: the admission UPDATE is the
// write that closes the DB gate, so when it FAILS both gates are open at once. The
// teardown must therefore keep the same order as runAndHandle's defer — worktree
// first, slot last — or a concurrent Start warm-reuses the deterministic phase-<id>
// path this line is about to delete.
func TestStart_DBFailure_WorktreeRemovedBeforeSlotRelease(t *testing.T) {
	db, _, p1, _ := fixture(t)
	// A BEFORE UPDATE trigger fails the run_state='running' write while every
	// admission SELECT still succeeds — exactly the path under test.
	mustExec(t, db, `CREATE TRIGGER phaserun_block_update BEFORE UPDATE ON epic_phases
		BEGIN SELECT RAISE(ABORT, 'update blocked'); END`)

	wt := &stubWt{}
	var (
		s          *Service
		slotHeld   bool
		hookCalled bool
	)
	wt.onRemove = func() {
		hookCalled = true
		slotHeld = s.Slots.IsActive(s.slotKey(p1))
	}
	s = newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(p1, ""); err == nil {
		t.Fatal("Start = nil, want the failed run_state UPDATE surfaced")
	}
	if !hookCalled {
		t.Fatal("the acquired worktree was never removed after the failed UPDATE")
	}
	if !slotHeld {
		t.Error("single-flight slot was already released when the worktree was removed — " +
			"a concurrent Start could warm-reuse the path this teardown then deletes")
	}
	if s.Slots.IsActive(s.slotKey(p1)) {
		t.Error("slot still held after the failed admission")
	}
}

// ── retry: leftover branch reclamation ──

// TestStart_ReclaimsEmptyLeftoverBranch: every run's teardown removes the worktree
// with keepBranch=true, so swarm/phase-<id> outlives the run and the NEXT Acquire
// would hit ErrBranchBusy. An empty leftover is reclaimed automatically, before
// Acquire, making "Retry run" work.
func TestStart_ReclaimsEmptyLeftoverBranch(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{} // reclaimAhead=0 ⇒ the leftover was empty and got deleted
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := "swarm/phase-" + itoa64(p1)
	if got := wt.reclaimedList(); len(got) != 1 || got[0] != want {
		t.Fatalf("reclaimed = %v, want exactly [%s]", got, want)
	}
	// Reclaim is a precondition of acquisition, not a replacement for it.
	if wt.acquiredCount() != 1 {
		t.Errorf("acquired = %v, want the run to proceed to Acquire after reclaim", wt.acquired)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "done" {
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

// TestStart_BranchDirty_NamesTheBase: worktree.ReclaimEmptyBranch counts commits
// against the repo's CURRENT checkout (matching Acquire's start point), so the same
// branch is "3 commits ahead" of dev and "0 ahead" of a feature branch that already
// contains them. The 409 has to say which one it measured, or the user cannot tell a
// real conflict from base skew.
func TestStart_BranchDirty_NamesTheBase(t *testing.T) {
	db, _, p1, _ := fixture(t)
	git := &stubGit{head: "dev"}
	s := newTestService(db, &stubRunner{}, &stubWt{reclaimAhead: 3})
	s.Git = git

	var bde *BranchDirtyError
	if _, err := s.Start(p1, ""); !errors.As(err, &bde) {
		t.Fatalf("err = %v, want a *BranchDirtyError", err)
	}
	if bde.Base != "dev" {
		t.Errorf("Base = %q, want the checked-out branch dev", bde.Base)
	}
	// A detached HEAD (or any git failure) names nothing rather than guessing.
	s2 := newTestService(db, &stubRunner{}, &stubWt{reclaimAhead: 3})
	s2.Git = &stubGit{err: errors.New("detached HEAD")}
	if _, err := s2.Start(p1, ""); !errors.As(err, &bde) {
		t.Fatalf("err = %v, want a *BranchDirtyError", err)
	}
	if bde.Base != "" {
		t.Errorf("Base = %q, want empty when git cannot name a branch", bde.Base)
	}
}

// TestStart_BranchDirty_RefusesAndReleasesSlot: a leftover branch holding commits is
// never destroyed to make room. Start refuses with a typed error naming the branch and
// the commit count (the api's 409 body / the UI's delete-or-merge prompt), and the
// single-flight slot must be released so the user's follow-up attempt is admitted.
func TestStart_BranchDirty_RefusesAndReleasesSlot(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{reclaimAhead: 3}
	s := newTestService(db, &stubRunner{}, wt)

	_, err := s.Start(p1, "")
	if !errors.Is(err, ErrBranchDirty) {
		t.Fatalf("err = %v, want ErrBranchDirty", err)
	}
	var bde *BranchDirtyError
	if !errors.As(err, &bde) {
		t.Fatalf("err = %v, want a *BranchDirtyError", err)
	}
	if bde.Branch != "swarm/phase-"+itoa64(p1) {
		t.Errorf("Branch = %q, want swarm/phase-%d", bde.Branch, p1)
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
	if len(wt.deleted) != 0 {
		t.Errorf("deleted = %v, want the dirty branch left alone", wt.deleted)
	}
	if wt.acquiredCount() != 0 {
		t.Errorf("acquired = %v, want no acquisition after a dirty-branch refusal", wt.acquired)
	}
	if s.Slots.IsActive(s.slotKey(p1)) {
		t.Error("single-flight slot still held after a dirty-branch refusal")
	}
	// The refusal is not sticky: once the branch is resolved, a retry is admitted
	// (not rejected with ErrRunning by a leaked slot).
	wt.mu.Lock()
	wt.reclaimAhead = 0
	wt.mu.Unlock()
	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("retry after the branch was resolved: %v", err)
	}
}

func TestStart_ReclaimError_ReleasesSlot(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{reclaimErr: errors.New("could not lock ref")}
	s := newTestService(db, &stubRunner{}, wt)

	if _, err := s.Start(p1, ""); err == nil || !strings.Contains(err.Error(), "could not lock ref") {
		t.Fatalf("err = %v, want the reclaim failure surfaced", err)
	}
	if wt.acquiredCount() != 0 {
		t.Error("Acquire ran despite a failed reclaim probe")
	}
	wt.mu.Lock()
	wt.reclaimErr = nil
	wt.mu.Unlock()
	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("retry after a reclaim failure: %v", err)
	}
}

// ── explicit branch deletion ──

func TestDeleteRunBranch(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)
	// The branch has to be on the row: DeleteRunBranch follows the stamp (0043) and
	// refuses to re-derive one from the row id. See TestDeleteRunBranchUsesStampedBranch.
	want := "swarm/phase-" + itoa64(p1)
	mustExec(t, db, `UPDATE epic_phases SET run_state='done', run_branch=? WHERE id=?`, want, p1)

	branch, existed, err := s.DeleteRunBranch(p1)
	if err != nil {
		t.Fatalf("DeleteRunBranch: %v", err)
	}
	if branch != want {
		t.Errorf("branch = %q, want %q", branch, want)
	}
	if !existed {
		t.Error("existed = false for a branch the boundary reported deleted")
	}
	if len(wt.deleted) != 1 || wt.deleted[0] != want {
		t.Errorf("deleted = %v, want [%s]", wt.deleted, want)
	}
}

// A branch that was never there must not be reported as deleted — worktree.DeleteBranch
// is idempotent, so "deleted: true" on a no-op is a claim the UI turns into a cleared
// dirty-branch banner over a branch that is still (or was never) there.
func TestDeleteRunBranch_MissingBranchReportsNotExisted(t *testing.T) {
	db, _, p1, _ := fixture(t)
	// The stamp (0043) is what makes DeleteRunBranch attempt a delete at all — an
	// unstamped phase refuses with ErrNoRunBranch instead (covered by
	// TestDeleteRunBranchWithoutStampRefuses). Here the branch IS recorded and is
	// simply gone from git, which is the idempotent path under test.
	stamped := "swarm/phase-" + itoa64(p1)
	mustExec(t, db, `UPDATE epic_phases SET run_branch=? WHERE id=?`, stamped, p1)
	wt := &stubWt{branchMissing: true} // worktree.DeleteBranch: (false, nil)
	s := newTestService(db, &stubRunner{}, wt)

	branch, existed, err := s.DeleteRunBranch(p1)
	if err != nil {
		t.Fatalf("DeleteRunBranch on a missing branch = %v, want nil (idempotent)", err)
	}
	if branch != stamped {
		t.Errorf("branch = %q, want %q", branch, stamped)
	}
	if existed {
		t.Error("existed = true for a branch that was never there")
	}
}

// TestDeleteRunBranch_ErrRunning: deleting the branch out from under a live run
// would strand its commits — refuse while the phase holds a single-flight slot.
func TestDeleteRunBranch_ErrRunning(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{block: make(chan struct{})}
	wt := &stubWt{}
	s := NewService(db, r, wt) // real goroutine — run stays in flight
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	s.UUID = func() string { return "uuid-1" }

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "running"
	})
	if _, _, err := s.DeleteRunBranch(p1); !errors.Is(err, ErrRunning) {
		t.Fatalf("err = %v, want ErrRunning while a run is in flight", err)
	}
	if len(wt.deleted) != 0 {
		t.Errorf("deleted = %v, want no deletion during a live run", wt.deleted)
	}

	close(r.block)
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "done"
	})
	// Once the run is over the deletion is allowed.
	if _, _, err := s.DeleteRunBranch(p1); err != nil {
		t.Fatalf("DeleteRunBranch after the run finished: %v", err)
	}
}

func TestDeleteRunBranch_UnknownPhase(t *testing.T) {
	db, _, _, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, _, err := s.DeleteRunBranch(9999); !errors.Is(err, ErrPhaseNotFound) {
		t.Fatalf("err = %v, want ErrPhaseNotFound", err)
	}
}

func TestDeleteRunBranch_NoProjectPath(t *testing.T) {
	db, _, p1, _ := fixture(t)
	mustExec(t, db, `UPDATE projects SET path='' WHERE id=1`)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, _, err := s.DeleteRunBranch(p1); !errors.Is(err, ErrNoPath) {
		t.Fatalf("err = %v, want ErrNoPath", err)
	}
}

func TestCancel(t *testing.T) {
	db, _, p1, _ := fixture(t)
	r := &stubRunner{block: make(chan struct{})}
	wt := &stubWt{}
	s := NewService(db, r, wt) // real goroutine
	s.RepoRoot = func(p string, _ ...string) (string, error) { return p, nil }
	s.UUID = func() string { return "uuid-1" }

	if _, err := s.Start(p1, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "running"
	})
	if !s.Cancel(p1) {
		t.Fatal("Cancel = false, want true for an in-flight run")
	}
	waitFor(t, func() bool {
		state, _, _, _ := phaseRow(t, db, p1)
		return state == "failed"
	})
	_, _, _, runErr := phaseRow(t, db, p1)
	if runErr.String != "cancelled" {
		t.Errorf("run_error = %q, want cancelled", runErr.String)
	}
	waitFor(t, func() bool { return wt.removedCount() == 1 })

	if s.Cancel(p1) {
		t.Error("Cancel with nothing running = true, want false")
	}
}

func TestHealStale(t *testing.T) {
	db, _, p1, p2 := fixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_state='running' WHERE id=?`, p1)
	mustExec(t, db, `UPDATE epic_phases SET run_state='done' WHERE id=?`, p2)
	s := newTestService(db, &stubRunner{}, &stubWt{})

	if err := s.HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	state, _, _, runErr := phaseRow(t, db, p1)
	if state != "failed" || runErr.String != "daemon restart" {
		t.Errorf("healed row: state=%q run_error=%q", state, runErr.String)
	}
	// A crash-orphaned row is a terminal transition too — it gets an end stamp.
	if _, endedAt := phaseOutcome(t, db, p1); !endedAt.Valid || endedAt.String == "" {
		t.Errorf("healed row run_ended_at = %q (valid=%v), want a timestamp", endedAt.String, endedAt.Valid)
	}
	state, _, _, _ = phaseRow(t, db, p2)
	if state != "done" {
		t.Errorf("done row touched by heal: state=%q", state)
	}
	if _, endedAt := phaseOutcome(t, db, p2); endedAt.Valid {
		t.Errorf("done row run_ended_at = %q, want untouched by heal", endedAt.String)
	}
}

// TestHealStale_ClosesCheckboxInterval: admission resets run_checkboxes_after to
// NULL, so a crash-healed run would otherwise end with before=X, after=NULL for
// ever — a half-open interval. phasediag.OutcomeFromRow falls back to the LIVE
// count for a NULL right edge, which keeps drifting with every later writer of
// checkboxes_done, so the healed run's outcome would change under it. HealStale is
// a terminal transition like stamp(), and closes the interval exactly the same way.
func TestHealStale_ClosesCheckboxInterval(t *testing.T) {
	db, _, p1, p2 := fixture(t)
	mustExec(t, db, `UPDATE epic_phases
		   SET run_state='running', checkboxes_total=8, checkboxes_done=5,
		       run_checkboxes_before=2, run_checkboxes_after=NULL WHERE id=?`, p1)
	// An idle row with an open interval must not be touched — heal only owns rows
	// left 'running'.
	mustExec(t, db, `UPDATE epic_phases
		   SET run_state='idle', checkboxes_done=4, run_checkboxes_after=NULL WHERE id=?`, p2)
	s := newTestService(db, &stubRunner{}, &stubWt{})

	if err := s.HealStale(); err != nil {
		t.Fatalf("HealStale: %v", err)
	}
	after := phaseAfter(t, db, p1)
	if !after.Valid || after.Int64 != 5 {
		t.Fatalf("run_checkboxes_after = %v, want 5 — the count as of the heal", after)
	}
	// Pinned, not tracking: a later tick must not move the healed run's right edge.
	mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=8 WHERE id=?`, p1)
	if got := phaseAfter(t, db, p1); !got.Valid || got.Int64 != 5 {
		t.Errorf("run_checkboxes_after = %v after a later tick, want a frozen 5", got)
	}
	if got := phaseAfter(t, db, p2); got.Valid {
		t.Errorf("non-running row run_checkboxes_after = %v, want untouched NULL", got)
	}
}

// newUUID now lives in internal/runcore (one copy for all five engines); its
// test moved with it to internal/runcore/spawner_test.go.

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func (w *stubWt) lastAcquireRoot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.acquireRoots) == 0 {
		return ""
	}
	return w.acquireRoots[len(w.acquireRoots)-1]
}
