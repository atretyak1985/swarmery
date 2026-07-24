package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// errAcquire is a canned worktree-acquisition failure for the admission-failure test.
var errAcquire = errors.New("stub acquire failure")

// ── test doubles ──

// stubRunner records the specs it was asked to run and returns a canned Run.
// runFn, when set, computes the Run per spec (e.g. to vary exit code); it also
// runs any sideEffect (e.g. ingest a session + turn) before returning so exit
// handling sees a linked transcript.
type stubRunner struct {
	mu    sync.Mutex
	specs []RunSpec
	run   func(spec RunSpec) (*Run, error)
}

func (s *stubRunner) Start(_ context.Context, spec RunSpec) (*Run, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	fn := s.run
	s.mu.Unlock()
	if fn != nil {
		return fn(spec)
	}
	return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
}

func (s *stubRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.specs)
}

// stubWt is a scripted WorktreeManager: Acquire returns a deterministic path +
// swarm/<id> branch and records calls; Remove records calls. acquireErr forces
// a failure.
type stubWt struct {
	mu          sync.Mutex
	acquired    []string // task ids acquired
	removed     []string // task ids (via branch) removed
	acquireErr  error
}

func (w *stubWt) Acquire(repoRoot, projectSlug, taskID string) (worktree.Acquired, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.acquireErr != nil {
		return worktree.Acquired{}, w.acquireErr
	}
	w.acquired = append(w.acquired, taskID)
	return worktree.Acquired{
		Path:       filepath.Join("/wt", projectSlug, taskID),
		Branch:     "swarm/" + taskID,
		StartPoint: "deadbeef",
	}, nil
}

func (w *stubWt) Remove(repoRoot string, a worktree.Acquired, keepBranch bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.removed = append(w.removed, a.Branch)
	return nil
}

func (w *stubWt) acquiredCount() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.acquired) }
func (w *stubWt) removedCount() int  { w.mu.Lock(); defer w.mu.Unlock(); return len(w.removed) }

// ── harness ──

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "dispatch.db"))
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

// newTestService builds a Service whose async spawn runs INLINE (Go seam) and
// whose UUID is deterministic per call, so tests are fully synchronous.
func newTestService(t *testing.T, db *sql.DB, r Runner, wt WorktreeManager) *Service {
	t.Helper()
	s := NewService(db, Config{
		MaxConcurrent: 2, MaxWorktrees: 4,
		PollInterval: time.Hour, RunTimeout: time.Minute, Enabled: true,
	}, r, wt)
	// Inline spawn: run the goroutine body synchronously so a Schedule() call
	// completes the whole run+exit before returning (deterministic assertions).
	s.Go = func(fn func()) { fn() }
	var n int
	s.UUID = func() string { n++; return "uuid-" + itoa(n) }
	return s
}

// insertTask inserts a queue board task and returns its integer id. opts mutate
// the row after insert (column, scope, deps, pause, project).
type taskOpts struct {
	column     string
	priority   int
	fileScope  string // JSON
	deps       string // JSON
	paused     int
	userPaused int
	createdAt  string
	projectID  int64
}

