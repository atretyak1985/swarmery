package api

// Phase re-verification ("Verify again"):
//
//	POST /api/epics/{taskId}/phases/{phaseId}/verify → 202 {status:"verifying", phaseId}
//
// Grades a finished phase's existing run branch with the read-only verifier — no
// executor, no reviewer, no change to run_state, the ticks or the commits
// (internal/phaserun/reverify.go). Every admission gate runs synchronously, so a
// refusal comes back with its real status and a stable `code`; the grading itself
// runs off-thread, mirroring verifyTask (verify.go). The verdict lands through
// verify.Service.VerifyPhase's own Stamp + Notify, so the Plans page refreshes
// over the existing plan_updated frame — no new WS type.

import (
	"errors"
	"log"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
)

// phasesVerifying answers verifyRunning for every phase of one plan with ONE
// query (never one per phase): which of them has a verification_runs row in
// status 'running' under its target key (verify.PhaseKey). Advisory like the
// other per-plan side reads — an unreadable table logs and reads as "nothing
// running" rather than failing the plan list.
func (h *Handler) phasesVerifying(phases []epicPhaseDTO) map[int64]bool {
	out := map[int64]bool{}
	if len(phases) == 0 {
		return out
	}
	byKey := make(map[string]int64, len(phases))
	for _, p := range phases {
		byKey[verify.PhaseKey(p.ID)] = p.ID
	}
	rows, err := h.DB.Query(
		`SELECT target_key FROM verification_runs WHERE status = 'running' AND target_key LIKE 'phase:%'`)
	if err != nil {
		log.Printf("warning: epics: running verifications unreadable: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			log.Printf("warning: epics: running verification row unreadable: %v", err)
			return out
		}
		if id, ok := byKey[key]; ok {
			out[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("warning: epics: running verifications unreadable: %v", err)
	}
	return out
}

// reverifyPhase — POST /api/epics/{taskId}/phases/{phaseId}/verify.
// requireLocalOrigin. 202 when admitted; 404 unknown phase or a phase of another
// task; 409/422/503 coded refusals {"error","code"} (verify-off, criteria-open,
// no-run-branch, already-passed, already-running, plan-running, branch-busy,
// worktree-dirty, verifier-not-attached); 409 no-free-run-slot; 503 phase runs not attached.
func (h *Handler) reverifyPhase(w http.ResponseWriter, r *http.Request) {
	if phaserunSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "phase runs not attached")
		return
	}
	phaseID, ok := h.parsePhaseRunParams(w, r)
	if !ok {
		return
	}
	start, err := phaserunSvc.AdmitReverify(phaseID)
	if err != nil {
		writeReverifyRefusal(w, err)
		return
	}
	// Recovered: a panic in the grading goroutine must not take the daemon down.
	// start's own defers (worktree, then slot) still run while it unwinds.
	runcore.Go(phaserun.Engine, nil, start)
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": "verifying", "phaseId": phaseID})
}

// writeReverifyRefusal renders a non-nil AdmitReverify error. A typed refusal
// carries its own status and code; the errors that travel as themselves reuse the
// phase-run vocabulary so both surfaces answer one condition with one shape.
func writeReverifyRefusal(w http.ResponseWriter, err error) {
	var ref *phaserun.ReverifyRefusal
	var noSlot *runcore.NoSlotError
	switch {
	case errors.As(err, &ref):
		writeJSONStatus(w, ref.Status, map[string]any{"error": ref.Msg, "code": ref.Code})
	case errors.Is(err, phaserun.ErrPhaseNotFound):
		writeClientErr(w, http.StatusNotFound, "phase not found")
	case errors.As(err, &noSlot):
		writeNoRunSlot(w, noSlot)
	case errors.Is(err, phaserun.ErrNoPath):
		writeConflict(w, codeNoProjectPath, "project has no known path to run in")
	case errors.Is(err, phaserun.ErrRepoOutsideProject):
		writeConflict(w, codeRepoOutsideProject, err.Error())
	case errors.Is(err, phaserun.ErrNoRepoRoot):
		writeConflict(w, codeNoRepoRoot, err.Error())
	default:
		if code, msg, ok := worktreeConflict(err); ok {
			writeConflict(w, code, msg)
			return
		}
		writeErr(w, err)
	}
}
