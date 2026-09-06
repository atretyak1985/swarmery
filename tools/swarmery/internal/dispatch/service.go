package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/playbooks"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// tsFormat matches the millisecond-Z style the api package writes.
const tsFormat = "2006-01-02T15:04:05.000Z"

// WorktreeManager and Verifier are runcore's, re-exported here as ALIASES.
//
// Both were declared in this package because dispatch was the first engine to need
// them; phaserun and planrun then imported dispatch for nothing else, and verify
// satisfied a dispatch-declared interface. They now live in internal/runcore, which
// every engine already depends on, so those import edges are gone.
//
// Aliases rather than new names: `dispatch.WorktreeManager` appears in
// cmd/swarmery's wiring, in internal/api's reach-throughs and in several test
// stubs, and an alias keeps every one of them compiling against the same type
// instead of trading one coupling for a rename.
type (
	WorktreeManager = runcore.WorktreeManager
	Verifier        = runcore.Verifier
)

// Service owns the dispatch loop: candidate selection, admission gates, spawn,
// exit/sentinel handling, event-driven Poke + poll fallback, and startup heal.
type Service struct {
	DB     *sql.DB
	Cfg    Config
	Run    Runner
	Wt     WorktreeManager
	UUID   func() string      // session-uuid generator (test seam)
	now    func() time.Time   // clock (test seam)
	Go     func(func())       // async-spawn seam (nil ⇒ real `go`), mirrors improveGo
	Notify func(taskID int64) // emits task_updated (wired to api.publishTaskUpdated)
	// Verifier, when attached, is poked on a no-sentinel in_review landing so
	// auto-verification (fusion phase 6) grades the work while the worktree is
	// still live (before any terminal done/archived transition reclaims it via
	// RemoveWorktreeFor). nil ⇒ not attached (call is guarded); keeps dispatch
	// unit tests hermetic. The interface (Verifier, above) is declared in this
	// package and satisfied by *verify.Service, so verify imports dispatch's data
	// deps without dispatch importing verify — no import cycle.
	Verifier Verifier
	// Playbooks, when attached, resolves a task's execution recipe (fusion phase
	// 13): how many sequential stages to run in the task's single worktree and
	// which verify strictness to hand Phase 6. nil ⇒ every task runs the classic
	// single-stage flow (the `standard` recipe), so pre-playbook dispatch and all
	// existing dispatch unit tests are unchanged. Resolved per admission through
	// the candidate's project path (built-ins overlaid by project-local files).
	Playbooks *playbooks.Registry
	// FindRun locates the live process of a dispatched run by its session uuid
	// (adopt.go). nil ⇒ a ps scan. Test seam.
	FindRun func(sessionUUID string) (int, bool)
	// ProcAlive reports whether an adopted pid still exists. nil ⇒ signal-0 probe.
	ProcAlive func(pid int) bool
	// adoptPoll overrides runcore.AdoptPollInterval when > 0 (tests shrink it).
	adoptPoll time.Duration

	// WorkspaceRoot is the workspace tree a dispatched card materializes its
	// micro-plan into (wsingest.Root() — the daemon wires the same value both
	// packages use, so the writer and the scanner can never disagree about paths).
	// "" disables minting, as does SWARMERY_MICRO_PLANS=0: the run then proceeds
	// docless, exactly as it did before micro-plans existed.
	WorkspaceRoot string

	// Slots is the DAEMON-WIDE run registry and budget (internal/runcore): the
	// same-task single-flight gate, the input to MaxConcurrent, and — new — one
	// pool shared with phase and plan runs, so the three engines can no longer
	// each spawn up to their own cap against a total none of them could see.
	// NewService gives every Service its own pool (hermetic unit tests); the
	// daemon replaces it with the one instance all three engines hold.
	Slots *runcore.Slots

	scheduling atomic.Bool // re-entrance guard: overlapping Schedule passes skip
}

