package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// tsFormat matches the millisecond-Z style the api package writes.
const tsFormat = "2006-01-02T15:04:05.000Z"

// WorktreeManager is the subset of *worktree.Manager the dispatcher uses. An
// interface so the service can be unit-tested with a stub (the real Manager is
// itself Git-mockable, but stubbing at this level keeps dispatch tests focused
// on scheduling logic, not git-list parsing). *worktree.Manager satisfies it.
// keepBranch is always true for dispatched runs — a task's swarm/<id> branch
// carries its Swarm-Task-Id commits, which verification (Phase 6) and the user
// need reachable after the worktree directory is reclaimed.
type WorktreeManager interface {
	Acquire(repoRoot, projectSlug, taskID string) (worktree.Acquired, error)
	Remove(repoRoot string, a worktree.Acquired, keepBranch bool) error
}

// Verifier is the auto-verification trigger seam (fusion phase 6). Declared HERE
// (in dispatch) and satisfied by *verify.Service, so `verify` can depend on
// dispatch's data deps (worktree/store) WITHOUT dispatch importing verify — no
// import cycle. Poke is non-blocking (verify spawns its own goroutine). Attached
// via the Service.Verifier field; nil ⇒ auto-verification not wired (guarded).
type Verifier interface {
	Poke(taskID int64)
}

// Service owns the dispatch loop: candidate selection, admission gates, spawn,
// exit/sentinel handling, event-driven Poke + poll fallback, and startup heal.
type Service struct {
	DB   *sql.DB
	Cfg  Config
	Run  Runner
	Wt   WorktreeManager
	UUID func() string       // session-uuid generator (test seam)
	now  func() time.Time    // clock (test seam)
	Go   func(func())        // async-spawn seam (nil ⇒ real `go`), mirrors improveGo
	Notify func(taskID int64) // emits task_updated (wired to api.publishTaskUpdated)
	// Verifier, when attached, is poked on a no-sentinel in_review landing so
	// auto-verification (fusion phase 6) grades the work while the worktree is
	// still live (before any terminal done/archived transition reclaims it via
	// RemoveWorktreeFor). nil ⇒ not attached (call is guarded); keeps dispatch
	// unit tests hermetic. The interface (Verifier, above) is declared in this
	// package and satisfied by *verify.Service, so verify imports dispatch's data
	// deps without dispatch importing verify — no import cycle.
	Verifier Verifier

	scheduling atomic.Bool // re-entrance guard: overlapping Schedule passes skip

	mu     sync.Mutex          // guards active
	active map[int64]struct{}  // task ids with a live run (MaxConcurrent + same-task single-flight)
}

// NewService builds a dispatcher. The caller wires DB, Cfg, Run (ClaudeRunner),
// Wt (worktree.Manager), and Notify; UUID/now/Go default to production impls.
func NewService(db *sql.DB, cfg Config, r Runner, wt WorktreeManager) *Service {
	return &Service{
		DB: db, Cfg: cfg, Run: r, Wt: wt,
		UUID:   newUUID,
		now:    time.Now,
		active: make(map[int64]struct{}),
	}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
func (s *Service) ts() string { return s.clock().UTC().Format(tsFormat) }

func (s *Service) spawn(fn func()) {
	wrapped := func() {
		// A panic in a dispatch goroutine must never take the daemon down —
		// recover + log (mirrors spawnImprove / spawnProvision). The task row
		// stays wherever it reached; startup heal reclaims a wedged in_progress.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("error: dispatch: goroutine panic recovered: %v", r)
			}
		}()
		fn()
	}
	if s.Go != nil {
		s.Go(wrapped)
		return
	}
	go wrapped()
}

func (s *Service) notify(id int64) {
	if s.Notify != nil {
		s.Notify(id)
	}
}

// ── active-run tracking (in-memory; durable truth is board_column) ──

func (s *Service) markActive(id int64) {
	s.mu.Lock()
	s.active[id] = struct{}{}
	s.mu.Unlock()
}

