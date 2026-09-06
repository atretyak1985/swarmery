// Package planning implements Planning Mode (fusion phase 8): "idea → structured
// plan". POST /api/projects/{id}/planning spawns a headless `claude -p
// --session-id <uuid>` planner run in the project's directory; it asks
// clarifying questions as reply text (the phase-8 spike proved AskUserQuestion
// does NOT fire the permission hook under `-p`), the user answers via the
// existing session-resume chat, and the run writes a plan into the private
// workspace which wsingest surfaces as a workspace task row the user can
// "activate" into board tasks.
//
// No new tables (phase-8 spec): the run IS a normal session (ingested normally),
// so single-flight state lives in-memory — a map[projectID]run keyed by project,
// guarded by one mutex, exactly the idiom of api/session_message.go's msgInFlight.
// A daemon restart clears in-flight planning (the orphaned claude process has
// either finished writing its plan or the user re-triggers). The pre-generated
// session uuid is reconciled to the ingested sessions row lazily (SessionID in
// the status snapshot), mirroring dispatch's dispatch_session_uuid → session_id.
package planning

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrev"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// Sentinel errors mapped to HTTP statuses by the api layer.
var (
	// ErrActive: a planner run is already in flight for this project (409).
	ErrActive = errors.New("a planning run is already active for this project")
	// ErrProjectNotFound: no project row for the given id (404).
	ErrProjectNotFound = errors.New("project not found")
	// ErrNoPath: the project has no filesystem path to run the planner in (409).
	ErrNoPath = errors.New("project has no known path to plan in")
	// ErrTaskNotFound: no workspace task row for the given id (404).
	ErrTaskNotFound = errors.New("workspace task not found")
	// ErrNoPlan: the task has no plan artifact — nothing to revise (409).
	ErrNoPlan = errors.New("task has no plan directory")
	// ErrPlanBusy: a phase of the plan (or the whole-plan run) is running —
	// revising mid-run would race the executor's own doc edits (409).
	ErrPlanBusy = errors.New("plan has a running phase or an active plan run")
	// ErrRevisionOpen: the plan already has a staged revision awaiting a
	// decision — one open revision per plan (409).
	ErrRevisionOpen = errors.New("a staged revision is already open for this plan")
)

// run is one in-flight planner: its cancel (aborts the child claude), start
// time (drives the live "planning… (Ns)" indicator), and the pre-generated
// session uuid (the explicit task↔session link, reconciled lazily on read).
type run struct {
	cancel    context.CancelFunc
	startedAt time.Time
	uuid      string
}

// Service owns the planner-run lifecycle: single-flight admission, spawn, and
// the status snapshot. Notify (wired to api.publishSessionUpdated) is emitted at
// the run's edges so an open Planning page flips its Start button live — reusing
// the FROZEN session_updated frame, no new WS type.
type Service struct {
	DB   *sql.DB
	Run  Runner
	UUID func() string    // session-uuid generator (test seam; default runcore.NewUUID)
	now  func() time.Time // clock (test seam; default time.Now)
	Go   func(func())     // async-spawn seam (nil ⇒ real `go`); mirrors improveGo
	// Notify emits a session_updated for a project's in-flight change. The api
	// layer has no session id at spawn time (the row is minted later by ingest),
	// so this is keyed by PROJECT id; the api adapter republishes the project's
	// sessions. nil ⇒ no live nudge (guarded).
	Notify func(projectID int64)
	// ResumeInFlight reports whether the api layer is running a `claude -r`
	// resume for a session uuid — the second half of processAlive (the wizard's
	// raw-fallback parse and stale reconcile must not fire mid-resume). Wired by
	// api.AttachPlanning; nil ⇒ no resume tracking (bare unit tests).
	ResumeInFlight func(sessionUUID string) bool
	// FindRun locates a planner process by session uuid — the third liveness
	// source in processAlive, which is what makes a planner that outlived a daemon
	// restart still read as alive. nil ⇒ a ps scan (procfind). Test seam.
	FindRun func(sessionUUID string) (int, bool)
	// ScratchRoot is where revise sessions stage proposed plan files (one
	// subdir per session uuid). Wired by cmd/swarmery to <db dir>/revisions;
	// "" falls back to the OS temp dir (bare unit tests).
	ScratchRoot string
	// WorkspaceRoot is the workspace tree the SCANNER walks, handed to the
	// planner in its prompt so it writes the plan where wsingest will find it
	// instead of inferring a root from documentation. Wired by cmd/swarmery to
	// the same value wsingest gets (mirrors dispatch.Service.WorkspaceRoot); ""
	// leaves the prompt's placeholder in place.
	WorkspaceRoot string

	mu     sync.Mutex
	active map[int64]run // projectID → in-flight planner
	// triggers remembers the epic_phases.id a revise session was started FROM
	// (a phase-diagnosis "Revise plan" action), keyed by session uuid, so Stage
	// can stamp origin=phase_diagnosis + trigger_phase_id on the revision row.
	// In-memory only: a daemon restart mid-interview degrades the origin to
	// operator_revise, which is honest — nobody can prove the trigger anymore.
	triggers map[string]int64
}

