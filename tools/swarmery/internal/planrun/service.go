// Package planrun executes a WHOLE plan by handing it to one agent: a single
// headless `claude -p --agent <a>` run in one isolated git worktree, state
// tracked on plan_runs (migration 0040). Where internal/phaserun runs ONE phase
// per session, this hands the plan README plus a manifest of phase docs to a
// single orchestrating session that works the phases in order itself — so the
// phases share context instead of each starting cold.
//
// Progress is NOT re-invented: the executor ticks each phase doc's checkboxes as
// it goes, wsingest rescans, and the Plans page's existing per-phase rollup
// moves. Nothing here writes epic_phases.run_state — those belong to phase runs,
// and claiming them would make the two mechanisms lie about each other.
//
// Modeled on internal/phaserun (single-flight map + spawn seam + Notify) and
// reusing internal/dispatch's WorktreeManager — nothing is duplicated.
package planrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Sentinel errors mapped to HTTP statuses by the api layer.
var (
	// ErrPlanNotFound: no workspace task with phases for the given id (404).
	ErrPlanNotFound = errors.New("plan not found")
	// ErrRunning: this plan already has a run in flight (409).
	ErrRunning = errors.New("a run is already active for this plan")
	// ErrPhaseRunning: a per-phase run is in flight for this plan; the two would
	// fight over the same phase docs (409).
	ErrPhaseRunning = errors.New("a phase run is active for this plan")
	// ErrNotActive: the plan is paused or archived (409).
	ErrNotActive = errors.New("plan is not active")
	// ErrNoPhases: the plan has no phases to execute (409).
	ErrNoPhases = errors.New("plan has no phases")
	// ErrNoDoc: the plan README could not be read (409).
	ErrNoDoc = errors.New("plan README is unreadable")
	// ErrNoPath: the project has no filesystem path to run in (409).
	ErrNoPath = errors.New("project has no known path to run in")
	// ErrComplete: every phase is already done — nothing to run (409).
	ErrComplete = errors.New("every phase of this plan is already done")
	// ErrNoRepoRoot: the project has a path, but neither it nor anything the plan
	// declares is a git repository (409). Distinct from ErrNoPath ("no path at
	// all"): here the fix is a `Repo` cell or project.json's mainApp, and the
	// wrapped repopath error names every candidate that was checked — the
	// diagnostic the raw "fatal: not a git repository" never gave.
	ErrNoRepoRoot = repopath.ErrNoRepoRoot
	// ErrRepoOutsideProject mirrors phaserun's: a phase of this plan declares a
	// checkout outside the project that is not a registered project (409).
	ErrRepoOutsideProject = repopath.ErrRepoOutsideProject
	// ErrPlanSpansRepos: the plan's unfinished phases declare more than one repo,
	// and a plan run executes in ONE worktree (409). Returned as a
	// *PlanSpansReposError, which errors.Is-matches this sentinel.
	ErrPlanSpansRepos = errors.New("plan spans multiple repositories")
	// ErrBranchDirty: the previous run's branch still exists and holds commits, so
	// the deterministic branch name cannot be reclaimed automatically. Returned as
	// a *BranchDirtyError, which errors.Is-matches this sentinel. Mirrors
	// phaserun.ErrBranchDirty — the two run surfaces answer retry identically.
	ErrBranchDirty = errors.New("run branch has unmerged commits")
	// ErrSpecUncovered is returned by Start when plan/spec.md declares acceptance
	// criteria that no phase doc covers — the plan is not ready to run whole.
	ErrSpecUncovered = errors.New("spec has uncovered criteria")
)

// SpecUncoveredError carries the uncovered ids (mirrors PlanSpansReposError).
type SpecUncoveredError struct{ Uncovered []string }

func (e *SpecUncoveredError) Error() string {
	return "spec.md criteria not covered by any phase: " + strings.Join(e.Uncovered, ", ")
}

func (e *SpecUncoveredError) Unwrap() error { return ErrSpecUncovered }

// BranchDirtyError names the blocking branch and how many commits would be lost, so
// the api's 409 body and the UI can offer an explicit delete-or-merge decision
// instead of silently destroying work. Shape mirrors phaserun.BranchDirtyError.
type BranchDirtyError struct {
	Branch       string
	CommitsAhead int
	// Base is the branch CommitsAhead was measured against — the repo's current
	// checkout, because worktree.ReclaimEmptyBranch counts against the same start
	// point Acquire pins to. Empty when the base could not be named (no Git seam
	// wired, detached HEAD, git failure) — never guessed.
	Base string
}

func (e *BranchDirtyError) Error() string {
	return fmt.Sprintf("run branch %s has %d unmerged commit(s)", e.Branch, e.CommitsAhead)
}

func (e *BranchDirtyError) Is(target error) bool { return target == ErrBranchDirty }

