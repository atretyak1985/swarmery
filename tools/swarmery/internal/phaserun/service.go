// Package phaserun executes ONE plan phase directly from its phase doc
// (interactive planning v2 phase 5): a headless `claude -p` run in an isolated
// git worktree, state tracked on epic_phases (run_state / run_session_uuid /
// run_started_at / run_ended_at / run_error, plus the run's measurement interval
// run_checkboxes_before → run_checkboxes_after). No `tasks` row, no board
// involvement — progress
// (checkbox ticks) keeps flowing through wsingest as the executor edits the
// phase docs in the private workspace.
//
// Modeled on internal/planning's service (single-flight map + spawn seam +
// Notify) and reusing internal/dispatch's WorktreeManager + internal/worktree
// mechanics — nothing is duplicated.
package phaserun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Sentinel errors mapped to HTTP statuses by the api layer.
var (
	// ErrPhaseNotFound: no epic_phases row for the given id (404).
	ErrPhaseNotFound = errors.New("phase not found")
	// ErrRunning: this phase already has a run in flight (409).
	ErrRunning = errors.New("a run is already active for this phase")
	// ErrDepsUnmet: one or more dependsOn phases are not complete (409).
	// Returned as a *DepsUnmetError, which errors.Is-matches this sentinel.
	ErrDepsUnmet = errors.New("phase has unmet dependencies")
	// ErrNoDoc: the phase doc could not be read (409).
	ErrNoDoc = errors.New("phase doc is unreadable")
	// ErrNoPath: the project has no filesystem path to run in (409).
	ErrNoPath = errors.New("project has no known path to run in")
	// ErrBranchDirty: the previous run's branch still exists and holds commits, so
	// the deterministic branch name cannot be reclaimed automatically. Returned as
	// a *BranchDirtyError, which errors.Is-matches this sentinel.
	ErrBranchDirty = errors.New("run branch has unmerged commits")
	// ErrPlanRunning: a run of the WHOLE plan this phase belongs to is in flight
	// (409). The mirror of planrun.ErrPhaseRunning, and the reason it exists is the
	// same in both directions: a plan run and a phase run of the same plan edit the
	// same docs from two different worktrees, so whichever starts second is
	// overwriting the first's work. Only planrun checked before this; the asymmetry
	// meant a phase started DURING a live plan run put two orchestrators on one plan.
	ErrPlanRunning = errors.New("a plan run is active for this plan")
	// ErrNoRunBranch: the phase has no recorded run_branch (0043), so there is no
	// branch this service is willing to name — either it never ran, or it ran before
	// the column existed and the backfill did not reach it. Deliberately NOT a
	// fallback to the derived "swarm/phase-<id>": after a doc rename that name is a
	// branch that does not exist, and deleting it would report success while the
	// branch actually holding the commits survives (409).
	ErrNoRunBranch = errors.New("phase has no recorded run branch")
)

// ErrNoRepoRoot: the project has a path, but neither it nor the repo this phase
// declares is a git repository (409). Distinct from ErrNoPath ("no path at all"):
// the fix here is a `Repo` header in the phase doc or project.json's mainApp, and
// the wrapped repopath error names every candidate that was checked.
var ErrNoRepoRoot = repopath.ErrNoRepoRoot

// ErrRepoOutsideProject: the phase doc declares a repo that IS a git checkout but
// lies outside the project and is not a registered project (409). Refused at
// admission on purpose — before this, the declaration was silently dropped and the
// phase ran in the project checkout, where its instructions matched nothing.
var ErrRepoOutsideProject = repopath.ErrRepoOutsideProject

// BranchDirtyError names the blocking branch and how many commits would be lost, so
// the api's 409 body and the UI can offer an explicit delete-or-merge decision
// instead of silently destroying work.
type BranchDirtyError struct {
	Branch       string
	CommitsAhead int
	// Base is the branch CommitsAhead was measured against — the repo's current
	// checkout, because worktree.ReclaimEmptyBranch counts against the same start
	// point Acquire pins to. The same branch is "3 commits ahead" of dev and "0
	// ahead" of a feature branch that already contains them, so a 409 that does not
	// name its base cannot be told apart from base skew. Empty when the base could
	// not be named (no Git seam wired, detached HEAD, git failure) — never guessed.
	Base string
}

func (e *BranchDirtyError) Error() string {
	return fmt.Sprintf("run branch %s has %d unmerged commit(s)", e.Branch, e.CommitsAhead)
}

func (e *BranchDirtyError) Is(target error) bool { return target == ErrBranchDirty }

// DepsUnmetError carries WHICH dependency seqs are unmet so the api's 409 body
// can name them. errors.Is(err, ErrDepsUnmet) matches.
type DepsUnmetError struct {
	Unmet []int // dependency seq numbers not yet satisfied
}

func (e *DepsUnmetError) Error() string {
	return fmt.Sprintf("phase has unmet dependencies: phases %v", e.Unmet)
}

func (e *DepsUnmetError) Is(target error) bool { return target == ErrDepsUnmet }