func (s *Service) clearActive(id int64) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}

func (s *Service) isActive(id int64) bool {
	s.mu.Lock()
	_, ok := s.active[id]
	s.mu.Unlock()
	return ok
}

func (s *Service) activeCount() int {
	s.mu.Lock()
	n := len(s.active)
	s.mu.Unlock()
	return n
}

// ── public API ──

// Poke requests a scheduling pass. Called from the event fast-path (task
// created, moved to todo, unpaused, run exit, pause toggle, dependency
// completion). Non-blocking: it runs Schedule inline (Schedule is itself
// re-entrance-guarded, so concurrent Pokes coalesce). The api layer calls this
// from request handlers — cheap enough to run synchronously (one indexed query
// + a bounded admission loop).
func (s *Service) Poke() {
	s.Schedule()
}

// StartScheduler launches the poll-fallback ticker and blocks until ctx is
// done. The daemon runs it in a goroutine (see cmd/swarmery). An initial
// Schedule runs immediately so a restart drains any Todo backlog without
// waiting a full interval.
func (s *Service) StartScheduler(ctx context.Context) {
	s.Schedule()
	t := time.NewTicker(s.Cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Schedule()
		}
	}
}

// Schedule runs one admission pass: pick eligible Todo tasks in priority order
// and admit as many as the gates allow. Re-entrance-guarded — an overlapping
// call (concurrent Poke + tick) returns immediately.
func (s *Service) Schedule() {
	if !s.scheduling.CompareAndSwap(false, true) {
		return // a pass is already running; it will observe our writes
	}
	defer s.scheduling.Store(false)

	// Gate 1: kill-switch / global pause.
	if !s.Cfg.Enabled || s.isPaused("global") {
		return
	}

	cands, err := s.candidates()
	if err != nil {
		log.Printf("error: dispatch: load candidates: %v", err)
		return
	}
	if len(cands) == 0 {
		return
	}

	// Active file scopes (in_progress tasks) form the overlap set. Re-read each
	// pass so admissions within the same pass are reflected for later candidates.
	activeScopes, err := s.activeScopes()
	if err != nil {
		log.Printf("error: dispatch: load active scopes: %v", err)
		return
	}
	liveWorktrees, err := s.liveWorktreeCount()
	if err != nil {
		log.Printf("error: dispatch: count worktrees: %v", err)
		return
	}

	for _, c := range cands {
		// Gate: project pause.
		if s.isPaused(ProjectScope(c.ProjectID)) {
			continue
		}
		// Gate: concurrency cap.
		if s.activeCount() >= s.Cfg.MaxConcurrent {
			break // no point scanning further this pass
		}
		// Gate: same-task single-flight (Acquire is idempotent for a live task,
		// so WE must reject a re-dispatch — DESIGN handoff).
		if s.isActive(c.ID) {
			continue
		}
		// Gate: worktree cap.
		if liveWorktrees >= s.Cfg.MaxWorktrees {
			break
		}
		// Gate: dependencies all resolved (done|archived).
		if !c.depsSatisfied {
			continue
		}
		// Gate: file-scope overlap vs every active task in the SAME project.
		if scopeConflicts(c, activeScopes) {
			continue
		}

		admitted := s.admit(c)
		if !admitted {
			continue // lost the CAS or a step failed; error already logged/surfaced
		}
		// Reflect the admission for the rest of this pass.
		activeScopes = append(activeScopes, activeScope{ProjectID: c.ProjectID, Scope: c.FileScope})
		liveWorktrees++
	}
}

// ── candidate model ──

// candidate is one eligible-or-nearly Todo task with the fields the gates need.
type candidate struct {
	ID           int64
	ExternalID   string
	ProjectID    int64
	ProjectSlug  string
	ProjectPath  string // repo root for worktree.Acquire
	Prompt       string
	Model        sql.NullString
	Priority     int
	CreatedAt    string
	FileScope    []string
	Dependencies []string
	depsSatisfied bool
}

