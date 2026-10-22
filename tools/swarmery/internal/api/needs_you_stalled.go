// The needs-you source for stalled plan phases (verify-not-trust phase 11).
//
// stalled_phase: a phase whose active stage — executor, reviewer or verifier —
// stopped working. Read from the stall_notices ledger the detector's 60 s tick
// keeps (internal/stall), never re-derived here, so the queue, the plan's badge
// and the phase_stalled webhook always agree. Ranks just below approval
// (needsYouKindRank): a stalled run holds its slot while nobody looks.
package api

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/stall"
)

const needsYouStalledPhase = "stalled_phase"

// needsYouStall is the diagnosis of a stalled_phase item, and of an approval item
// a permission-pending stall was merged into (dedupeNeedsYou). Mirrors
// NeedsYouStall in web/src/api/types.ts.
type needsYouStall struct {
	Stage        string `json:"stage"`  // executor | reviewer | verifier
	Reason       string `json:"reason"` // no-turns | ended | permission-pending
	SinceMinutes int    `json:"sinceMinutes"`
}

// queryNeedsYouStalledPhases lists the ledger's stalls of non-archived plans. The
// stage session, once ingested, is the item's session; blockingSince is when the
// stall began (the last sign of life, or the permission request's time).
func queryNeedsYouStalledPhases(db *sql.DB, scope string, projArgs []any, now time.Time) ([]needsYouItem, error) {
	q := `SELECT n.phase_id, n.stage, n.reason, n.session_uuid, n.request_id, n.since,
		       e.seq, e.name, t.id, COALESCE(t.external_id, ''), COALESCE(t.title, ''), p.slug,
		       COALESCE(s.id, 0), s.term_focus_url
		FROM stall_notices n
		JOIN epic_phases e ON e.id = n.phase_id
		JOIN tasks t ON t.id = e.workspace_task_id
		JOIN projects p ON p.id = t.project_id
		LEFT JOIN sessions s ON s.session_uuid = n.session_uuid AND n.session_uuid <> ''
		WHERE t.archived_at IS NULL` + scope
	rows, err := db.Query(q, projArgs...)
	if err != nil {
		return nil, fmt.Errorf("needs-you: stalled phases: %w", err)
	}
	defer rows.Close()
	out := []needsYouItem{}
	for rows.Next() {
		var ph needsYouPhase
		var st needsYouStall
		var it needsYouItem
		var reqID sql.NullInt64
		if err := rows.Scan(&ph.PhaseID, &st.Stage, &st.Reason, &it.SessionUUID, &reqID, &it.BlockingSince,
			&ph.Seq, &ph.Name, &ph.TaskID, &ph.PlanExternalID, &ph.PlanTitle, &it.ProjectSlug,
			&it.SessionID, &it.TermFocusURL); err != nil {
			return nil, err
		}
		it.at = parseNeedsYouTS(it.BlockingSince)
		st.SinceMinutes = int(blockingSeconds(it.at, now) / 60)
		if reqID.Valid {
			it.RequestID = &reqID.Int64
		}
		it.Kind = needsYouStalledPhase
		it.SessionName = manualPhaseTitle(ph)
		it.Preview = st.Stage + " — " + stall.Describe(stall.Stage(st.Stage), stall.Reason(st.Reason), st.SinceMinutes)
		it.Phase = &ph
		it.Stall = &st
		out = append(out, it)
	}
	return out, rows.Err()
}

// mergeStallsIntoApprovals folds every permission-pending stalled_phase item into
// the approval (or question) item of the same request: the decision there is the
// only thing that unstalls the phase, so the approval row stays the actionable one
// and gains the phase as a second action («open phase»). A permission-pending
// stall whose request is not listed any more stays standalone. Returns the items
// to keep.
func mergeStallsIntoApprovals(items []needsYouItem) []needsYouItem {
	requestItem := map[int64]int{} // request id → index of its approval/question item
	for i, it := range items {
		if (it.Kind == needsYouApproval || it.Kind == needsYouQuestion) && it.RequestID != nil {
			requestItem[*it.RequestID] = i
		}
	}
	merged := map[int]bool{}
	for i, it := range items {
		if it.Kind != needsYouStalledPhase || it.Stall == nil || it.RequestID == nil ||
			it.Stall.Reason != string(stall.ReasonPermissionPending) {
			continue
		}
		j, ok := requestItem[*it.RequestID]
		if !ok || items[j].Phase != nil {
			continue
		}
		items[j].Phase, items[j].Stall = it.Phase, it.Stall
		merged[i] = true
	}
	if len(merged) == 0 {
		return items
	}
	out := make([]needsYouItem, 0, len(items)-len(merged))
	for i, it := range items {
		if !merged[i] {
			out = append(out, it)
		}
	}
	return out
}
