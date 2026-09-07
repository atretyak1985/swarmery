package api

// Phase-run endpoints (interactive planning v2 phase 5): execute a plan phase
// directly from its phase doc — a headless claude run in an isolated worktree
// via internal/phaserun, state on epic_phases (run_state & co, migrations 0034 +
// 0041 run_checkboxes_before/run_ended_at + 0042 run_checkboxes_after).
// No `tasks` row and no board involvement; checkbox progress keeps flowing
// through wsingest as the executor edits the docs.
//
//	POST   /api/epics/{taskId}/phases/{phaseId}/run        → 202 {status, sessionUuid}
//	POST   /api/epics/{taskId}/phases/{phaseId}/run/cancel → 202 {status}
//	GET    /api/epics/{taskId}/phases/{phaseId}/diagnosis  → 200 phasediag.Diagnosis
//	DELETE /api/epics/{taskId}/phases/{phaseId}/branch     → 200 {deleted, branch}
//	DELETE /api/epics/{taskId}/orphan-branch?branch=…      → 200 {deleted, branch}
//
// The service is attached once at daemon startup (AttachPhaseRun) — the same
// package-var idiom as AttachPlanning/AttachDispatch — so httptest handlers
// built with &Handler{DB: db} stay hermetic (nil ⇒ endpoints 503, no spawn).
// Its Notify is wired to publishPlanUpdated, the SAME plan_updated frame
// wsingest's NotifyPlan uses, so the Plans page refetches on run edges.

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasediag"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// phaserunSvc is attached once at daemon startup (nil ⇒ phase-run endpoints
// 503 and no notify wiring).
var phaserunSvc *phaserun.Service

// phasediagGit is the git boundary for on-demand phase diagnosis, attached once at
// daemon startup (nil ⇒ the endpoint still answers, with branch-derived blockers
// omitted — phasediag.Diagnose degrades by contract). Same package-var idiom as
// phaserunSvc, so httptest handlers built with &Handler{DB: db} stay hermetic.
var phasediagGit worktree.Git

// phasediagOwn is the ownership seam that lets a diagnosis tell a leftover run
// branch apart from a run whose own worktree is still checked out on it. nil ⇒
// "cannot tell", and phasediag reports the blocking reading (branch-dirty), which
// is the safe direction. Same package-var idiom as phasediagGit.
var phasediagOwn phasediag.OwnCheckout

// AttachPhaseDiag wires the boundaries used by the diagnosis endpoint: the git
// seam every branch-derived blocker reads through, and the ownership probe that
// splits own-worktree from branch-dirty. Both may be nil; the endpoint degrades
// rather than guessing.
func AttachPhaseDiag(g worktree.Git, own phasediag.OwnCheckout) {
	phasediagGit = g
	phasediagOwn = own
}

// AttachPhaseRun wires the phase-run service into the api layer and gives it
// the api-owned plan_updated emitter (keyed by workspace task id — the same
// frame wsingest publishes on checkbox rescans, so an open Plans page
// refetches on run edges with no new WS type). Called from cmd/swarmery after
// the service is constructed; left nil in unit tests so board/doc writes never
// trigger a real headless spawn.
func AttachPhaseRun(s *phaserun.Service) {
	if s != nil {
		s.Notify = publishPlanUpdated
	}
	phaserunSvc = s
}