// PlanSpansReposError names the repos a plan's unfinished phases declare. A plan
// run hands every phase to ONE session in ONE worktree, so there is no correct
// repo to pick here — and picking one silently would run half the plan in the
// wrong checkout. The list is what makes the alternative (run the phases
// individually) actionable.
type PlanSpansReposError struct{ Repos []string }

func (e *PlanSpansReposError) Error() string {
	return fmt.Sprintf("plan spans %d repositories (%s) — run its phases individually",
		len(e.Repos), strings.Join(e.Repos, ", "))
}

func (e *PlanSpansReposError) Is(target error) bool { return target == ErrPlanSpansRepos }

// Service owns the plan-run lifecycle: gate checks, worktree acquisition,
// spawn, exit stamping, and startup heal. Notify (wired to the api layer's
// plan_updated publisher) is keyed by the workspace task id so the Plans page
// refetches on run edges.
type Service struct {
	// Decide is the local decision classifier (learning-loop phase 9, D1). It is
	// consulted ONLY on the ambiguous branch of settle — a clean exit, criteria
	// unticked, no blocked line, stop_reason end_turn — AFTER the rules ran, and
	// only an ACTIVE answer changes anything. nil, no SWARMERY_DECIDE_URL, or
	// shadow mode ⇒ settle behaves exactly as it did without it.
	Decide *decide.Engine

	DB  *sql.DB
	Wt  runcore.WorktreeManager // shared worktree mechanics (runcore's seam)
	Run Runner
	// Git is an OPTIONAL read-only seam, used for one thing: naming the branch a
	// commits-ahead count was measured against (BranchDirtyError.Base). nil ⇒ the
	// base is reported as unknown; no run behaviour depends on it.
	Git worktree.Git
	// RepoRoot resolves the git repository a run executes in from the project path
	// and the repos the plan declares, in priority order. nil ⇒ repopath.Resolve.
	// A seam because the production resolver stats the filesystem, and the run
	// gates have to be testable without a real checkout on disk.
	RepoRoot func(projectPath string, cells ...string) (string, error)
	UUID     func() string    // session-uuid generator (test seam; default runcore.NewUUID)
	now      func() time.Time // clock (test seam; default time.Now)
	Go       func(func())     // async-spawn seam (nil ⇒ real `go`); mirrors phaserun.Go
	// Notify emits plan_updated for the plan at run edges. nil ⇒ no live nudge.
	Notify func(taskID int64)
	// InjectLessons returns the text appended to the run's prompt: the active
	// lessons whose areas overlap the priors of the plan's phases, within the
	// token budget, each recorded in lesson_uses (internal/lessons, learning-loop
	// phase 15). "" ⇒ the prompt is byte-identical to one without injection.
	// nil ⇒ not wired (the unit tests' state).
	InjectLessons func(taskID int64, sessionUUID string) string
	// LessonCitations marks, after the run, the injected lessons its transcript
	// cited. ADVISORY: it logs and returns. nil ⇒ off.
	LessonCitations func(taskID int64, sessionUUID string)
	// FindRun locates the live process of a run by its session uuid (adopt.go).
	// nil ⇒ a ps scan. Test seam: adoption must be exercisable without spawning.
	FindRun func(sessionUUID string) (int, bool)
	// ProcAlive reports whether an adopted pid still exists. nil ⇒ signal-0 probe.
	ProcAlive func(pid int) bool

	// Slots is the DAEMON-WIDE run registry and budget (internal/runcore): the
	// per-plan single-flight gate AND — new — a bound this engine never had. A plan
	// run is the most expensive thing the daemon starts (an orchestrator that
	// spawns its own sub-agents for hours) and it used to draw against no budget at
	// all. NewService gives every Service its own pool (hermetic unit tests); the
	// daemon replaces it with the one instance dispatch and phaserun also hold.
	Slots *runcore.Slots
	// adoptPoll overrides runcore.AdoptPollInterval when > 0 (tests shrink it).
	adoptPoll time.Duration
}

// NewService builds a plan-run service. The caller wires DB + Run
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

// Engine names plan runs in the shared slot registry: "planrun:<workspace task id>".
const Engine = "planrun"

func (s *Service) slotKey(taskID int64) string { return runcore.SlotKey(Engine, taskID) }

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

// planInfo is the Start admission read: the plan task joined to its project.
type planInfo struct {
	TaskID   int64
	PlanDir  string
	Status   string
	Archived bool
	// ProjectPath is projects.path — the project ROOT, which for a multi-repo
	// project is an umbrella dir and NOT a checkout. Never hand it to git; hand it
	// to runRoot.
	ProjectPath string
	ProjectSlug string
	// WorkspaceRoot is the workspace namespace dir (workspaces.root_path), the home
	// of overlay/project.json — one of the repo-hint sources. "" when the project
	// has no workspace mapped.
	WorkspaceRoot string
	// RepoRoot is the resolved git repository this run executes in. Set by Start
	// before the worktree is acquired; every Wt call must use it.
	RepoRoot string
	Phases   []Phase
}

