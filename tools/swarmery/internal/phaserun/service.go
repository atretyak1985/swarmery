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

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
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
	// Actuals records what a finished run actually did — files, lines, cost,
	// outcome, verdict (internal/actuals, learning-loop phase 12). Called from the
	// run's exit path AFTER stamp and verifyRun, so the row it reads carries this
	// run's terminal state and verdict, and BEFORE the slot is released, so no new
	// run can overwrite the row (or clear its run_events) mid-measurement. nil ⇒
	// not wired: the unit tests' state, and a daemon that never records actuals.
	// ADVISORY: the callee must never fail or block the run — it logs and returns.
	Actuals func(phaseID int64, sessionUUID, repoRoot string)
	// SurpriseVerify asks, after Actuals has measured and scored the run, whether
	// the run surprised the learning loop enough to be verified even though its
	// doc never asked (internal/surprise, learning-loop phase 13), and with what
	// focus hint. nil, or ok=false, ⇒ no auto-verification — the default: the
	// daemon's scorer answers false unless SWARMERY_SURPRISE_AUTOVERIFY_AT is set.
	// ADVISORY like the scorer: the verdict it produces is information on the
	// phase, and phasegate never gates on a verdict for a doc whose own mode is off.
	SurpriseVerify func(phaseID int64, sessionUUID string) (focusHint string, ok bool)
	// InjectLessons returns the text appended to the run's prompt: the active
	// lessons whose areas overlap the phase's prior forecast, within the token
	// budget, each already recorded in lesson_uses (internal/lessons, learning-loop
	// phase 15). "" ⇒ nothing appended, so the prompt is byte-identical to a run
	// without injection. nil ⇒ not wired (the unit tests' state).
	InjectLessons func(phaseID int64, sessionUUID string) string
	// LessonCitations marks, after the run, the injected lessons the executor
	// cited ("[L-12]") in its transcript or in the returned doc's Completion
	// Report. ADVISORY: it logs and returns, never failing the run. nil ⇒ off.
	LessonCitations func(phaseID int64, sessionUUID, docPath string)
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
	// DocModel is the RAW model the phase DOC declares (`**Model:** opus`,
	// wsingest.ParseModel; epic_phases.doc_model, migration 0069) — rung 2 of the
	// model ladder. "" when the doc declares nothing. Unvalidated on the way in:
	// resolveModel is the one place that judges it, and it fails the run rather than
	// dropping it.
	DocModel string
}

