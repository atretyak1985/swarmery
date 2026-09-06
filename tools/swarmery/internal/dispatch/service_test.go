package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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

// spec returns the i-th recorded RunSpec, for assertions on what the service
// handed the runner (model/agent field mapping).
func (s *stubRunner) spec(i int) RunSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.specs[i]
}

// stubWt is a scripted WorktreeManager: Acquire returns a deterministic path +
// swarm/<id> branch and records calls; Remove records calls. acquireErr forces
// a failure.
type stubWt struct {
	mu         sync.Mutex
	acquired   []string // task ids acquired
	removed    []string // task ids (via branch) removed
	acquireErr error
	commits    map[string][]string // external id → trailer-bearing commit SHAs
	commitsErr error               // when set, the progress signal is UNREADABLE
	// root overrides the fake "/wt" prefix with a REAL directory, for the tests
	// that need the worktree to exist on disk (lending the plan doc into it).
	// Left empty everywhere else so the cheap fake path stays the default.
	root string
}

func (w *stubWt) Acquire(repoRoot, projectSlug, taskID string) (worktree.Acquired, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.acquireErr != nil {
		return worktree.Acquired{}, w.acquireErr
	}
	w.acquired = append(w.acquired, taskID)
	base := w.root
	if base == "" {
		base = "/wt"
	}
	return worktree.Acquired{
		Path:       filepath.Join(base, projectSlug, taskID),
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

// Branch reclamation is not part of the dispatch flow (dispatch tasks own their
// branch for their whole lifetime) — inert here.
func (w *stubWt) ReclaimEmptyBranch(repoRoot, branch string) (int, error) { return 0, nil }
func (w *stubWt) DeleteBranch(repoRoot, branch string) (bool, error)      { return false, nil }

// commits is the scripted progress signal: SHAs per task external id. commitsErr,
// when set, makes CommitsForTask fail — the case that must NOT be read as zero
// progress.
func (w *stubWt) CommitsForTask(repoRoot, taskID string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.commitsErr != nil {
		return nil, w.commitsErr
	}
	return w.commits[taskID], nil
}

func (w *stubWt) acquiredCount() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.acquired) }

// acquiredIDs is a copy of the acquired task ids, for assertions that count
// per-id rather than in total.
func (w *stubWt) acquiredIDs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.acquired...)
}
func (w *stubWt) removedCount() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.removed) }

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
	agent      string // registry agent name; "" leaves tasks.agent NULL
	// origin: "" leaves the schema default ('manual', migration 0048). Set
	// "session" to model a card internal/taskcap captured from a session.
	origin string
	// worktreePath: "" leaves tasks.worktree_path NULL — i.e. a row the
	// dispatcher does NOT own. Set it to model an admitted dispatcher run
	// (admit() writes it in the same UPDATE as board_column='in_progress').
	worktreePath string
	// prompt: "" keeps the shared default ("do <extID>"). Set it to model a card
	// whose body matters to the assertion (a verify-fix card's inherited prompt).
	prompt string
	// originQuote / originFiles: the 0066 provenance columns. "" / nil leave them
	// NULL, i.e. a card the dispatcher appends no provenance block to.
	originQuote string
	originFiles []string
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
	// Set as a follow-up so the shared INSERT above stays exactly as every
	// pre-agent test knows it — an unset agent leaves the column NULL.
	if o.agent != "" {
		if _, err := db.Exec(`UPDATE tasks SET agent=? WHERE id=?`, o.agent, id); err != nil {
			t.Fatalf("set agent on %s: %v", extID, err)
		}
	}
	if o.origin != "" {
		if _, err := db.Exec(`UPDATE tasks SET origin=? WHERE id=?`, o.origin, id); err != nil {
			t.Fatalf("set origin on %s: %v", extID, err)
		}
	}
	if o.worktreePath != "" {
		if _, err := db.Exec(`UPDATE tasks SET worktree_path=? WHERE id=?`, o.worktreePath, id); err != nil {
			t.Fatalf("set worktree_path on %s: %v", extID, err)
		}
	}
	if o.prompt != "" {
		if _, err := db.Exec(`UPDATE tasks SET prompt=? WHERE id=?`, o.prompt, id); err != nil {
			t.Fatalf("set prompt on %s: %v", extID, err)
		}
	}
	if o.originQuote != "" || o.originFiles != nil {
		files, err := json.Marshal(o.originFiles)
		if err != nil {
			t.Fatalf("encode origin_files for %s: %v", extID, err)
		}
		if _, err := db.Exec(`UPDATE tasks SET origin_quote=?, origin_files=? WHERE id=?`,
			o.originQuote, string(files), id); err != nil {
			t.Fatalf("set provenance on %s: %v", extID, err)
		}
	}
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