// candidates returns Todo board tasks (source='queue', both pause flags clear)
// ordered by priority (urgent<high<normal<low ⇒ ascending int) then created_at
// then id, with per-candidate dependency satisfaction resolved in-memory.
func (s *Service) candidates() ([]candidate, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, COALESCE(t.external_id,''), t.project_id, p.slug, p.path,
		       t.prompt, t.model, t.priority, t.created_at, t.file_scope, t.dependencies
		  FROM tasks t JOIN projects p ON p.id = t.project_id
		 WHERE t.source='queue' AND t.board_column='todo'
		   AND t.paused=0 AND t.user_paused=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cands []candidate
	for rows.Next() {
		var c candidate
		var scopeJSON, depsJSON string
		if err := rows.Scan(&c.ID, &c.ExternalID, &c.ProjectID, &c.ProjectSlug,
			&c.ProjectPath, &c.Prompt, &c.Model, &c.Priority, &c.CreatedAt,
			&scopeJSON, &depsJSON); err != nil {
			return nil, err
		}
		if c.FileScope, err = decodeStringList(scopeJSON); err != nil {
			return nil, err
		}
		if c.Dependencies, err = decodeStringList(depsJSON); err != nil {
			return nil, err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Order: priority asc (urgent=1 first) → created_at asc → id asc. Done in Go
	// so the closed set is unambiguous and unit-testable.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Priority != cands[j].Priority {
			return cands[i].Priority < cands[j].Priority
		}
		if cands[i].CreatedAt != cands[j].CreatedAt {
			return cands[i].CreatedAt < cands[j].CreatedAt
		}
		return cands[i].ID < cands[j].ID
	})

	// Resolve dependency satisfaction once (a dep is satisfied iff its task is in
	// done|archived, keyed by external_id — the card id used in the trailer and
	// dependency arrays).
	for i := range cands {
		ok, err := s.depsSatisfied(cands[i].Dependencies)
		if err != nil {
			return nil, err
		}
		cands[i].depsSatisfied = ok
	}
	return cands, nil
}

// depsSatisfied reports whether every dependency external_id refers to a task
// currently in done|archived. Unknown ids are treated as UNSATISFIED (a
// dangling dependency must not silently unblock — conservative).
func (s *Service) depsSatisfied(deps []string) (bool, error) {
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		var col string
		err := s.DB.QueryRow(
			`SELECT board_column FROM tasks WHERE external_id=? LIMIT 1`, dep).Scan(&col)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if col != "done" && col != "archived" {
			return false, nil
		}
	}
	return true, nil
}

// activeScope pairs an in-progress task's project with its declared file scope.
type activeScope struct {
	ProjectID int64
	Scope     []string
}

// activeScopes returns the file scopes of all in-progress queue tasks (the
// overlap set). Read from the DB (not the in-memory active map) so it reflects
// durable state including any run this daemon did not itself start.
func (s *Service) activeScopes() ([]activeScope, error) {
	rows, err := s.DB.Query(
		`SELECT project_id, file_scope FROM tasks
		  WHERE source='queue' AND board_column='in_progress'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activeScope
	for rows.Next() {
		var a activeScope
		var scopeJSON string
		if err := rows.Scan(&a.ProjectID, &scopeJSON); err != nil {
			return nil, err
		}
		if a.Scope, err = decodeStringList(scopeJSON); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// scopeConflicts reports whether candidate c overlaps any active task in the
// SAME project. Cross-project tasks never conflict (separate repos/worktrees).
func scopeConflicts(c candidate, active []activeScope) bool {
	for _, a := range active {
		if a.ProjectID != c.ProjectID {
			continue
		}
		if pathsOverlap(c.FileScope, a.Scope) {
			return true
		}
	}
	return false
}

// liveWorktreeCount counts queue tasks holding a worktree (worktree_path set,
// still in_progress — the states that keep a worktree live before verification
// releases it on done/archived).
func (s *Service) liveWorktreeCount() (int, error) {
	var n int
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM tasks
		  WHERE source='queue' AND worktree_path IS NOT NULL
		    AND board_column='in_progress'`).Scan(&n)
	return n, err
}