// runRoot resolves the repository a plan run executes in.
//
// Hint order, most specific first: what the plan's UNFINISHED phases declare, then
// the workspace overlay's project.json, then the checkout's own .claude/project.json.
// repopath.Resolve appends ProjectPath itself as the final candidate, which is what
// keeps every single-repo project resolving exactly as it did before.
//
// Finished phases are excluded on purpose: a plan whose completed phase touched an
// infra repo is not a plan spanning repos today, and refusing it would strand the
// remaining work behind a conflict that no longer exists.
func (s *Service) runRoot(info planInfo) (string, error) {
	var cells []string
	seen := map[string]bool{}
	for _, p := range info.Phases {
		if p.complete() || strings.TrimSpace(p.Repo) == "" {
			continue
		}
		if name := repopath.Primary(p.Repo); name != "" && !seen[name] {
			seen[name] = true
		}
		cells = append(cells, p.Repo)
	}
	if len(seen) > 1 {
		names := make([]string, 0, len(seen))
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names) // deterministic message; the set has no natural order
		return "", &PlanSpansReposError{Repos: names}
	}
	cells = append(cells, repopath.Cells(info.ProjectPath, info.WorkspaceRoot)...)

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

// Start admits a run for a whole plan: gates (single-flight, lifecycle, phase
// runs, phases, README, path), then acquires a worktree, stamps
// run_state='running', and spawns the headless executor. `agent` names the
// agent the plan is handed to ("" ⇒ DefaultAgent()); `mode` is how it executes
// the phases ("" / unknown ⇒ ModeAuto, i.e. the skill's own triage). Returns the
// pre-generated session uuid so the caller answers 202 immediately. The run's
// own goroutine owns exit stamping, worktree removal (branch kept), and slot
// release.
func (s *Service) Start(taskID int64, agent, mode string) (sessionUUID string, err error) {
	info, err := s.loadPlan(taskID)
	if err != nil {
		return "", err
	}
	if info.Archived || info.Status == "paused" {
		return "", ErrNotActive
	}
	if info.ProjectPath == "" {
		return "", ErrNoPath
	}
	if len(info.Phases) == 0 {
		return "", ErrNoPhases
	}
	if allComplete(info.Phases) {
		return "", ErrComplete
	}
	busy, err := s.phaseRunActive(taskID)
	if err != nil {
		return "", err
	}
	if busy {
		return "", ErrPhaseRunning
	}
	state, err := s.runState(taskID)
	if err != nil {
		return "", err
	}
	if state == "running" {
		return "", ErrRunning
	}
	readme, err := os.ReadFile(filepath.Join(info.PlanDir, "README.md"))
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNoDoc, filepath.Join(info.PlanDir, "README.md"))
	}
	// Spec-coverage gate: while plan/spec.md declares criteria no phase doc
	// covers, the plan is not ready to run whole. Files, not DB, on purpose — the
	// gate stays truthful even when the 60s rescan hasn't converged. Still ahead
	// of the single-flight slot: a refusal leaves no state.
	if specBytes, serr := os.ReadFile(filepath.Join(info.PlanDir, "spec.md")); serr == nil {
		criteria := wsingest.ParseSpecCriteria(string(specBytes))
		if len(criteria) > 0 {
			covered := map[string]bool{}
			for _, ph := range info.Phases {
				body, rerr := os.ReadFile(ph.DocPath)
				if rerr != nil {
					continue // missing doc is ErrNoDoc territory elsewhere; not this gate's job
				}
				for _, cid := range wsingest.ParseCovers(string(body)) {
					covered[cid] = true
				}
			}
			var uncovered []string
			for _, c := range criteria {
				if !covered[c.Cid] {
					uncovered = append(uncovered, c.Cid)
				}
			}
			if len(uncovered) > 0 {
				return "", &SpecUncoveredError{Uncovered: uncovered}
			}
		}
	}
	// Resolve the repository BEFORE anything hands a path to git: projects.path is
	// the project ROOT, which for a multi-repo project is not a checkout at all,
	// and handing it to the branch probe is what made every run in such a project
	// die during admission with git's "fatal: not a git repository". Still ahead of
	// the single-flight slot — a resolution failure is an admission verdict and
	// must leave no state behind.
	info.RepoRoot, err = s.runRoot(info)
	if err != nil {
		return "", err
	}
	if agent == "" {
		agent = DefaultAgent()
	}
	runMode := ValidMode(mode)

	// Run slot BEFORE the (slow, git-touching) Acquire so a concurrent Start cannot
	// double-spawn; released on any admission failure below. The slot is now
	// daemon-wide (runcore.Slots): a plan run is the most expensive thing the daemon
	// starts, and it used to draw against no budget at all.
	uuid := s.UUID()
	ctx, cancel := context.WithCancel(context.Background())
	releaseSlot, err := s.Slots.TryAcquire(s.slotKey(taskID), uuid, cancel)
	if err != nil {
		cancel()
		// ErrBusy is this plan already running — the sentinel the API renders as
		// "already-running". ErrNoSlot is a FULL POOL, a different answer that
		// travels as itself (it names its holders) and must never be reported as a
		// failed run: nothing was stamped, and the caller may simply retry.
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
	// run's swarm/plan-<taskID> is still there and Acquire would fail ErrBranchExists
	// — the dead end that made a plan's SECOND run fail for ever. Reclaim it first
	// when it is empty; refuse loudly when it holds work. The literal below must
	// come from runcore, which owns the derivation worktree.Acquire performs
	// Acquire derives from the taskName passed just after.
	taskName := runcore.PlanTaskName(taskID)
	branch := runcore.PlanBranch(taskID)
	ahead, err := s.Wt.ReclaimEmptyBranch(info.RepoRoot, branch)
	if err != nil {
		release()
		return "", fmt.Errorf("reclaim run branch: %w", err)
	}
	if ahead > 0 {
		release()
		return "", &BranchDirtyError{Branch: branch, CommitsAhead: ahead, Base: s.baseBranch(info.RepoRoot)}
	}

	acq, err := s.Wt.Acquire(info.RepoRoot, info.ProjectSlug, taskName)
	if err != nil {
		release()
		return "", fmt.Errorf("worktree acquire: %w", err)
	}

	// Resolved ONCE and from the SAME instant run_started_at records: the prompt
	// states it, every continuation quotes the elapsed side of it, and settle uses
	// it as the deadline for the whole loop instead of giving each nudge a fresh
	// 8 hours.
	budget := runcore.Budget{Timeout: runTimeout(), Started: s.clock()}
	if err := s.stampStart(taskID, agent, string(runMode), uuid, budget.Started.UTC().Format(time.RFC3339)); err != nil {
		// Worktree FIRST, slot LAST — the same invariant runAndHandle's defer
		// enforces. Releasing the slot while the worktree still exists lets a
		// concurrent Start warm-reuse (worktree invariant 4) the deterministic
		// plan-<taskID> path we are about to delete; the failed stamp is precisely
		// the write that would have closed the DB gate, so nothing else holds it.
		s.removeWorktree(info.RepoRoot, acq)
		release()
		return "", err
	}

	// Attach this run's session to the plan it drives. Almost always a no-op here
	// (the process has not started, so nothing is ingested); runAndHandle links again
	// at exit and wsingest's reconcile pass converges whatever is still missing.
	runcore.LinkSession(s.DB, Engine, taskID, uuid)

	log.Printf("planrun: start plan=%d agent=%s mode=%s uuid=%s worktree=%q phases=%d",
		taskID, agent, runMode, uuid, acq.Path, len(info.Phases))
	s.notify(taskID)

	prompt := BuildPromptIn(info.PlanDir, string(readme), info.Phases, runMode, info.RepoRoot, info.ProjectPath, acq.Path, budget)
	if s.InjectLessons != nil {
		prompt += s.InjectLessons(taskID, uuid)
	}
	spec := RunSpec{
		Prompt:       prompt,
		SessionUUID:  uuid,
		Cwd:          acq.Path,
		Agent:        agent,
		SettingsFile: repopath.InheritedSettings(info.ProjectPath, info.RepoRoot, acq.Path),
		ProjectPath:  info.ProjectPath,
	}
	if spec.SettingsFile != "" {
		log.Printf("planrun: plan=%d inheriting project settings %s (worktree is a checkout of %s)",
			taskID, spec.SettingsFile, info.RepoRoot)
	}
	// A retry's timeline must show THIS run's decisions, not the last attempt's.
	runcore.ClearRunEvents(s.DB, Engine, taskID)
	s.spawn(func() { s.runAndHandle(ctx, cancel, releaseSlot, info, acq, spec, budget) })
	return uuid, nil
}