// Service owns the phase-run lifecycle: gate checks, worktree acquisition,
// spawn, exit stamping, and startup heal. Notify (wired to the api layer's
// plan_updated publisher) is keyed by the WORKSPACE task id so the Plans page
// refetches on run edges.
type Service struct {
	DB  *sql.DB
	Wt  runcore.WorktreeManager // shared worktree mechanics (runcore's seam)
	Run Runner
	// Git is an OPTIONAL read-only seam, used for one thing: naming the branch a
	// commits-ahead count was measured against (BranchDirtyError.Base). nil ⇒ the
	// base is reported as unknown; no run behaviour depends on it.
	Git worktree.Git
	// RepoRoot resolves the git repository a run executes in from the project path
	// and the repo the phase declares. nil ⇒ repopath.Resolve. A seam because the
	// production resolver stats the filesystem, and the run gates have to be
	// testable without a real checkout on disk.
	RepoRoot func(projectPath string, cells ...string) (string, error)
	UUID     func() string    // session-uuid generator (test seam; default runcore.NewUUID)
	now      func() time.Time // clock (test seam; default time.Now)
	Go       func(func())     // async-spawn seam (nil ⇒ real `go`); mirrors planning.Go
	// Notify emits plan_updated for the phase's workspace task at run edges.
	// nil ⇒ no live nudge (guarded).
	Notify func(taskID int64)
	// FindRun locates the live process of a run by its session uuid (adopt.go).
	// nil ⇒ a ps scan. Test seam: adoption must be exercisable without spawning.
	FindRun func(sessionUUID string) (int, bool)
	// ProcAlive reports whether an adopted pid still exists. nil ⇒ signal-0 probe.
	ProcAlive func(pid int) bool

	// Verify grades a finished run whose doc opted in (`**Verify:** strict`), through
	// the same engine board cards go through. nil ⇒ phase verification not wired,
	// which is both the unit tests' state and a valid production state (the daemon
	// wires it from the verify service). See verifyRun for the ordering contract: it
	// runs BEFORE the worktree is reclaimed, because the worktree is the subject.
	Verify runcore.PhaseVerifier
	// Slots is the DAEMON-WIDE run registry and budget (internal/runcore): the
	// per-phase single-flight gate AND — new — a bound this engine never had. A
	// phase run used to be limited by nothing at all: ten phases started from the
	// UI were ten `claude` processes. NewService gives every Service its own pool
	// (hermetic unit tests); the daemon replaces it with the one instance dispatch
	// and planrun also hold.
	Slots *runcore.Slots
	// adoptPoll overrides runcore.AdoptPollInterval when > 0 (tests shrink it).
	adoptPoll time.Duration
}

// NewService builds a phase-run service. The caller wires DB + Run
// (ClaudeRunner) + Wt (the shared worktree.Manager); UUID/now/Go default to
// production impls.
func NewService(db *sql.DB, r Runner, wt runcore.WorktreeManager) *Service {
	return &Service{
		DB:    db,
		Run:   r,
		Wt:    wt,
		UUID:  runcore.NewUUID,
		now:   time.Now,
		Slots: runcore.NewSlots(0),
	}
}

// Engine names phase runs in the shared slot registry: "phaserun:<epic_phases.id>".
const Engine = "phaserun"

func (s *Service) slotKey(phaseID int64) string { return runcore.SlotKey(Engine, phaseID) }

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) ts() string { return s.clock().UTC().Format(time.RFC3339) }

func (s *Service) spawn(fn func()) { runcore.Go(Engine, s.Go, fn) }

func (s *Service) notify(taskID int64) {
	if s.Notify != nil {
		s.Notify(taskID)
	}
}

// phaseInfo is the Start admission read: the phase joined to its epic task and
// project.
type phaseInfo struct {
	WorkspaceTaskID int64
	Seq             int
	DocPath         string
	DependsOn       []int
	RunState        string
	// ProjectPath is projects.path — the project ROOT, which for a multi-repo
	// project is an umbrella dir and NOT a checkout. Never hand it to git; hand it
	// to runRoot.
	ProjectPath string
	ProjectSlug string
	// WorkspaceRoot is the workspace namespace dir (workspaces.root_path), home of
	// overlay/project.json — one of the repo-hint sources. "" when unmapped.
	WorkspaceRoot string
	// Repo is the RAW declared Repo cell from epic_phases.repo (migration 0046);
	// "" when the phase doc declares nothing.
	Repo string
	// RepoRoot is the resolved repository this run executes in, set by Start before
	// the worktree is acquired. Every Wt call must use it.
	RepoRoot string
	// RunBranch is the branch a previous run committed to, as STAMPED at spawn
	// (migration 0043) — never re-derived from the row id. epic_phases identity is
	// doc_path, so a renamed or regenerated phase doc replaces the row and mints a
	// new id; a derived name would then point at a branch that does not exist while
	// the real one, holding the run's commits, became unreachable. Empty only for a
	// phase that has never run.
	RunBranch string
	// Name is the phase's title, which is what the verifier is told it is grading.
	Name string
	// VerifyMode is the phase DOC's opt-in to post-run verification
	// (`**Verify:** strict`, wsingest.ParseDocVerify), off|normal|strict. Read at
	// ADMISSION, not at exit: the mode that was in effect when the operator pressed
	// Run is the contract this run answers to, and a doc rescan mid-run must not
	// retroactively decide the run should (or should not) have been graded.
	VerifyMode string
}

