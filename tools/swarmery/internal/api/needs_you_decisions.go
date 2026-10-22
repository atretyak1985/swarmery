// The needs-you source for guard decisions (verify-not-trust phase 4, D25).
//
// phase_decision: a phase run touched protected paths — changed them, or tried
// to and was refused by the phase-guard hook — and the daemon raised a
// source='stop' phase_decisions row instead of killing the run. The run has
// finished; what it did to protected paths waits on the operator: allow (add to
// scope) or reject. Answered through POST
// /api/epics/{taskId}/phases/{phaseId}/decisions/{decisionId}.
//
// Since verify-not-trust phase 9 it also lists source='review' decisions: a
// review cycle that could not settle on its own (an incomplete review, or one
// still failing after its automatic rounds) — repeat the review or accept it as
// is — answered through the same endpoint.
//
// Read-only, one query over phase_decisions, honouring ?project= through the
// same scopeFilter as the other sources; an archived plan is skipped.
package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// needsYouPhaseDecision ranks between question and failed (needsYouKindRank):
// a click on an answer card, after the sessions literally waiting on one.
const needsYouPhaseDecision = "phase_decision"

// needsYouDecisionOption is one answer a decision offers.
type needsYouDecisionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// needsYouDecision is a phase_decision item's payload. Mirrors
// NeedsYouDecision in web/src/api/types.ts.
type needsYouDecision struct {
	TaskID     int64  `json:"taskId"`
	PhaseID    int64  `json:"phaseId"`
	DecisionID string `json:"decisionId"`
	// Source is the decision's source: "stop" (a guard decision) or "review".
	Source   string                   `json:"source"`
	Question string                   `json:"question"`
	Options  []needsYouDecisionOption `json:"options"`
	// Guard is the run's guard check (phaserun.GuardResult as stored).
	Guard json.RawMessage `json:"guard"`
}

// guardOptionLabels are the operator-facing labels of the guard options.
var guardOptionLabels = map[string]string{
	phaserun.GuardOptionAllow:   "Allow (add to scope)",
	phaserun.GuardOptionReject:  "Reject",
	phaserun.ReviewOptionRepeat: "Repeat the review",
	phaserun.ReviewOptionAccept: "Accept as is",
}

// queryNeedsYouPhaseDecisions lists the open guard and review decisions.
func queryNeedsYouPhaseDecisions(db *sql.DB, scope string, projArgs []any) ([]needsYouItem, error) {
	q := `SELECT d.decision_id, d.source, d.question, d.option_ids_json, d.guard_json, d.created_at,
		       e.id, e.seq, e.name, t.id, COALESCE(t.external_id, ''), COALESCE(t.title, ''), p.slug
		FROM phase_decisions d
		JOIN epic_phases e ON e.id = d.phase_id
		JOIN tasks t ON t.id = d.workspace_task_id
		JOIN projects p ON p.id = t.project_id
		WHERE d.source IN (?, ?) AND d.status = ?
		  AND t.archived_at IS NULL` + scope
	args := append([]any{store.DecisionSourceStop, store.DecisionSourceReview, store.DecisionUndecided}, projArgs...)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("needs-you: phase decisions: %w", err)
	}
	defer rows.Close()
	out := []needsYouItem{}
	for rows.Next() {
		var (
			ph               needsYouPhase
			it               needsYouItem
			dec              needsYouDecision
			optJSON, guardJS string
		)
		if err := rows.Scan(&dec.DecisionID, &dec.Source, &dec.Question, &optJSON, &guardJS, &it.BlockingSince,
			&ph.PhaseID, &ph.Seq, &ph.Name, &ph.TaskID, &ph.PlanExternalID, &ph.PlanTitle, &it.ProjectSlug); err != nil {
			return nil, err
		}
		dec.TaskID, dec.PhaseID = ph.TaskID, ph.PhaseID
		dec.Options = decisionOptions(optJSON)
		dec.Guard = json.RawMessage(guardJS)
		if !json.Valid(dec.Guard) {
			dec.Guard = json.RawMessage("{}")
		}
		it.Kind = needsYouPhaseDecision
		it.SessionName = manualPhaseTitle(ph)
		it.Question = clipTail(dec.Question, needsYouQuestionMax)
		it.Preview = clipHead(dec.DecisionID+": "+dec.Question, needsYouPreviewMax)
		it.Phase = &ph
		it.Decision = &dec
		it.at = parseNeedsYouTS(it.BlockingSince)
		out = append(out, it)
	}
	return out, rows.Err()
}

// decisionOptions turns stored option ids into labelled options; an id without
// a known label is shown as itself, and an unreadable list falls back to the
// guard options — a card without buttons could not be answered at all.
func decisionOptions(optJSON string) []needsYouDecisionOption {
	var ids []string
	if json.Unmarshal([]byte(optJSON), &ids) != nil || len(ids) == 0 {
		ids = phaserun.GuardOptions
	}
	out := make([]needsYouDecisionOption, 0, len(ids))
	for _, id := range ids {
		label := guardOptionLabels[id]
		if label == "" {
			label = id
		}
		out = append(out, needsYouDecisionOption{ID: id, Label: label})
	}
	return out
}

// phaseGuardDecisions maps each phase of a plan to its open guard decision —
// the newest one, should a phase hold several. Best-effort, like the other
// per-plan reads: a failed read leaves every phase without one.
func (h *Handler) phaseGuardDecisions(taskID int64) map[int64]*needsYouDecision {
	return h.phaseOpenDecisions(taskID, store.DecisionSourceStop)
}

// phaseReviewDecisions maps each phase of a plan to its open review decision
// (verify-not-trust phase 9) — the Review tab answers it inline.
func (h *Handler) phaseReviewDecisions(taskID int64) map[int64]*needsYouDecision {
	return h.phaseOpenDecisions(taskID, store.DecisionSourceReview)
}

// phaseOpenDecisions maps each phase of a plan to its newest open decision of
// source.
func (h *Handler) phaseOpenDecisions(taskID int64, source string) map[int64]*needsYouDecision {
	out := map[int64]*needsYouDecision{}
	ds, err := store.ListTaskDecisions(h.DB, taskID)
	if err != nil {
		log.Printf("warning: epics: guard decisions unreadable (task %d): %v", taskID, err)
		return out
	}
	for _, d := range ds {
		if d.Source != source || d.Status != store.DecisionUndecided || d.PhaseID == nil {
			continue
		}
		g := json.RawMessage(d.GuardJSON)
		if !json.Valid(g) {
			g = json.RawMessage("{}")
		}
		out[*d.PhaseID] = &needsYouDecision{TaskID: taskID, PhaseID: *d.PhaseID, DecisionID: d.DecisionID, Source: d.Source,
			Question: d.Question, Options: decisionOptions(mustJSON(d.OptionIDs)), Guard: g}
	}
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}