// allComplete reports whether every phase already has all its criteria ticked.
func allComplete(phases []Phase) bool {
	for _, p := range phases {
		if !p.complete() {
			return false
		}
	}
	return len(phases) > 0
}

// runAndHandle executes the run to completion, stamps the exit state, removes
// the worktree (branch kept), and always releases the slot.
func (s *Service) runAndHandle(ctx context.Context, cancel context.CancelFunc, releaseSlot func(), info planInfo, acq worktree.Acquired, spec RunSpec, budget runcore.Budget) {
	defer func() {
		cancel()
		if s.LessonCitations != nil {
			s.LessonCitations(info.TaskID, spec.SessionUUID)
		}
		// Worktree FIRST, slot LAST. stamp() has already moved the row off
		// 'running', so the DB gate in Start is open; releasing the single-flight
		// slot before the (git shell-out, tens of ms) removal opens a window where a
		// re-Start re-acquires the SAME deterministic worktree path (worktree
		// invariant-4 reuse) and this defer then rips the new run's worktree out
		// from under it.
		s.removeWorktree(info.RepoRoot, acq)
		releaseSlot()
		// The reconcile arm — see Start. This is where the link usually lands.
		runcore.LinkSession(s.DB, Engine, info.TaskID, spec.SessionUUID)
		s.notify(info.TaskID)
	}()

	res, err := s.Run.Start(ctx, spec)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		// Cancel() beat the exit — whatever the child reported, the outcome is a
		// user cancellation.
		log.Printf("planrun: plan=%d uuid=%s cancelled", info.TaskID, spec.SessionUUID)
		s.stamp(info.TaskID, "failed", "cancelled")
	case err != nil:
		log.Printf("error: planrun: plan=%d uuid=%s could not start: %v", info.TaskID, spec.SessionUUID, err)
		s.stamp(info.TaskID, "failed", err.Error())
	case res.TimedOut:
		log.Printf("warning: planrun: plan=%d uuid=%s timed out", info.TaskID, spec.SessionUUID)
		s.stamp(info.TaskID, "failed", "timeout")
	case res.ExitCode != 0:
		log.Printf("warning: planrun: plan=%d uuid=%s exited %d: %s", info.TaskID, spec.SessionUUID, res.ExitCode, res.Stderr)
		msg := res.Stderr
		if msg == "" {
			msg = fmt.Sprintf("exit %d", res.ExitCode)
		}
		s.stamp(info.TaskID, "failed", msg)
	default:
		// A clean exit is where the decision STARTS. settle reads the phase docs
		// and the transcript and may resume the session; see its comment for why
		// exit 0 never meant "done" in the first place.
		log.Printf("planrun: plan=%d uuid=%s exited 0 in %s, settling", info.TaskID, spec.SessionUUID, res.Duration)
		state, detail := s.settle(ctx, info, spec, budget)
		s.stamp(info.TaskID, state, detail)
	}
}