// TestAdmitPersistsStartPoint pins the base verification is allowed to measure
// against. admit() is the ONLY place that knows which SHA the worktree was
// pinned to (Acquired.StartPoint); if it does not write that down, every later
// consumer has to guess, and the verifier's guess used to be the task's own
// branch — a diff of the branch against itself, forever empty.
func TestAdmitPersistsStartPoint(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{} // Acquire returns StartPoint "deadbeef"
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "Done.")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, wt)
	id := insertTask(t, db, "T-sp1", taskOpts{})

	s.Schedule()

	sp := taskField(t, db, id, "start_point")
	if !sp.Valid || sp.String != "deadbeef" {
		t.Fatalf("start_point = %v (valid=%v), want the acquired StartPoint %q",
			sp.String, sp.Valid, "deadbeef")
	}
	// The same UPDATE still carries branch + worktree_path — start_point rides
	// along on the guarded CAS, it does not replace anything.
	if b := taskField(t, db, id, "branch"); b.String != "swarm/T-sp1" {
		t.Errorf("branch = %q, want swarm/T-sp1", b.String)
	}
}

// A task that never gets admitted must not carry a start_point: the column
// states "this worktree was pinned here", and a row that lost the admission
// race owns no worktree.
func TestAdmitFailureLeavesStartPointNull(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{acquireErr: errAcquire}
	s := newTestService(t, db, &stubRunner{}, wt)
	id := insertTask(t, db, "T-sp2", taskOpts{})

	s.Schedule()

	if sp := taskField(t, db, id, "start_point"); sp.Valid {
		t.Fatalf("start_point = %q on a task that was never admitted, want NULL", sp.String)
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

// The service's whole job for agent selection is field mapping: tasks.agent →
// RunSpec.Agent, with the prompt left ALONE. Asserting the absence of the
// mention in spec.Prompt is what pins "exactly one code path" from this side —
// ClaudeRunner.agentPrompt applies it, and a service that also did would
// double it.
func TestDispatchThreadsAgentIntoRunSpec(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	insertTask(t, db, "T-ag", taskOpts{agent: "tech-lead"})

	s.Schedule()

	if r.count() != 1 {
		t.Fatalf("runner started %d times, want 1", r.count())
	}
	spec := r.spec(0)
	if spec.Agent != "tech-lead" {
		t.Errorf("RunSpec.Agent = %q, want tech-lead", spec.Agent)
	}
	if strings.Contains(spec.Prompt, "@tech-lead") {
		t.Errorf("service must not prefix the prompt (that is ClaudeRunner's single site); got %q", spec.Prompt)
	}
}

// Regression: a card with no agent selected dispatches exactly as it did before
// the feature — an empty Agent and an unmentioned prompt.
func TestDispatchWithoutAgentLeavesRunSpecAgentEmpty(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newTestService(t, db, r, &stubWt{})
	insertTask(t, db, "T-noag", taskOpts{})

	s.Schedule()

	if r.count() != 1 {
		t.Fatalf("runner started %d times, want 1", r.count())
	}
	spec := r.spec(0)
	if spec.Agent != "" {
		t.Errorf("RunSpec.Agent = %q, want empty for a task with no agent", spec.Agent)
	}
	if strings.Contains(spec.Prompt, "@") {
		t.Errorf("prompt should carry no mention: %q", spec.Prompt)
	}
}

// Exit routing is untouched by agent selection: an agent-prefixed run that
// lands with no sentinel still routes to in_review and still keeps its
// worktree, exactly like TestScheduleAdmitsAndRunsExit0.
func TestAgentRunWithoutSentinelStillRoutesToInReview(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{}
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "Done, committed with the trailer.")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, wt)
	id := insertTask(t, db, "T-ag-review", taskOpts{agent: "tech-lead"})

	s.Schedule()

	if got := r.spec(0).Agent; got != "tech-lead" {
		t.Fatalf("precondition: RunSpec.Agent = %q, want tech-lead", got)
	}
	if got := column(t, db, id); got != "in_review" {
		t.Errorf("task column = %q, want in_review", got)
	}
	if wt.removedCount() != 0 {
		t.Errorf("worktree removed %d times on clean exit; want 0 (kept for review)", wt.removedCount())
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
	// A dispatcher-owned run: admit() always leaves a worktree_path behind, which
	// is what marks the row as the dispatcher's to reclaim.
	id := insertTask(t, db, "T-stuck", taskOpts{column: "in_progress", worktreePath: "/wt/T-stuck"})

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

// ── HealDeadProcess: evidence-driven reclaim (phase 1, close-the-run-loops) ──

// deadDispatchTask stamps a running task with a dispatch session whose proc_state
// is `procState`, mirroring what procwatch writes once it has observed the process.
func deadDispatchTask(t *testing.T, db *sql.DB, extID, source, procState string) int64 {
	t.Helper()
	uuid := "u-" + extID
	if _, err := db.Exec(
		`INSERT INTO sessions(project_id, session_uuid, status, started_at, proc_state)
		 VALUES(1, ?, 'completed', '2026-07-24T00:00:00Z', NULLIF(?, ''))`, uuid, procState); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	res, err := db.Exec(`
		INSERT INTO tasks(project_id, title, prompt, status, created_at, source, external_id,
		                  board_column, dispatch_session_uuid)
		VALUES(1, ?, 'do it', 'running', '2026-07-01T00:00:00Z', ?, ?, 'triage', ?)`,
		"t-"+extID, source, extID, uuid)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestHealDeadProcessRequeuesQueueTaskOnEvidence(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	// 'triage' on purpose: this is where every stuck task in the live database sits,
	// and HealStale's in_progress predicate never sees it.
	id := deadDispatchTask(t, db, "T-dead", "queue", "dead")

	if err := s.HealDeadProcess(); err != nil {
		t.Fatal(err)
	}
	if got := column(t, db, id); got != "todo" {
		t.Errorf("board_column = %q, want todo", got)
	}
	if e := taskField(t, db, id, "dispatch_error"); e.String != "dispatch process gone (procwatch: dead)" {
		t.Errorf("dispatch_error = %q, want the procwatch reason", e.String)
	}
	if st := taskField(t, db, id, "status"); st.String != "queued" {
		t.Errorf("status = %q, want queued", st.String)
	}
}

// The regression this test exists for: a workspace row's status is a projection of
// the workspace artifacts (internal/wsingest upserts DO UPDATE SET status), so a
// write here is reverted on the next scan and reads as success in the data.
func TestHealDeadProcessNeverTouchesWorkspaceTask(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	id := deadDispatchTask(t, db, "T-ws", "workspace", "dead")

	if err := s.HealDeadProcess(); err != nil {
		t.Fatal(err)
	}
	if got := column(t, db, id); got != "triage" {
		t.Errorf("board_column = %q, want it untouched at triage — the daemon may not own a workspace row's state", got)
	}
	if st := taskField(t, db, id, "status"); st.String != "running" {
		t.Errorf("status = %q, want it untouched at running", st.String)
	}
	if e := taskField(t, db, id, "dispatch_error"); e.Valid && e.String != "" {
		t.Errorf("dispatch_error = %q, want untouched", e.String)
	}
}

// Absence of a liveness signal is absence of evidence — never proof of death. Fusion's
// first stuck-task detector ignored this and killed everything older than ~30 minutes.
func TestHealDeadProcessIgnoresMissingAndLiveEvidence(t *testing.T) {
	for _, procState := range []string{"", "running", "orphaned", "unknown"} {
		t.Run("proc_state="+procState, func(t *testing.T) {
			db := testDB(t)
			s := newTestService(t, db, &stubRunner{}, &stubWt{})
			id := deadDispatchTask(t, db, "T-"+procState, "queue", procState)

			if err := s.HealDeadProcess(); err != nil {
				t.Fatal(err)
			}
			if got := column(t, db, id); got != "triage" {
				t.Errorf("board_column = %q, want untouched — %q is not evidence of death", got, procState)
			}
		})
	}
}

func TestHealDeadProcessLeavesNonRunningAlone(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	id := deadDispatchTask(t, db, "T-done", "queue", "dead")
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	if err := s.HealDeadProcess(); err != nil {
		t.Fatal(err)
	}
	if st := taskField(t, db, id, "status"); st.String != "done" {
		t.Errorf("status = %q, want done — a finished task is not stuck", st.String)
	}
}

// ── done-sentinel verification (phase 2, close-the-run-loops) ──

// probeVerifier records tasks.worktree_path AS IT WAS at the moment Poke fired.
// The assertion needs that snapshot, not the final row: finishDone nulls the column
// immediately after, so reading it later cannot distinguish the correct order from
// the broken one.
type probeVerifier struct {
	db      *sql.DB
	poked   []int64
	wtAtPop []sql.NullString
}

func (p *probeVerifier) Poke(id int64) {
	var wt sql.NullString
	_ = p.db.QueryRow(`SELECT worktree_path FROM tasks WHERE id=?`, id).Scan(&wt)
	p.poked = append(p.poked, id)
	p.wtAtPop = append(p.wtAtPop, wt)
}

// TestDoneSentinelPokesVerifyBeforeWorktreeCleared pins the ORDER, not just the
// call. Swap pokeVerify and finishDone in service.go and this test goes red: the
// probe sees an already-nulled worktree_path and verification would have had
// nothing to memoize on.
func TestDoneSentinelPokesVerifyBeforeWorktreeCleared(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		// PREMISE STALE is a doneSentinels entry (prompt.go): the exact reply all
		// five real dispatched runs ended with.
		ingestSession(t, db, spec.SessionUUID, "PREMISE STALE: already on HEAD")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	pv := &probeVerifier{db: db}
	s.Verifier = pv
	id := insertTask(t, db, "T-premise", taskOpts{})

	s.Schedule()

	if len(pv.poked) != 1 || pv.poked[0] != id {
		t.Fatalf("poked = %v, want exactly [%d] — a done sentinel must still be graded", pv.poked, id)
	}
	if !pv.wtAtPop[0].Valid || pv.wtAtPop[0].String == "" {
		t.Fatal("worktree_path was already cleared when verification was poked: " +
			"pokeVerify must run BEFORE finishDone, which nulls it")
	}
	// And the task still lands done — grading is added, not substituted.
	if column(t, db, id) != "done" {
		t.Errorf("board_column = %q, want done", column(t, db, id))
	}
}

// A blocked sentinel is not a claim about finished work: finishBlocked parks the
// task for a human and deliberately keeps the worktree. Nothing to grade.
func TestBlockedSentinelDoesNotPokeVerify(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "BLOCKED: needs a decision")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	pv := &probeVerifier{db: db}
	s.Verifier = pv
	insertTask(t, db, "T-blocked", taskOpts{})

	s.Schedule()

	if len(pv.poked) != 0 {
		t.Errorf("poked = %v, want none — a blocked task produced no work to grade", pv.poked)
	}
}

// ── progress high-water + terminal park (phase 3, close-the-run-loops) ──

// healWithProgress runs one dead-process heal pass with a scripted progress signal
// and returns the row's post-state.
func healWithProgress(t *testing.T, commits []string, commitsErr error, retryCount, highWater, bound int) (
	column, status, dispatchErr string, paused int64, gotRetry, gotHighWater int) {
	t.Helper()
	db := testDB(t)
	wt := &stubWt{commits: map[string][]string{"T-p": commits}, commitsErr: commitsErr}
	s := newTestService(t, db, &stubRunner{}, wt)
	s.Cfg.MaxNoProgressRetries = bound
	id := deadDispatchTask(t, db, "T-p", "queue", "dead")
	if _, err := db.Exec(`UPDATE tasks SET retry_count=?, progress_high_water=? WHERE id=?`,
		retryCount, highWater, id); err != nil {
		t.Fatal(err)
	}

	if err := s.HealDeadProcess(); err != nil {
		t.Fatalf("HealDeadProcess: %v", err)
	}
	if err := db.QueryRow(
		`SELECT board_column, status, COALESCE(dispatch_error,''), paused, retry_count, progress_high_water
		   FROM tasks WHERE id=?`, id).
		Scan(&column, &status, &dispatchErr, &paused, &gotRetry, &gotHighWater); err != nil {
		t.Fatal(err)
	}
	return
}

func TestHealDeadProcessAdvancedProgressRequeuesWithoutChargingRetry(t *testing.T) {
	col, st, _, paused, retry, hw := healWithProgress(t,
		[]string{"sha1", "sha2", "sha3"}, nil, 0 /*retry*/, 1 /*highWater*/, 3 /*bound*/)

	if col != "todo" || st != "queued" {
		t.Errorf("column/status = %q/%q, want todo/queued", col, st)
	}
	if paused != 0 {
		t.Error("paused = 1; a task that advanced must not be parked")
	}
	if retry != 0 {
		t.Errorf("retry_count = %d, want 0 — progress is not a retry", retry)
	}
	if hw != 3 {
		t.Errorf("progress_high_water = %d, want 3", hw)
	}
}

func TestHealDeadProcessNoProgressChargesRetryBelowBound(t *testing.T) {
	col, st, _, paused, retry, hw := healWithProgress(t,
		[]string{"sha1"}, nil, 0 /*retry*/, 1 /*highWater — already seen*/, 3)

	if col != "todo" || st != "queued" {
		t.Errorf("column/status = %q/%q, want todo/queued while under the bound", col, st)
	}
	if paused != 0 {
		t.Error("paused = 1 below the bound; the task still has budget")
	}
	if retry != 1 {
		t.Errorf("retry_count = %d, want 1", retry)
	}
	if hw != 1 {
		t.Errorf("progress_high_water = %d, want it unchanged at 1", hw)
	}
}

func TestHealDeadProcessParksAtBoundInsteadOfRequeueing(t *testing.T) {
	col, _, dispatchErr, paused, _, _ := healWithProgress(t,
		[]string{"sha1"}, nil, 2 /*retry — one short of the bound*/, 1, 3)

	if paused != 1 {
		t.Fatalf("paused = %d, want 1 — the bound must stop the cycle, not extend it", paused)
	}
	if col == "todo" {
		t.Error("board_column = todo: a parked task must NOT be requeued as well")
	}
	if !contains(dispatchErr, "no progress after 3") {
		t.Errorf("dispatch_error = %q, want it to name the bound", dispatchErr)
	}
}

// A squash or branch reset lowers the observable count. The mark must not follow it
// down, or the next pass reads the drop as fresh progress and the bound never binds.
func TestHealDeadProcessHighWaterNeverDecreases(t *testing.T) {
	_, _, _, _, _, hw := healWithProgress(t,
		[]string{"sha1"}, nil, 0, 7 /*highWater from before a squash*/, 3)

	if hw != 7 {
		t.Errorf("progress_high_water = %d, want it held at 7 — MAX(), never assignment", hw)
	}
}

// An unreadable repo is not evidence. Spending the retry budget on it would charge a
// task that may be progressing fine for git's failure.
func TestHealDeadProcessUnreadableProgressChangesNothing(t *testing.T) {
	col, st, dispatchErr, paused, retry, hw := healWithProgress(t,
		nil, errors.New("fatal: not a git repository"), 1, 2, 3)

	if col != "triage" || st != "running" {
		t.Errorf("column/status = %q/%q, want them untouched at triage/running", col, st)
	}
	if paused != 0 || retry != 1 || hw != 2 {
		t.Errorf("paused=%d retry=%d hw=%d, want 0/1/2 — nothing may change on an unreadable signal",
			paused, retry, hw)
	}
	if dispatchErr != "" {
		t.Errorf("dispatch_error = %q, want empty", dispatchErr)
	}
}

func TestHealDeadProcessBoundZeroDisablesParking(t *testing.T) {
	col, st, _, paused, retry, _ := healWithProgress(t,
		[]string{"sha1"}, nil, 99 /*far past any bound*/, 1, 0 /*bound disabled*/)

	if paused != 0 {
		t.Error("paused = 1 with the bound disabled; 0 must mean pre-0045 behaviour")
	}
	if col != "todo" || st != "queued" {
		t.Errorf("column/status = %q/%q, want todo/queued", col, st)
	}
	if retry != 100 {
		t.Errorf("retry_count = %d, want 100 — retries are still counted, just not enforced", retry)
	}
}

// ── dependency gate reads the verdict (phase 4, close-the-run-loops) ──

// depFixture inserts a dependency task in `column` with `verdict`, then a dependent
// task that declares it, and returns the dependent's id plus the service.
func depFixture(t *testing.T, column, verdict string) (*Service, *sql.DB, int64) {
	t.Helper()
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	if _, err := db.Exec(`
		INSERT INTO tasks(project_id, title, prompt, status, created_at, source, external_id,
		                  board_column, verify_verdict)
		VALUES(1, 'dep', 'x', 'done', '2026-07-01T00:00:00Z', 'queue', 'T-dep', ?, NULLIF(?, ''))`,
		column, verdict); err != nil {
		t.Fatal(err)
	}
	id := insertTask(t, db, "T-child", taskOpts{deps: `["T-dep"]`})
	return s, db, id
}

func TestDepGateBlocksOnExplicitFailOnly(t *testing.T) {
	for _, tc := range []struct {
		verdict   string
		wantBlock bool
		why       string
	}{
		{"fail", true, "an explicit failure is the one verdict that blocks"},
		{"", false, "NULL must NOT block: verify_verdict is NULL for 100% of live tasks, " +
			"and a gate that blocked on it would make the board impassable"},
		{"pass", false, "a passing dependency obviously proceeds"},
		{"inconclusive", false, "nothing gradeable is not a failure — same invariant as phasediag"},
	} {
		t.Run("verdict="+tc.verdict, func(t *testing.T) {
			s, _, _ := depFixture(t, "done", tc.verdict)
			blocker, err := s.depBlocker([]string{"T-dep"})
			if err != nil {
				t.Fatal(err)
			}
			if (blocker != nil) != tc.wantBlock {
				t.Fatalf("blocker = %v, want blocked=%v — %s", blocker, tc.wantBlock, tc.why)
			}
			if tc.wantBlock && blocker.Reason != "verification failed" {
				t.Errorf("Reason = %q, want 'verification failed'", blocker.Reason)
			}
		})
	}
}

func TestDepGateBlocksUnknownDependency(t *testing.T) {
	s, _, _ := depFixture(t, "done", "pass")
	blocker, err := s.depBlocker([]string{"T-nope"})
	if err != nil {
		t.Fatal(err)
	}
	if blocker == nil {
		t.Fatal("a dangling dependency must block — it cannot be proven satisfied")
	}
	if blocker.Reason != "not found" {
		t.Errorf("Reason = %q, want 'not found'", blocker.Reason)
	}
}

func TestDepGateBlocksOnColumnAndNamesIt(t *testing.T) {
	s, _, _ := depFixture(t, "todo", "")
	blocker, err := s.depBlocker([]string{"T-dep"})
	if err != nil {
		t.Fatal(err)
	}
	if blocker == nil {
		t.Fatal("a dependency still in todo must block")
	}
	if !contains(blocker.Reason, "column=todo") {
		t.Errorf("Reason = %q, want it to name the column", blocker.Reason)
	}
}

func TestDepGateArchivedDependencyPasses(t *testing.T) {
	s, _, _ := depFixture(t, "archived", "")
	blocker, err := s.depBlocker([]string{"T-dep"})
	if err != nil {
		t.Fatal(err)
	}
	if blocker != nil {
		t.Errorf("blocker = %v, want nil — archived counts as resolved", blocker)
	}
}

func TestDepGateRecordsReasonOnTheTask(t *testing.T) {
	s, db, id := depFixture(t, "done", "fail")
	s.Schedule()

	e := taskField(t, db, id, "dispatch_error")
	if !contains(e.String, "verification failed") || !contains(e.String, "T-dep") {
		t.Errorf("dispatch_error = %q, want it to name both the dependency and the reason", e.String)
	}
	if column(t, db, id) != "todo" {
		t.Errorf("board_column = %q, want the card to stay in todo", column(t, db, id))
	}
}

// A real failure carries information the dep gate does not have. Every scheduling
// pass re-evaluates the same blocked card, so without the prefix guard the gate would
// overwrite a runner error with "waiting on a dependency" within seconds.
func TestDepGateNeverOverwritesAForeignError(t *testing.T) {
	s, db, id := depFixture(t, "done", "fail")
	if _, err := db.Exec(`UPDATE tasks SET dispatch_error='runner start: exec format error' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	s.Schedule()

	if e := taskField(t, db, id, "dispatch_error"); e.String != "runner start: exec format error" {
		t.Errorf("dispatch_error = %q, want the original runner error preserved", e.String)
	}
}

func TestDepGateEmptyDepsPass(t *testing.T) {
	db := testDB(t)
	s := newTestService(t, db, &stubRunner{}, &stubWt{})
	for _, deps := range [][]string{nil, {}, {""}, {"  "}} {
		blocker, err := s.depBlocker(deps)
		if err != nil {
			t.Fatal(err)
		}
		if blocker != nil {
			t.Errorf("deps %v → %v, want nil", deps, blocker)
		}
	}
}

// TestScheduleWorktreeSingleFlight is the guard for the ONE resource two board
// rows can silently share: the worktree.
//
// worktree.Manager keys both the checkout path and the branch on the external
// id (worktree.go:158-159), and Acquire warm-REUSES a path whose branch already
// matches (invariant 4, worktree.go:198-201) rather than refusing it — by
// design, so a crashed run can be resumed. The dispatcher's own same-task gate
// (isActive) keys on the task ID instead, so it cannot see two rows converging
// on one directory.
//
// Two rows share an external id in exactly one shipped flow: verify's fix task
// carries external_id=<root external id> so its failure charges the root
// (verify/service.go createFixTask). In practice the file-scope gate hides this
// — a fix task inherits the root's scope verbatim and identical scopes always
// overlap — but that is an INCIDENTAL guard: it holds only while the two rows
// agree on file_scope, and file_scope is operator-patchable
// (PATCH /api/board/tasks/{id}). Disjoint scopes here reproduce exactly that,
// and without a worktree-identity gate both rows admit and two headless agents
// get the same checkout on the same branch.
func TestScheduleWorktreeSingleFlight(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(t, db, r, wt)

	// Same external id (⇒ same worktree + branch), deliberately DISJOINT scopes
	// so the file-scope gate cannot be what saves us.
	insertTask(t, db, "T-root1", taskOpts{fileScope: `["a/"]`})
	insertTask(t, db, "T-root1", taskOpts{fileScope: `["b/"]`, priority: 3})

	s.Schedule()

	var n int
	for _, id := range wt.acquiredIDs() {
		if id == "T-root1" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Acquire(T-root1) called %d time(s), want 1 — two board rows sharing an "+
			"external id must never hold the same worktree/branch concurrently", n)
	}

	// The gate DEFERS, it does not drop: the run above finished inline and left
	// in_review, so the checkout is free and the loser admits on the next pass.
	// Without this half, a gate that silently stranded the second row forever
	// would pass the assertion above.
	s.Schedule()
	n = 0
	for _, id := range wt.acquiredIDs() {
		if id == "T-root1" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("Acquire(T-root1) called %d time(s) across two passes, want 2 — the "+
			"deferred row must be admitted once the checkout is free", n)
	}
}

// ── dispatched-prompt assembly (board redesign v2 phase 3) ──

// TestBuildDispatchPromptGolden pins, byte for byte, the text a card is
// dispatched with, over the three shapes a queue card comes in: hand-written,
// captured from a session (0066 provenance columns set), and minted by the
// verifier as a fix for a failed run.
//
// It goes through candidates() rather than hand-building the struct so the
// golden covers the whole read path — origin_quote/origin_files → provenance
// block → assembled body — and not just the concatenation at the end.
//
// Two invariants are load-bearing and neither is visible from a single-case
// test. (1) The user's own prompt is FIRST and verbatim: nothing is prepended,
// nothing is paraphrased, so what a person typed is what the model reads first.
// (2) A card with no provenance dispatches with a prompt identical to the
// pre-0066 one — the provenance work must be invisible to every card that has
// none, which is most of them.
func TestBuildDispatchPromptGolden(t *testing.T) {
	db := testDB(t)

	const sessionPrompt = "Extract the retry helper into internal/retry"
	const sessionQuote = "Refactor the retry helper and add tests for the backoff."
	// A fix card inherits the root's prompt with the verifier's reasons appended
	// (verify.createFixTask) and carries no provenance columns of its own.
	const fixPrompt = "Extract the retry helper into internal/retry\n\n## Verification failed\ntypecheck: 2 errors"

	insertTask(t, db, "T-man", taskOpts{prompt: "Add a --json flag to the status command"})
	insertTask(t, db, "T-cap", taskOpts{
		origin:      "session",
		prompt:      sessionPrompt,
		originQuote: sessionQuote,
		originFiles: []string{"internal/retry/retry.go", "internal/retry/retry_test.go"},
	})
	insertTask(t, db, "T-fix", taskOpts{origin: "verify-fix", prompt: fixPrompt})

	cands, err := newTestService(t, db, &stubRunner{}, &stubWt{}).candidates()
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	got := map[string]string{}
	for _, c := range cands {
		got[c.ExternalID] = buildDispatchPrompt(c)
	}

	want := map[string]string{
		// Manual: the prompt and nothing else. The card's title ("t-T-man") does
		// not appear — see buildDispatchPrompt's doc comment.
		"T-man": "Add a --json flag to the status command",
		// Captured: prompt first, then the provenance block. The quote is the
		// session's opening ask; the files are what that session touched.
		"T-cap": sessionPrompt + "\n\n" +
			"--- PROVENANCE (captured card) ---\n" +
			"The session this card was captured from was asked:\n" +
			sessionQuote + "\n" +
			"Files that session touched: internal/retry/retry.go, internal/retry/retry_test.go\n" +
			"--- END PROVENANCE ---",
		// verify-fix: the inherited prompt plus the verifier's reasons, no block
		// — createFixTask copies neither origin_quote nor origin_files from the
		// root, so a fix card has no provenance to render.
		"T-fix": fixPrompt,
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("buildDispatchPrompt(%s) mismatch:\n got %q\nwant %q", id, got[id], w)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("candidates() returned %d cards, want %d", len(got), len(want))
	}

	// The regression the plan names outright: a card with no source dispatches
	// with its prompt untouched. Stated separately from the table so a golden
	// updated in haste cannot quietly relax it.
	for _, id := range []string{"T-man", "T-fix"} {
		if strings.Contains(got[id], "PROVENANCE") {
			t.Errorf("%s (no origin_* columns) grew a provenance block:\n%s", id, got[id])
		}
	}
	if !strings.HasPrefix(got["T-cap"], sessionPrompt) {
		t.Errorf("captured card must open with the user's own prompt:\n%s", got["T-cap"])
	}
}