func insertTask(t *testing.T, db *sql.DB, extID string, o taskOpts) int64 {
	t.Helper()
	if o.column == "" {
		o.column = "todo"
	}
	if o.priority == 0 {
		o.priority = 5
	}
	if o.fileScope == "" {
		o.fileScope = "[]"
	}
	if o.deps == "" {
		o.deps = "[]"
	}
	if o.createdAt == "" {
		o.createdAt = "2026-07-24T00:00:00.000Z"
	}
	if o.projectID == 0 {
		o.projectID = 1
	}
	res, err := db.Exec(`
		INSERT INTO tasks(project_id, title, prompt, priority, status, created_at,
		                  source, external_id, board_column, file_scope, dependencies,
		                  paused, user_paused)
		VALUES(?, ?, ?, ?, 'queued', ?, 'queue', ?, ?, ?, ?, ?, ?)`,
		o.projectID, "t-"+extID, "do "+extID, o.priority, o.createdAt,
		extID, o.column, o.fileScope, o.deps, o.paused, o.userPaused)
	if err != nil {
		t.Fatalf("insert task %s: %v", extID, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func column(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var c string
	if err := db.QueryRow(`SELECT board_column FROM tasks WHERE id=?`, id).Scan(&c); err != nil {
		t.Fatalf("read column %d: %v", id, err)
	}
	return c
}

func taskField(t *testing.T, db *sql.DB, id int64, col string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(`SELECT `+col+` FROM tasks WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatalf("read %s of %d: %v", col, id, err)
	}
	return v
}

// ingestSession simulates the ingest pipeline landing a dispatched session + a
// final assistant turn with the given text, so exit-time sentinel parsing +
// linking find a transcript.
func ingestSession(t *testing.T, db *sql.DB, uuid, assistantText string) {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO sessions(project_id, session_uuid, status, started_at) VALUES(1,?, 'completed','2026-07-24T00:00:00Z')`, uuid)
	if err != nil {
		t.Fatalf("ingest session: %v", err)
	}
	sid, _ := res.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO turns(session_id, seq, role, started_at, text) VALUES(?,1,'assistant','2026-07-24T00:00:01Z',?)`,
		sid, assistantText); err != nil {
		t.Fatalf("ingest turn: %v", err)
	}
}

// ── tests ──

func TestScheduleAdmitsAndRunsExit0(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{}
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		// A plain completion with no sentinel → in_review.
		ingestSession(t, db, spec.SessionUUID, "Done, committed with the trailer.")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, wt)
	id := insertTask(t, db, "T-aaa", taskOpts{})

	s.Schedule()

	if r.count() != 1 {
		t.Fatalf("runner started %d times, want 1", r.count())
	}
	if got := column(t, db, id); got != "in_review" {
		t.Errorf("task column = %q, want in_review", got)
	}
	// Explicit link: task_sessions row with link_source='explicit'.
	var linkSrc string
	if err := db.QueryRow(
		`SELECT link_source FROM task_sessions WHERE task_id=?`, id).Scan(&linkSrc); err != nil {
		t.Fatalf("no task_sessions link: %v", err)
	}
	if linkSrc != "explicit" {
		t.Errorf("link_source = %q, want explicit", linkSrc)
	}
	// dispatch_session_uuid parked; worktree kept (not removed) for review.
	if u := taskField(t, db, id, "dispatch_session_uuid"); !u.Valid || u.String == "" {
		t.Error("dispatch_session_uuid should be recorded")
	}
	if wt.removedCount() != 0 {
		t.Errorf("worktree removed %d times on clean exit; want 0 (kept for review)", wt.removedCount())
	}
}

func TestScheduleNonzeroExitSurfacesError(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 2, Stderr: "boom"}, nil
	}}
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	s.Run = r
	id := insertTask(t, db, "T-err", taskOpts{})

	s.Schedule()

	if got := column(t, db, id); got != "in_review" {
		t.Errorf("column = %q, want in_review (error surfaced, still reviewable)", got)
	}
	if e := taskField(t, db, id, "dispatch_error"); !e.Valid || e.String == "" {
		t.Error("dispatch_error should be set on nonzero exit")
	}
}

func TestScheduleTimeoutSurfaced(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: -1, TimedOut: true}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	id := insertTask(t, db, "T-to", taskOpts{})

	s.Schedule()

	if got := column(t, db, id); got != "in_review" {
		t.Errorf("column = %q, want in_review", got)
	}
	if e := taskField(t, db, id, "dispatch_error"); !e.Valid || e.String == "" {
		t.Error("timeout should surface a dispatch_error")
	}
}

func TestSentinelDoneMovesToDoneAndRemovesWorktree(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{}
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "PREMISE STALE: already on HEAD")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, wt)
	id := insertTask(t, db, "T-stale", taskOpts{})

	s.Schedule()

	if got := column(t, db, id); got != "done" {
		t.Errorf("column = %q, want done", got)
	}
	if note := taskField(t, db, id, "result_note"); note.String != "PREMISE STALE: already on HEAD" {
		t.Errorf("result_note = %q, want the sentinel line", note.String)
	}
	if wt.removedCount() != 1 {
		t.Errorf("worktree removed %d times, want 1 (done ⇒ reclaim)", wt.removedCount())
	}
	if wp := taskField(t, db, id, "worktree_path"); wp.Valid {
		t.Error("worktree_path should be cleared on done")
	}
}

func TestSentinelBlockedRoutesToTodoPaused(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{}
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "BLOCKED: needs an out-of-scope migration")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, wt)
	id := insertTask(t, db, "T-blk", taskOpts{})

	s.Schedule()

	if got := column(t, db, id); got != "todo" {
		t.Errorf("column = %q, want todo", got)
	}
	if p := taskField(t, db, id, "paused"); p.String != "1" {
		t.Errorf("paused = %q, want 1", p.String)
	}
	if e := taskField(t, db, id, "dispatch_error"); e.String != "BLOCKED: needs an out-of-scope migration" {
		t.Errorf("dispatch_error = %q, want the BLOCKED line", e.String)
	}
	if wt.removedCount() != 0 {
		t.Error("blocked task should keep its worktree")
	}
}

func TestKillSwitchBlocksAllAdmission(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	s.Cfg.Enabled = false
	insertTask(t, db, "T-off", taskOpts{})

	s.Schedule()

	if r.count() != 0 {
		t.Errorf("kill-switch off: runner started %d times, want 0", r.count())
	}
}

func TestGlobalAndProjectPauseParkAdmission(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	id := insertTask(t, db, "T-p", taskOpts{})

	// Global pause parks everything.
	if err := s.SetPause("global", true); err != nil {
		t.Fatal(err)
	}
	s.Schedule()
	if r.count() != 0 {
		t.Fatalf("global pause: runner started %d, want 0", r.count())
	}

	// Lift global, set project pause → still parked.
	if err := s.SetPause("global", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPause(ProjectScope(1), true); err != nil {
		t.Fatal(err)
	}
	s.Schedule()
	if r.count() != 0 {
		t.Fatalf("project pause: runner started %d, want 0", r.count())
	}

	// Lift project pause → admits.
	if err := s.SetPause(ProjectScope(1), false); err != nil {
		t.Fatal(err)
	}
	s.Schedule()
	if r.count() != 1 {
		t.Errorf("after lifting pause: runner started %d, want 1", r.count())
	}
	_ = id
}

// TestLockedDownPresetBlocksAdmission: a locked-down permission preset (fusion
// phase 11) parks the project's Todo tasks with the documented dispatch_error
// and never spawns a run; lifting the preset admits.
func TestLockedDownPresetBlocksAdmission(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	id := insertTask(t, db, "T-lock", taskOpts{})

	// Lock the project down.
	if _, err := db.Exec(
		`INSERT INTO project_permission_presets(project_id, preset, updated_at)
		 VALUES(1, 'locked-down', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	s.Schedule()
	if r.count() != 0 {
		t.Fatalf("locked-down: runner started %d, want 0 (dispatch must be blocked)", r.count())
	}
	if col := column(t, db, id); col != "todo" {
		t.Fatalf("locked-down task column = %q, want todo (never admitted)", col)
	}
	if e := taskField(t, db, id, "dispatch_error"); !e.Valid || e.String != "project locked down" {
		t.Fatalf("dispatch_error = %v, want 'project locked down'", e)
	}

	// A second pass must NOT re-stamp (idempotent — stays quiet).
	before := r.count()
	s.Schedule()
	if r.count() != before {
		t.Fatalf("second pass spawned a run (%d)", r.count())
	}

	// Lift the lock (→ approval-required) → the task admits.
	if _, err := db.Exec(
		`UPDATE project_permission_presets SET preset='approval-required' WHERE project_id=1`); err != nil {
		t.Fatal(err)
	}
	s.Schedule()
	if r.count() != 1 {
		t.Errorf("after unlock: runner started %d, want 1", r.count())
	}
}

func TestBothTaskPauseFlagsSkip(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	insertTask(t, db, "T-sys", taskOpts{paused: 1})
	insertTask(t, db, "T-usr", taskOpts{userPaused: 1})

	s.Schedule()

	if r.count() != 0 {
		t.Errorf("paused/user_paused tasks admitted %d, want 0", r.count())
	}
}

func TestDependencyGating(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})

	// Dependency T-dep is in_progress (not done) → dependent stays todo.
	insertTask(t, db, "T-dep", taskOpts{column: "in_progress"})
	dependent := insertTask(t, db, "T-child", taskOpts{deps: `["T-dep"]`, fileScope: `["child/only"]`})

	s.Schedule()
	if column(t, db, dependent) != "todo" {
		t.Fatalf("dependent admitted while dep unfinished")
	}
	if r.count() != 0 {
		t.Fatalf("dependent ran while dep unfinished: %d", r.count())
	}

	// Move dep to done and re-schedule → dependent admits.
	if _, err := db.Exec(`UPDATE tasks SET board_column='done' WHERE external_id='T-dep'`); err != nil {
		t.Fatal(err)
	}
	s.Schedule()
	if r.count() != 1 {
		t.Errorf("dependent should admit once dep is done; runner=%d", r.count())
	}
}

func TestDanglingDependencyIsUnsatisfied(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	id := insertTask(t, db, "T-orphan", taskOpts{deps: `["T-nonexistent"]`})
	s.Schedule()
	if r.count() != 0 || column(t, db, id) != "todo" {
		t.Error("a dangling dependency must NOT unblock the task")
	}
}

func TestOverlapGateBlocksSecondSameProject(t *testing.T) {
	db := testDB(t)
	// Never-returning runs so both would-be admissions stay "active".
	blockCh := make(chan struct{})
	var started sync.WaitGroup
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		started.Done()
		<-blockCh // hold the slot
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	// Real goroutines here (not inline) so the held run doesn't block Schedule.
	s.Go = func(fn func()) { go fn() }

	// Two tasks with overlapping scope (both touch src/api).
	a := insertTask(t, db, "T-a", taskOpts{fileScope: `["src/api"]`, createdAt: "2026-07-24T00:00:00.000Z"})
	b := insertTask(t, db, "T-b", taskOpts{fileScope: `["src/api/handlers.go"]`, createdAt: "2026-07-24T00:00:01.000Z"})

	started.Add(1)
	s.Schedule()
	started.Wait() // first run is live and holding src/api

	// Second Schedule pass: b overlaps a's active scope → must NOT admit.
	s.Schedule()
	if r.count() != 1 {
		close(blockCh)
		t.Fatalf("overlapping task admitted concurrently: runner=%d", r.count())
	}
	if column(t, db, b) != "todo" {
		close(blockCh)
		t.Fatalf("overlapping task b moved off todo")
	}

	close(blockCh) // release a
	waitFor(t, func() bool { return column(t, db, a) == "in_review" })
	_ = b
}

func TestDisjointScopesRunConcurrentlyToLimit(t *testing.T) {
	db := testDB(t)
	blockCh := make(chan struct{})
	var started sync.WaitGroup
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		started.Done()
		<-blockCh
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Go = func(fn func()) { go fn() }

	// Three disjoint-scope tasks; MaxConcurrent=2 → only two run at once.
	insertTask(t, db, "T-1", taskOpts{fileScope: `["a"]`, createdAt: "2026-07-24T00:00:00.000Z"})
	insertTask(t, db, "T-2", taskOpts{fileScope: `["b"]`, createdAt: "2026-07-24T00:00:01.000Z"})
	third := insertTask(t, db, "T-3", taskOpts{fileScope: `["c"]`, createdAt: "2026-07-24T00:00:02.000Z"})

	started.Add(2)
	s.Schedule()
	started.Wait()

	if r.count() != 2 {
		close(blockCh)
		t.Fatalf("concurrent runs = %d, want 2 (MaxConcurrent)", r.count())
	}
	if column(t, db, third) != "todo" {
		close(blockCh)
		t.Fatalf("third task should wait for a free slot")
	}
	close(blockCh)
}

func TestMaxWorktreesCap(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	s.Cfg.MaxConcurrent = 10 // isolate the worktree cap
	s.Cfg.MaxWorktrees = 1

	// Pre-existing in_progress task already holds a worktree (disjoint scope so
	// overlap doesn't mask the worktree cap).
	if _, err := db.Exec(`
		UPDATE tasks SET board_column='in_progress', worktree_path='/wt/p/T-live'
		 WHERE id=?`, insertTask(t, db, "T-live", taskOpts{column: "in_progress", fileScope: `["live/x"]`})); err != nil {
		t.Fatal(err)
	}
	blocked := insertTask(t, db, "T-wait", taskOpts{fileScope: `["other/y"]`})

	s.Schedule()
	if r.count() != 0 || column(t, db, blocked) != "todo" {
		t.Errorf("worktree cap not enforced: runner=%d col=%s", r.count(), column(t, db, blocked))
	}
}

func TestPriorityOrdering(t *testing.T) {
	db := testDB(t)
	blockCh := make(chan struct{})
	var started sync.WaitGroup
	var firstSpec string
	var once sync.Once
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		once.Do(func() { firstSpec = spec.SessionUUID })
		started.Done()
		<-blockCh
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Go = func(fn func()) { go fn() }
	s.Cfg.MaxConcurrent = 1 // only the top-priority task admits first

	// low priority created first, urgent created later — urgent must win.
	insertTask(t, db, "T-low", taskOpts{priority: 7, fileScope: `["x"]`, createdAt: "2026-07-24T00:00:00.000Z"})
	urgent := insertTask(t, db, "T-urgent", taskOpts{priority: 1, fileScope: `["y"]`, createdAt: "2026-07-24T00:00:05.000Z"})

	started.Add(1)
	s.Schedule()
	started.Wait()
	close(blockCh)

	// The urgent task got uuid-1 (admitted first). Confirm it's the one that left todo.
	waitFor(t, func() bool { return column(t, db, urgent) != "todo" })
	if firstSpec != "uuid-1" {
		t.Errorf("first admitted spec = %q, want uuid-1 (urgent first)", firstSpec)
	}
}

func TestSameTaskSingleFlight(t *testing.T) {
	db := testDB(t)
	s := NewService(db, Config{MaxConcurrent: 5, MaxWorktrees: 5, RunTimeout: time.Minute, Enabled: true},
		&stubRunner{}, &stubWt{})
	s.UUID = func() string { return "uuid-x" }
	id := insertTask(t, db, "T-solo", taskOpts{})

	// Simulate the task already being active (a live run this process started).
	s.markActive(id)
	// A stub runner that would count spawns if admission slipped through.
	r := &stubRunner{}
	s.Run = r
	s.Go = func(fn func()) { fn() }

	s.Schedule()
	if r.count() != 0 {
		t.Errorf("active task re-admitted: runner=%d, want 0 (single-flight)", r.count())
	}
	// Still todo (never moved) because admission was skipped.
	if column(t, db, id) != "todo" {
		t.Errorf("column = %q, want todo (skipped, not admitted)", column(t, db, id))
	}
}

func TestHealStaleReclaimsInProgress(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	id := insertTask(t, db, "T-stuck", taskOpts{column: "in_progress"})

	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	if column(t, db, id) != "todo" {
		t.Errorf("stuck in_progress not healed to todo")
	}
	if e := taskField(t, db, id, "dispatch_error"); e.String != "daemon restart" {
		t.Errorf("dispatch_error = %q, want 'daemon restart'", e.String)
	}
}

func TestScheduleReentranceGuard(t *testing.T) {
	db := testDB(t)
	// The runner re-enters Schedule() while a pass is in flight; the guard must
	// make the nested call a no-op (no double admission / no deadlock).
	var s *Service
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		s.Schedule() // re-entrant call — must return immediately
		ingestSession(t, db, spec.SessionUUID, "done")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s = newTestService(t, db, r, &stubWt{})
	insertTask(t, db, "T-re", taskOpts{})

	s.Schedule()
	if r.count() != 1 {
		t.Errorf("re-entrance guard failed: runner=%d, want 1", r.count())
	}
}

func TestAcquireFailureLeavesTaskTodo(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{acquireErr: errAcquire}
	r := &stubRunner{}
	s := newTestService(t, db, r, wt)
	id := insertTask(t, db, "T-acqfail", taskOpts{})

	s.Schedule()
	if r.count() != 0 {
		t.Error("runner should not start when Acquire fails")
	}
	if column(t, db, id) != "todo" {
		t.Errorf("column = %q, want todo (admission failed)", column(t, db, id))
	}
	if e := taskField(t, db, id, "dispatch_error"); e.String == "" {
		t.Error("acquire failure should surface a dispatch_error")
	}
}

func TestSnapshotAndPauseState(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	if err := s.SetPause("global", true); err != nil {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !st.GlobalPaused || !st.Enabled {
		t.Errorf("snapshot: globalPaused=%v enabled=%v", st.GlobalPaused, st.Enabled)
	}
	if st.MaxConcurrent != 2 || st.FreeSlots != 2 {
		t.Errorf("snapshot slots: max=%d free=%d", st.MaxConcurrent, st.FreeSlots)
	}
	if len(st.PausedScopes) != 1 || st.PausedScopes[0] != "global" {
		t.Errorf("snapshot pausedScopes = %v", st.PausedScopes)
	}
}

func TestRemoveWorktreeForClearsPath(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{}
	s := newTestService(t, db, &stubRunner{}, wt)
	id := insertTask(t, db, "T-rm", taskOpts{column: "in_review"})
	if _, err := db.Exec(
		`UPDATE tasks SET worktree_path='/wt/p/T-rm', branch='swarm/T-rm' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	s.RemoveWorktreeFor(id)

	if wt.removedCount() != 1 {
		t.Errorf("RemoveWorktreeFor removed %d, want 1", wt.removedCount())
	}
	if wp := taskField(t, db, id, "worktree_path"); wp.Valid {
		t.Error("worktree_path should be cleared after removal")
	}
}

func TestStartSchedulerRunsAndStops(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "done")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Cfg.PollInterval = 5 * time.Millisecond
	id := insertTask(t, db, "T-tick", taskOpts{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.StartScheduler(ctx); close(done) }()

	waitFor(t, func() bool { return column(t, db, id) == "in_review" })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartScheduler did not stop on ctx cancel")
	}
}

// ── helpers ──

func TestNotifyFiresOnTransitions(t *testing.T) {
	db := testDB(t)
	var mu sync.Mutex
	var notified []int64
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "done, no sentinel")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Notify = func(id int64) { mu.Lock(); notified = append(notified, id); mu.Unlock() }
	id := insertTask(t, db, "T-note", taskOpts{})

	s.Schedule()

	mu.Lock()
	defer mu.Unlock()
	// At least the admit (in_progress) and the finishReview transition notify.
	var sawID bool
	for _, n := range notified {
		if n == id {
			sawID = true
		}
	}
	if !sawID {
		t.Errorf("Notify never fired for task %d; got %v", id, notified)
	}
}

func TestRemoveWorktreeForNoWorktreeIsNoop(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{}
	s := newTestService(t, db, &stubRunner{}, wt)
	id := insertTask(t, db, "T-nowt", taskOpts{column: "done"}) // no worktree_path

	s.RemoveWorktreeFor(id) // must not panic or call Remove

	if wt.removedCount() != 0 {
		t.Errorf("Remove called %d times for a task with no worktree", wt.removedCount())
	}
}

func TestNilNotifyAndNilWtAreSafe(t *testing.T) {
	db := testDB(t)
	// A service with no Notify and a nil worktree manager must not panic on the
	// happy path or on removeWorktree.
	s := NewService(db, Config{MaxConcurrent: 1, MaxWorktrees: 1, RunTimeout: time.Minute, Enabled: true},
		&stubRunner{}, nil)
	s.Go = func(fn func()) { fn() }
	s.UUID = func() string { return "uuid-nil" }
	s.notify(123)                     // nil Notify → no-op
	s.removeWorktree("/r", "/p", "b") // nil Wt → no-op
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}