// blockedSentinel is the ending this engine's prompt asks for; a continuation
// quotes it back rather than inventing a second vocabulary. The `at phase <n>`
// qualifier the contract adds is free-form to runcore.ClassifyEnd's matcher.
const blockedSentinel = "PLAN BLOCKED at phase <n>"

// planCriteria aggregates the plan's acceptance criteria across every phase doc
// AS THEY STAND ON DISK RIGHT NOW.
//
// It re-reads the docs rather than trusting info.Phases, whose Done/Total were
// read at admission — which is to say, before the run did anything. ok=false when
// NO phase doc could be read at all: that is the "no evidence" case, and the loop
// refuses to conclude from it. A plan where only some docs are readable still
// decides, on the ones that are, because a plan run's job is spread across the
// set and a single unreadable doc must not strand the other nine.
//
// Unticked labels are prefixed with their phase so the continuation message says
// WHERE the work is — a bare criterion text is ambiguous across ten phase docs
// that often word their gates identically ("typecheck passes").
func planCriteria(phases []Phase) (done, total int, unticked []string, ok bool) {
	for _, p := range phases {
		body, err := os.ReadFile(p.DocPath)
		if err != nil {
			log.Printf("warning: planrun: phase %d doc %q unreadable while settling: %v", p.Seq, p.DocPath, err)
			continue
		}
		ok = true
		d, t := wsingest.CountCheckboxes(string(body))
		done, total = done+d, total+t
		for _, label := range wsingest.UntickedCheckboxes(string(body)) {
			unticked = append(unticked, fmt.Sprintf("phase %d — %s", p.Seq, label))
		}
	}
	return done, total, unticked, ok
}