// NewService builds a planning service. The caller wires DB + Run (ClaudeRunner);
// UUID/now/Go default to production impls.
func NewService(db *sql.DB, r Runner) *Service {
	return &Service{
		DB:     db,
		Run:    r,
		UUID:   runcore.NewUUID,
		now:    time.Now,
		active: make(map[int64]run),
	}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) spawn(fn func()) {
	wrapped := func() {
		// A panic in a planner goroutine must never take the daemon down —
		// recover + log (mirrors spawnImprove / dispatch.spawn).
		defer func() {
			if r := recover(); r != nil {
				log.Printf("error: planning: goroutine panic recovered: %v", r)
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

func (s *Service) notify(projectID int64) {
	if s.Notify != nil {
		s.Notify(projectID)
	}
}

// Status is one project's planner state (GET /api/projects/{id}/planning).
type Status struct {
	Active bool `json:"active"`
	// SessionUUID is the pre-generated planner session uuid (present while
	// active), so the page can link to /sessions/{uuid} and match the
	// transcript even before the numeric row is minted.
	SessionUUID string `json:"sessionUuid"`
	// SessionID is the numeric sessions row id once the transcript/hook has
	// minted it (null until then) — the page filters approvals + reads turns by
	// it. Resolved lazily from session_uuid, mirroring dispatch's link.
	SessionID *int64 `json:"sessionId"`
	// StartedAt is the RFC3339 start of the in-flight run, for a live timer.
	StartedAt *string `json:"startedAt"`
}

// Start admits a planner run for a project: single-flight (ErrActive when one is
// already in flight), project + path validation, then spawns the headless run
// and returns the pre-generated session uuid so the caller answers 202
// immediately. The run's own goroutine owns exit handling and slot release.
//
// model is the operator's choice (a Models short name or full ID; "" = the
// default). It is resolved BEFORE the slot is taken and stored on the wizard
// row, so the resume turns run on the same model as the first one.
func (s *Service) Start(projectID int64, idea, model string) (sessionUUID string, err error) {
	modelID, err := ResolveModel(model)
	if err != nil {
		return "", err
	}
	// Validate the project + resolve its path BEFORE taking a slot (a phantom
	// project or a pathless one is a clean client error, not a wedged slot).
	var path sql.NullString
	qerr := s.DB.QueryRow(`SELECT path FROM projects WHERE id = ?`, projectID).Scan(&path)
	if errors.Is(qerr, sql.ErrNoRows) {
		return "", ErrProjectNotFound
	}
	if qerr != nil {
		return "", qerr
	}
	if !path.Valid || path.String == "" {
		return "", ErrNoPath
	}

	s.mu.Lock()
	if _, busy := s.active[projectID]; busy {
		s.mu.Unlock()
		return "", ErrActive
	}
	uuid := s.UUID()
	ctx, cancel := context.WithCancel(context.Background())
	s.active[projectID] = run{cancel: cancel, startedAt: s.clock(), uuid: uuid}
	s.mu.Unlock()

	// Durable wizard row (phase 2). Supersede any still-open previous wizard
	// first — the newest row IS the project's wizard (newestWizard), so leaving
	// an old awaiting_answer row open would resurrect it once this run ends.
	now := s.ts()
	if s.markCancelled(projectID) {
		log.Printf("planning: project=%d superseded an open wizard", projectID)
	}
	// mode='plan' explicitly, never via the column default — the two modes must
	// be distinguishable in the row itself, not by which code path inserted it.
	if _, err := s.DB.Exec(
		`INSERT INTO planning_sessions(project_id, session_uuid, status, idea, mode, model, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		projectID, uuid, StatusGenerating, idea, ModePlan, modelID, now, now); err != nil {
		// Non-fatal: the run still executes; the wizard just has no durable row
		// (OnSessionTurns will no-op on the uuid miss).
		log.Printf("error: planning: insert wizard row project=%d uuid=%s: %v", projectID, uuid, err)
	}

	log.Printf("planning: start project=%d uuid=%s cwd=%q model=%s (%d chars idea)", projectID, uuid, path.String, modelID, len(idea))
	s.notify(projectID) // active=true → page shows the run

	spec := RunSpec{Prompt: BuildPrompt(idea, s.WorkspaceRoot), SessionUUID: uuid, Cwd: path.String, Model: modelID}
	s.spawn(func() { s.runAndHandle(ctx, cancel, projectID, spec) })
	return uuid, nil
}

// scratchRoot resolves the staging root for revise sessions.
func (s *Service) scratchRoot() string {
	if s.ScratchRoot != "" {
		return s.ScratchRoot
	}
	return filepath.Join(os.TempDir(), "swarmery-revisions")
}

// StartRevise admits a revise-mode wizard for an existing plan: the SAME
// interview loop as Start, but seeded with the plan being revised plus the
// evidence of what its phases achieved, and ending in a staged revision
// (REVISION STAGED: sentinel → Stage) instead of a new plan dir. Nothing under
// the plan dir is written by this path — Apply is a later, separate decision.
//
// triggerPhaseID is the epic_phases.id of the diagnosis that prompted the
// revision (nil for a plain operator "Revise" action); it selects the staged
// revision's origin.
func (s *Service) StartRevise(taskID int64, reason string, triggerPhaseID *int64) (sessionUUID string, err error) {
	// Resolve the workspace task → project id/path + title. Same resolution the
	// api layer's epic endpoints perform; duplicated here (with the join pulled
	// in) because planning must not import internal/api.
	var (
		projectID int64
		projPath  sql.NullString
		title     string
	)
	qerr := s.DB.QueryRow(`
		SELECT t.project_id, p.path, t.title
		  FROM tasks t JOIN projects p ON p.id = t.project_id
		 WHERE t.id = ? AND t.source = 'workspace'`, taskID).
		Scan(&projectID, &projPath, &title)
	if errors.Is(qerr, sql.ErrNoRows) {
		return "", ErrTaskNotFound
	}
	if qerr != nil {
		return "", qerr
	}
	if !projPath.Valid || projPath.String == "" {
		return "", ErrNoPath
	}

	// Plan dir from the task's plan artifact — the exact SELECT of
	// api.Handler.resolveEpicDirs (do not drift: both must resolve the same dir).
	var planDir string
	qerr = s.DB.QueryRow(
		`SELECT path FROM task_artifacts WHERE task_id = ? AND kind = 'plan'`,
		taskID).Scan(&planDir)
	if errors.Is(qerr, sql.ErrNoRows) {
		return "", ErrNoPlan
	}
	if qerr != nil {
		return "", qerr
	}

	// Refuse while the plan is executing: a phase run edits its own phase doc,
	// and a whole-plan run edits all of them — a revision staged against those
	// moving bytes would be stale before the operator could read it.
	var busy int
	if err := s.DB.QueryRow(`
		SELECT (SELECT COUNT(*) FROM epic_phases WHERE workspace_task_id = ? AND run_state = 'running')
		     + (SELECT COUNT(*) FROM plan_runs   WHERE workspace_task_id = ? AND run_state = 'running')`,
		taskID, taskID).Scan(&busy); err != nil {
		return "", err
	}
	if busy > 0 {
		return "", ErrPlanBusy
	}

	// One open revision per plan — a second staged proposal over the same base
	// would make Apply's base_hash checks meaningless.
	if rev, err := planrev.LatestStaged(s.DB, taskID); err != nil {
		return "", err
	} else if rev != nil {
		return "", ErrRevisionOpen
	}

	evidence, doneDocs, err := BuildEvidence(s.DB, taskID)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	if _, busyRun := s.active[projectID]; busyRun {
		s.mu.Unlock()
		return "", ErrActive
	}
	uuid := s.UUID()
	ctx, cancel := context.WithCancel(context.Background())
	s.active[projectID] = run{cancel: cancel, startedAt: s.clock(), uuid: uuid}
	if triggerPhaseID != nil {
		if s.triggers == nil {
			s.triggers = make(map[string]int64)
		}
		s.triggers[uuid] = *triggerPhaseID
	}
	s.mu.Unlock()

	release := func() {
		cancel()
		s.mu.Lock()
		delete(s.active, projectID)
		delete(s.triggers, uuid)
		s.mu.Unlock()
	}

	scratchDir := filepath.Join(s.scratchRoot(), uuid)
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		release()
		return "", err
	}

	// Durable wizard row: mode='revise' + the task link. idea = the operator's
	// reason, so the existing history UI has something meaningful to show.
	now := s.ts()
	if s.markCancelled(projectID) {
		log.Printf("planning: project=%d superseded an open wizard (revise)", projectID)
	}
	if _, err := s.DB.Exec(
		`INSERT INTO planning_sessions(project_id, session_uuid, status, idea, mode, revise_task_id, model, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		projectID, uuid, StatusGenerating, reason, ModeRevise, taskID, DefaultModel, now, now); err != nil {
		// Non-fatal, mirroring Start: the run still executes; OnSessionTurns
		// no-ops on the uuid miss and no revision can be staged for it.
		log.Printf("error: planning: insert revise wizard row task=%d uuid=%s: %v", taskID, uuid, err)
	}

	// Seed: the full current plan (README + every phase doc) travels in the
	// prompt so the agent starts from what IS, not from a re-read that could
	// race a concurrent edit.
	readme, _ := os.ReadFile(filepath.Join(planDir, "README.md")) // "" when absent
	prompt := BuildRevisePrompt(ReviseInput{
		Reason:     reason,
		PlanDir:    planDir,
		ScratchDir: scratchDir,
		PlanTitle:  title,
		Evidence:   evidence,
		DoneDocs:   doneDocs,
		Readme:     string(readme),
		Docs:       readSeedDocs(planDir),
	})

	log.Printf("planning: start revise task=%d project=%d uuid=%s plan=%q scratch=%q",
		taskID, projectID, uuid, planDir, scratchDir)
	s.notify(projectID)

	spec := RunSpec{Prompt: prompt, SessionUUID: uuid, Cwd: projPath.String, Model: DefaultModel}
	s.spawn(func() { s.runAndHandle(ctx, cancel, projectID, spec) })
	return uuid, nil
}

// Model returns the full model ID a wizard's resume turns must run on: the ID
// stamped on its planning_sessions row, or DefaultModel when the row predates
// the column or does not exist. Never "" — an empty --model would let a resume
// inherit the account default the first turn was pinned away from.
func (s *Service) Model(sessionUUID string) string {
	var model sql.NullString
	err := s.DB.QueryRow(`SELECT model FROM planning_sessions WHERE session_uuid = ?`, sessionUUID).Scan(&model)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("error: planning: read model uuid=%s: %v", sessionUUID, err)
	}
	if model.Valid && model.String != "" {
		return model.String
	}
	return DefaultModel
}

// readSeedDocs loads every phase/step doc of the plan dir (sorted, README
// excluded) for the revise prompt seed. Best-effort: an unreadable doc is
// skipped — the evidence table still names it.
func readSeedDocs(planDir string) []SeedDoc {
	var docs []SeedDoc
	for _, name := range listPlanDocNames(planDir) {
		b, err := os.ReadFile(filepath.Join(planDir, name))
		if err != nil {
			continue
		}
		docs = append(docs, SeedDoc{Name: name, Content: string(b)})
	}
	return docs
}

// runAndHandle executes the planner run to completion and always releases the
// slot. The transcript is the source of truth for the resulting turns/plan
// (ingested independently); here we only log the outcome and re-emit the frozen
// session_updated at the run's edges.
func (s *Service) runAndHandle(ctx context.Context, cancel context.CancelFunc, projectID int64, spec RunSpec) {
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.active, projectID)
		s.mu.Unlock()
		s.notify(projectID) // active=false → page re-enables Start
		// Settle pass (phase 2): ingest may have tailed the final transcript
		// lines BEFORE the process exited, in which case the raw-fallback path
		// was still gated on processAlive and no further bus notification will
		// arrive — re-run the parse now that the slot is released. Idempotent
		// (question turns / terminal states no-op).
		s.OnSessionTurns(spec.SessionUUID)
	}()

	run, err := s.Run.Start(ctx, spec)
	if err != nil {
		log.Printf("error: planning: run project=%d uuid=%s could not start: %v", projectID, spec.SessionUUID, err)
		s.markFailed(spec.SessionUUID)
		return
	}
	switch {
	case run.TimedOut:
		log.Printf("warning: planning: run project=%d uuid=%s timed out", projectID, spec.SessionUUID)
		s.markFailed(spec.SessionUUID) // guarded on 'generating' — never clobbers cancelled
	case run.ExitCode != 0:
		log.Printf("warning: planning: run project=%d uuid=%s exited %d: %s", projectID, spec.SessionUUID, run.ExitCode, run.Stderr)
		s.markFailed(spec.SessionUUID)
	default:
		log.Printf("planning: run project=%d uuid=%s completed in %s", projectID, spec.SessionUUID, run.Duration)
	}
}

// Cancel aborts the project's wizard: stamps every open planning_sessions row
// cancelled AND kills an in-flight child claude if one is running. Returns
// whether anything was cancelled (a process or an open row — an awaiting
// wizard with no live process is still dismissible). The stamp happens BEFORE
// the kill so runAndHandle's markFailed (guarded on status='generating')
// cannot race the cancellation into 'failed'.
func (s *Service) Cancel(projectID int64) bool {
	stamped := s.markCancelled(projectID)
	s.mu.Lock()
	r, ok := s.active[projectID]
	s.mu.Unlock()
	if ok {
		r.cancel()
	}
	if stamped {
		s.notify(projectID)
	}
	return ok || stamped
}

// Snapshot builds the status for a project: active flag, the pre-generated uuid,
// the numeric session id (resolved lazily from the uuid once ingest/the hook
// mints the row), and the start time.
func (s *Service) Snapshot(projectID int64) Status {
	s.mu.Lock()
	r, active := s.active[projectID]
	s.mu.Unlock()
	if !active {
		return Status{Active: false}
	}
	st := Status{Active: true, SessionUUID: r.uuid}
	started := r.startedAt.UTC().Format(time.RFC3339)
	st.StartedAt = &started
	// Lazily reconcile the numeric session id (the row is minted by ingest / the
	// permission hook after spawn). A miss just leaves SessionID nil — the page
	// falls back to the uuid until it resolves, same as dispatch's link.
	var sid int64
	if err := s.DB.QueryRow(`SELECT id FROM sessions WHERE session_uuid = ?`, r.uuid).Scan(&sid); err == nil {
		st.SessionID = &sid
	} else if !errors.Is(err, sql.ErrNoRows) {
		log.Printf("error: planning: resolve session id for uuid %s: %v", r.uuid, err)
	}
	return st
}