// NewService builds a dispatcher. The caller wires DB, Cfg, Run (ClaudeRunner),
// Wt (worktree.Manager), and Notify; UUID/now/Go default to production impls.
func NewService(db *sql.DB, cfg Config, r Runner, wt WorktreeManager) *Service {
	return &Service{
		DB: db, Cfg: cfg, Run: r, Wt: wt,
		UUID:  runcore.NewUUID,
		now:   time.Now,
		Slots: runcore.NewSlots(0),
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

// Engine names dispatch in the shared slot registry: board runs are keyed
// "dispatch:<taskID>", so a board task and a phase that happen to share a numeric
// id are different runs.
const Engine = "dispatch"

func (s *Service) slotKey(id int64) string { return runcore.SlotKey(Engine, id) }

// markActive claims the task's run slot: the same-task single-flight gate AND a
// draw on the daemon-wide budget. Unlike the setter it replaces it can FAIL, and
// that is the point — a full pool must refuse an admission rather than over-spawn.
// runcore.ErrBusy means this task is already running (a duplicate); ErrNoSlot
// means the pool is full and the candidate should be retried next pass. Neither
// is a terminal state for the row.
func (s *Service) markActive(id int64) error {
	_, err := s.Slots.TryAcquire(s.slotKey(id), "", nil)
	return err
}

func (s *Service) clearActive(id int64) {
	s.Slots.Release(s.slotKey(id))
}

func (s *Service) isActive(id int64) bool {
	return s.Slots.IsActive(s.slotKey(id))
}

// IsActive is isActive for callers outside this package — the board's review
// exits (api/tasks_review.go), which must refuse to re-queue or archive a card
// the dispatcher still owns. The durable columns are the truth everywhere else,
// but they lag this set by the width of one exit path, and that window is
// precisely when a user staring at a card that "looks finished" clicks Re-run.
func (s *Service) IsActive(id int64) bool { return s.isActive(id) }

// activeCount counts DISPATCH's runs only: MaxConcurrent is the board's own cap
// and must keep meaning "board runs in flight", not "runs in flight anywhere".
// The daemon-wide total is s.Slots.Count().
func (s *Service) activeCount() int {
	return s.Slots.CountEngine(Engine)
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
	// Which checkouts are already occupied. Re-read each pass for the same reason
	// activeScopes is, and extended in-loop on every admission below.
	heldWorktrees, err := s.liveWorktreeKeys()
	if err != nil {
		log.Printf("error: dispatch: load held worktrees: %v", err)
		return
	}

	for _, c := range cands {
		// Gate: permission preset (fusion phase 11). A locked-down project never
		// admits its Todo tasks — stamp the documented error and skip. Enforced
		// here at admission; the preset ALSO compiled zero auto-approve rules, so
		// even if this gate were bypassed nothing could auto-approve (defense in
		// depth). A read error is logged and treated as NOT locked (mirrors
		// isPaused) — the kill-switch/pause are the hard stops, and the missing
		// auto-approve rules keep a locked-down project safe regardless.
		if s.projectLockedDown(c.ProjectID) {
			s.stampLockedDown(c.ID)
			continue
		}
		// Gate: project pause.
		if s.isPaused(ProjectScope(c.ProjectID)) {
			continue
		}
		// Gate: concurrency cap — the BOARD's own cap, over board runs only.
		if s.activeCount() >= s.Cfg.MaxConcurrent {
			break // no point scanning further this pass
		}
		// Gate: the daemon-wide run budget, shared with phase and plan runs. A board
		// run is bounded by min(MaxConcurrent, free slots): with a free pool this is
		// exactly the old behaviour, and while a plan run holds the machine the board
		// waits instead of piling on. Advisory — the TryAcquire inside admit is the
		// race-free authority — so winning this check cannot admit past the budget.
		if s.Slots.Count() >= s.Slots.Max() {
			break
		}
		// Gate: same-task single-flight (Acquire is idempotent for a live task,
		// so WE must reject a re-dispatch — DESIGN handoff).
		if s.isActive(c.ID) {
			continue
		}
		// Gate: worktree single-flight. The gate above keys on the task ID; the
		// checkout is keyed on the external id, and Acquire warm-reuses rather than
		// refuses (see liveWorktreeKeys). Without this, two rows sharing an external
		// id — verify's fix task and its root — could put two headless agents in one
		// directory on one branch. Skipping only defers: the loser is still a Todo
		// candidate on the next pass, by which time the checkout is free.
		if c.ExternalID != "" && heldWorktrees[runcore.WorktreeKey{ProjectID: c.ProjectID, Branch: runcore.TaskBranch(c.ExternalID)}] {
			continue
		}
		// Gate: worktree cap.
		if liveWorktrees >= s.Cfg.MaxWorktrees {
			break
		}
		// Gate: dependencies all resolved (done|archived, verification not failed).
		// The reason is surfaced, not swallowed — an operator seeing a card sit still
		// with no explanation is what this gate used to produce.
		if c.depBlocker != nil {
			s.recordDepBlock(c.ID, *c.depBlocker)
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
		if c.ExternalID != "" {
			heldWorktrees[runcore.WorktreeKey{ProjectID: c.ProjectID, Branch: runcore.TaskBranch(c.ExternalID)}] = true
		}
	}
}

// ── candidate model ──

// candidate is one eligible-or-nearly Todo task with the fields the gates need.
type candidate struct {
	ID           int64
	ExternalID   string
	Title        string // the card's title — the micro-plan's headings and table cell
	ProjectID    int64
	ProjectSlug  string
	ProjectPath  string // repo root for worktree.Acquire
	Prompt       string
	Model        sql.NullString
	Agent        sql.NullString // registry agent name (NULL ⇒ plain run, no mention)
	Playbook     sql.NullString // selected recipe name (NULL ⇒ default 'standard')
	Priority     int
	CreatedAt    string
	FileScope    []string
	Dependencies []string
	// Provenance is the block appended to the task body of a CAPTURED card:
	// the session's opening prompt and the files it touched, read from the
	// origin_* columns (0066). "" for a manual card. It is added here, at
	// dispatch, and nowhere else — the card's own prompt no longer carries the
	// quote, so the run sees it exactly once.
	Provenance string
	depBlocker *DepBlocker // nil ⇒ every dependency clear
}

// buildDispatchPrompt assembles the task body a run is built from: the card's
// own prompt followed by, for a captured card, its provenance block. It feeds
// the {task_prompt} variable and the implicit single stage alike, so the two
// paths cannot disagree about what the runner is told, and it is the ONE place
// that decides what a dispatched card says.
//
// The card's TITLE is deliberately not part of it. For a captured card the
// title is a clipped prefix of the prompt (ingest/capture.go), and for a
// title-only manual card the intake contract already copied the title INTO the
// prompt ("empty prompt = title", NewTaskModal) — prepending it would repeat
// the same sentence back at the model. The title does reach the run, as the
// heading of the micro-plan doc the contract points at (mintMicroPlan). What
// the user wrote therefore stays first and verbatim, and a card with no
// provenance dispatches with a prompt byte-identical to the pre-0066 one.
func buildDispatchPrompt(c candidate) string { return c.Prompt + c.Provenance }

// provenanceBlock renders the dispatch-time provenance of a captured card.
// "" when there is nothing to say. The wording deliberately differs from the
// pre-0066 prose marker ("That session opened with:") so the text can never be
// mistaken for a card body that still needs the migration's backfill.
func provenanceBlock(quote string, files []string) string {
	quote = strings.TrimSpace(quote)
	if quote == "" && len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n--- PROVENANCE (captured card) ---\n")
	if quote != "" {
		b.WriteString("The session this card was captured from was asked:\n")
		b.WriteString(quote)
		b.WriteString("\n")
	}
	if len(files) > 0 {
		b.WriteString("Files that session touched: ")
		b.WriteString(strings.Join(files, ", "))
		b.WriteString("\n")
	}
	b.WriteString("--- END PROVENANCE ---")
	return b.String()
}

// candidates returns Todo board tasks (source='queue', both pause flags clear)
// ordered by priority (urgent<high<normal<low ⇒ ascending int) then created_at
// then id, with per-candidate dependency satisfaction resolved in-memory.
func (s *Service) candidates() ([]candidate, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, COALESCE(t.external_id,''), COALESCE(t.title,''), t.project_id, p.slug, p.path,
		       t.prompt, t.model, t.agent, t.playbook, t.priority, t.created_at, t.file_scope, t.dependencies,
		       COALESCE(t.origin_quote,''), COALESCE(t.origin_files,'')
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
		var scopeJSON, depsJSON, quote, filesJSON string
		if err := rows.Scan(&c.ID, &c.ExternalID, &c.Title, &c.ProjectID, &c.ProjectSlug,
			&c.ProjectPath, &c.Prompt, &c.Model, &c.Agent, &c.Playbook, &c.Priority, &c.CreatedAt,
			&scopeJSON, &depsJSON, &quote, &filesJSON); err != nil {
			return nil, err
		}
		if c.FileScope, err = decodeStringList(scopeJSON); err != nil {
			return nil, err
		}
		if c.Dependencies, err = decodeStringList(depsJSON); err != nil {
			return nil, err
		}
		// origin_files is best-effort provenance: an undecodable value drops the
		// file list rather than parking the card.
		files, _ := decodeStringList(filesJSON)
		c.Provenance = provenanceBlock(quote, files)
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

	// Resolve dependency satisfaction once, keeping the REASON: a dep is satisfied iff
	// its task is in done|archived and its verification did not explicitly fail.
	// Keyed by external_id — the card id used in the trailer and dependency arrays.
	for i := range cands {
		blocker, err := s.depBlocker(cands[i].Dependencies)
		if err != nil {
			return nil, err
		}
		cands[i].depBlocker = blocker
	}
	return cands, nil
}

// DepBlocker names WHY a dependency is not satisfied. A bool cannot be shown to an
// operator, and "not dispatching, no reason given" is a state this codebase has
// already paid for: four phase runs once launched on top of a 0/7 dependency because
// the gate accepted a board column as proof of completion. Fusion's
// getTaskMergeBlocker (packages/core/src/task-merge.ts:239) returns the reason for
// exactly this purpose.
type DepBlocker struct {
	Dep    string // external_id of the blocking dependency
	Reason string // "not found" | "not done (column=X)" | "verification failed"
}

func (b DepBlocker) String() string { return b.Dep + ": " + b.Reason }

// depBlocker returns the first unsatisfied dependency, or nil when every dependency
// is clear. Unknown ids block (a dangling dependency must not silently unblock).
//
// Rule order matters. The verdict check comes LAST and fires only on an explicit
// 'fail':
//
// NULL, ”, 'pass' and 'inconclusive' all pass the gate. This is not leniency — it
// is the invariant this codebase already keeps in internal/phasediag/outcome.go,
// where a missing baseline yields a zero delta and never a negative verdict, and the
// one Fusion keeps at packages/engine/src/reviewer.ts:50, where a provider error
// never becomes a verdict. Unavailability of a measurement is not a bad measurement.
//
// The practical stake: verify_verdict is NULL for 100% of tasks in the live database.
// A gate that blocked on NULL would make the board impassable on its first boot.
func (s *Service) depBlocker(deps []string) (*DepBlocker, error) {
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		var col, verdict string
		err := s.DB.QueryRow(
			`SELECT board_column, COALESCE(verify_verdict,'') FROM tasks WHERE external_id=? LIMIT 1`,
			dep).Scan(&col, &verdict)
		if errors.Is(err, sql.ErrNoRows) {
			return &DepBlocker{Dep: dep, Reason: "not found"}, nil
		}
		if err != nil {
			return nil, err
		}
		if col != "done" && col != "archived" {
			return &DepBlocker{Dep: dep, Reason: "not done (column=" + col + ")"}, nil
		}
		if verdict == verdictFail {
			return &DepBlocker{Dep: dep, Reason: "verification failed"}, nil
		}
	}
	return nil, nil
}

// verdictFail is the only verify_verdict value that blocks. Named so the contrast
// with the values that do NOT block is explicit at the call site.
const verdictFail = "fail"

// depBlockPrefix marks a dispatch_error this gate wrote, so a later pass can
// recognise and refresh its own message without clobbering an error some other part
// of the dispatcher recorded.
const depBlockPrefix = "blocked by dependency "

// recordDepBlock surfaces the blocking reason on the task row.
//
// It only ever overwrites nothing or its OWN previous message. A real failure — a
// runner crash, a worktree error, a parked no-progress marker — carries information
// this gate does not have and must not be replaced by "waiting on a dependency",
// which would read as benign. Every scheduling pass re-evaluates the same blocked
// card, so without the prefix check the gate would overwrite such an error within
// seconds of it being written.
func (s *Service) recordDepBlock(id int64, b DepBlocker) {
	msg := depBlockPrefix + b.String()
	if _, err := s.DB.Exec(`
		UPDATE tasks SET dispatch_error=?
		 WHERE id=? AND (dispatch_error IS NULL OR dispatch_error='' OR dispatch_error LIKE ?)`,
		msg, id, depBlockPrefix+"%"); err != nil {
		log.Printf("error: dispatch: record dep block (task %d): %v", id, err)
	}
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

// liveWorktreeKeys returns the checkout identity of every run currently holding
// one — the set a candidate must not collide with. Delegated to runcore, which
// counts ALL THREE run surfaces (board, phase, plan); this used to read `tasks`
// alone, so a phase run's checkout was invisible to the gate that exists to
// arbitrate checkouts.
//
// The gate is needed because the same-task gate (isActive) keys on the task ID
// while the resource is keyed on the branch, and Acquire will NOT arbitrate the
// difference: a path whose branch already matches is warm-REUSED as-is
// (worktree.go invariant 4), deliberately, so a crashed run can be resumed rather
// than destroyed. Idempotent-for-a-live-run is exactly why admission has to do the
// rejecting.
//
// One shipped flow puts two board rows on one checkout: verify's fix task carries
// external_id=<root external id> so its own failure charges the root
// (verify/service.go createFixTask). Sharing the root's branch is the POINT there —
// the fix continues the work it is fixing — so the answer is not to split the key
// but to keep the two runs strictly sequential.
//
// The file-scope gate happens to cover that instance (a fix task inherits the
// root's scope, and identical scopes always overlap). That is incidental: it holds
// only while the two rows agree on file_scope, which an operator can patch apart
// via PATCH /api/board/tasks/{id}. This gate is keyed on the resource itself, so it
// does not depend on the coincidence.
func (s *Service) liveWorktreeKeys() (map[runcore.WorktreeKey]bool, error) {
	return runcore.WorktreeKeys(s.DB)
}

// liveWorktreeCount counts every live worktree the daemon holds, across board,
// phase and plan runs (runcore.WorktreeCount). MaxWorktrees used to be enforced
// against queue tasks alone: a machine running four phases and a plan reported ZERO
// worktrees to the cap that exists to bound them.
func (s *Service) liveWorktreeCount() (int, error) {
	return runcore.WorktreeCount(s.DB)
}

// ── admission ──

// admit acquires a worktree, writes the explicit session link, moves the task
// todo→in_progress via a guarded CAS, and spawns the run goroutine. Returns
// true when the run was launched. Any failure is surfaced on the row's
// dispatch_error and the task is left in todo (no worktree leak — Acquire is
// idempotent and the next pass retries or a human intervenes).
func (s *Service) admit(c candidate) bool {
	// CAS FIRST is not possible before we have branch/worktree — but we must not
	// double-spawn. Claim the run slot up-front (in-memory) so a concurrent pass in
	// the same process cannot also pick this task; the guarded UPDATE below is the
	// durable CAS. If admission fails we release the slot.
	//
	// This is also where the daemon-wide budget is actually enforced — the gate in
	// Schedule is advisory, this claim is atomic. A refusal is NOT an error on the
	// row: the task stays a Todo candidate and the next pass retries, the same
	// posture as the file-scope and worktree gates.
	if err := s.markActive(c.ID); err != nil {
		log.Printf("dispatch: task=%d not admitted: %v", c.ID, err)
		return false
	}

	acq, err := s.Wt.Acquire(c.ProjectPath, c.ProjectSlug, c.ExternalID)
	if err != nil {
		s.clearActive(c.ID)
		s.failAdmission(c.ID, "worktree acquire: "+err.Error())
		return false
	}

	uuid := s.UUID()

	// Guarded CAS: only move a row that is STILL todo (a concurrent PATCH could
	// have moved/paused it since candidates() read). On 0 rows affected, back off.
	//
	// start_point is written HERE because this is the only place that knows it:
	// Acquired.StartPoint is the SHA the worktree was pinned to, and it is what
	// verification must diff against (0051). Acquire is idempotent warm-reuse, so
	// a re-dispatch REFRESHES start_point to whatever SHA the (possibly reused)
	// worktree is now pinned to — that is the correct base for the next run's
	// diff, not the first run's.
	res, err := s.DB.Exec(`
		UPDATE tasks
		   SET board_column='in_progress', status='running',
		       branch=?, worktree_path=?, start_point=?,
		       dispatch_error=NULL, column_moved_at=?, started_at=COALESCE(started_at, ?)
		 WHERE id=? AND source='queue' AND board_column='todo'
		   AND paused=0 AND user_paused=0`,
		acq.Branch, acq.Path, acq.StartPoint, s.ts(), s.ts(), c.ID)
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

	// Materialize the card's micro-plan (execution-engine unification phase 4): a
	// single-phase plan dir in the workspace, so this card's outcome is evidence in a
	// doc rather than the column someone dragged it to. Non-fatal by construction —
	// a docless run is the pre-micro-plan behaviour, and refusing to run work because
	// a markdown file could not be written would be the worse trade.
	taskDoc := s.mintMicroPlan(c)

	s.notify(c.ID)

	// Resolve the playbook (fusion phase 13): NULL ⇒ an auto-profile picks one and
	// stamps it back on the card; unknown ⇒ the classic single-stage flow. The
	// resolved stage list drives how many sequential headless runs execute in this
	// one worktree; the recipe's model/permission knobs shape every spawn. The
	// first stage reuses the pre-generated uuid recorded above (so the task↔session
	// link already points at stage 1); later stages mint their own uuids inside
	// runPlaybook.
	pb := s.resolvePlaybook(c)

	// Spawn the run. The goroutine owns exit handling + slot release.
	s.spawn(func() { s.runPlaybook(c, acq, pb, uuid, taskDoc) })
	return true
}

// linkMicroPlanSession links a dispatched run's session to the WORKSPACE task row
// wsingest created from the card's micro-plan dir.
//
// Resolved by path (tasks.workspace_dir → task_artifacts.path of kind 'plan'),
// because the dir is the durable identity and the row is not: wsingest re-derives
// the row from the tree, so caching an id here would go stale on the first rename.
// A missing row is the normal case at the first stage exit — wsingest may not have
// scanned yet — and is silently skipped.
func (s *Service) linkMicroPlanSession(taskID int64, uuid string) {
	var wsTaskID int64
	err := s.DB.QueryRow(`
		SELECT ta.task_id
		  FROM tasks c
		  JOIN task_artifacts ta ON ta.kind='plan' AND ta.path = c.workspace_dir || '/plan'
		  JOIN tasks w ON w.id = ta.task_id AND w.source='workspace'
		 WHERE c.id = ? AND c.workspace_dir IS NOT NULL AND c.workspace_dir <> ''`,
		taskID).Scan(&wsTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return // no micro-plan, or wsingest has not indexed it yet
	}
	if err != nil {
		log.Printf("error: dispatch: resolve micro-plan task (card %d): %v", taskID, err)
		return
	}
	runcore.LinkSession(s.DB, Engine, wsTaskID, uuid)
}

// microPlansEnv is the escape hatch: SWARMERY_MICRO_PLANS=0 (or false/off) makes a
// dispatched card behave exactly as it did before micro-plans — no dir, no doc, no
// paragraph in the contract.
const microPlansEnv = "SWARMERY_MICRO_PLANS"

// microPlansEnabled reports whether dispatched cards materialize micro-plans.
// Default ON: the honesty contract is the point of the feature, and an operator who
// wants the old behaviour has to ask for it.
func microPlansEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(microPlansEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// mintMicroPlan writes the card's micro-plan and returns its phase-doc path, or ""
// when there is none (feature off, no workspace root wired, or a mint that failed).
//
// The dir is recorded on tasks.workspace_dir — the join between the card and the
// plan it materialized. No workspace task ROW is written here: wsingest discovers
// the dir on its next pass and stays the only writer of those rows, so there is one
// path from a dir to a row and no way for the two to disagree.
func (s *Service) mintMicroPlan(c candidate) string {
	if s.WorkspaceRoot == "" || !microPlansEnabled() {
		return ""
	}
	dir, err := taskdir.MintMicroPlan(s.WorkspaceRoot, c.ProjectSlug, taskdir.Card{
		ExternalID: c.ExternalID,
		Title:      c.Title,
		Prompt:     c.Prompt,
		RepoPath:   c.ProjectPath,
	}, s.clock())
	if err != nil {
		// Logged, not surfaced on the card: the run is about to happen either way, and
		// a dispatch_error here would read as "your task failed" over a task that ran.
		log.Printf("warning: dispatch: task=%d micro-plan mint failed: %v", c.ID, err)
		return ""
	}
	if _, err := s.DB.Exec(`UPDATE tasks SET workspace_dir=? WHERE id=?`, dir, c.ID); err != nil {
		log.Printf("error: dispatch: record workspace_dir (task %d): %v", c.ID, err)
	}
	return taskdir.PhaseDocPath(dir)
}

// resolvedStage is one stage's rendered prompt plus its name (for a stage-fail
// dispatch_error). The execution contract is appended at spawn time so a stage
// body stays a pure template.
type resolvedStage struct {
	name string
	body string // the playbook stage body (template vars unresolved except per-stage ones)
}

// resolvedPlaybook is a candidate's recipe as the run needs it: the ordered
// stages plus the knobs that shape every spawn of the chain. Model and
// permission mode belong to the RECIPE, not to one step of it, so they are
// resolved once here and applied to each stage identically.
type resolvedPlaybook struct {
	stages         []resolvedStage
	model          string // recipe's declared --model ("" = fall through to card/default)
	permissionMode string // recipe's --permission-mode ("" = inherit the global knob)
}

// resolvePlaybook returns the resolved recipe for a candidate. With no registry
// attached, or an unresolvable playbook name, it degrades to a single implicit
// stage whose body is the task's own prompt and no knobs — byte-for-byte the
// pre-playbook behavior. Stage bodies are returned unrendered; runPlaybook
// renders each with the per-run var map (incl. previous_stage_output).
//
// A card that never chose a playbook gets one auto-selected here and STAMPED
// back onto the row, so the board shows the recipe that actually ran.
func (s *Service) resolvePlaybook(c candidate) resolvedPlaybook {
	single := resolvedPlaybook{stages: []resolvedStage{{name: "implement", body: buildDispatchPrompt(c)}}}
	if s.Playbooks == nil {
		return single
	}

	// An implicit default that is never written down is invisible: the playbook
	// column sat 99% NULL for its whole life while every one of those cards
	// silently ran 'standard'. Pick explicitly, then record the pick.
	name := strings.TrimSpace(c.Playbook.String)
	chosen := name == ""
	if chosen {
		name = autoProfile(c.Prompt, c.Dependencies)
	}

	pb, ok := s.Playbooks.Get(c.ProjectPath, name)
	if !ok || len(pb.Stages) == 0 {
		return single
	}
	if chosen {
		// pb.Name, not name: the registry canonicalizes (an alias resolves to the
		// recipe it points at), and the card must name what ran.
		s.stampPlaybook(c.ID, pb.Name)
	}

	out := make([]resolvedStage, 0, len(pb.Stages))
	for _, st := range pb.Stages {
		out = append(out, resolvedStage{name: st.Name, body: st.Body})
	}
	return resolvedPlaybook{stages: out, model: pb.Model, permissionMode: pb.PermissionMode}
}

// autoProfileThreshold is the prompt size above which a card is treated as
// plan-worthy. Deliberately a plain byte count: it is available before any model
// runs, costs nothing, and is reproducible — a card dispatched twice profiles
// the same way both times.
const autoProfileThreshold = 1500

// autoProfile picks a recipe for a card that never chose one. Deterministic and
// cheap on purpose: prompt size and declared dependencies are the two signals we
// have before any model runs. A big ask or one that lands on top of other work
// earns a planning stage first; everything else runs the single pass.
//
// review-heavy is NEVER auto-selected. It is a human opt-in: auto-escalating to
// strict verification would spend the verify budget on noise.
func autoProfile(prompt string, deps []string) string {
	if len(prompt) > autoProfileThreshold || len(deps) > 0 {
		return "plan-first"
	}
	return "standard"
}

// stampPlaybook records an auto-selected recipe on the card. The WHERE clause —
// not the caller — is the durable guarantee that an explicit choice is never
// overwritten: a concurrent PATCH that lands between resolution and this UPDATE
// wins, and the run simply keeps the recipe it already resolved. Best-effort;
// a failure to record must not fail the dispatch.
func (s *Service) stampPlaybook(id int64, name string) {
	res, err := s.DB.Exec(
		`UPDATE tasks SET playbook=? WHERE id=? AND (playbook IS NULL OR playbook='')`, name, id)
	if err != nil {
		log.Printf("error: dispatch: stamp auto-profiled playbook (task %d): %v", id, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return // an explicit choice landed first — nothing to announce
	}
	s.notify(id)
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

// ── run + exit handling (multi-stage, fusion phase 13) ──

// runPlaybook executes a task's resolved playbook stages SEQUENTIALLY in the
// one acquired worktree and always releases the slot. Each stage is a separate
// headless run with its own session UUID, all linked to the task via
// task_sessions (the task card shows the latest). Stage 1 reuses firstUUID (the
// pre-generated id already parked on dispatch_session_uuid); stages 2+ mint
// their own. The chain stops early on: a sentinel (BLOCKED/done — honest exit,
// authoritative), a stage start-failure, or a non-final stage's nonzero exit
// (dispatch_error='stage <name> failed'). Only after the FINAL stage lands
// cleanly does the classic no-sentinel routing run (in_review + pokeVerify).
//
// A single-stage playbook (the default) walks this loop exactly once, so its
// behavior is byte-for-byte the pre-playbook runAndHandle: link → sentinel →
// exit-code routing → pokeVerify.
func (s *Service) runPlaybook(c candidate, acq worktree.Acquired, pb resolvedPlaybook, firstUUID, taskDoc string) {
	defer s.clearActive(c.ID)

	// Lend the card's micro-plan doc INTO the worktree and quote it by its
	// relative path: the contract's first line makes this worktree the agent's
	// one root, and telling it to edit a file outside that root contradicts the
	// fence and is refused by the sandbox. A lend failure is non-fatal for the
	// same reason a mint failure is — a docless run is the pre-micro-plan
	// behaviour, and refusing to run work over a markdown file is the worse trade.
	docRel, lendErr := worktree.LendPlanDoc(acq.Path, taskDoc)
	if lendErr != nil {
		log.Printf("warning: dispatch: task=%d could not lend the plan doc into %s: %v", c.ID, acq.Path, lendErr)
	}
	// Returned on EVERY exit path — done, blocked, review, stage failure. The
	// dashboard renders the workspace copy's `## Completion Report` and nothing
	// else, so a report that stays in the worktree is work that shipped reading
	// as "no summary of the work written". The defer is what makes "every path"
	// true without auditing each return.
	defer worktree.ReturnPlanDocLogged(fmt.Sprintf("dispatch task=%d", c.ID), acq.Path, docRel, taskDoc)

	// A card WITHOUT a plan doc has no workspace file to return, so the contract
	// points it at worktree.ReportPath instead and this reads that back onto the
	// card. Same deal as the return trip above, and deferred for the same reason:
	// the contract asks for a report on the blocked path too, and the report about
	// why work stopped is the one most worth not losing. Instructing an agent to
	// write a file nobody reads would be worse than saying nothing.
	if docRel == "" {
		defer s.collectDoclessReport(c.ID, acq.Path)
	}

	stages := pb.stages

	// The Claude account every stage of this task runs under, resolved ONCE from
	// the PROJECT path. It cannot be resolved at the spawn site: that runs with
	// cwd=acq.Path (the worktree), and a worktree carries no
	// .claude/settings.local.json, so resolving there would silently yield the
	// default account (plan A3). "" = unbound project = default account = no env
	// delta. Read once rather than per stage: every stage of one playbook belongs
	// to the same project, and a re-read mid-chain could split one task across two
	// accounts if the operator rebinds while it runs.
	account := claudeacct.Binding(c.ProjectPath)

	var prevOutput string
	for i, st := range stages {
		last := i == len(stages)-1

		uuid := firstUUID
		if i > 0 {
			uuid = s.UUID()
		}

		// Render this stage's body with the per-run var map, then append the
		// execution contract (appended to EVERY stage regardless of playbook).
		vars := playbooks.Vars{
			TaskPrompt:          buildDispatchPrompt(c),
			StartPoint:          acq.StartPoint,
			Branch:              acq.Branch,
			TaskID:              c.ExternalID,
			FileScope:           scopeText(c.FileScope),
			PreviousStageOutput: prevOutput,
			TaskDoc:             docRel,
		}
		prompt := BuildStagePromptDoc(playbooks.Render(st.body, vars), acq.Branch, c.ExternalID, docRel, c.FileScope)
		if i == 0 {
			// Record the exact text the runner is about to see, so the card can
			// answer "what was this task actually told?" — the body, the
			// provenance block, the rendered recipe stage and the contract, as
			// one string. First stage only: it is the one that carries the task.
			s.recordDispatchedPrompt(c.ID, prompt)
		}
		// Model precedence, most specific first: the card's own override, then the
		// recipe's declared model, then the global default. The middle step is what
		// makes the `model:` frontmatter knob real — it parsed and rendered as a UI
		// chip since phase 13 while dispatch ignored it, so the chip named a model
		// no run ever used.
		model := c.Model.String
		if model == "" {
			model = pb.model
		}
		if model == "" {
			model = defaultModel
		}
		// Agent is carried, never applied: ClaudeRunner.agentPrompt owns the single
		// "@<agent>: " prefix site. Every stage of a playbook runs as the same agent
		// — the selection belongs to the card, not to one recipe step. The same
		// holds for the permission mode: it is the recipe's, so every stage of the
		// chain gets it ("" ⇒ the runner falls back to the global knob).
		spec := RunSpec{Prompt: prompt, SessionUUID: uuid, Cwd: acq.Path, Model: model,
			Agent: c.Agent.String, Account: account, PermissionMode: pb.permissionMode}

		run, err := s.runStage(spec)
		if err != nil {
			// Process never ran (PATH miss / fork failure) on this stage → surface +
			// stop; keep the worktree for inspection. Name the stage on a multi-stage
			// playbook so the error points at where the chain broke.
			s.finishError(c.ID, s.stageErr(stages, st.name, "runner start: "+err.Error()))
			s.Poke()
			return
		}

		// Reconcile the explicit task↔session link for THIS stage's session (all
		// stage sessions land in task_sessions; the primary session_id sticks to
		// the first that ingests).
		s.linkSession(c.ID, uuid)
		// And to the card's MICRO-PLAN, which is a different task row: the Plans page
		// reads task_sessions for the workspace task, so without this second link the
		// micro-plan renders with no session that ever ran it — the same blindness
		// phase 3 fixed for phase and plan runs. Parks silently until wsingest has
		// ingested the dir; every later stage exit retries, and wsingest's own
		// reconcile pass converges the rest.
		s.linkMicroPlanSession(c.ID, uuid)

		// Sentinel classification runs FIRST on any stage exit — an honest BLOCKED /
		// PREMISE-STALE / NO-OP reply is authoritative regardless of exit code and
		// stops the chain immediately (no point running later stages).
		sentinel := s.classifyLastTurn(uuid)
		switch sentinel.Kind {
		case "done":
			// A done sentinel is a CLAIM, not proof. PREMISE STALE / NO-OP / DUPLICATE
			// (prompt.go doneSentinels) all mean "no work was needed" — the one path
			// where an agent closes a task without producing anything, and the cheapest
			// path available to it. On the live database 5 of 5 dispatched tasks took it
			// (all PREMISE STALE), which is exactly why verify_verdict was NULL for all
			// 74 tasks and verification_runs sat at 0 with the trigger enabled.
			//
			// BEFORE finishDone, not after: finishDone nulls tasks.worktree_path and
			// verification memoizes on the worktree tree hash, so the reverse order
			// grades nothing while looking correct. Pinned by
			// TestDoneSentinelPokesVerifyBeforeWorktreeCleared.
			s.pokeVerify(c.ID)
			s.finishDone(c, sentinel.Line)
			s.Poke() // a completed task may unblock dependents (FN-3895)
			return
		case "blocked":
			s.finishBlocked(c.ID, sentinel.Line)
			s.Poke()
			return
		}

		if !last {
			// A non-final stage must succeed before the next one runs. A nonzero exit
			// or timeout stops the chain with a stage-scoped error (the worktree is
			// kept for review). Verification is NOT poked — the work is incomplete.
			if run.ExitCode != 0 {
				s.finishReview(c.ID, s.stageErr(stages, st.name, stageExitMsg(run, s.Cfg.RunTimeout)))
				s.Poke()
				return
			}
			// Carry this stage's last assistant text into the next stage as
			// {previous_stage_output}. Missing transcript ⇒ empty (best-effort).
			prevOutput = s.lastAssistantText(uuid)
			continue
		}

		// FINAL stage, no sentinel: the classic exit-code routing. Exit 0 →
		// in_review clean; nonzero/timeout → in_review with the error surfaced.
		s.finishReview(c.ID, stageExitMsg(run, s.Cfg.RunTimeout))
		// A no-sentinel landing produced gradeable work on the swarm/<id> branch:
		// poke auto-verification NOW, while the worktree is still live. Non-blocking
		// + nil-safe; a nonzero exit still gets graded (verify → INCONCLUSIVE if
		// nothing gradeable). The verifier resolves this task's playbook verify knob
		// itself (strict/normal/off) from the tasks.playbook column at grade time.
		s.pokeVerify(c.ID)
		s.Poke()
		return
	}
}

// runStage runs one stage's headless process to completion, bounded by the
// per-run timeout, and returns its Run (a timeout is an outcome, not an error).
func (s *Service) runStage(spec RunSpec) (*Run, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.Cfg.RunTimeout)
	defer cancel()
	return s.Run.Start(ctx, spec)
}

// stageErr scopes an error message to the failing stage's name ONLY for a
// multi-stage playbook — a single-stage run keeps the bare message so its
// dispatch_error is identical to pre-playbook behavior.
func (s *Service) stageErr(stages []resolvedStage, name, msg string) string {
	if len(stages) <= 1 {
		return msg
	}
	return "stage " + name + " failed: " + msg
}

// stageExitMsg renders the dispatch_error for a stage's exit ("" on clean exit 0,
// a timeout note, or "session exited N: <stderr>"). Shared by the mid-chain and
// final-stage routing so the wording matches the single-stage path exactly.
func stageExitMsg(run *Run, timeout time.Duration) string {
	if run.ExitCode == 0 {
		return ""
	}
	if run.TimedOut {
		return "run timed out after " + timeout.String()
	}
	msg := "session exited " + itoa(run.ExitCode)
	if run.Stderr != "" {
		msg += ": " + run.Stderr
	}
	return msg
}

// collectDoclessReport moves the worktree's report file onto the card's
// result_note — the field the board renders as the card's summary. Best-effort
// by construction: an agent that wrote no report still shipped its commits, and
// this must never be able to fail a run.
//
// An existing note is kept and the report appended below it, because the note
// may already hold an honest-exit sentinel line (NO-OP:, PREMISE STALE:) which
// is the more precise statement of what happened. The two are complementary:
// the sentinel says how the run ended, the report says what it did.
func (s *Service) collectDoclessReport(id int64, worktreePath string) {
	report := worktree.CollectReport(worktreePath)
	if report == "" {
		return
	}
	if _, err := s.DB.Exec(`
		UPDATE tasks
		   SET result_note = CASE
		         WHEN result_note IS NULL OR result_note = '' THEN ?
		         ELSE result_note || char(10) || char(10) || ?
		       END
		 WHERE id = ?`, report, report, id); err != nil {
		log.Printf("warning: dispatch: task=%d report not collected from %s: %v", id, worktreePath, err)
		return
	}
	log.Printf("dispatch: task %d — collected %d-byte report from %s", id, len(report), worktree.ReportPath)
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
	// Board says done — check the phase doc's remaining acceptance boxes so plan
	// progress follows (a sentinel exit like PREMISE STALE never touched the doc).
	if n, err := wsingest.TickPhaseChecklist(s.DB, c.ID); err != nil {
		log.Printf("warn: dispatch: tick phase checklist (task %d): %v", c.ID, err)
	} else if n > 0 {
		log.Printf("dispatch: task %d done — ticked %d phase checkbox(es)", c.ID, n)
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

// recordDispatchedPrompt stamps tasks.dispatched_prompt (0066) with the text
// handed to the runner. Best-effort: a failure to record what was said must
// not stop it being said, so the error is logged and the run proceeds.
func (s *Service) recordDispatchedPrompt(taskID int64, prompt string) {
	if _, err := s.DB.Exec(`UPDATE tasks SET dispatched_prompt=? WHERE id=?`, prompt, taskID); err != nil {
		log.Printf("warning: dispatch: task=%d could not record the dispatched prompt: %v", taskID, err)
	}
}

// linkSession reconciles the explicit task↔session link: once the dispatched
// session's transcript is ingested (a sessions row with our uuid exists), write
// task_sessions('explicit') and stamp tasks.session_id. Idempotent. If the row is
// not yet ingested this is a no-op; the next exit/heal pass or a verification run
// re-links. Best-effort — a missing link never blocks state.
//
// The mechanics are runcore's now, shared with phase and plan runs, which had no
// linking at all before. Two things that were local to dispatch stay dispatch's:
// stamping tasks.session_id (a board card has ONE session; an epic row does not),
// and calling this from the exit path at all.
func (s *Service) linkSession(taskID int64, uuid string) {
	sid, linked := runcore.LinkSession(s.DB, Engine, taskID, uuid)
	if !linked {
		return
	}
	runcore.StampPrimarySession(s.DB, Engine, taskID, sid)
}

// classifyLastTurn fetches the linked session's last assistant turn text (by
// uuid) and classifies its sentinel. Returns an empty Sentinel when the session
// or its text is not available (no transcript yet ⇒ fall through to exit-code
// routing).
func (s *Service) classifyLastTurn(uuid string) Sentinel {
	text := s.lastAssistantText(uuid)
	if text == "" {
		return Sentinel{}
	}
	return ClassifySentinel(text)
}

// lastAssistantText returns a session's final assistant turn text (by uuid), or
// "" when the session/transcript is not (yet) ingested. It feeds both sentinel
// classification and the {previous_stage_output} of the next playbook stage — a
// missing transcript degrades gracefully to an empty carry-forward.
func (s *Service) lastAssistantText(uuid string) string {
	var text sql.NullString
	err := s.DB.QueryRow(`
		SELECT tr.text
		  FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid=? AND tr.role='assistant' AND tr.text IS NOT NULL
		 ORDER BY tr.seq DESC LIMIT 1`, uuid).Scan(&text)
	if err != nil || !text.Valid {
		return ""
	}
	return text.String
}

// ── startup heal ──

// HealStale reclaims tasks left in_progress by a crashed/restarted daemon: move
// them back to todo with a marker so the next Schedule re-admits them
// (provision-heal idiom, scheduler FN semantics). The worktree is kept
// (idempotent re-Acquire reuses it).
//
// "Orphaned" is now PROVEN, not assumed: an executor in its own process group
// outlives a daemon restart, and requeuing its task would put a second executor
// into the worktree the first is still writing in. Survivors are adopted instead
// (adopt.go) — the slot is held until their process exits, and the existing
// evidence-based HealDeadProcess reclaims the task once procwatch sees it die.
func (s *Service) HealStale() error {
	adopted, err := s.adoptSurvivors()
	if err != nil {
		// Best-effort: a failed probe must not leave tasks stuck in_progress.
		log.Printf("error: swarmery dispatch: adoption probe: %v", err)
	}
	// worktree_path IS NOT NULL is the dispatcher-OWNERSHIP guard, and it is load
	// bearing: source='queue' alone stopped meaning "a dispatcher run" once
	// internal/taskcap began minting captured session cards on the board with that
	// same source. A card the user ACCEPTED (triage → in_progress) has no worktree
	// and no run behind it, so requeuing it here would stamp a bogus
	// dispatch_error='daemon restart' on it AND hand it to candidates() — which
	// selects exactly source='queue' AND board_column='todo' — turning every daemon
	// restart into a silent auto-dispatch of work the user only agreed to look at.
	//
	// worktree_path, NOT origin, is the discriminator. origin is immutable by design
	// (api/tasks_board.go refuses to patch origin/capture_key/origin_session_id
	// because capture_key is 0048's permanent idempotency key), so a captured card
	// the user reworks (in_review → todo) and the dispatcher then re-admits is
	// genuinely dispatcher-owned while still reading origin='session'; guarding on
	// origin would strand that row in_progress forever after a crash. admit() writes
	// worktree_path in the SAME UPDATE as board_column='in_progress', so a
	// dispatcher-owned row is never transiently NULL here, and this predicate makes
	// the healed set exactly liveWorktreeCount's live set. It is the mirror of the
	// `worktree_path IS NULL` guard capture uses for the same disjointness.
	//
	// The survivors are excluded by runcore.HealExcluding, which owns the one rule
	// three engines each re-derived: `NOT IN ()` is invalid SQL and `x NOT IN (NULL)`
	// is never true, so the clause may only exist when there is something to exclude.
	n, err := runcore.HealExcluding(s.DB, `UPDATE tasks
		   SET board_column='todo', status='queued', dispatch_error='daemon restart',
		       column_moved_at=?
		 WHERE source='queue' AND board_column='in_progress'
		   AND worktree_path IS NOT NULL`, "id", adopted, s.ts())
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("swarmery dispatch: healed %d stuck in_progress task(s) to todo", n)
	}
	return nil
}

// HealDeadProcess requeues a dispatcher-owned task whose process is PROVABLY gone.
//
// Deliberately narrower than HealStale in its evidence and wider in its reach.
// HealStale runs once at boot and cannot tell a long-lived run from a dead one, so
// it is restricted to in_progress and to that single moment. This one acts only on
// evidence — procwatch observed the process itself and wrote proc_state='dead'
// (internal/procwatch/ticker.go:82-85) — and can therefore also reclaim a task
// parked in 'triage', which is where every stuck task in the live database actually
// sits and which HealStale's predicate never sees.
//
// source='queue' is not a convenience filter. A workspace row's status is a
// projection of the workspace artifacts and is rewritten by internal/wsingest on
// the next scan, so writing it here would be a silent write-then-revert loop that
// reads as success in the data. Deriving a verdict for those rows is
// internal/staleness's job; acting on them is nobody's.
//
// NULL and 'unknown' proc_state are NOT evidence of death and must never match:
// Fusion's first stuck-task detector was "structurally blind to EPHEMERAL EXECUTOR
// agents" and killed everything running longer than ~30 minutes. Absence of a
// liveness signal is absence of evidence.
//
// Does not Poke: healing is a state change, scheduling is the caller's decision —
// the same split HealStale already keeps. Folding a Poke in here also made the
// reclaim untestable, because the scheduler ran the reclaimed task to completion
// before the assertion could observe the requeue.
func (s *Service) HealDeadProcess() error {
	rows, err := s.DB.Query(`
		SELECT t.id, t.external_id, p.path, t.retry_count, t.progress_high_water
		  FROM tasks t JOIN projects p ON p.id = t.project_id
		 WHERE t.source='queue' AND t.status='running'
		   AND t.dispatch_session_uuid IS NOT NULL
		   AND EXISTS (SELECT 1 FROM sessions ds
		                WHERE ds.session_uuid = t.dispatch_session_uuid
		                  AND ds.proc_state = ?)`, procwatch.StateDead)
	if err != nil {
		return err
	}
	type reclaim struct {
		id         int64
		externalID string
		repoRoot   string
		retryCount int
		highWater  int
	}
	var pending []reclaim
	for rows.Next() {
		var r reclaim
		var extID, path sql.NullString
		if err := rows.Scan(&r.id, &extID, &path, &r.retryCount, &r.highWater); err != nil {
			rows.Close()
			return err
		}
		r.externalID, r.repoRoot = extID.String, path.String
		pending = append(pending, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var requeued, parked int
	for _, r := range pending {
		observed, ok := s.observedProgress(r.repoRoot, r.externalID)
		switch {
		case !ok:
			// git could not answer. NOT zero progress: treating an unreadable repo as
			// "nothing advanced" would burn the retry budget of a task that may be
			// progressing fine, and the budget's whole purpose is to be spent on
			// evidence. Leave the row exactly as it is and try again next tick.
			log.Printf("dispatch: task %d: progress unreadable, deferring reclaim", r.id)
			continue
		case observed > r.highWater:
			// Advanced since the last observation: record the new mark and give the
			// task another run without charging it a retry.
			if err := s.bumpProgress(r.id, observed); err != nil {
				log.Printf("error: dispatch: bump progress (task %d): %v", r.id, err)
				continue
			}
			if err := s.requeueDead(r.id); err != nil {
				log.Printf("error: dispatch: requeue (task %d): %v", r.id, err)
				continue
			}
			requeued++
		case s.Cfg.MaxNoProgressRetries > 0 && r.retryCount+1 >= s.Cfg.MaxNoProgressRetries:
			// Bound reached with nothing to show. Park rather than requeue — the same
			// terminal shape verify.pauseExhausted uses, and visible to the operator
			// through the existing pause surface. A fourth `status` value would need a
			// consumer in the board's closed column set and in every UI filter; the
			// cycle stopping and saying why is what terminal means here.
			if err := s.parkNoProgress(r.id); err != nil {
				log.Printf("error: dispatch: park no-progress (task %d): %v", r.id, err)
				continue
			}
			parked++
		default:
			if err := s.chargeRetry(r.id); err != nil {
				log.Printf("error: dispatch: charge retry (task %d): %v", r.id, err)
				continue
			}
			if err := s.requeueDead(r.id); err != nil {
				log.Printf("error: dispatch: requeue (task %d): %v", r.id, err)
				continue
			}
			requeued++
		}
	}
	if requeued > 0 || parked > 0 {
		log.Printf("swarmery dispatch: dead-process heal — %d requeued, %d parked without progress",
			requeued, parked)
	}
	return nil
}

// observedProgress counts commits carrying this task's trailer. The bool reports
// whether the count is TRUSTWORTHY: a git failure, a missing worktree manager or a
// blank repo path all yield (0, false), never (0, true). The distinction is the
// point — a zero that means "could not look" must never be spent as evidence that
// nothing happened.
func (s *Service) observedProgress(repoRoot, externalID string) (int, bool) {
	if s.Wt == nil || strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(externalID) == "" {
		return 0, false
	}
	shas, err := s.Wt.CommitsForTask(repoRoot, externalID)
	if err != nil {
		return 0, false
	}
	return len(shas), true
}

// bumpProgress records observed as a high-water mark. MAX(), never assignment: a
// squash or branch reset lowers the observable count, and a mark that followed it
// down would read as fresh progress next pass (Fusion requeue-loop.ts:32).
func (s *Service) bumpProgress(id int64, observed int) error {
	_, err := s.DB.Exec(
		`UPDATE tasks SET progress_high_water = MAX(progress_high_water, ?) WHERE id=?`,
		observed, id)
	return err
}

func (s *Service) chargeRetry(id int64) error {
	_, err := s.DB.Exec(`UPDATE tasks SET retry_count = retry_count + 1 WHERE id=?`, id)
	return err
}

func (s *Service) requeueDead(id int64) error {
	if _, err := s.DB.Exec(`
		UPDATE tasks SET board_column='todo', status='queued',
		                 dispatch_error='dispatch process gone (procwatch: dead)',
		                 column_moved_at=?
		 WHERE id=? AND source='queue'`, s.ts(), id); err != nil {
		return err
	}
	s.notify(id)
	return nil
}

func (s *Service) parkNoProgress(id int64) error {
	marker := fmt.Sprintf("no progress after %d re-dispatch(es)", s.Cfg.MaxNoProgressRetries)
	if _, err := s.DB.Exec(`
		UPDATE tasks SET paused=1, dispatch_error=?, column_moved_at=?
		 WHERE id=? AND source='queue'`, marker, s.ts(), id); err != nil {
		return err
	}
	log.Printf("dispatch: task %d parked: %s", id, marker)
	s.notify(id)
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

// lockedDownError is the documented dispatch_error a locked-down project's
// Todo tasks carry (DESIGN.md §2 item 11).
const lockedDownError = "project locked down"

// projectLockedDown reports whether a project's permission preset (fusion phase
// 11) is 'locked-down'. Read via inline SQL — dispatch must not import approvals
// (that package imports ingest/notify, and dispatch stays a leaf of the data
// layer). A missing row ⇒ not locked (the fail-closed default is
// approval-required, which does NOT block dispatch). A read error is logged and
// treated as not-locked (same posture as isPaused): the hard stops are the
// kill-switch/pause, and a locked-down project has zero compiled auto-approve
// rules so nothing can auto-approve regardless of this gate.
func (s *Service) projectLockedDown(projectID int64) bool {
	var preset string
	err := s.DB.QueryRow(
		`SELECT preset FROM project_permission_presets WHERE project_id = ?`, projectID).Scan(&preset)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		log.Printf("error: dispatch: read preset for project %d: %v", projectID, err)
		return false
	}
	return preset == "locked-down"
}

// stampLockedDown records the locked-down error on a Todo task — but only when
// it is not already set, so a locked project does not re-emit task_updated on
// every poll pass (the row is already parked in todo; nothing changed).
func (s *Service) stampLockedDown(id int64) {
	var cur sql.NullString
	if err := s.DB.QueryRow(`SELECT dispatch_error FROM tasks WHERE id=?`, id).Scan(&cur); err != nil {
		log.Printf("error: dispatch: read dispatch_error (task %d): %v", id, err)
		return
	}
	if cur.Valid && cur.String == lockedDownError {
		return // already stamped — stay quiet
	}
	s.failAdmission(id, lockedDownError)
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
	Enabled       bool     `json:"enabled"`      // kill-switch state
	GlobalPaused  bool     `json:"globalPaused"` // durable global pause flag
	MaxConcurrent int      `json:"maxConcurrent"`
	MaxWorktrees  int      `json:"maxWorktrees"`
	ActiveRuns    int      `json:"activeRuns"`   // live BOARD runs in this process
	FreeSlots     int      `json:"freeSlots"`    // maxConcurrent - activeRuns (>=0)
	PausedScopes  []string `json:"pausedScopes"` // every currently-paused scope

	// MaxRuns/RunsInFlight/RunSlots describe the DAEMON-WIDE budget
	// (SWARMERY_MAX_RUNS), which board, phase and plan runs share. Without them the
	// board's own numbers explain nothing when admission stalls: activeRuns 0 and
	// freeSlots 2 while nothing starts is exactly what a machine full of phase runs
	// looks like from here. RunSlots lists every holder so the answer is visible
	// rather than inferable.
	MaxRuns      int                `json:"maxRuns"`
	RunsInFlight int                `json:"runsInFlight"`
	RunSlots     []runcore.SlotInfo `json:"runSlots"`
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
		MaxRuns:       s.Slots.Max(),
		RunsInFlight:  s.Slots.Count(),
		RunSlots:      s.Slots.Snapshot(),
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