// settle is the plan-run completion loop — phaserun.settle's twin, and
// deliberately its twin rather than shared code: the two read their criteria from
// different places (one lent document vs. a set of docs that never enter the
// worktree), and the only part that IS the same rule — classify, cap, nudge —
// already lives in runcore.
//
// Returns the run_state to stamp and its detail.
func (s *Service) settle(ctx context.Context, info planInfo, spec RunSpec, budget runcore.Budget) (state, detail string) {
	// D1 ground truth (phase 9.3): what a continuation achieved, recorded
	// against the decision taken just before it. No-op without a classifier.
	d1 := s.Decide.Tracker()
	for attempt := 0; ; attempt++ {
		// Transcript first, and classified first: a `PLAN BLOCKED at phase n:`
		// ending is evidence on its own and must win even when no phase doc can be
		// read — see phaserun.settle for the failure this ordering prevents.
		text := runcore.LastAssistantText(s.DB, spec.SessionUUID)
		// stop_reason beside the sentinel — see phaserun.settle. A plan run is
		// the longer of the two and therefore the more expensive one to resume
		// into a safeguard it has already tripped.
		stop := runcore.LastStopReason(s.DB, spec.SessionUUID)
		refusalCat := runcore.RefusalCategory(s.DB, spec.SessionUUID)

		done, total, unticked, ok := planCriteria(info.Phases)
		if !ok {
			if reason, blocked := runcore.BlockedOrRefused(text, stop, refusalCat); blocked {
				s.event(info.TaskID, spec.SessionUUID, runcore.EventBlocked, 0, reason)
				log.Printf("planrun: plan=%d uuid=%s blocked (no readable phase doc): %s", info.TaskID, spec.SessionUUID, reason)
				return "blocked", reason
			}
			// Nothing to measure and nothing to put in a continuation. An unknown
			// tick count is not evidence of completion, so this settles `partial`
			// with the cause named rather than stamping the old green `done`.
			detail := fmt.Sprintf("no readable phase doc at exit (%d phases)", len(info.Phases))
			s.event(info.TaskID, spec.SessionUUID, runcore.EventPartial, attempt, detail)
			log.Printf("warning: planrun: plan=%d uuid=%s partial: %s", info.TaskID, spec.SessionUUID, detail)
			return "partial", detail
		}

		end, reason := runcore.ClassifyRunEnd(text, stop, refusalCat, done, total)
		d1.Observe(string(end), done)
		switch end {
		case runcore.EndBlocked:
			s.event(info.TaskID, spec.SessionUUID, runcore.EventBlocked, 0, reason)
			log.Printf("planrun: plan=%d uuid=%s blocked: %s", info.TaskID, spec.SessionUUID, reason)
			return "blocked", reason
		case runcore.EndDone:
			// Only when the loop intervened — see phaserun.settle for why an
			// uneventful run writes no event.
			if attempt > 0 {
				s.event(info.TaskID, spec.SessionUUID, runcore.EventDone, attempt, fmt.Sprintf("%d/%d criteria ticked after %d continuations", done, total, attempt))
			}
			return "done", ""
		}

		// Measured BEFORE the classifier: its latency (up to the local backend's
		// timeout) must never count against the time-left guard below, or a shadow
		// call could turn a continuation into `partial`.
		elapsed := budget.Elapsed(s.clock())

		// D1 (phase 9): the rules reached `continue` — the one branch they leave
		// ambiguous. The classifier may hand the run to the operator or stamp it
		// blocked; any other answer (and every shadow/unconfigured call) lets the
		// rules' continuation stand.
		switch o := d1.Decide(ctx, decide.D1Input{Engine: Engine, SubjectID: info.TaskID, SessionUUID: spec.SessionUUID,
			LastText: text, StopReason: stop, Done: done, Total: total, Attempt: attempt}); o.Action {
		case decide.StampBlocked:
			s.event(info.TaskID, spec.SessionUUID, runcore.EventBlocked, 0, o.Detail)
			log.Printf("planrun: plan=%d uuid=%s blocked by classifier: %s", info.TaskID, spec.SessionUUID, o.Detail)
			return "blocked", o.Detail
		case decide.NotifyOperator:
			s.event(info.TaskID, spec.SessionUUID, runcore.EventPartial, attempt, o.Detail)
			log.Printf("planrun: plan=%d uuid=%s handed to operator by classifier: %s", info.TaskID, spec.SessionUUID, o.Detail)
			return "partial", o.Detail
		}

		switch {
		case attempt >= runcore.MaxContinuations:
			d := fmt.Sprintf("%d of %d criteria ticked after %d continuations", done, total, attempt)
			s.event(info.TaskID, spec.SessionUUID, runcore.EventPartial, attempt, d)
			log.Printf("planrun: plan=%d uuid=%s partial: %s", info.TaskID, spec.SessionUUID, d)
			return "partial", d
		case budget.Timeout > 0 && budget.Timeout-elapsed < runcore.MinContinuationWindow:
			// The twin of phaserun's guard, and for the same reason: a spawn with
			// seconds left on the clock is killed by the deadline before it can
			// finish anything, so it is pure spend. See runcore.MinContinuationWindow.
			d := fmt.Sprintf("%d of %d criteria ticked with less than %s left of the %s budget",
				done, total, runcore.MinContinuationWindow, budget.Timeout)
			s.event(info.TaskID, spec.SessionUUID, runcore.EventPartial, attempt, d)
			return "partial", d
		}

		msg := runcore.ContinuationMessage(unticked, blockedSentinel, elapsed, budget.Timeout)
		s.event(info.TaskID, spec.SessionUUID, runcore.EventContinuation, attempt+1, msg)
		log.Printf("planrun: plan=%d uuid=%s continuation %d/%d (%d/%d criteria ticked)",
			info.TaskID, spec.SessionUUID, attempt+1, runcore.MaxContinuations, done, total)

		cont := spec
		cont.Resume = true
		cont.Prompt = msg

		cctx, ccancel := continuationContext(ctx, budget)
		res, err := s.Run.Start(cctx, cont)
		ccancel()
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return "failed", "cancelled"
		case err != nil:
			log.Printf("error: planrun: plan=%d uuid=%s continuation could not start: %v", info.TaskID, spec.SessionUUID, err)
			return "partial", "continuation could not start: " + err.Error()
		case res == nil:
			return "partial", "continuation returned no result"
		case res.TimedOut:
			return "partial", "continuation timed out"
		case res.ExitCode != 0:
			return "partial", fmt.Sprintf("continuation exited %d: %s", res.ExitCode, runcore.Tail(res.Stderr, 512))
		}
	}
}