// ── admission ──

// admit acquires a worktree, writes the explicit session link, moves the task
// todo→in_progress via a guarded CAS, and spawns the run goroutine. Returns
// true when the run was launched. Any failure is surfaced on the row's
// dispatch_error and the task is left in todo (no worktree leak — Acquire is
// idempotent and the next pass retries or a human intervenes).
func (s *Service) admit(c candidate) bool {
	// CAS FIRST is not possible before we have branch/worktree — but we must not
	// double-spawn. Mark active up-front (in-memory) so a concurrent pass in the
	// same process can't also pick this task; the guarded UPDATE below is the
	// durable CAS. If admission fails we clear active.
	s.markActive(c.ID)

	acq, err := s.Wt.Acquire(c.ProjectPath, c.ProjectSlug, c.ExternalID)
	if err != nil {
		s.clearActive(c.ID)
		s.failAdmission(c.ID, "worktree acquire: "+err.Error())
		return false
	}

	uuid := s.UUID()

	// Guarded CAS: only move a row that is STILL todo (a concurrent PATCH could
	// have moved/paused it since candidates() read). On 0 rows affected, back off.
	res, err := s.DB.Exec(`
		UPDATE tasks
		   SET board_column='in_progress', status='running',
		       branch=?, worktree_path=?,
		       dispatch_error=NULL, column_moved_at=?, started_at=COALESCE(started_at, ?)
		 WHERE id=? AND source='queue' AND board_column='todo'
		   AND paused=0 AND user_paused=0`,
		acq.Branch, acq.Path, s.ts(), s.ts(), c.ID)
	if err != nil {
		s.clearActive(c.ID)
		s.failAdmission(c.ID, "admit update: "+err.Error())
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Lost the race — the row changed under us. Release the in-memory slot;
		// the worktree stays (idempotent Acquire will reuse it if the task comes
		// back to todo).
		s.clearActive(c.ID)
		return false
	}

	// Record the explicit task↔session link. The sessions row does not exist yet
	// (the process creates it via ingest), and tasks.session_id/task_sessions are
	// INTEGER FKs — so the pre-generated uuid is parked on tasks.dispatch_session_uuid
	// now and reconciled into tasks.session_id + task_sessions(link_source='explicit')
	// once the transcript is ingested (see linkSession).
	if _, err := s.DB.Exec(
		`UPDATE tasks SET dispatch_session_uuid=? WHERE id=?`, uuid, c.ID); err != nil {
		log.Printf("error: dispatch: record dispatch session uuid (task %d): %v", c.ID, err)
	}

	s.notify(c.ID)

	// Spawn the run. The goroutine owns exit handling + slot release.
	prompt := BuildPrompt(c.Prompt, acq.Branch, c.ExternalID, c.FileScope)
	spec := RunSpec{Prompt: prompt, SessionUUID: uuid, Cwd: acq.Path, Model: c.Model.String}
	s.spawn(func() { s.runAndHandle(c, spec) })
	return true
}

// failAdmission stamps dispatch_error on a task that could not be admitted,
// leaving it in todo. Best-effort.
func (s *Service) failAdmission(id int64, msg string) {
	if _, err := s.DB.Exec(
		`UPDATE tasks SET dispatch_error=? WHERE id=?`, msg, id); err != nil {
		log.Printf("error: dispatch: stamp admission error (task %d): %v", id, err)
		return
	}
	s.notify(id)
}

// ── run + exit handling ──