// runRoot resolves the repository this phase runs in: what the phase doc declares
// first, then the workspace overlay's project.json, then the checkout's own
// .claude/project.json. repopath.Resolve appends ProjectPath as the last
// candidate, which is what keeps every single-repo project resolving as before.
func (s *Service) runRoot(info phaseInfo) (string, error) {
	var cells []string
	if strings.TrimSpace(info.Repo) != "" {
		cells = append(cells, info.Repo)
	}
	if info.WorkspaceRoot != "" {
		cells = append(cells, repopath.FileHints(filepath.Join(info.WorkspaceRoot, "overlay", "project.json"))...)
	}
	cells = append(cells, repopath.FileHints(filepath.Join(info.ProjectPath, ".claude", "project.json"))...)

	resolve := s.RepoRoot
	if resolve == nil {
		// Registered projects are the trusted roots: a declared repo outside this
		// project is honoured only when it is one of them (runcore.RegisteredRoots).
		resolve = func(projectPath string, cells ...string) (string, error) {
			return repopath.ResolveTrusted(projectPath, runcore.RegisteredRoots(s.DB), cells...)
		}
	}
	return resolve(info.ProjectPath, cells...)
}

// Start admits a run for a phase: gates (single-flight, deps, doc, path), then
// acquires a worktree, stamps run_state='running', and spawns the headless
// executor. Returns the pre-generated session uuid so the caller answers 202
// immediately. The run's own goroutine owns exit stamping, worktree removal
// (branch kept), and slot release.
func (s *Service) Start(phaseID int64) (sessionUUID string, err error) {
	info, err := s.loadPhase(phaseID)
	if err != nil {
		return "", err
	}
	if info.RunState == "running" {
		return "", ErrRunning
	}
	// The mirror of planrun's phaseRunActive gate: refuse while the whole plan is
	// being run. Both directions are needed — one of them alone is not "mostly
	// safe", it is a gate that only closes if the operator happens to press the
	// buttons in one particular order.
	if busy, err := s.planRunActive(info.WorkspaceTaskID); err != nil {
		return "", err
	} else if busy {
		return "", ErrPlanRunning
	}
	if info.ProjectPath == "" {
		return "", ErrNoPath
	}
	if unmet, err := s.unmetDeps(info); err != nil {
		return "", err
	} else if len(unmet) > 0 {
		return "", &DepsUnmetError{Unmet: unmet}
	}
	doc, err := os.ReadFile(info.DocPath)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNoDoc, info.DocPath)
	}
	// Resolve the repository BEFORE anything hands a path to git: projects.path is
	// the project ROOT, which for a multi-repo project is not a checkout at all, and
	// handing it to the branch probe is what made every run in such a project die
	// during admission with git's "fatal: not a git repository". Still ahead of the
	// single-flight slot — a resolution failure is an admission verdict and must
	// leave no state behind.
	info.RepoRoot, err = s.runRoot(info)
	if err != nil {
		return "", err
	}

	// Run slot BEFORE the (slow, git-touching) Acquire so a concurrent Start cannot
	// double-spawn; released on any admission failure below. The slot is now
	// daemon-wide (runcore.Slots), so this is also where a phase run finally
	// respects a budget: it used to be bounded by nothing, and ten phases started
	// from the UI were ten `claude` processes on one machine.
	uuid := s.UUID()
	ctx, cancel := context.WithCancel(context.Background())
	releaseSlot, err := s.Slots.TryAcquire(s.slotKey(phaseID), uuid, cancel)
	if err != nil {
		cancel()
		// ErrBusy is this phase already running — the sentinel the API already
		// renders as "already-running". ErrNoSlot is a FULL POOL, a different answer
		// that travels as itself (it names its holders) and must never be reported as
		// a failed run: nothing was stamped, and the caller may simply retry.
		if errors.Is(err, runcore.ErrBusy) {
			return "", ErrRunning
		}
		return "", err
	}

	release := func() {
		cancel()
		releaseSlot()
	}

	// Every teardown removes the worktree with keepBranch=true, so the PREVIOUS
	// run's swarm/phase-<id> is still there and Acquire would fail ErrBranchExists.
	// Reclaim it first when it is empty; refuse loudly when it holds work.
	//
	// The name comes from runcore, which owns the one derivation worktree.Acquire
	// performs (taskName ⇒ swarm/<taskName>): the pair below must agree, and when the
	// literals were spelled out here that agreement was a comment rather than code.
	taskName := runcore.PhaseTaskName(phaseID)
	branch := runcore.PhaseBranch(phaseID)
	ahead, err := s.Wt.ReclaimEmptyBranch(info.RepoRoot, branch)
	if err != nil {
		release()
		return "", fmt.Errorf("reclaim run branch: %w", err)
	}
	if ahead > 0 {
		release()
		return "", &BranchDirtyError{Branch: branch, CommitsAhead: ahead, Base: s.baseBranch(info.RepoRoot)}
	}

	// A run this phase performed under a PREVIOUS row id left its commits on a branch
	// the deterministic name above no longer reaches (epic_phases identity is doc_path,
	// so a renamed doc mints a new id). run_branch is the only record of it. Reclaim it
	// too: empty ⇒ a harmless leftover name, deleted; non-empty ⇒ real work that this
	// run would strand for ever, so refuse and let the operator decide — the same
	// contract the deterministic branch gets, applied to the branch that actually holds
	// the commits.
	if prev := info.RunBranch; prev != "" && prev != branch {
		prevAhead, err := s.Wt.ReclaimEmptyBranch(info.RepoRoot, prev)
		if err != nil {
			release()
			return "", fmt.Errorf("reclaim previous run branch %s: %w", prev, err)
		}
		if prevAhead > 0 {
			release()
			return "", &BranchDirtyError{Branch: prev, CommitsAhead: prevAhead, Base: s.baseBranch(info.RepoRoot)}
		}
	}

	acq, err := s.Wt.Acquire(info.RepoRoot, info.ProjectSlug, taskName)
	if err != nil {
		release()
		return "", fmt.Errorf("worktree acquire: %w", err)
	}

	// run_checkboxes_before=checkboxes_done snapshots the ticked-criteria baseline
	// in the SAME statement — no extra round trip, and no race with a concurrent
	// wsingest rescan. The delta against it is what proves work actually landed.
	// Opening the interval resets BOTH edges: run_checkboxes_after must not keep
	// the PREVIOUS run's stamp, or a running phase's diagnosis quotes a right edge
	// belonging to a different run — and a daemon crash mid-run freezes that
	// mismatch in place (stamp() would otherwise heal it at exit).
	// run_branch is stamped in the SAME statement that opens the run: the branch must
	// be recorded before anything can commit to it, or a crash between the two writes
	// leaves commits on a branch nothing names (migration 0043).
	//
	// run_start_point joins it for the same reason and is recorded rather than
	// re-derived (migration 0057, mirroring tasks.start_point from 0051): it is the SHA
	// Acquire actually pinned this worktree to, and it is the ONLY honest base for the
	// verifier's `diff base...HEAD`. Re-deriving it at exit would read whatever the
	// repo's HEAD has moved to since — and falling back to the branch would diff the
	// branch against itself, which is empty by construction and grades landed work as
	// "nothing was done".
	if _, err := s.DB.Exec(`
		UPDATE epic_phases
		   SET run_state='running', run_session_uuid=?, run_started_at=?,
		       run_error=NULL, run_ended_at=NULL, run_branch=?,
		       run_start_point=NULLIF(?, ''),
		       run_checkboxes_before=checkboxes_done, run_checkboxes_after=NULL
		 WHERE id=?`, uuid, s.ts(), branch, acq.StartPoint, phaseID); err != nil {
		// Worktree FIRST, slot LAST — the same invariant runAndHandle's defer
		// enforces. Releasing the slot while the worktree still exists lets a
		// concurrent Start warm-reuse (worktree invariant 4) the deterministic
		// phase-<id> path we are about to delete; the failed UPDATE is precisely
		// the write that would have closed the DB gate, so nothing else holds it.
		s.removeWorktree(info.RepoRoot, acq)
		release()
		return "", err
	}

	// Attach this run's session to the PLAN it serves (epic_phases.workspace_task_id
	// — the epic's own tasks row). Almost always a no-op here: the process has not
	// started, so its transcript cannot be ingested yet. The call belongs at the
	// start anyway, because a daemon that dies mid-run leaves the link to wsingest's
	// reconcile pass, and that pass reads the same run_session_uuid this write has
	// already stamped. runAndHandle links again at exit.
	runcore.LinkSession(s.DB, Engine, info.WorkspaceTaskID, uuid)

	log.Printf("phaserun: start phase=%d task=%d uuid=%s worktree=%q", phaseID, info.WorkspaceTaskID, uuid, acq.Path)
	s.notify(info.WorkspaceTaskID)

	// Lend the phase doc INTO the worktree and quote it by its relative path.
	// The contract's first line makes this worktree the agent's one root; an
	// instruction to edit the workspace copy by absolute path contradicts that
	// and is refused by the sandbox. A lend failure is not fatal — the run still
	// has the doc's CONTENT inlined in the prompt below, so it degrades to the
	// old "read-only view of the doc" rather than to no run at all.
	docRel, lendErr := worktree.LendPlanDoc(acq.Path, info.DocPath)
	if lendErr != nil {
		log.Printf("warning: phaserun: phase=%d could not lend the plan doc into %s: %v", phaseID, acq.Path, lendErr)
	}
	prompt := BuildPromptIn(docRel, filepath.Base(info.DocPath), string(doc), info.RepoRoot, info.ProjectPath)
	spec := RunSpec{
		Prompt:       prompt,
		SessionUUID:  uuid,
		Cwd:          acq.Path,
		SettingsFile: repopath.InheritedSettings(info.ProjectPath, info.RepoRoot, acq.Path),
		ProjectPath:  info.ProjectPath,
	}
	if spec.SettingsFile != "" {
		log.Printf("phaserun: phase=%d inheriting project settings %s (worktree is a checkout of %s)",
			phaseID, spec.SettingsFile, info.RepoRoot)
	}
	s.spawn(func() { s.runAndHandle(ctx, cancel, releaseSlot, phaseID, info, acq, spec, docRel) })
	return uuid, nil
}