// continuationContext bounds a continuation by the ORIGINAL run's wall clock,
// so a plan that needs two nudges cannot spend 24 hours under an 8-hour budget.
func continuationContext(ctx context.Context, budget runcore.Budget) (context.Context, context.CancelFunc) {
	if budget.Timeout <= 0 || budget.Started.IsZero() {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, budget.Started.Add(budget.Timeout))
}

// event appends one completion-loop decision to run_events (migration 0077).
// Best-effort — see runcore.RecordRunEvent.
func (s *Service) event(taskID int64, uuid, kind string, attempt int, detail string) {
	runcore.RecordRunEvent(s.DB, Engine, taskID, uuid, kind, attempt, detail, s.ts())
}

// stampStart upserts the plan_runs row into the running state.
// startedAt is passed in rather than read here so the row's run_started_at and
// the budget line in the prompt name the SAME instant.
func (s *Service) stampStart(taskID int64, agent, mode, uuid, startedAt string) error {
	_, err := s.DB.Exec(`
		INSERT INTO plan_runs (workspace_task_id, agent, mode, run_state, run_session_uuid, run_started_at, run_error)
		VALUES (?, ?, ?, 'running', ?, ?, NULL)
		ON CONFLICT(workspace_task_id) DO UPDATE SET
			agent            = excluded.agent,
			mode             = excluded.mode,
			run_state        = 'running',
			run_session_uuid = excluded.run_session_uuid,
			run_started_at   = excluded.run_started_at,
			run_error        = NULL`, taskID, agent, mode, uuid, startedAt)
	return err
}

// stamp writes the terminal run state; runError "" ⇒ NULL.
func (s *Service) stamp(taskID int64, state, runError string) {
	var re any
	if runError != "" {
		re = runError
	}
	if _, err := s.DB.Exec(
		`UPDATE plan_runs SET run_state=?, run_error=? WHERE workspace_task_id=?`,
		state, re, taskID); err != nil {
		log.Printf("error: planrun: stamp plan=%d state=%s: %v", taskID, state, err)
	}
}

// baseBranch names the repo's current checkout — the branch
// worktree.ReclaimEmptyBranch's commit count is relative to (it resolves its start
// point from the same symbolic HEAD Acquire does). Purely descriptive: any failure,
// a detached HEAD, or no Git seam yields "" and the consumer omits the base rather
// than naming one that was not measured. Mirrors phaserun.baseBranch.
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
// (its commits stay reachable for the user — mirrors phaserun.removeWorktree).
func (s *Service) removeWorktree(repoRoot string, acq worktree.Acquired) {
	if s.Wt == nil {
		return
	}
	if err := s.Wt.Remove(repoRoot, acq, true /* keepBranch */); err != nil {
		log.Printf("warning: planrun: remove worktree %s: %v", acq.Path, err)
	}
}

// Cancel aborts an in-flight run: the context cancel kills the child claude,
// and the run goroutine's exit path stamps failed/cancelled. Returns whether a
// run was actually in flight.
func (s *Service) Cancel(taskID int64) bool {
	// Slots.Cancel invokes the run's cancel and leaves the slot held: the run
	// goroutine's own exit path releases it, so a Retry cannot get in before the
	// dying run has let go of its worktree.
	return s.Slots.Cancel(s.slotKey(taskID))
}

// DeleteRunBranch force-deletes a plan's run branch, INCLUDING one that holds
// commits — the explicit user decision behind a BranchDirtyError. Refuses while
// the branch is checked out or a run is in flight for this plan. Mirrors
// phaserun.DeleteRunBranch.
//
// existed reports whether the branch was actually there: worktree.DeleteBranch is
// idempotent (a missing branch is a silent nil), so a caller with only an error to
// read cannot tell a real deletion from a no-op and would claim "deleted" either
// way. It now comes from the delete call itself — the probe that answers it runs
// inside checkBranchReclaimable anyway, so the local rev-parse this method used to
// issue first was a second answer to a question the boundary already knew, with a
// window between the two in which they could disagree.
func (s *Service) DeleteRunBranch(taskID int64) (branch string, existed bool, err error) {
	info, err := s.loadPlan(taskID)
	if err != nil {
		return "", false, err
	}
	if info.ProjectPath == "" {
		return "", false, ErrNoPath
	}
	// A live run owns the branch; deleting it underneath would strand its commits.
	if s.Slots.IsActive(s.slotKey(taskID)) {
		return "", false, ErrRunning
	}
	// The branch lives in the repository the run resolved to, not at the project
	// root — deleting it anywhere else finds nothing and reports a no-op deletion
	// as success.
	root, err := s.runRoot(info)
	if err != nil {
		return "", false, err
	}
	// Same deterministic name Start reclaims and Acquire derives (runcore mints it).
	branch = runcore.PlanBranch(taskID)
	existed, err = s.Wt.DeleteBranch(root, branch)
	if err != nil {
		return "", false, err
	}
	if existed {
		log.Printf("planrun: deleted run branch %s (plan=%d)", branch, taskID)
	}
	return branch, existed, nil
}

