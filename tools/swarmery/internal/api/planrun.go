package api

// Plan-run endpoints: execute a WHOLE plan by handing it to one agent — a
// headless claude run in an isolated worktree via internal/planrun, state on
// plan_runs (migration 0040). The run's procedure is core's `run-plan` skill;
// this layer only starts and stops it. Per-phase progress keeps flowing through
// wsingest as the controller ticks the phase docs.
//
//	POST   /api/epics/{taskId}/run        {agent?} → 202 {status, sessionUuid, agent}
//	POST   /api/epics/{taskId}/run/cancel          → 202 {status}
//	DELETE /api/epics/{taskId}/branch              → 200 {deleted, branch}
//
// The service is attached once at daemon startup (AttachPlanRun) — the same
// package-var idiom as AttachPhaseRun/AttachPlanning — so httptest handlers
// built with &Handler{DB: db} stay hermetic (nil ⇒ endpoints 503, no spawn).
// Its Notify is wired to publishPlanUpdated, the SAME plan_updated frame
// wsingest's NotifyPlan uses, so the Plans page refetches on run edges.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// planrunSvc is attached once at daemon startup (nil ⇒ plan-run endpoints 503
// and no notify wiring).
var planrunSvc *planrun.Service

// AttachPlanRun wires the plan-run service into the api layer and gives it the
// api-owned plan_updated emitter (keyed by workspace task id — the same frame
// wsingest publishes on checkbox rescans, so an open Plans page refetches on
// run edges with no new WS type). Called from cmd/swarmery after the service is
// constructed; left nil in unit tests so doc writes never trigger a real
// headless spawn.
func AttachPlanRun(s *planrun.Service) {
	if s != nil {
		s.Notify = publishPlanUpdated
	}
	planrunSvc = s
}

// planRunBody is the optional POST body: which agent the plan is handed to and
// how it executes the phases. Absent/empty ⇒ planrun.DefaultAgent() +
// planrun.ModeAuto (the run-plan skill's own route triage).
type planRunBody struct {
	Agent string `json:"agent"`
	Mode  string `json:"mode"` // auto | subagents | inline
}

// parsePlanRunTask parses {taskId}. Writes the error response itself.
func parsePlanRunTask(w http.ResponseWriter, r *http.Request) (int64, bool) {
	taskID, err := strconv.ParseInt(r.PathValue("taskId"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid task id")
		return 0, false
	}
	return taskID, true
}

// runPlan — POST /api/epics/{taskId}/run. requireLocalOrigin.
// 202 {status:"running", sessionUuid, agent}; 404 unknown plan; 409 already
// running / a phase run is active / paused-archived / no phases / already
// complete / unreadable README / pathless project / any branch sentinel; 503 not
// attached. Every 409 carries the SAME `code` vocabulary the phase-run endpoint
// uses (runconflict.go) — the two run surfaces must answer retry identically, and
// a dirty plan branch used to arrive here as an opaque 500.
func (h *Handler) runPlan(w http.ResponseWriter, r *http.Request) {
	if planrunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "plan runs not attached")
		return
	}
	taskID, ok := parsePlanRunTask(w, r)
	if !ok {
		return
	}
	// The body is optional — a bare POST means "use the default agent".
	var body planRunBody
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body) // malformed body ⇒ default agent, not a 400
	}
	agent := strings.TrimSpace(body.Agent)
	mode := planrun.ValidMode(body.Mode)

	uuid, err := planrunSvc.Start(taskID, agent, string(mode))
	var (
		dirtyErr *planrun.BranchDirtyError
		spansErr *planrun.PlanSpansReposError
		specErr  *planrun.SpecUncoveredError
		noSlot   *runcore.NoSlotError
	)
	// Start wraps the reclaim failure (fmt.Errorf("reclaim run branch: %w", …)), so
	// errors.Is still matches through the wrap. Resolved before the switch and
	// matched above the generic arm — an arm below `case err != nil` is dead code
	// (see runconflict.go).
	wtCode, wtMsg, isWtConflict := worktreeConflict(err)
	switch {
	case errors.Is(err, planrun.ErrPlanNotFound):
		writeClientErr(w, http.StatusNotFound, "plan not found")
	case errors.Is(err, planrun.ErrRunning):
		writeConflict(w, codeAlreadyRunning, "a run is already active for this plan")
	case errors.Is(err, planrun.ErrPhaseRunning):
		writeConflict(w, codePhaseRunning, "a phase run is active — cancel it before running the whole plan")
	// The daemon-wide run budget is full. Purely transient and nothing was stamped,
	// so the body names the holders instead of blaming the plan.
	case errors.As(err, &noSlot):
		writeNoRunSlot(w, noSlot)
	case errors.Is(err, planrun.ErrNotActive):
		writeConflict(w, codePlanInactive, "plan is not active")
	case errors.Is(err, planrun.ErrNoPhases):
		writeConflict(w, codeNoPhases, "plan has no phases")
	case errors.Is(err, planrun.ErrComplete):
		writeConflict(w, codePlanComplete, "every phase of this plan is already done")
	case errors.Is(err, planrun.ErrNoDoc):
		writeConflict(w, codeDocUnreadable, "plan README is unreadable")
	case errors.Is(err, planrun.ErrNoPath):
		writeConflict(w, codeNoProjectPath, "project has no known path to run in")
	// The project path is not a checkout and no declared repo resolved to one. The
	// wrapped repopath message lists every candidate that was tried, so it is
	// forwarded verbatim rather than replaced by a generic sentence.
	// A declared repo that is a real checkout outside the project and not a
	// registered project. Checked BEFORE ErrNoRepoRoot, which it also wraps.
	case errors.Is(err, planrun.ErrRepoOutsideProject):
		writeConflict(w, codeRepoOutsideProject, err.Error())
	case errors.Is(err, planrun.ErrNoRepoRoot):
		writeConflict(w, codeNoRepoRoot, err.Error())
	// One worktree, several declared repos: name them, so "run the phases
	// individually" is an instruction rather than a guess.
	case errors.As(err, &spansErr):
		writeConflictFields(w, codePlanSpansRepos, spansErr.Error(), map[string]any{
			"repos": spansErr.Repos,
		})
	// spec.md promises outcomes no phase delivers: name the uncovered ids, so the
	// fix (a **Covers:** line, or trimming the spec) is an instruction, not a hunt.
	case errors.As(err, &specErr):
		writeConflictFields(w, codeSpecUncovered,
			"spec.md declares criteria no phase covers: "+strings.Join(specErr.Uncovered, ", ")+
				". Add **Covers:** lines to the phase docs or trim the spec.",
			map[string]any{"uncovered": specErr.Uncovered})
	// The previous run's branch still holds commits, so reclaiming its name would
	// destroy them. Same body shape runPhase emits (error/code/branch/commitsAhead/
	// base) — the two run surfaces answer retry identically, and the UI parses one
	// shape. `base` is what turns "2 commits ahead" into a decision: ahead of `dev`
	// is work to merge, ahead of a branch that already contains them is nothing at
	// all. errors.As (not a type assertion): Start returns the value directly today,
	// but a future wrap must not silently downgrade this back to a 500.
	case errors.As(err, &dirtyErr):
		writeConflictFields(w, codeBranchDirty, dirtyErr.Error(), map[string]any{
			"branch":       dirtyErr.Branch,
			"commitsAhead": dirtyErr.CommitsAhead,
			"base":         dirtyErr.Base,
		})
	// Every branch sentinel at once, including the ones that used to reach the
	// generic arm as opaque 500s: checked out elsewhere, is HEAD, outside swarm/,
	// busy, or a detached HEAD that leaves no base to measure against. Each is a
	// state the user can actually resolve, which a 500 would never say.
	case isWtConflict:
		writeConflict(w, wtCode, wtMsg)
	case err != nil:
		writeErr(w, err)
	default:
		if agent == "" {
			agent = planrun.DefaultAgent()
		}
		writeJSONStatus(w, http.StatusAccepted, map[string]string{
			"status":      "running",
			"sessionUuid": uuid,
			"agent":       agent,
			"mode":        string(mode),
		})
	}
}