// runAndHandle executes the run to completion, stamps the exit state, optionally
// verifies the work, removes the worktree (branch kept), and always releases the slot.
func (s *Service) runAndHandle(ctx context.Context, cancel context.CancelFunc, releaseSlot func(), phaseID int64, info phaseInfo, acq worktree.Acquired, spec RunSpec, docRel string) {
	// The terminal state, read by the defer below. Only a run that ENDED CLEANLY is
	// worth grading: a cancelled or crashed executor may have left the tree mid-edit,
	// and a verdict on that measures the interruption, not the work.
	endState := ""
	defer func() {
		cancel()
		// Verify BEFORE the worktree goes away — the worktree IS the thing being
		// graded, and removeWorktree below deletes the only copy of it. This is the
		// same ordering argument as worktree-before-slot, one step earlier in the
		// sequence, and it is why verification lives in the defer at all rather than
		// after the switch: every exit path has to pass through it in this order.
		s.verifyRun(phaseID, info, acq, endState)
		// Return the doc BEFORE removeWorktree deletes the only copy of it, and
		// on EVERY exit path — done, blocked, failed, cancelled. The dashboard
		// renders the workspace copy's `## Completion Report` and nothing else,
		// so a report that stays in the worktree is work that shipped and reads
		// as "no summary of the work written". A report about why a run STOPPED
		// is the one most worth not losing.
		worktree.ReturnPlanDocLogged(fmt.Sprintf("phaserun phase=%d", phaseID), acq.Path, docRel, info.DocPath)
		// Worktree FIRST, slot LAST. stamp() has already moved the row off
		// 'running', so the DB gate in Start is open; releasing the single-flight
		// slot before the (git shell-out, tens of ms) removal opens a window where a
		// re-Start re-acquires the SAME deterministic worktree path (worktree
		// invariant-4 reuse) and this defer then rips the new run's worktree out
		// from under it.
		s.removeWorktree(info.RepoRoot, acq)
		releaseSlot()
		// The reconcile arm: by now the transcript exists, so this is where the link
		// usually actually lands. Still not guaranteed — ingest is a separate pipeline
		// with its own lag — which is why wsingest converges the rest.
		runcore.LinkSession(s.DB, Engine, info.WorkspaceTaskID, spec.SessionUUID)
		s.notify(info.WorkspaceTaskID)
	}()

	res, err := s.Run.Start(ctx, spec)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		// Cancel() beat the exit — whatever the child reported, the outcome is a
		// user cancellation.
		log.Printf("phaserun: phase=%d uuid=%s cancelled", phaseID, spec.SessionUUID)
		endState = "failed"
		s.stamp(phaseID, info.DocPath, "failed", "cancelled")
	case err != nil:
		log.Printf("error: phaserun: phase=%d uuid=%s could not start: %v", phaseID, spec.SessionUUID, err)
		endState = "failed"
		s.stamp(phaseID, info.DocPath, "failed", err.Error())
	case res.TimedOut:
		log.Printf("warning: phaserun: phase=%d uuid=%s timed out", phaseID, spec.SessionUUID)
		endState = "failed"
		s.stamp(phaseID, info.DocPath, "failed", "timeout")
	case res.ExitCode != 0:
		log.Printf("warning: phaserun: phase=%d uuid=%s exited %d: %s", phaseID, spec.SessionUUID, res.ExitCode, res.Stderr)
		msg := res.Stderr
		if msg == "" {
			msg = fmt.Sprintf("exit %d", res.ExitCode)
		}
		endState = "failed"
		s.stamp(phaseID, info.DocPath, "failed", msg)
	default:
		log.Printf("phaserun: phase=%d uuid=%s completed in %s", phaseID, spec.SessionUUID, res.Duration)
		endState = "done"
		s.stamp(phaseID, info.DocPath, "done", "")
	}
}