// runAndHandle executes the run to completion, links the session, applies
// sentinel/exit routing, and always releases the slot.
func (s *Service) runAndHandle(c candidate, spec RunSpec) {
	defer s.clearActive(c.ID)

	ctx, cancel := context.WithTimeout(context.Background(), s.Cfg.RunTimeout)
	defer cancel()

	run, err := s.Run.Start(ctx, spec)
	if err != nil {
		// Process never ran (PATH miss / fork failure). Surface + leave in_review
		// so the user sees it; keep the worktree for inspection.
		s.finishError(c.ID, "runner start: "+err.Error())
		s.Poke()
		return
	}

	// Best-effort: reconcile the explicit task↔session link now that the
	// transcript has (usually) been ingested.
	s.linkSession(c.ID, spec.SessionUUID)

	// Sentinel classification runs FIRST on any exit — an honest BLOCKED /
	// PREMISE-STALE reply is authoritative regardless of exit code. Only if no
	// sentinel matched do we apply exit-code routing.
	sentinel := s.classifyLastTurn(spec.SessionUUID)
	switch sentinel.Kind {
	case "done":
		s.finishDone(c, sentinel.Line)
		s.Poke() // a completed task may unblock dependents (FN-3895)
		return
	case "blocked":
		s.finishBlocked(c.ID, sentinel.Line)
		s.Poke()
		return
	}

	// No sentinel: exit-code routing. Exit 0 → in_review clean; nonzero/timeout →
	// in_review with dispatch_error surfaced. Verification (Phase 6) owns retries,
	// so retry_count is untouched here.
	if run.ExitCode == 0 {
		s.finishReview(c.ID, "")
	} else if run.TimedOut {
		s.finishReview(c.ID, "run timed out after "+s.Cfg.RunTimeout.String())
	} else {
		msg := "session exited " + itoa(run.ExitCode)
		if run.Stderr != "" {
			msg += ": " + run.Stderr
		}
		s.finishReview(c.ID, msg)
	}
	// A no-sentinel landing produced gradeable work on the swarm/<id> branch: poke
	// auto-verification NOW, while the worktree is still live (a terminal move
	// would reclaim it via RemoveWorktreeFor). Non-blocking + nil-safe. A nonzero
	// exit still gets graded — verify degrades to INCONCLUSIVE if nothing gradeable.
	s.pokeVerify(c.ID)
	s.Poke()
}

// pokeVerify triggers auto-verification for a task when a Verifier is attached.
// A nil-safe wrapper so the exit path can call it unconditionally (nil ⇒ no-op,
// keeping dispatch unit tests hermetic).
func (s *Service) pokeVerify(id int64) {
	if s.Verifier != nil {
		s.Verifier.Poke(id)
	}
}

// finishReview moves a run to in_review (the normal end state), setting or
// clearing dispatch_error. The worktree is KEPT (verification + the user need
// it; removal happens on the done/archived transition via RemoveWorktreeFor).
func (s *Service) finishReview(id int64, errMsg string) {
	if _, err := s.DB.Exec(`
		UPDATE tasks SET board_column='in_review', status='needs_review',
		                 dispatch_error=NULLIF(?, ''), column_moved_at=?
		 WHERE id=? AND source='queue'`, errMsg, s.ts(), id); err != nil {
		log.Printf("error: dispatch: finish review (task %d): %v", id, err)
		return
	}
	s.notify(id)
}

// finishError is finishReview with a guaranteed error (start failure).
func (s *Service) finishError(id int64, errMsg string) { s.finishReview(id, errMsg) }

// finishDone moves a sentinel-done task straight to done, records the sentinel
// line as result_note, and removes the worktree (no review needed — the model
// declared no change).
func (s *Service) finishDone(c candidate, line string) {
	var branch, wtpath sql.NullString
	_ = s.DB.QueryRow(`SELECT branch, worktree_path FROM tasks WHERE id=?`, c.ID).
		Scan(&branch, &wtpath)
	if _, err := s.DB.Exec(`
		UPDATE tasks SET board_column='done', status='done',
		                 result_note=?, dispatch_error=NULL, finished_at=?, column_moved_at=?,
		                 worktree_path=NULL
		 WHERE id=? AND source='queue'`, line, s.ts(), s.ts(), c.ID); err != nil {
		log.Printf("error: dispatch: finish done (task %d): %v", c.ID, err)
		return
	}
	s.removeWorktree(c.ProjectPath, wtpath.String, branch.String)
	s.notify(c.ID)
}