// runRoot resolves the repository this phase runs in: what the phase doc declares
// first, then the workspace overlay's project.json, then the checkout's own
// .claude/project.json. repopath.Resolve appends ProjectPath as the last
// candidate, which is what keeps every single-repo project resolving as before.
func (s *Service) runRoot(info phaseInfo) (string, error) {
	cells := repopath.Cells(info.ProjectPath, info.WorkspaceRoot, info.Repo)

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

// RunRoot resolves the repository a phase's runs execute in, by the same rules
// Start applies — so a reader measuring a past run (internal/actuals' backfill)
// looks for its branch where the run actually committed it.
func (s *Service) RunRoot(phaseID int64) (string, error) {
	info, err := s.loadPhase(phaseID)
	if err != nil {
		return "", err
	}
	if info.ProjectPath == "" {
		return "", ErrNoPath
	}
	return s.runRoot(info)
}

// DocModelError: the phase DOC declares a `**Model:**` this daemon does not know.
// Rung 2's failure, and deliberately NOT rung 1's: a bad value in a document is a
// defect in the plan, not a bad HTTP request, so it names the document the author
// has to edit instead of the closed set the operator never typed. The api layer
// answers it 409 `doc-model-unknown` (runconflict.go), above the 400 arm, because
// it wraps planning.ErrUnknownModel and would otherwise be swallowed by it.
type DocModelError struct {
	// Doc is the phase doc's absolute path — the whole point of this error type.
	Doc string
	// Declared is what the doc actually says, verbatim, so the author can grep for
	// the line. (The line NUMBER is not carried: epic_phases stores the value, not
	// its position, and one greppable string beats a second doc-owned column that a
	// checkbox flip would have to keep in sync.)
	Declared string
	err      error
}

func (e *DocModelError) Error() string {
	return fmt.Sprintf("phase doc %s declares **Model:** %q, which is not a known model "+
		"(opus, sonnet, fable) — fix the line in the doc: %v", e.Doc, e.Declared, e.err)
}

// Unwrap keeps errors.Is(err, planning.ErrUnknownModel) true through this type.
func (e *DocModelError) Unwrap() error { return e.err }

// resolveModel walks the phase-run model ladder and returns what reaches
// --model ("" ⇒ no flag at all, today's behaviour).
//
//  1. an operator choice on the request — VALIDATED through planning.ResolveModel,
//     so a typo is a 400 before anything is acquired or stamped;
//  2. otherwise the phase DOC's own `**Model:** opus` header (epic_phases.doc_model,
//     wsingest.ParseModel, migration 0069) — VALIDATED too, because it is authored
//     text, but failing DIFFERENTLY: a *DocModelError naming the document, so the
//     run does not start and the author learns which file to edit. Never silently
//     ignored — internal/dispatch/service.go:979 records exactly that bug for
//     playbooks, where a `model:` chip named a model no run ever used;
//  3. otherwise SWARMERY_PHASERUN_MODEL — passed through VERBATIM, deliberately
//     unvalidated. The env knob is pinned by whoever runs the daemon and legitimately
//     holds full IDs outside planning.Models (a "[1m]" context-window suffix, say);
//     routing it through the validator would reject it and silently drop every phase
//     run back to the account default — the exact failure this ladder exists to remove;
//  4. otherwise planning.DefaultModel.
//
// Rung 4 used to be "" — no --model flag at all — which did NOT mean "some sensible
// house default": it meant the ACCOUNT default, and on these accounts that is Fable,
// at roughly twice the Opus price. So the one rung an operator never chooses, the one
// every un-picked "Run phase" lands on, was the most expensive of the four. Every
// other engine in this daemon already pins a full model ID for exactly that reason;
// this rung brings phase runs in line with them.
//
// Rungs 1–3 are unchanged: a request model still wins outright, and a doc that
// declares nothing (docModel == "") falls straight through to the env knob.
func resolveModel(choice, docModel, docPath string) (string, error) {
	if strings.TrimSpace(choice) != "" {
		id, err := planning.ResolveModel(choice)
		if err != nil {
			// planning.ResolveModel already wraps ErrUnknownModel, so errors.Is
			// still matches through this second wrap at the api layer.
			return "", fmt.Errorf("phase run model: %w", err)
		}
		return id, nil
	}
	if declared := strings.TrimSpace(docModel); declared != "" {
		id, err := planning.ResolveModel(declared)
		if err != nil {
			return "", &DocModelError{Doc: docPath, Declared: declared, err: err}
		}
		return id, nil
	}
	if env := strings.TrimSpace(os.Getenv(modelEnv)); env != "" {
		return env, nil
	}
	return planning.DefaultModel, nil
}

// DocEffortError: the phase DOC declares an `**Effort:**` outside the CLI's
// closed set. Rung 2's failure, and — exactly like DocModelError, for the same
// reason — deliberately NOT rung 3's: a bad value in a DOCUMENT is a defect in
// the plan and must name the file its author has to edit, while a bad value in
// an ENV knob is an operator's typo that must degrade with a warning rather than
// wedge every phase run on the machine.
//
// It also cannot be ignored: `claude --effort bogus` rejects the flag and the
// process dies before the run starts, so a doc typo passed through verbatim
// would surface as an unexplained dead phase instead of a named line to fix.
type DocEffortError struct {
	// Doc is the phase doc's absolute path — the whole point of this error type.
	Doc string
	// Declared is what the doc actually says, verbatim, so the author can grep
	// for the line.
	Declared string
}

func (e *DocEffortError) Error() string {
	return fmt.Sprintf("phase doc %s declares **Effort:** %q, which is not a known effort (%s) — fix the line in the doc",
		e.Doc, e.Declared, strings.Join(claudeflags.ValidEfforts(), ", "))
}

// resolveEffort walks the phase-run effort ladder and returns what reaches
// --effort. It mirrors resolveModel rung for rung:
//
//  1. an operator choice on the request — validated, so a typo is a 400 before
//     anything is acquired or stamped;
//  2. otherwise the phase DOC's own `**Effort:** high` header
//     (wsingest.ParseEffort) — validated too, failing as a *DocEffortError that
//     names the document;
//  3. otherwise SWARMERY_PHASERUN_EFFORT, then DefaultEffort — both through
//     internal/claudeflags, which degrades an env typo with a warning.
//
// The one structural difference from the model ladder: the doc rung reads the
// doc BODY the service already loaded, not a stamped column. doc_model exists
// because the dashboard renders that chip on the plans page without opening the
// file; nothing renders an effort chip yet, and adding a second doc-derived
// column (plus the migration and the rescan that keeps it in sync) to serve one
// reader that is already holding the bytes would be storage for its own sake.
// If a chip ever needs it, ParseEffort is the same parser a scanner would call.
func resolveEffort(choice, doc, docPath string) (string, error) {
	if canonical, ok := claudeflags.NormalizeEffort(choice); !ok {
		return "", fmt.Errorf("phase run effort: %w: %q (valid: %s)",
			planning.ErrUnknownEffort, choice, strings.Join(claudeflags.ValidEfforts(), ", "))
	} else if canonical != "" {
		return canonical, nil
	}
	if declared := strings.TrimSpace(wsingest.ParseEffort(doc)); declared != "" {
		canonical, ok := claudeflags.NormalizeEffort(declared)
		if !ok {
			return "", &DocEffortError{Doc: docPath, Declared: declared}
		}
		if canonical != "" {
			return canonical, nil
		}
	}
	return claudeflags.Effort(effortEnv, DefaultEffort), nil
}

// Start admits a run for a phase: gates (single-flight, deps, doc, path), then
// acquires a worktree, stamps run_state='running', and spawns the headless
// executor. Returns the pre-generated session uuid so the caller answers 202
// immediately. The run's own goroutine owns exit stamping, worktree removal
// (branch kept), and slot release.
//
// model and effort are the operator's choices for THIS run ("" = none, for
// either). resolveModel and resolveEffort above own the two ladders and both run
// before anything is acquired or stamped, so a bad value in either costs nothing.
func (s *Service) Start(phaseID int64, model, effort string) (sessionUUID string, err error) {
	// loadPhase is a pure READ — one SELECT, no stamp, no acquire — and rung 2 of
	// the ladder lives on the row it returns (epic_phases.doc_model), so model
	// resolution cannot precede it. It sits right after the already-running gates
	// (a live run outranks a bad model as the thing to report) and before
	// everything that leaves a trace: the single-flight slot, the worktree,
	// run_state='running'. An unknown model (rung 1 or rung 2) is an admission
	// verdict that must leave none.
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
	// After the two "something is already running" gates, before anything else:
	// a live run is the operator's real blocker and must be the answer even when
	// the doc's **Model:** line was broken in the meantime — otherwise the client
	// is told to edit a document while the actual reason is "wait". Still ahead
	// of every step that leaves a trace (slot, worktree, run_state), so an
	// unknown model is an admission verdict and nothing else.
	runModel, err := resolveModel(model, info.DocModel, info.DocPath)
	if err != nil {
		return "", err
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
	// Effort is resolved HERE rather than beside the model above only because its
	// rung 2 reads the doc's BODY (the model's reads a stamped column), and this
	// is the first line that has the bytes. It is still an admission verdict: no
	// slot has been taken, no worktree acquired and nothing stamped yet, so a
	// typo on the request or in the document costs exactly what a bad model does.
	runEffort, err := resolveEffort(effort, string(doc), info.DocPath)
	if err != nil {
		return "", err
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
	// The run's wall clock, resolved ONCE and from the SAME instant run_started_at
	// records: the prompt states it (so the executor can see how long it has,
	// which is what stops a 4-hour window being spent re-deriving context), every
	// continuation message quotes the elapsed side of it, and settle uses it as
	// the deadline for the whole loop rather than giving each continuation a fresh
	// full window. Reading the clock a second time would put the prompt's
	// "started" a tick after the column's, for no gain.
	budget := runcore.Budget{Timeout: timeoutFromEnv(), Started: s.clock()}
	if _, err := s.DB.Exec(`
		UPDATE epic_phases
		   SET run_state='running', run_session_uuid=?, run_started_at=?,
		       run_error=NULL, run_ended_at=NULL, run_branch=?,
		       run_start_point=NULLIF(?, ''),
		       run_checkboxes_before=checkboxes_done, run_checkboxes_after=NULL,
		       run_effort=NULLIF(?, '')
		 WHERE id=?`, uuid, budget.Started.UTC().Format(time.RFC3339), branch, acq.StartPoint, runEffort, phaseID); err != nil {
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
	prompt := BuildPromptIn(docRel, filepath.Base(info.DocPath), string(doc), info.RepoRoot, info.ProjectPath, acq.Path, budget)
	// After run_session_uuid is stamped (so every lesson_uses row names a run the
	// pending-session registry already answers for) and before the spawn.
	if s.InjectLessons != nil {
		prompt += s.InjectLessons(phaseID, uuid)
	}
	spec := RunSpec{
		Prompt:       prompt,
		SessionUUID:  uuid,
		Cwd:          acq.Path,
		SettingsFile: repopath.InheritedSettings(info.ProjectPath, info.RepoRoot, acq.Path),
		ProjectPath:  info.ProjectPath,
		// The ladder, already walked by resolveModel at the top of Start: the
		// request's model (validated) → the phase DOC's **Model:** (validated) →
		// SWARMERY_PHASERUN_MODEL (verbatim) → planning.DefaultModel.
		Model: runModel,
		// The effort ladder, walked by resolveEffort once the doc was read: the
		// request's effort → the doc's **Effort:** → SWARMERY_PHASERUN_EFFORT →
		// DefaultEffort. Never empty unless an operator asked for "off".
		Effort: runEffort,
	}
	if spec.SettingsFile != "" {
		log.Printf("phaserun: phase=%d inheriting project settings %s (worktree is a checkout of %s)",
			phaseID, spec.SettingsFile, info.RepoRoot)
	}
	// A retry's timeline must show THIS run's decisions, not the previous
	// attempt's — the same reason the checkbox interval resets both edges above.
	runcore.ClearRunEvents(s.DB, Engine, phaseID)
	s.spawn(func() { s.runAndHandle(ctx, cancel, releaseSlot, phaseID, info, acq, spec, docRel, budget) })
	return uuid, nil
}

// runAndHandle executes the run to completion, stamps the exit state, optionally
// verifies the work, removes the worktree (branch kept), and always releases the slot.
func (s *Service) runAndHandle(ctx context.Context, cancel context.CancelFunc, releaseSlot func(), phaseID int64, info phaseInfo, acq worktree.Acquired, spec RunSpec, docRel string, budget runcore.Budget) {
	// The terminal state, read by the defer below. Only a run that ENDED CLEANLY is
	// worth grading: a cancelled or crashed executor may have left the tree mid-edit,
	// and a verdict on that measures the interruption, not the work.
	endState := ""
	// The executor ticks and writes its report in the LENT copy inside the
	// worktree; returnDoc copies that back over the workspace document. It runs
	// once, as early as the run's exit allows, because two readers downstream
	// both claim to read what the executor wrote and neither can until it has:
	//
	//   - stamp() counts the ticked criteria to close the run's measurement
	//     interval. Reading the workspace copy first stamps run_checkboxes_after
	//     at the PRE-run count, and phasediag.OutcomeFromRow prefers that stamped
	//     edge over the live count forever — so a phase whose work landed is
	//     chipped `noop` permanently. Observed on every phase run of both plans
	//     on 2026-09-18, including one that ticked eight criteria.
	//   - verifyRun() reads info.DocPath to grade "the doc as it stands NOW",
	//     which before this was the doc as it stood BEFORE the run.
	//
	// The defer still calls it, guarded, so a panic between here and the switch
	// cannot lose the report — the one artifact most worth not losing.
	docReturned := false
	// returnDocNow always copies. The completion loop needs a REPEATABLE
	// copy-back: a continuation ticks further criteria inside the worktree, and
	// the next iteration decides from the workspace copy, so a once-only closure
	// would make every continuation invisible to the very check that ordered it.
	returnDocNow := func() {
		docReturned = true
		worktree.ReturnPlanDocLogged(fmt.Sprintf("phaserun phase=%d", phaseID), acq.Path, docRel, info.DocPath)
	}
	returnDoc := func() {
		if docReturned {
			return
		}
		returnDocNow()
	}
	defer func() {
		cancel()
		// Safety net only: the normal path already returned the doc before
		// stamping. Must stay AHEAD of verifyRun and removeWorktree.
		returnDoc()
		// Verify BEFORE the worktree goes away — the worktree IS the thing being
		// graded, and removeWorktree below deletes the only copy of it. This is the
		// same ordering argument as worktree-before-slot, one step earlier in the
		// sequence, and it is why verification lives in the defer at all rather than
		// after the switch: every exit path has to pass through it in this order.
		s.verifyRun(phaseID, info, acq, endState)
		// After the verdict, before the slot: see the Actuals field. The branch
		// survives removeWorktree (keepBranch), so the order against it is free.
		if s.Actuals != nil {
			s.Actuals(phaseID, spec.SessionUUID, info.RepoRoot)
		}
		// Next to the actuals recorder: the doc has been returned, so its
		// Completion Report is the executor's.
		if s.LessonCitations != nil {
			s.LessonCitations(phaseID, spec.SessionUUID, info.DocPath)
		}
		// After the score (Actuals computes it), before the worktree goes: an
		// auto-verification grades the worktree exactly as verifyRun does.
		s.surpriseVerifyRun(phaseID, spec.SessionUUID, info, acq, endState)
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
	// Before the switch: every arm below stamps, and stamp counts the ticks in the
	// workspace copy.
	returnDoc()
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
		// A clean exit is the START of the decision, not the end of it. settle
		// reads the doc and the transcript, may resume the session, and hands back
		// the state that is actually true: done, blocked, or partial.
		log.Printf("phaserun: phase=%d uuid=%s exited 0 in %s, settling", phaseID, spec.SessionUUID, res.Duration)
		state, detail := s.settle(ctx, phaseID, info, spec, budget, returnDocNow)
		endState = state
		s.stamp(phaseID, info.DocPath, state, detail)
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
	// `done` OR `partial`: both are exit-0 runs whose worktree is intact and whose
	// diff is exactly what the verifier grades. `partial` only exists because the
	// completion loop now distinguishes "finished" from "stopped with work left" —
	// before it, that same run WAS `done` and was graded, and refusing to grade it
	// now would silently drop verification from every run that needed a nudge.
	// `blocked` and `failed` are excluded for the original reason: a run that
	// stopped mid-edit is measuring the interruption, not the work.
	if s.Verify == nil || acq.Path == "" || (endState != "done" && endState != "partial") {
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

// surpriseVerifyRun is the opt-in auto-verification of a SURPRISING run (learning
// loop phase 13.5): a run whose surprise score reached SWARMERY_SURPRISE_AUTOVERIFY_AT
// is graded by the same read-only verifier verifyRun uses, with the surprise summary
// as a focus hint, even though its doc did not ask. Same ordering contract as
// verifyRun (blocking, before removeWorktree — the worktree is the subject) and the
// same skips, plus one: a doc that opted into verification was already graded by
// verifyRun, and grading the same tree twice would buy nothing.
//
// The verdict lands on the phase like any verdict. For a doc whose own verify mode is
// off, phasegate never gates on it — the grade is information, not a fence.
func (s *Service) surpriseVerifyRun(phaseID int64, sessionUUID string, info phaseInfo, acq worktree.Acquired, endState string) {
	if s.Verify == nil || s.SurpriseVerify == nil || acq.Path == "" || (endState != "done" && endState != "partial") {
		return
	}
	if info.VerifyMode != "" && info.VerifyMode != wsingest.VerifyOff {
		return
	}
	hint, ok := s.SurpriseVerify(phaseID, sessionUUID)
	if !ok {
		return
	}
	doc, err := os.ReadFile(info.DocPath)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d surprise verify skipped, doc %q unreadable: %v", phaseID, info.DocPath, err)
		return
	}
	log.Printf("phaserun: phase=%d surprise auto-verify worktree=%q", phaseID, acq.Path)
	if err := s.Verify.VerifyPhase(context.Background(), runcore.PhaseVerifyRequest{
		PhaseID:         phaseID,
		WorkspaceTaskID: info.WorkspaceTaskID,
		Mode:            wsingest.VerifyNormal,
		WorktreePath:    acq.Path,
		Branch:          acq.Branch,
		StartPoint:      acq.StartPoint,
		Title:           info.Name,
		Prompt:          string(doc),
		ProjectPath:     info.ProjectPath,
		FocusHint:       hint,
	}); err != nil {
		log.Printf("error: phaserun: phase=%d surprise verify: %v", phaseID, err)
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

// criteria is what the phase doc says about its own completion at one instant:
// how many acceptance checkboxes are ticked, how many there are, and the LABELS
// of the ones that are not. ok=false when the doc cannot be read, and then the
// completion loop refuses to conclude anything from it.
type criteria struct {
	Done     int
	Total    int
	Unticked []string
}

// criteriaInDoc reads the phase doc as it stands on disk right now. Same parser
// as tickedInDoc — wsingest owns the format — so the number the loop decides on
// and the number stamped into run_checkboxes_after can never disagree.
//
// It must be called AFTER the lent copy has been returned: the executor ticks
// inside the worktree, so reading info.DocPath before the copy-back measures the
// state the run STARTED in and would continue a phase that is already finished.
func criteriaInDoc(docPath string) (criteria, bool) {
	if docPath == "" {
		return criteria{}, false
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return criteria{}, false
	}
	done, total := wsingest.CountCheckboxes(string(body))
	return criteria{Done: done, Total: total, Unticked: wsingest.UntickedCheckboxes(string(body))}, true
}

// blockedSentinel is the ending this engine's prompt asks for, echoed back in
// every continuation so a nudged run is pointed at the vocabulary it was given
// rather than at a second one invented by the harness.
const blockedSentinel = "PHASE BLOCKED"

// settle is the completion loop: it decides what a CLEANLY EXITED phase run
// actually achieved, and resumes the same session when the answer is "not yet".
//
// The rule it replaces was `exit 0 ⇒ run_state='done'`. That rule is wrong for
// the same reason on every run: `claude -p` exits 0 whenever the model ends its
// turn, and a model running unattended ends its turn to report a milestone, to
// name the next step, or to list decisions it made — all of which are exit 0 with
// the work unfinished. The endings this engine's own prompt demands
// (`PHASE DONE` / `PHASE BLOCKED:`) were written to the transcript and read by
// nothing.
//
// Order of operations per iteration, all three load bearing:
//
//  1. Return the lent doc. The executor's ticks live in the worktree copy; every
//     decision below is made from the workspace copy, so reading before the
//     copy-back measures the PREVIOUS state.
//  2. Read the criteria from the doc and the ending from the TRANSCRIPT
//     (runcore.LastAssistantText). Neither alone is enough: the ticks cannot
//     express "blocked", and the sentinel cannot be trusted about "done".
//  3. Classify, and either settle or resume.
//
// Returns the run_state to stamp; the caller stamps it, because stamping is also
// what every other exit path does and the two must stay in one place.
func (s *Service) settle(ctx context.Context, phaseID int64, info phaseInfo, spec RunSpec, budget runcore.Budget, returnDocNow func()) (state, detail string) {
	// D1 ground truth (phase 9.3): what a continuation achieved, recorded
	// against the decision taken just before it. No-op without a classifier.
	d1 := s.Decide.Tracker()
	for attempt := 0; ; attempt++ {
		returnDocNow()

		// The TRANSCRIPT is read first and classified first. A `PHASE BLOCKED:`
		// ending is evidence in its own right and must win even when the document
		// it refers to can no longer be read — the doc being renamed or rewritten
		// mid-run (an operator edit, a plan revision) is exactly when a run is
		// likeliest to end blocked, and it was precisely then that the old order
		// returned `done` with a NULL run_error over `PHASE BLOCKED: <reason>`.
		text := runcore.LastAssistantText(s.DB, spec.SessionUUID)

		// The turn's stop_reason (migration 0078) is read from the same
		// transcript and weighed beside the sentinel, not in a second machine:
		// an Opus 5.5 safeguard ends the turn with stop_reason=refusal and the
		// process still exits 0, so without this a refused run classifies as
		// `continue` and the loop resumes the session straight back into the
		// classifier — twice, at this run's pinned effort.
		stop := runcore.LastStopReason(s.DB, spec.SessionUUID)
		refusalCat := runcore.RefusalCategory(s.DB, spec.SessionUUID)

		c, ok := criteriaInDoc(info.DocPath)
		if !ok {
			if reason, blocked := runcore.BlockedOrRefused(text, stop, refusalCat); blocked {
				s.event(phaseID, spec.SessionUUID, runcore.EventBlocked, 0, reason)
				log.Printf("phaserun: phase=%d uuid=%s blocked (doc %q unreadable): %s",
					phaseID, spec.SessionUUID, info.DocPath, reason)
				return "blocked", reason
			}
			// No doc and no blocked line: the tick count is UNKNOWN, and an unknown
			// tick count is not evidence of completion. Continuing is impossible too
			// (the unticked list would be empty — "0 criteria are still unticked,
			// continue with them"), so the honest state is `partial` with the cause
			// named, not the green stamp the exit code used to buy.
			detail := fmt.Sprintf("phase doc unreadable at exit: %s", info.DocPath)
			s.event(phaseID, spec.SessionUUID, runcore.EventPartial, attempt, detail)
			log.Printf("warning: phaserun: phase=%d uuid=%s partial: %s", phaseID, spec.SessionUUID, detail)
			return "partial", detail
		}

		end, reason := runcore.ClassifyRunEnd(text, stop, refusalCat, c.Done, c.Total)
		d1.Observe(string(end), c.Done)
		switch end {
		case runcore.EndBlocked:
			s.event(phaseID, spec.SessionUUID, runcore.EventBlocked, 0, reason)
			log.Printf("phaserun: phase=%d uuid=%s blocked: %s", phaseID, spec.SessionUUID, reason)
			return "blocked", reason
		case runcore.EndDone:
			// Recorded only when the loop actually intervened. A run that finished
			// on its first turn has no timeline worth showing, and writing one event
			// per uneventful run would bury the continuations this table exists to
			// surface.
			if attempt > 0 {
				s.event(phaseID, spec.SessionUUID, runcore.EventDone, attempt, fmt.Sprintf("%d/%d criteria ticked after %d continuations", c.Done, c.Total, attempt))
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
		switch o := d1.Decide(ctx, decide.D1Input{Engine: Engine, SubjectID: phaseID, SessionUUID: spec.SessionUUID,
			LastText: text, StopReason: stop, Done: c.Done, Total: c.Total, Attempt: attempt}); o.Action {
		case decide.StampBlocked:
			s.event(phaseID, spec.SessionUUID, runcore.EventBlocked, 0, o.Detail)
			log.Printf("phaserun: phase=%d uuid=%s blocked by classifier: %s", phaseID, spec.SessionUUID, o.Detail)
			return "blocked", o.Detail
		case decide.NotifyOperator:
			s.event(phaseID, spec.SessionUUID, runcore.EventPartial, attempt, o.Detail)
			log.Printf("phaserun: phase=%d uuid=%s handed to operator by classifier: %s", phaseID, spec.SessionUUID, o.Detail)
			return "partial", o.Detail
		}

		// From here the run stopped with work left. Three things can stop us
		// continuing, and each is a different honest answer.
		switch {
		case attempt >= runcore.MaxContinuations:
			detail := fmt.Sprintf("%d of %d criteria ticked after %d continuations", c.Done, c.Total, attempt)
			s.event(phaseID, spec.SessionUUID, runcore.EventPartial, attempt, detail)
			log.Printf("phaserun: phase=%d uuid=%s partial: %s", phaseID, spec.SessionUUID, detail)
			return "partial", detail
		case budget.Timeout > 0 && budget.Timeout-elapsed < runcore.MinContinuationWindow:
			// Not enough wall clock left to be worth a billed turn. The guard used to
			// be `elapsed >= budget.Timeout`, which let a run with seconds left spawn
			// a continuation the deadline killed immediately — a spawn that cannot
			// finish anything is pure spend. See runcore.MinContinuationWindow.
			detail := fmt.Sprintf("%d of %d criteria ticked with less than %s left of the %s budget",
				c.Done, c.Total, runcore.MinContinuationWindow, budget.Timeout)
			s.event(phaseID, spec.SessionUUID, runcore.EventPartial, attempt, detail)
			return "partial", detail
		}

		msg := runcore.ContinuationMessage(c.Unticked, blockedSentinel, elapsed, budget.Timeout)
		s.event(phaseID, spec.SessionUUID, runcore.EventContinuation, attempt+1, msg)
		log.Printf("phaserun: phase=%d uuid=%s continuation %d/%d (%d/%d criteria ticked)",
			phaseID, spec.SessionUUID, attempt+1, runcore.MaxContinuations, c.Done, c.Total)

		// The continuation inherits the ENTIRE original spec — model, effort,
		// permission mode, settings file, account, cwd — and changes exactly two
		// fields. Re-deriving those values would be a second copy of the ladder
		// phase 2 built, and an omitted --effort on a resume is not a cheap default
		// but the CLI's xhigh.
		cont := spec
		cont.Resume = true
		cont.Prompt = msg

		// The deadline is the ORIGINAL run's, not a fresh window per continuation:
		// the runner applies its own per-spawn timeout on top, and without this the
		// worst case would be (1 + MaxContinuations) × the phase timeout.
		cctx, ccancel := s.continuationContext(ctx, budget)
		res, err := s.Run.Start(cctx, cont)
		ccancel()
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			return "failed", "cancelled"
		case err != nil:
			log.Printf("error: phaserun: phase=%d uuid=%s continuation could not start: %v", phaseID, spec.SessionUUID, err)
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

// continuationContext bounds a continuation by the ORIGINAL run's wall clock. A
// zero/unknown budget leaves the parent ctx alone (a no-op cancel is returned so
// the caller's defer shape stays uniform), and an already-expired budget is
// handled by settle before it gets here.
func (s *Service) continuationContext(ctx context.Context, budget runcore.Budget) (context.Context, context.CancelFunc) {
	if budget.Timeout <= 0 || budget.Started.IsZero() {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, budget.Started.Add(budget.Timeout))
}

// event appends one completion-loop decision to run_events (migration 0077), so
// the operator can see that a `partial` phase was nudged twice rather than
// guessing from a single state column. Best-effort — see runcore.RecordRunEvent.
func (s *Service) event(phaseID int64, uuid, kind string, attempt int, detail string) {
	runcore.RecordRunEvent(s.DB, Engine, phaseID, uuid, kind, attempt, detail, s.ts())
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
		// NULL for every phase whose doc declares no `**Model:**` — which is every
		// phase predating migration 0069.
		docModel sql.NullString
	)
	// LEFT JOIN workspaces: the overlay's project.json is a repo-hint source, and a
	// project with no workspace mapped must still load (the join is advisory).
	err := s.DB.QueryRow(`
		SELECT e.workspace_task_id, e.seq, e.name, e.doc_path, e.depends_on, e.run_state,
		       e.run_branch, e.repo, e.verify_mode, e.doc_model, p.path, p.slug, w.root_path
		  FROM epic_phases e
		  JOIN tasks t ON t.id = e.workspace_task_id
		  JOIN projects p ON p.id = t.project_id
		  LEFT JOIN workspaces w ON w.project_id = p.id
		 WHERE e.id = ?`, phaseID).Scan(
		&info.WorkspaceTaskID, &info.Seq, &info.Name, &info.DocPath, &depsJSON,
		&info.RunState, &runBranch, &repo, &info.VerifyMode, &docModel, &path, &info.ProjectSlug, &wsRoot)
	if errors.Is(err, sql.ErrNoRows) {
		return info, ErrPhaseNotFound
	}
	if err != nil {
		return info, err
	}
	info.ProjectPath = path.String
	info.WorkspaceRoot = wsRoot.String
	info.Repo = repo.String
	info.DocModel = docModel.String
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