// verifyRun grades a finished phase run when its doc opted in (`**Verify:** strict`),
// via the SAME engine that grades board cards (internal/verify.VerifyTarget behind the
// runcore.PhaseVerifier seam). The verdict lands on epic_phases as an INPUT to the
// phase's diagnosis — phasediag turns a fail into a `verify-failed` blocker — never as
// a second status: the checkboxes remain the only truth about progress (decision D5).
//
// BLOCKING, on purpose, and called from runAndHandle's defer before removeWorktree:
// the run's slot stays held for the duration, which is exactly right. The slot is what
// keeps a retry from warm-reusing the deterministic worktree path out from under the
// verifier that is reading it.
//
// Skipped when: verification is not wired (no seam), the doc did not opt in, the run
// did not end cleanly, or the worktree is unknown. Every skip is silent except an
// actual verification error, which is logged and dropped — a failed grade must never
// turn a phase run that DID land work into a reported failure.
func (s *Service) verifyRun(phaseID int64, info phaseInfo, acq worktree.Acquired, endState string) {
	if s.Verify == nil || endState != "done" || acq.Path == "" {
		return
	}
	// `off` (and the empty string a pre-0057 row carries) is the default: a plan that
	// never asked keeps today's behaviour exactly. Checked here as well as inside the
	// verifier so an opted-out phase does not even pay a doc read.
	if info.VerifyMode == "" || info.VerifyMode == wsingest.VerifyOff {
		return
	}
	// The doc as it stands NOW: the executor's ticks are part of what is being
	// graded, and the criteria list is the contract the verifier judges against.
	// Unreadable ⇒ nothing to grade against, so skip rather than send an empty
	// contract the verifier would have to guess at.
	doc, err := os.ReadFile(info.DocPath)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d verify skipped, doc %q unreadable: %v", phaseID, info.DocPath, err)
		return
	}
	log.Printf("phaserun: phase=%d verifying (%s) worktree=%q", phaseID, info.VerifyMode, acq.Path)
	// context.Background(), not the run's ctx: the defer's cancel() has already fired,
	// and this is a NEW read-only run with its own timeout — reusing the dead child's
	// cancelled context would kill the verifier before it started.
	if err := s.Verify.VerifyPhase(context.Background(), runcore.PhaseVerifyRequest{
		PhaseID:         phaseID,
		WorkspaceTaskID: info.WorkspaceTaskID,
		Mode:            info.VerifyMode,
		WorktreePath:    acq.Path,
		Branch:          acq.Branch,
		StartPoint:      acq.StartPoint,
		Title:           info.Name,
		Prompt:          string(doc),
		ProjectPath:     info.ProjectPath,
	}); err != nil {
		log.Printf("error: phaserun: phase=%d verify: %v", phaseID, err)
	}
}