// finishBlocked routes a BLOCKED sentinel back to todo + paused with the line as
// dispatch_error. The worktree is kept (the user resumes after unblocking).
func (s *Service) finishBlocked(id int64, line string) {
	if _, err := s.DB.Exec(`
		UPDATE tasks SET board_column='todo', status='queued',
		                 paused=1, dispatch_error=?, column_moved_at=?
		 WHERE id=? AND source='queue'`, line, s.ts(), id); err != nil {
		log.Printf("error: dispatch: finish blocked (task %d): %v", id, err)
		return
	}
	s.notify(id)
}

// removeWorktree best-effort removes a task's worktree directory, KEEPING the
// swarm/<id> branch (its commits stay reachable for verification + the user). A
// failure is logged but never blocks the state transition. A blank path is a
// no-op (task never got a worktree).
func (s *Service) removeWorktree(repoRoot, wtPath, branch string) {
	if s.Wt == nil || strings.TrimSpace(wtPath) == "" {
		return
	}
	acq := worktree.Acquired{Path: wtPath, Branch: branch}
	if err := s.Wt.Remove(repoRoot, acq, true /* keepBranch */); err != nil {
		log.Printf("warning: dispatch: remove worktree %s: %v", wtPath, err)
	}
}

// RemoveWorktreeFor is the callback the api board-patch flow invokes when a task
// enters done/archived from the board (user-driven), so worktrees are reclaimed
// on the terminal transition the dispatcher does not itself perform. Best-effort;
// clears worktree_path so the row no longer counts as holding a live worktree.
func (s *Service) RemoveWorktreeFor(taskID int64) {
	var repoPath string
	var branch, wtpath sql.NullString
	err := s.DB.QueryRow(`
		SELECT p.path, t.branch, t.worktree_path
		  FROM tasks t JOIN projects p ON p.id=t.project_id
		 WHERE t.id=? AND t.worktree_path IS NOT NULL`, taskID).Scan(&repoPath, &branch, &wtpath)
	if errors.Is(err, sql.ErrNoRows) {
		return // no live worktree
	}
	if err != nil {
		log.Printf("error: dispatch: lookup worktree for removal (task %d): %v", taskID, err)
		return
	}
	s.removeWorktree(repoPath, wtpath.String, branch.String)
	if _, err := s.DB.Exec(`UPDATE tasks SET worktree_path=NULL WHERE id=?`, taskID); err != nil {
		log.Printf("error: dispatch: clear worktree_path (task %d): %v", taskID, err)
	}
}

// ── session link + sentinel read ──

// linkSession reconciles the explicit task↔session link: once the dispatched
// session's transcript is ingested (sessions row with our uuid exists), insert
// task_sessions(link_source='explicit'). Idempotent (INSERT OR IGNORE). If the
// row is not yet ingested this is a no-op; the next exit/heal pass or a
// verification run can re-link. Best-effort — a missing link never blocks state.
func (s *Service) linkSession(taskID int64, uuid string) {
	var sid int64
	err := s.DB.QueryRow(`SELECT id FROM sessions WHERE session_uuid=?`, uuid).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		log.Printf("error: dispatch: resolve session for link (task %d): %v", taskID, err)
		return
	}
	if _, err := s.DB.Exec(
		`INSERT OR IGNORE INTO task_sessions(task_id, session_id, link_source, confidence)
		 VALUES(?,?, 'explicit', 1.0)`, taskID, sid); err != nil {
		log.Printf("error: dispatch: insert task_session link (task %d): %v", taskID, err)
		return
	}
	// Also stamp tasks.session_id (the primary FK) if unset.
	if _, err := s.DB.Exec(
		`UPDATE tasks SET session_id=COALESCE(session_id, ?) WHERE id=?`, sid, taskID); err != nil {
		log.Printf("error: dispatch: set task.session_id (task %d): %v", taskID, err)
	}
}