// parsePhaseRunParams parses {taskId} + {phaseId} and verifies the phase
// belongs to that workspace task (a mismatched pair is a 404 — the route
// addresses a phase THROUGH its epic). Writes the error response itself.
func (h *Handler) parsePhaseRunParams(w http.ResponseWriter, r *http.Request) (phaseID int64, ok bool) {
	taskID, err := strconv.ParseInt(r.PathValue("taskId"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return 0, false
	}
	phaseID, err = strconv.ParseInt(r.PathValue("phaseId"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid phase id")
		return 0, false
	}
	var wsTaskID int64
	qerr := h.DB.QueryRow(
		`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, phaseID).Scan(&wsTaskID)
	if errors.Is(qerr, sql.ErrNoRows) || (qerr == nil && wsTaskID != taskID) {
		writeClientErr(w, http.StatusNotFound, "phase not found")
		return 0, false
	}
	if qerr != nil {
		writeErr(w, qerr)
		return 0, false
	}
	return phaseID, true
}

// runPhase — POST /api/epics/{taskId}/phases/{phaseId}/run. requireLocalOrigin.
// 202 {status:"running", sessionUuid}; 404 unknown phase; 409 already running /
// unmet deps (body names them) / unreadable doc / pathless project / any branch
// sentinel; 503 not attached. EVERY 409 carries a `code` (see runconflict.go)
// alongside its pre-existing fields, so the client discriminates on one stable
// value instead of sniffing which fields happen to be present.
func (h *Handler) runPhase(w http.ResponseWriter, r *http.Request) {
	if phaserunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "phase runs not attached")
		return
	}
	phaseID, ok := h.parsePhaseRunParams(w, r)
	if !ok {
		return
	}
	uuid, err := phaserunSvc.Start(phaseID)
	var depsErr *phaserun.DepsUnmetError
	var dirtyErr *phaserun.BranchDirtyError
	var noSlot *runcore.NoSlotError
	// Start wraps the reclaim/acquire failures (fmt.Errorf("reclaim run branch:
	// %w", …)), so errors.Is still matches through the wrap. Resolved BEFORE the
	// switch and placed above the generic arm — an arm below `case err != nil` is
	// unreachable, which no body assertion would ever reveal.
	wtCode, wtMsg, isWtConflict := worktreeConflict(err)
	switch {
	case errors.Is(err, phaserun.ErrPhaseNotFound):
		writeClientErr(w, http.StatusNotFound, "phase not found")
	case errors.Is(err, phaserun.ErrRunning):
		writeConflict(w, codeAlreadyRunning, "a run is already active for this phase")
	// The mirror of runPlan's phase-running arm: a whole-plan run is driving these
	// same docs from its own worktree, so a phase run started now would fight it.
	case errors.Is(err, phaserun.ErrPlanRunning):
		writeConflict(w, codePlanRunning,
			"a plan run is active for this plan — cancel it before running one phase")
	// The daemon-wide run budget is full. Purely transient and nothing was stamped,
	// so the body names the holders instead of blaming the phase.
	case errors.As(err, &noSlot):
		writeNoRunSlot(w, noSlot)
	case errors.As(err, &depsErr):
		writeConflictFields(w, codeDepsUnmet, depsErr.Error(), map[string]any{
			"unmetDeps": depsErr.Unmet,
		})
	case errors.Is(err, phaserun.ErrNoDoc):
		writeConflict(w, codeDocUnreadable, "phase doc is unreadable")
	case errors.Is(err, phaserun.ErrNoPath):
		writeConflict(w, codeNoProjectPath, "project has no known path to run in")
	// The project path is not a checkout and the phase's declared repo did not
	// resolve to one. The wrapped repopath message lists every candidate that was
	// tried, so it is forwarded verbatim.
	// A declared repo that is a real checkout outside the project and not a
	// registered project. Checked BEFORE ErrNoRepoRoot, which it also wraps.
	case errors.Is(err, phaserun.ErrRepoOutsideProject):
		writeConflict(w, codeRepoOutsideProject, err.Error())
	case errors.Is(err, phaserun.ErrNoRepoRoot):
		writeConflict(w, codeNoRepoRoot, err.Error())
	// The leftover run branch holds commits a retry would collide with. Commit
	// SUBJECTS are deliberately absent here — the UI already fetches /diagnosis,
	// whose branch-dirty blocker carries them, and git ownership stays in phasediag.
	// `base` names what the count was measured against (empty when it could not be
	// named): "2 commits ahead" is only actionable once the user knows ahead of what.
	// Same four fields runPlan emits, so both run surfaces parse as one shape.
	case errors.As(err, &dirtyErr):
		writeConflictFields(w, codeBranchDirty, dirtyErr.Error(), map[string]any{
			"branch":       dirtyErr.Branch,
			"commitsAhead": dirtyErr.CommitsAhead,
			"base":         dirtyErr.Base,
		})
	case isWtConflict:
		writeConflict(w, wtCode, wtMsg)
	case err != nil:
		writeErr(w, err)
	default:
		writeJSONStatus(w, http.StatusAccepted, map[string]string{
			"status":      "running",
			"sessionUuid": uuid,
		})
	}
}

// cancelPhaseRun — POST /api/epics/{taskId}/phases/{phaseId}/run/cancel.
// requireLocalOrigin. 202 when an in-flight run was cancelled (its goroutine
// stamps failed/cancelled); 409 nothing running; 503 not attached.
func (h *Handler) cancelPhaseRun(w http.ResponseWriter, r *http.Request) {
	if phaserunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "phase runs not attached")
		return
	}
	phaseID, ok := h.parsePhaseRunParams(w, r)
	if !ok {
		return
	}
	if !phaserunSvc.Cancel(phaseID) {
		writeClientErr(w, http.StatusConflict, "no run is active for this phase")
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

// phaseDiagnosis — GET /api/epics/{taskId}/phases/{phaseId}/diagnosis.
// 200 phasediag.Diagnosis; 404 unknown phase or a phase that does not belong to
// that epic. A read endpoint, so no requireLocalOrigin (matching the other GETs).
func (h *Handler) phaseDiagnosis(w http.ResponseWriter, r *http.Request) {
	phaseID, ok := h.parsePhaseRunParams(w, r)
	if !ok {
		return
	}
	d, err := phasediag.Diagnose(h.DB, phasediagGit, phasediagOwn, phaseID)
	switch {
	case errors.Is(err, phasediag.ErrPhaseNotFound):
		writeClientErr(w, http.StatusNotFound, "phase not found")
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, d, nil)
	}
}

// deletePhaseRunBranch — DELETE /api/epics/{taskId}/phases/{phaseId}/branch.
// requireLocalOrigin. The explicit user decision behind a 409 branch-dirty: force-
// deletes swarm/phase-<id> INCLUDING its commits. 200 {deleted, branch}; 409 (with a
// `code`) while a run is in flight, the branch is checked out / is HEAD / is outside
// the swarm namespace, or the project is pathless; 503 not attached.
//
// `deleted` is the service's `existed`, not a constant: worktree.DeleteBranch is
// idempotent, so a delete of an already-gone branch used to answer
// {deleted:true} and the modal cleared its dirty-branch banner on a no-op.
func (h *Handler) deletePhaseRunBranch(w http.ResponseWriter, r *http.Request) {
	if phaserunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "phase runs not attached")
		return
	}
	phaseID, ok := h.parsePhaseRunParams(w, r)
	if !ok {
		return
	}
	branch, existed, err := phaserunSvc.DeleteRunBranch(phaseID)
	// Resolved before the switch and matched above the generic arm — see runPhase.
	wtCode, wtMsg, isWtConflict := worktreeConflict(err)
	switch {
	case errors.Is(err, phaserun.ErrPhaseNotFound):
		writeClientErr(w, http.StatusNotFound, "phase not found")
	case errors.Is(err, phaserun.ErrRunning):
		writeConflict(w, codeAlreadyRunning, "a run is active for this phase")
	case errors.Is(err, phaserun.ErrNoPath):
		writeConflict(w, codeNoProjectPath, "project has no known path")
	// A declared repo that is a real checkout outside the project and not a
	// registered project. Checked BEFORE ErrNoRepoRoot, which it also wraps.
	case errors.Is(err, phaserun.ErrRepoOutsideProject):
		writeConflict(w, codeRepoOutsideProject, err.Error())
	case errors.Is(err, phaserun.ErrNoRepoRoot):
		writeConflict(w, codeNoRepoRoot, err.Error())
	case errors.Is(err, phaserun.ErrNoRunBranch):
		writeConflict(w, codeNoRunBranch, "this phase has no recorded run branch")
	case isWtConflict:
		writeConflict(w, wtCode, wtMsg)
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, map[string]any{"deleted": existed, "branch": branch}, nil)
	}
}

// orphanBranchPattern is the ONE branch shape the orphan route accepts:
// swarm/phase-<id>, nothing else. The namespace guard in worktree.DeleteBranch
// would refuse the rest anyway, but a route that takes a branch NAME from the
// client validates it before spending a git call, and a 409 that names the rule
// beats a sentinel translated back into prose.
//
// The id is [1-9][0-9]* rather than [0-9]+ so the accepted shape is exactly
// strconv's canonical decimal form: no leading zeros, no bare 0. That makes
// rebuilding the name from the parsed id (see deleteOrphanBranch) an identity on
// everything this route accepts — swarm/phase-007 is refused instead of quietly
// resolving to swarm/phase-7, a DIFFERENT branch. Phase ids are SQLite rowids, so
// no reachable branch is excluded.
var orphanBranchPattern = regexp.MustCompile(`^swarm/phase-([1-9][0-9]*)$`)

// deleteOrphanBranch — DELETE /api/epics/{taskId}/orphan-branch?branch=…
// requireLocalOrigin. Deletes a swarm/phase-<id> branch whose id matches NO
// epic_phases row: work stranded under a previous id generation, which the
// phase-scoped delete route structurally cannot reach because it derives the
// branch from a row that no longer exists.
//
// This is deliberately a SIBLING route rather than a `branch` parameter on the
// phase-scoped one. Keeping that route incapable of naming an arbitrary branch is
// the property phase 2's namespace guard was added to protect; widening it would
// hand every caller of a phase-addressed URL the ability to delete any branch the
// daemon can reach. So the client-supplied name is confined here, behind two
// guards it cannot talk its way past:
//
//  1. the name must match swarm/phase-<id> exactly;
//  2. the id must be absent from epic_phases — a LIVE phase's branch is refused,
//     even when that phase belongs to another epic (ids are global).
//
// Past those guards the name handed to git is REBUILT from the parsed id, so no
// byte of the request string reaches an argv slot; the request value survives only
// in the guard comparison and the echoed response.
//
// 200 {deleted, branch} where `deleted` reports whether the branch was actually
// there; 400 a missing/blank branch; 409 (with a `code`) for either guard or any
// worktree sentinel; 503 not attached.
func (h *Handler) deleteOrphanBranch(w http.ResponseWriter, r *http.Request) {
	if phaserunSvc == nil || phaserunSvc.Wt == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "phase runs not attached")
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("taskId"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return
	}
	requested := strings.TrimSpace(r.URL.Query().Get("branch"))
	if requested == "" {
		writeClientErr(w, http.StatusBadRequest, "branch is required")
		return
	}
	m := orphanBranchPattern.FindStringSubmatch(requested)
	if m == nil {
		writeConflict(w, codeBranchRefused,
			"refusing to delete a branch outside the swarm/phase-<id> namespace")
		return
	}

	// Guard 2: the id must match no phase row AT ALL. Scoped to the whole table on
	// purpose — ids are global across epics, so an epic-scoped check would let this
	// route delete another plan's live run branch.
	//
	// The capture is converted to int64 rather than bound as the string it arrives
	// as. SQLite's INTEGER affinity would coerce '5' to 5 and the guard would work
	// either way, but a guard on a destructive route should not rest on an implicit
	// conversion: bound explicitly, "does this id have a row" cannot become "no row
	// matched because the types differed", which fails OPEN and deletes the branch.
	branchID, convErr := strconv.ParseInt(m[1], 10, 64)
	if convErr != nil {
		// Unreachable through orphanBranchPattern (^swarm/phase-([1-9][0-9]*)$),
		// EXCEPT for an id too large for int64 — which lands here rather than sliding
		// past the guard, since ParseInt returns the clamped MaxInt64 on range errors
		// and that id would very likely have no row.
		writeConflict(w, codeBranchRefused, "not a swarm/phase-<id> run branch")
		return
	}
	// From here on the branch name is REBUILT from the validated id instead of being
	// forwarded from the query string: the value that reaches `git branch -D` is
	// generated by this process, not client bytes, so no argv slot can carry
	// anything the pattern happened not to anticipate (CodeQL go/command-injection
	// flags the forwarded form, and the flag is a fair reading of the shape even
	// though the pattern already excluded leading dashes). The pattern's canonical
	// [1-9][0-9]* id makes this an identity: branch == requested, always.
	branch := runcore.PhaseBranch(branchID)

	var one int
	switch err := h.DB.QueryRow(
		`SELECT 1 FROM epic_phases WHERE id = ?`, branchID).Scan(&one); {
	case err == nil:
		writeConflict(w, codeBranchLivePhase,
			"that branch belongs to a live phase — delete it from that phase instead")
		return
	case !errors.Is(err, sql.ErrNoRows):
		writeErr(w, err)
		return
	}

	// The repo to operate in comes from the addressed epic's project, never from
	// the client.
	var projectPath sql.NullString
	err = h.DB.QueryRow(`
		SELECT p.path FROM tasks t JOIN projects p ON p.id = t.project_id
		 WHERE t.id = ?`, taskID).Scan(&projectPath)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "epic not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if projectPath.String == "" {
		writeConflict(w, codeNoProjectPath, "project has no known path")
		return
	}

	existed, err := phaserunSvc.Wt.DeleteBranch(projectPath.String, branch)
	wtCode, wtMsg, isWtConflict := worktreeConflict(err)
	switch {
	case isWtConflict:
		writeConflict(w, wtCode, wtMsg)
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, map[string]any{"deleted": existed, "branch": branch}, nil)
	}
}