// HealStale settles every plan_runs row left 'running' by a crashed or restarted
// daemon. Like phaserun.HealStale it does NOT assume they are all dead: a run in
// its own process group outlives a restart and keeps working, so each row is
// probed and the survivors are adopted (adopt.go). Called from cmd/swarmery
// before serving.
func (s *Service) HealStale() error {
	adopted, err := s.adoptSurvivors()
	if err != nil {
		// Best-effort: a failed probe must not leave rows stuck 'running' for ever.
		log.Printf("error: swarmery planrun: adoption probe: %v", err)
	}
	// The survivors are excluded by runcore.HealExcluding, which owns the one rule
	// three engines each re-derived: `NOT IN ()` is invalid SQL and `x NOT IN (NULL)`
	// is never true, so the clause may only exist when there is something to exclude.
	n, err := runcore.HealExcluding(s.DB,
		`UPDATE plan_runs SET run_state='failed', run_error='daemon restart'
		 WHERE run_state='running'`, "workspace_task_id", adopted)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("swarmery planrun: healed %d orphaned running plan(s) to failed", n)
	}
	return nil
}

// runState reads the plan's stored run state ("" when it has never run).
func (s *Service) runState(taskID int64) (string, error) {
	var state string
	err := s.DB.QueryRow(
		`SELECT run_state FROM plan_runs WHERE workspace_task_id = ?`, taskID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return state, err
}

// phaseRunActive reports whether any phase of this plan has a per-phase run in
// flight — the two mechanisms would edit the same docs from two worktrees.
func (s *Service) phaseRunActive(taskID int64) (bool, error) {
	var n int
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id = ? AND run_state = 'running'`,
		taskID).Scan(&n)
	return n > 0, err
}

// loadPlan reads the plan task + its project + its phases for admission.
func (s *Service) loadPlan(taskID int64) (planInfo, error) {
	var (
		info    planInfo
		planDir sql.NullString
		path    sql.NullString
		wsRoot  sql.NullString
	)
	// LEFT JOIN workspaces: the overlay's project.json is a repo-hint source, and a
	// project with no workspace mapped must still load (the join is advisory, the
	// plan is not).
	err := s.DB.QueryRow(`
		SELECT t.id, t.status, t.archived_at IS NOT NULL, p.path, p.slug, w.root_path,
		       (SELECT path FROM task_artifacts WHERE task_id = t.id AND kind = 'plan')
		  FROM tasks t
		  JOIN projects p ON p.id = t.project_id
		  LEFT JOIN workspaces w ON w.project_id = p.id
		 WHERE t.id = ? AND t.source = 'workspace'`, taskID).Scan(
		&info.TaskID, &info.Status, &info.Archived, &path, &info.ProjectSlug, &wsRoot, &planDir)
	if errors.Is(err, sql.ErrNoRows) {
		return info, ErrPlanNotFound
	}
	if err != nil {
		return info, err
	}
	info.ProjectPath = path.String
	info.WorkspaceRoot = wsRoot.String
	info.PlanDir = planDir.String
	if info.PlanDir == "" {
		return info, ErrPlanNotFound
	}
	phases, err := s.loadPhases(taskID)
	if err != nil {
		return info, err
	}
	info.Phases = phases
	return info, nil
}

// loadPhases reads the plan's phases in execution order.
func (s *Service) loadPhases(taskID int64) ([]Phase, error) {
	rows, err := s.DB.Query(`
		SELECT seq, name, doc_path, depends_on, checkboxes_done, checkboxes_total, repo
		  FROM epic_phases
		 WHERE workspace_task_id = ?
		 ORDER BY seq, id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Phase
	for rows.Next() {
		var (
			p        Phase
			depsJSON string
			repo     sql.NullString
		)
		if err := rows.Scan(&p.Seq, &p.Name, &p.DocPath, &depsJSON, &p.Done, &p.Total, &repo); err != nil {
			return nil, err
		}
		p.Repo = repo.String
		if err := json.Unmarshal([]byte(depsJSON), &p.DependsOn); err != nil {
			p.DependsOn = nil // garbage depends_on ⇒ no ordering hint (epics.go posture)
		}
		sort.Ints(p.DependsOn)
		out = append(out, p)
	}
	return out, rows.Err()
}