// classifyLastTurn fetches the linked session's last assistant turn text (by
// uuid) and classifies its sentinel. Returns an empty Sentinel when the session
// or its text is not available (no transcript yet ⇒ fall through to exit-code
// routing).
func (s *Service) classifyLastTurn(uuid string) Sentinel {
	var text sql.NullString
	err := s.DB.QueryRow(`
		SELECT tr.text
		  FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid=? AND tr.role='assistant' AND tr.text IS NOT NULL
		 ORDER BY tr.seq DESC LIMIT 1`, uuid).Scan(&text)
	if err != nil || !text.Valid {
		return Sentinel{}
	}
	return ClassifySentinel(text.String)
}

// ── startup heal ──

// HealStale reclaims tasks left in_progress by a crashed/restarted daemon: with
// no live run in THIS process (there can't be — we just started), an
// in_progress queue task is orphaned. Move it back to todo with a marker so the
// next Schedule re-admits it (provision-heal idiom, scheduler FN semantics). The
// worktree is kept (idempotent re-Acquire reuses it).
func (s *Service) HealStale() error {
	res, err := s.DB.Exec(`
		UPDATE tasks
		   SET board_column='todo', status='queued', dispatch_error='daemon restart',
		       column_moved_at=?
		 WHERE source='queue' AND board_column='in_progress'`, s.ts())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("swarmery dispatch: healed %d stuck in_progress task(s) to todo", n)
	}
	return nil
}

// ── pause flags ──

// isPaused reports whether a scope row exists and is paused. Absent ⇒ not
// paused. Any read error is treated as "not paused" but logged (a transient DB
// error should not silently park the dispatcher forever; the kill-switch is the
// hard stop).
func (s *Service) isPaused(scope string) bool {
	var paused int
	err := s.DB.QueryRow(`SELECT paused FROM dispatch_pause WHERE scope=?`, scope).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		log.Printf("error: dispatch: read pause scope %q: %v", scope, err)
		return false
	}
	return paused != 0
}

// SetPause upserts a pause row for a scope. Exposed for the api pause endpoint.
func (s *Service) SetPause(scope string, paused bool) error {
	_, err := s.DB.Exec(`
		INSERT INTO dispatch_pause(scope, paused, updated_at) VALUES(?,?,?)
		ON CONFLICT(scope) DO UPDATE SET paused=excluded.paused, updated_at=excluded.updated_at`,
		scope, boolToInt(paused), s.ts())
	return err
}

// Status is the GET /api/dispatch snapshot.
type Status struct {
	Enabled       bool     `json:"enabled"`       // kill-switch state
	GlobalPaused  bool     `json:"globalPaused"`  // durable global pause flag
	MaxConcurrent int      `json:"maxConcurrent"`
	MaxWorktrees  int      `json:"maxWorktrees"`
	ActiveRuns    int      `json:"activeRuns"`    // live runs in this process
	FreeSlots     int      `json:"freeSlots"`     // maxConcurrent - activeRuns (>=0)
	PausedScopes  []string `json:"pausedScopes"`  // every currently-paused scope
}

// Snapshot builds the status DTO.
func (s *Service) Snapshot() (Status, error) {
	active := s.activeCount()
	free := s.Cfg.MaxConcurrent - active
	if free < 0 {
		free = 0
	}
	st := Status{
		Enabled:       s.Cfg.Enabled,
		GlobalPaused:  s.isPaused("global"),
		MaxConcurrent: s.Cfg.MaxConcurrent,
		MaxWorktrees:  s.Cfg.MaxWorktrees,
		ActiveRuns:    active,
		FreeSlots:     free,
		PausedScopes:  []string{},
	}
	rows, err := s.DB.Query(`SELECT scope FROM dispatch_pause WHERE paused=1 ORDER BY scope`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return st, err
		}
		st.PausedScopes = append(st.PausedScopes, scope)
	}
	return st, rows.Err()
}

// ProjectScope renders the durable pause-scope key for a project id
// ("project:<id>"). Exported so the api pause endpoint builds the same key.
func ProjectScope(projectID int64) string { return "project:" + itoa64(projectID) }
