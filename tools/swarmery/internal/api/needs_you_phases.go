// The needs-you source for plan phases only a human can finish (phase-run
// outcomes plan, phase 3, D3): a phase whose every OPEN criterion is
// `[MANUAL]` — a production check, a console, a hand test. No run can close
// it (StartWith refuses it with 409 `manual-only`), so unless the queue names
// it, it sits at "almost done" forever.
//
// Read-only, one query over epic_phases (wsingest owns criteria_manual_open,
// migration 0105), honouring ?project= through the same scopeFilter as the
// session sources. An archived plan is skipped: archiving is the operator's
// own "not any more".
package api

import (
	"database/sql"
	"fmt"
	"strconv"
)

// needsYouManualPhase is the kind; it ranks last among same-instant blockers
// (needsYouKindRank): a session waiting on a click is more urgent than a doc.
const needsYouManualPhase = "manual_phase"

// needsYouPhase identifies a manual_phase item's plan phase. Mirrors
// NeedsYouPhase in web/src/api/types.ts.
type needsYouPhase struct {
	TaskID int64 `json:"taskId"`
	// PlanExternalID is tasks.external_id — what the Plans deep link addresses.
	PlanExternalID string `json:"planExternalId"`
	PlanTitle      string `json:"planTitle"`
	PhaseID        int64  `json:"phaseId"`
	Seq            int    `json:"seq"`
	Name           string `json:"name"`
	// ManualOpen is how many [MANUAL] criteria are still unticked — every open
	// criterion of the phase, by the selection rule.
	ManualOpen int `json:"manualOpen"`
}

// queryNeedsYouManualPhases lists the phases left to the operator. The rule is
// exact, not "has a manual criterion": open criteria = manual open criteria,
// so a phase that still has executable or [LAND] work is not listed (a run, or
// the merge, closes that first). doc_status may be NULL (no Status: line).
func queryNeedsYouManualPhases(db *sql.DB, scope string, projArgs []any) ([]needsYouItem, error) {
	q := `SELECT e.id, e.seq, e.name, e.criteria_manual_open,
		       t.id, COALESCE(t.external_id, ''), COALESCE(t.title, ''), p.slug,
		       COALESCE(NULLIF(e.doc_updated_at, ''), t.started_at, t.created_at)
		FROM epic_phases e
		JOIN tasks t ON t.id = e.workspace_task_id
		JOIN projects p ON p.id = t.project_id
		WHERE e.criteria_manual_open > 0
		  AND e.checkboxes_total - e.checkboxes_done = e.criteria_manual_open
		  AND COALESCE(e.doc_status, '') <> 'done'
		  AND t.archived_at IS NULL` + scope
	rows, err := db.Query(q, projArgs...)
	if err != nil {
		return nil, fmt.Errorf("needs-you: manual phases: %w", err)
	}
	defer rows.Close()
	out := []needsYouItem{}
	for rows.Next() {
		var ph needsYouPhase
		var it needsYouItem
		if err := rows.Scan(&ph.PhaseID, &ph.Seq, &ph.Name, &ph.ManualOpen,
			&ph.TaskID, &ph.PlanExternalID, &ph.PlanTitle, &it.ProjectSlug, &it.BlockingSince); err != nil {
			return nil, err
		}
		it.Kind = needsYouManualPhase
		it.SessionName = manualPhaseTitle(ph)
		it.Preview = manualPhasePreview(ph.ManualOpen)
		it.Phase = &ph
		it.at = parseNeedsYouTS(it.BlockingSince)
		out = append(out, it)
	}
	return out, rows.Err()
}

// manualPhaseTitle is the row's headline: "<plan> · Phase <seq>: <name>".
func manualPhaseTitle(ph needsYouPhase) string {
	head := "Phase " + strconv.Itoa(ph.Seq) + ": " + ph.Name
	if ph.PlanTitle == "" {
		return head
	}
	return ph.PlanTitle + " · " + head
}

// manualPhasePreview says what is left, in the operator's terms.
func manualPhasePreview(n int) string {
	if n == 1 {
		return "1 [MANUAL] criterion left — only you can close it"
	}
	return strconv.Itoa(n) + " [MANUAL] criteria left — only you can close them"
}