// tickedInDoc counts the acceptance criteria ticked in the phase doc as it stands
// on disk right now, through the SAME parser that defines
// epic_phases.checkboxes_done (wsingest.CountCheckboxes) — never a second copy of
// the format, which would drift and make the two counts disagree about one file.
// ok=false when there is no readable doc.
func tickedInDoc(docPath string) (int, bool) {
	if docPath == "" {
		return 0, false
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return 0, false
	}
	done, _ := wsingest.CountCheckboxes(string(body))
	return done, true
}

// stamp writes the terminal run state; runError "" ⇒ NULL. run_ended_at is set on
// every terminal transition so the UI can show a duration, and
// run_checkboxes_after closes the measurement interval opened by
// run_checkboxes_before at spawn, so the right edge is pinned at the instant the
// run ends rather than drifting with every later writer of checkboxes_done.
//
// The right edge is counted from the DOC, not from checkboxes_done. That column is
// owned by internal/wsingest, which rescans on a 500 ms debounce and is triggered by
// nothing at run end: an executor whose final tick lands inside that window exits
// while the column still holds the pre-tick count, and the interval closes on it.
// phasediag.OutcomeFromRow then prefers the stamped edge over the live count
// FOREVER, so a phase whose work actually landed is chipped 'noop' permanently and
// silently. Reading the artifact the executor wrote has no such window.
//
// COALESCE keeps the fallback explicit: a doc that cannot be read (workspace moved,
// plan rescan mid-run) still closes the interval on the live count, because leaving
// run_checkboxes_after NULL would hand the outcome back to the column that keeps
// moving.
func (s *Service) stamp(phaseID int64, docPath, state, runError string) {
	var re any
	if runError != "" {
		re = runError
	}
	var after any // NULL ⇒ COALESCE falls back to checkboxes_done
	if n, ok := tickedInDoc(docPath); ok {
		after = n
	} else {
		log.Printf("warning: phaserun: stamp phase=%d: phase doc %q unreadable, closing the run interval on the live count instead",
			phaseID, docPath)
	}
	res, err := s.DB.Exec(`
		UPDATE epic_phases
		   SET run_state=?, run_error=?, run_ended_at=?,
		       run_checkboxes_after=COALESCE(?, checkboxes_done)
		 WHERE id=?`, state, re, s.ts(), after, phaseID)
	if err != nil {
		log.Printf("error: phaserun: stamp phase=%d state=%s: %v", phaseID, state, err)
		return
	}
	// Zero rows means the phase row vanished mid-run — historically a rescan
	// deleting and re-inserting it. Silent data loss; log it loudly. The driver
	// error is handled rather than discarded: this branch exists precisely to catch
	// a lost write, and swallowing the one error that says "I cannot tell you
	// whether the write landed" defeats it.
	n, err := res.RowsAffected()
	switch {
	case err != nil:
		log.Printf("error: phaserun: stamp phase=%d state=%s: rows affected unavailable: %v", phaseID, state, err)
	case n == 0:
		log.Printf("error: phaserun: stamp phase=%d state=%s: row vanished mid-run", phaseID, state)
	}
}

