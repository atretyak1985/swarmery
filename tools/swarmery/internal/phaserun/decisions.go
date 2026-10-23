package phaserun

import (
	"database/sql"
	"encoding/json"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
)

// DecisionUndecidedError refuses a phase whose doc declares
// `**Depends on decisions:**` on a decision that is still undecided and expensive
// to change (verify-not-trust phase 2). Nothing was acquired or stamped; the fix
// is to take the decision (or mark it cheap), not to retry. Force does not lift
// it, for the reason it does not lift the doc gates: the gate is IN the doc.
type DecisionUndecidedError struct {
	// Detail is phasegate's hold detail: `decision-undecided: D-3 (<question>)`.
	Detail string
	// Decisions are the holding `D-n` ids, in the order the doc lists them.
	Decisions []string
	// Refs are the same decisions with the planning turn that asked each one
	// (verify-not-trust phase 10): where the operator takes it ("decide now").
	Refs []phasegate.HoldRef
}

// Error is the hold detail, followed — when the first holding decision has a
// planning turn — by where to take it, so the refusal names its own way out.
func (e *DecisionUndecidedError) Error() string {
	for _, r := range e.Refs {
		if r.PlanningTurn != "" {
			return e.Detail + " — decide " + r.DecisionID + " on its planning turn: " + r.PlanningTurn
		}
	}
	return e.Detail
}

// querier is the read half of *sql.DB / *sql.Tx.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// DecisionHold reads one phase's declared decision dependencies and its plan's
// decisions and asks phasegate whether they hold it — nil when they do not.
// Exported for the API so the phase card and the start gate answer from the same
// call. A phase that declares nothing never reads phase_decisions.
func DecisionHold(q querier, phaseID int64) (*DecisionUndecidedError, error) {
	var (
		taskID   int64
		depsJSON string
		slug     string
	)
	if err := q.QueryRow(`SELECT e.workspace_task_id, e.decision_deps, COALESCE(p.slug, '')
		  FROM epic_phases e
		  LEFT JOIN tasks t ON t.id = e.workspace_task_id
		  LEFT JOIN projects p ON p.id = t.project_id
		 WHERE e.id = ?`, phaseID).
		Scan(&taskID, &depsJSON, &slug); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var deps []string
	if json.Unmarshal([]byte(depsJSON), &deps) != nil || len(deps) == 0 {
		return nil, nil
	}
	decisions, err := TaskDecisions(q, taskID)
	if err != nil {
		return nil, err
	}
	return HoldForIn(slug, deps, decisions), nil
}

// TaskDecisions loads a plan's decisions in the shape phasegate judges.
func TaskDecisions(q querier, taskID int64) ([]phasegate.Decision, error) {
	rows, err := q.Query(`SELECT decision_id, question, cost, status, seq, COALESCE(planning_session_id, 0)
		FROM phase_decisions WHERE workspace_task_id = ? ORDER BY seq, id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []phasegate.Decision
	for rows.Next() {
		var d phasegate.Decision
		if err := rows.Scan(&d.ID, &d.Question, &d.Cost, &d.Status, &d.Seq, &d.PlanningSessionID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// HoldFor is DecisionHold over already-loaded rows (the API's per-plan read),
// without planning-turn links.
func HoldFor(deps []string, decisions []phasegate.Decision) *DecisionUndecidedError {
	return HoldForIn("", deps, decisions)
}

// HoldForIn is HoldFor with the project's slug, so each holding decision that
// came from an interview carries its planning-turn link.
func HoldForIn(projectSlug string, deps []string, decisions []phasegate.Decision) *DecisionUndecidedError {
	hold, reason := phasegate.HoldsForDecisions(deps, decisions)
	if !hold {
		return nil
	}
	e := &DecisionUndecidedError{Detail: reason, Refs: phasegate.HoldRefs(projectSlug, deps, decisions)}
	for _, r := range e.Refs {
		e.Decisions = append(e.Decisions, r.DecisionID)
	}
	return e
}