// cancelPlanRun — POST /api/epics/{taskId}/run/cancel. requireLocalOrigin.
// 202 when an in-flight run was cancelled (its goroutine stamps
// failed/cancelled); 409 nothing running; 503 not attached.
func (h *Handler) cancelPlanRun(w http.ResponseWriter, r *http.Request) {
	if planrunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "plan runs not attached")
		return
	}
	taskID, ok := parsePlanRunTask(w, r)
	if !ok {
		return
	}
	if !planrunSvc.Cancel(taskID) {
		writeClientErr(w, http.StatusConflict, "no run is active for this plan")
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

// deletePlanRunBranch — DELETE /api/epics/{taskId}/branch. requireLocalOrigin.
// The explicit user decision behind the branch-dirty 409: force-deletes
// swarm/plan-<taskId> INCLUDING its commits. Mirrors deletePhaseRunBranch — same
// {deleted, branch} shape, same 503-when-unattached posture.
//
// `deleted` reports what actually happened, not what was attempted:
// DeleteRunBranch answers `existed` because worktree.DeleteBranch is idempotent
// (a missing branch is a silent nil), and a handler that hard-coded true would
// let the UI clear a dirty-branch banner on a no-op.
//
// Every refusal DeleteRunBranch can raise is a state the user can resolve — the
// branch is checked out somewhere, a run owns it, it is the repo's HEAD, or it is
// outside the swarm/ namespace this daemon is allowed to `branch -D`. Those are
// 409s carrying a `code` (see runconflict.go), never the raw 500 an unmatched
// error produces.
func (h *Handler) deletePlanRunBranch(w http.ResponseWriter, r *http.Request) {
	if planrunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "plan runs not attached")
		return
	}
	taskID, ok := parsePlanRunTask(w, r)
	if !ok {
		return
	}
	branch, existed, err := planrunSvc.DeleteRunBranch(taskID)
	// Above the generic arm, as everywhere else — see runconflict.go.
	wtCode, wtMsg, isWtConflict := worktreeConflict(err)
	switch {
	case errors.Is(err, planrun.ErrPlanNotFound):
		writeClientErr(w, http.StatusNotFound, "plan not found")
	case errors.Is(err, planrun.ErrRunning):
		writeConflict(w, codeAlreadyRunning, "a run is active for this plan")
	case errors.Is(err, planrun.ErrNoPath):
		writeConflict(w, codeNoProjectPath, "project has no known path")
	case isWtConflict:
		writeConflict(w, wtCode, wtMsg)
	case err != nil:
		writeErr(w, err)
	default:
		// `deleted` is the service's `existed` — deleting is idempotent, so a
		// constant true would report a no-op as a deletion.
		writeJSON(w, map[string]any{"deleted": existed, "branch": branch}, nil)
	}
}
