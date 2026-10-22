package api

import (
	"log"
	"time"
)

// phaseStallDTO is a phase's current stall (verify-not-trust phase 11): which
// stage stopped, why, and for how long — the «stalled: <stage> — <reason> <N min>»
// badge on the phase card. Read from the stall_notices ledger the detector's 60 s
// tick keeps (internal/stall), the same rows the Needs-you stalled_phase item and
// the phase_stalled webhook come from. Mirrors PhaseStall in web/src/api/types.ts.
type phaseStallDTO struct {
	Stage  string `json:"stage"`  // executor | reviewer | verifier
	Reason string `json:"reason"` // no-turns | ended | permission-pending
	// Since is when the stall began (RFC3339): the last sign of life, or the
	// permission request's time.
	Since        string `json:"since"`
	SinceMinutes int    `json:"sinceMinutes"`
	// SessionUUID is the stalled stage's session; "" while it has not linked one.
	SessionUUID string `json:"sessionUuid"`
	// RequestID is the pending permission request behind permission-pending.
	RequestID *int64 `json:"requestId"`
}

// phaseStalls reads the plan's stalls, keyed by phase id. Detect yields at most
// one stall per phase, so a phase never has two rows; should it (a ledger row the
// next tick has not cleared yet), the oldest stall wins. A read failure is logged
// and reads as "no stalls" — a badge is a hint, never a reason to fail the plan.
func (h *Handler) phaseStalls(taskID int64, now time.Time) map[int64]*phaseStallDTO {
	out := map[int64]*phaseStallDTO{}
	rows, err := h.DB.Query(`
		SELECT n.phase_id, n.stage, n.reason, n.since, n.session_uuid, n.request_id
		  FROM stall_notices n
		  JOIN epic_phases e ON e.id = n.phase_id
		 WHERE e.workspace_task_id = ?
		 ORDER BY n.since DESC`, taskID)
	if err != nil {
		log.Printf("warning: epics: phase stalls unreadable (task %d): %v", taskID, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var phaseID int64
		var d phaseStallDTO
		var req *int64
		if err := rows.Scan(&phaseID, &d.Stage, &d.Reason, &d.Since, &d.SessionUUID, &req); err != nil {
			log.Printf("warning: epics: phase stall row unreadable (task %d): %v", taskID, err)
			return out
		}
		d.RequestID = req
		d.SinceMinutes = int(blockingSeconds(parseNeedsYouTS(d.Since), now) / 60)
		out[phaseID] = &d
	}
	return out
}
