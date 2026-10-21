// The needs-you sources for plan phases only a human can finish.
//
// manual_phase (phase-run outcomes plan, phase 3, D3): a phase whose every
// OPEN criterion is `[MANUAL]` — a production check, a console, a hand test.
// No run can close it (StartWith refuses it with 409 `manual-only`), so unless
// the queue names it, it sits at "almost done" forever.
//
// awaiting_merge (land-criteria UX plan, D3): a phase whose every OPEN
// criterion is `[LAND]` and whose run branch can land — ready, pushed, or with
// a PR open. Only the push / PR / merge is left, and that is the operator's.
//
// Read-only, one query each over epic_phases (wsingest owns
// criteria_manual_open / criteria_land_open, migration 0105), honouring
// ?project= through the same scopeFilter as the session sources. An archived
// plan is skipped: archiving is the operator's own "not any more".
package api

import (
	"database/sql"
	"fmt"
	"strconv"
)

// The phase kinds rank last among same-instant blockers (needsYouKindRank): a
// session waiting on a click is more urgent than a doc, and a merge waits
// behind a hand check.
const (
	needsYouManualPhase   = "manual_phase"
	needsYouAwaitingMerge = "awaiting_merge"
)

// needsYouPhase identifies a manual_phase / awaiting_merge item's plan phase.
// Mirrors NeedsYouPhase in web/src/api/types.ts.
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
	// LandOpen (awaiting_merge) is how many [LAND] criteria are still unticked
	// — every open criterion of the phase, by the selection rule.
	LandOpen int `json:"landOpen,omitempty"`
	// LandingState (awaiting_merge) is ready | pushed | pr_open; `ready` is
	// derived (stored none + run done), as landingRow.dto does.
	LandingState string `json:"landingState,omitempty"`
	// PrURL (awaiting_merge) is the change request's URL once one is open.
	PrURL string `json:"prUrl,omitempty"`
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

// queryNeedsYouAwaitingMerge lists the phases whose only open work is the
// merge: open criteria = [LAND] open criteria, a run branch to land, and a
// landing state the operator can move — ready (stored none + run done),
// pushed or pr_open. merged / returned are excluded, and so is a phase never
// run by the daemon (no run_branch): there is nothing to push. A doc marked
// done stays listed: executors set Status: Done once their own criteria are
// ticked, and the [LAND] they leave open is exactly what this item is for.
// blockingSince is when the run ended — when landing became possible — else
// the doc's mtime.
func queryNeedsYouAwaitingMerge(db *sql.DB, scope string, projArgs []any) ([]needsYouItem, error) {
	q := `SELECT e.id, e.seq, e.name, e.criteria_land_open, e.landing_state, COALESCE(e.pr_url, ''),
		       t.id, COALESCE(t.external_id, ''), COALESCE(t.title, ''), p.slug,
		       COALESCE(NULLIF(e.run_ended_at, ''), NULLIF(e.doc_updated_at, ''), t.started_at, t.created_at)
		FROM epic_phases e
		JOIN tasks t ON t.id = e.workspace_task_id
		JOIN projects p ON p.id = t.project_id
		WHERE e.criteria_land_open > 0
		  AND e.checkboxes_total - e.checkboxes_done = e.criteria_land_open
		  AND COALESCE(e.run_branch, '') <> ''
		  AND (e.landing_state IN (?, ?)
		       OR (COALESCE(e.landing_state, ?) = ? AND e.run_state = 'done'))
		  AND t.archived_at IS NULL` + scope
	args := append([]any{landingPushed, landingPROpen, landingNone, landingNone}, projArgs...)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("needs-you: awaiting merge: %w", err)
	}
	defer rows.Close()
	out := []needsYouItem{}
	for rows.Next() {
		var ph needsYouPhase
		var it needsYouItem
		if err := rows.Scan(&ph.PhaseID, &ph.Seq, &ph.Name, &ph.LandOpen, &ph.LandingState, &ph.PrURL,
			&ph.TaskID, &ph.PlanExternalID, &ph.PlanTitle, &it.ProjectSlug, &it.BlockingSince); err != nil {
			return nil, err
		}
		if ph.LandingState == landingNone {
			ph.LandingState = landingReady // the WHERE admits none only with run_state done
		}
		it.Kind = needsYouAwaitingMerge
		it.SessionName = manualPhaseTitle(ph)
		it.Preview = awaitingMergePreview(ph.LandOpen, ph.LandingState)
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

// awaitingMergePreview says what is left and where the next step happens.
func awaitingMergePreview(n int, state string) string {
	head := strconv.Itoa(n) + " [LAND] criteria left"
	if n == 1 {
		head = "1 [LAND] criterion left"
	}
	switch state {
	case landingPROpen:
		return head + " — the PR is open, merge it on the host"
	case landingPushed:
		return head + " — open a PR from Review"
	default:
		return head + " — push and open a PR from Review"
	}
}