// baseBranch names the repo's current checkout — the branch
// worktree.ReclaimEmptyBranch's commit count is relative to (it resolves its start
// point from the same symbolic HEAD Acquire does). Purely descriptive: any failure,
// a detached HEAD, or no Git seam yields "" and the consumer omits the base rather
// than naming one that was not measured.
func (s *Service) baseBranch(repoRoot string) string {
	if s.Git == nil || repoRoot == "" {
		return ""
	}
	out, err := s.Git.Run(repoRoot, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// removeWorktree best-effort removes the run's worktree, KEEPING the branch
// (its commits stay reachable for the user — mirrors dispatch.removeWorktree).
func (s *Service) removeWorktree(repoRoot string, acq worktree.Acquired) {
	if s.Wt == nil {
		return
	}
	if err := s.Wt.Remove(repoRoot, acq, true /* keepBranch */); err != nil {
		log.Printf("warning: phaserun: remove worktree %s: %v", acq.Path, err)
	}
}

// Cancel aborts an in-flight run: the context cancel kills the child claude,
// and the run goroutine's exit path stamps failed/cancelled. Returns whether a
// run was actually in flight.
func (s *Service) Cancel(phaseID int64) bool {
	// Slots.Cancel invokes the run's cancel and leaves the slot held: the run
	// goroutine's own exit path releases it, so a Retry cannot get in before the
	// dying run has let go of its worktree.
	return s.Slots.Cancel(s.slotKey(phaseID))
}

// DeleteRunBranch force-deletes a phase's run branch, INCLUDING one that holds
// commits — the explicit user decision behind a BranchDirtyError. Refuses while the
// branch is checked out or a run is in flight for this phase.
//
// existed reports whether the branch was actually there: worktree.DeleteBranch is
// idempotent (a missing branch is a silent nil), so a caller with only an error to
// read cannot tell a real deletion from a no-op and would claim "deleted" either
// way — the UI then clears its dirty-branch banner on nothing. Same shape as
// planrun.DeleteRunBranch, so the two run surfaces never disagree.
func (s *Service) DeleteRunBranch(phaseID int64) (branch string, existed bool, err error) {
	info, err := s.loadPhase(phaseID)
	if err != nil {
		return "", false, err
	}
	if info.ProjectPath == "" {
		return "", false, ErrNoPath
	}
	// A live run owns the branch; deleting it underneath would strand its commits.
	if s.Slots.IsActive(s.slotKey(phaseID)) {
		return "", false, ErrRunning
	}
	// The branch STAMPED at spawn (0043) — never re-derived from the row id. Deriving
	// it here would name swarm/phase-<current id>, which after a doc rename is a branch
	// that does not exist, while the one holding the run's commits survives untouched:
	// a delete that reports success and destroys nothing. No fallback for the same
	// reason — a fallback reinstates exactly that failure.
	branch = info.RunBranch
	if branch == "" {
		return "", false, ErrNoRunBranch
	}
	// The branch lives in the repository the run resolved to, not at the project
	// root — deleting it anywhere else finds nothing and reports a no-op deletion
	// as success.
	root, err := s.runRoot(info)
	if err != nil {
		return "", false, err
	}
	existed, err = s.Wt.DeleteBranch(root, branch)
	if err != nil {
		return "", false, err
	}
	if existed {
		log.Printf("phaserun: deleted run branch %s (phase=%d)", branch, phaseID)
	}
	return branch, existed, nil
}

// HealStale settles every epic_phases row left 'running' by a crashed or
// restarted daemon. It does NOT assume they are all dead: a run spawned in its
// own process group survives a daemon restart and keeps working, so each row is
// probed first — a run whose process is still there is re-adopted (state stays
// 'running', slot held, watcher stamps its exit; see adopt.go), and only rows
// with no live process are failed. Called from cmd/swarmery before serving.
//
// CAVEAT for any consumer deriving a duration: run_ended_at here is the RESTART
// time, not the moment the run actually died — the daemon has no record of that.
// A run orphaned for three days therefore reports a three-day duration. Callers
// building a duration DTO must suppress or flag it when run_error='daemon restart'.
//
// run_checkboxes_after is stamped alongside run_ended_at for the same reason
// stamp() does it: admission opens the interval by resetting the right edge to
// NULL, and a terminal transition that leaves it open leaves the run measured
// against the LIVE count (phasediag.OutcomeFromRow's fallback), which every later
// wsingest rescan and checklist tick moves. Healing is terminal, so it closes the
// interval — the count as of the restart is the last honest right edge available.
func (s *Service) HealStale() error {
	adopted, err := s.adoptSurvivors()
	if err != nil {
		// Adoption is best-effort: a probe that fails must not leave rows stuck
		// 'running' forever, so fall through to the heal below.
		log.Printf("error: swarmery phaserun: adoption probe: %v", err)
	}
	// The survivors are excluded by runcore.HealExcluding, which owns the one rule
	// three engines each re-derived: `NOT IN ()` is invalid SQL and `id NOT IN
	// (NULL)` is never true, so the clause may only exist when there is something to
	// exclude.
	n, err := runcore.HealExcluding(s.DB, `UPDATE epic_phases
		   SET run_state='failed', run_error='daemon restart', run_ended_at=?,
		       run_checkboxes_after=checkboxes_done
		 WHERE run_state='running'`, "id", adopted, s.ts())
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("swarmery phaserun: healed %d orphaned running phase(s) to failed", n)
	}
	return nil
}

// planRunActive reports whether the plan this phase belongs to has a whole-plan
// run in flight — the exact mirror of planrun.phaseRunActive, reading the other
// table. DB, not the slot registry: a plan run started by a PREVIOUS daemon and
// adopted after a restart is 'running' in plan_runs, and the honest answer to
// "is something already driving this plan" must include it.
func (s *Service) planRunActive(workspaceTaskID int64) (bool, error) {
	var n int
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM plan_runs WHERE workspace_task_id = ? AND run_state = 'running'`,
		workspaceTaskID).Scan(&n)
	return n > 0, err
}

// loadPhase reads the phase + its epic task + project for admission.
func (s *Service) loadPhase(phaseID int64) (phaseInfo, error) {
	var (
		info      phaseInfo
		depsJSON  string
		path      sql.NullString
		runBranch sql.NullString
		repo      sql.NullString
		wsRoot    sql.NullString
	)
	// LEFT JOIN workspaces: the overlay's project.json is a repo-hint source, and a
	// project with no workspace mapped must still load (the join is advisory).
	err := s.DB.QueryRow(`
		SELECT e.workspace_task_id, e.seq, e.name, e.doc_path, e.depends_on, e.run_state,
		       e.run_branch, e.repo, e.verify_mode, p.path, p.slug, w.root_path
		  FROM epic_phases e
		  JOIN tasks t ON t.id = e.workspace_task_id
		  JOIN projects p ON p.id = t.project_id
		  LEFT JOIN workspaces w ON w.project_id = p.id
		 WHERE e.id = ?`, phaseID).Scan(
		&info.WorkspaceTaskID, &info.Seq, &info.Name, &info.DocPath, &depsJSON,
		&info.RunState, &runBranch, &repo, &info.VerifyMode, &path, &info.ProjectSlug, &wsRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return info, ErrPhaseNotFound
	}
	if err != nil {
		return info, err
	}
	info.ProjectPath = path.String
	info.WorkspaceRoot = wsRoot.String
	info.Repo = repo.String
	info.RunBranch = runBranch.String
	if err := json.Unmarshal([]byte(depsJSON), &info.DependsOn); err != nil {
		info.DependsOn = nil // garbage depends_on ⇒ no gate (same posture as epics.go decodeIntList)
	}
	return info, nil
}

// unmetDeps returns the dependency seqs of info that are NOT yet satisfied. A
// dep seq is satisfied when a sibling phase row with that seq is complete via
// one of the two paths: all checkboxes ticked (total>0); or a legacy activated
// board task that is done/archived.
func (s *Service) unmetDeps(info phaseInfo) ([]int, error) {
	var unmet []int
	for _, dep := range info.DependsOn {
		ok, err := s.depSatisfied(info.WorkspaceTaskID, dep)
		if err != nil {
			return nil, err
		}
		if !ok {
			unmet = append(unmet, dep)
		}
	}
	return unmet, nil
}

// depSatisfied reports whether the sibling phase at seq is complete. Completion is
// proven by TICKED ACCEPTANCE CRITERIA, never by run_state: a headless run that
// exits 0 without ticking anything (failed precondition, refused work) is not a
// completed phase, and treating it as one let phases start on top of empty
// dependencies. Legacy activated board tasks still count via their column.
//
// The completion question itself is phasegate.Check's, not this function's — the
// same gate the Plans page and the diagnosis modal go through, so a dependency
// cannot read as satisfied here while reading as unverified there. Concretely: a
// phase that ASKED to be graded and carries no verdict (the verifier never
// started; the store holds such rows) no longer unblocks its dependents. A phase
// that never opted into verification is unaffected.
func (s *Service) depSatisfied(taskID int64, seq int) (bool, error) {
	rows, err := s.DB.Query(`
		SELECT e.checkboxes_done, e.checkboxes_total,
		       COALESCE(e.verify_mode,''), COALESCE(e.verify_verdict,''),
		       COALESCE(e.completion_report,''),
		       COALESCE(bt.board_column,''), bt.archived_at IS NOT NULL,
		       EXISTS (SELECT 1 FROM retro_lessons l
		                 JOIN task_retros r ON r.id = l.retro_id
		                WHERE r.task_id = e.workspace_task_id)
		  FROM epic_phases e
		  LEFT JOIN tasks bt ON bt.id = e.activated_board_task_id
		 WHERE e.workspace_task_id = ? AND e.seq = ?`, taskID, seq)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			boardCol            string
			verifyMode, verdict string
			report              string
			done, total         int
			archived, lesson    bool
		)
		if err := rows.Scan(&done, &total, &verifyMode, &verdict, &report,
			&boardCol, &archived, &lesson); err != nil {
			return false, err
		}
		// ClosureRequired is deliberately FALSE here, and this is the one place
		// the two halves of the gate are scoped differently.
		//
		// The closure conditions (a written Completion Report, a recorded lesson)
		// gate what is RECORDED as complete — the dashboard, the plan's status.
		// They must not gate what may START. A dependency whose work landed and was
		// verified is technically finished; refusing to open the next phase because
		// its predecessor's paperwork is unwritten stalls the DAG for a
		// documentation reason, which is precisely the deadlock this plan warns
		// against. Verification is different and does gate here: building on top of
		// work nobody could confirm is a technical risk, not a bookkeeping one.
		//
		// report and lesson are still SELECTed above so this decision is visible
		// beside the data it declines to use, rather than hidden as an absent column.
		_, _ = report, lesson
		gate := phasegate.Check(phasegate.Input{
			CriteriaDone:    done,
			CriteriaTotal:   total,
			VerifyMode:      verifyMode,
			VerifyVerdict:   verdict,
			LegacyDone:      boardCol == "done" || archived,
			ClosureRequired: false,
		})
		if gate.Complete() {
			return true, nil
		}
	}
	return false, rows.Err()
}
