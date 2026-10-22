package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Decision vocabulary — the closed sets migration 0120 CHECKs.
const (
	DecisionSourceInterview = "interview"
	DecisionSourceStop      = "stop"
	DecisionSourceReview    = "review"

	DecisionCostCheap     = "cheap"
	DecisionCostExpensive = "expensive"

	DecisionDecided     = "decided"
	DecisionUndecided   = "undecided"
	DecisionRecommended = "recommended"
)

// Decision is one phase_decisions row (migration 0120): an interview question,
// a stop trigger or a review outcome, named `D-n` within its plan.
type Decision struct {
	ID                int64    `json:"id"`
	WorkspaceTaskID   *int64   `json:"workspaceTaskId"`
	PlanningSessionID *int64   `json:"planningSessionId"`
	DecisionID        string   `json:"decisionId"` // "D-3"
	Seq               int      `json:"seq"`        // 3
	Source            string   `json:"source"`
	QuestionID        string   `json:"questionId"`
	Question          string   `json:"question"`
	Cost              string   `json:"cost"`
	CostReason        string   `json:"costReason"`
	Status            string   `json:"status"`
	Answer            string   `json:"answer"`
	OptionIDs         []string `json:"optionIds"`
	PhaseID           *int64   `json:"phaseId"`
	CreatedAt         string   `json:"createdAt"`
	DecidedAt         *string  `json:"decidedAt"`
	// GuardJSON is the guard check a stop decision is about (migration 0121,
	// phaserun.GuardResult); "{}" for every other decision.
	GuardJSON string `json:"-"`
}

// DecisionID renders the `D-n` id of a sequence number.
func DecisionID(seq int) string { return fmt.Sprintf("D-%d", seq) }

// HoldsWork reports whether this decision blocks a phase that depends on it:
// undecided AND expensive to change. A cheap undecided decision never holds —
// guessing it wrong costs less than waiting.
func (d Decision) HoldsWork() bool {
	return d.Status == DecisionUndecided && d.Cost == DecisionCostExpensive
}

// UndecidedStatus is the status an UNANSWERED question gets from its cost: a
// cheap one is taken at the planner's recommended option, an expensive one waits.
func UndecidedStatus(cost string) string {
	if cost == DecisionCostExpensive {
		return DecisionUndecided
	}
	return DecisionRecommended
}

const decisionCols = `id, workspace_task_id, planning_session_id, decision_id, seq, source,
	question_id, question, cost, cost_reason, status, answer, option_ids_json, phase_id,
	created_at, decided_at, guard_json`

func scanDecision(scan func(...any) error) (Decision, error) {
	var (
		d                 Decision
		task, sess, phase sql.NullInt64
		optJSON           string
		decidedAt         sql.NullString
	)
	if err := scan(&d.ID, &task, &sess, &d.DecisionID, &d.Seq, &d.Source,
		&d.QuestionID, &d.Question, &d.Cost, &d.CostReason, &d.Status, &d.Answer,
		&optJSON, &phase, &d.CreatedAt, &decidedAt, &d.GuardJSON); err != nil {
		return Decision{}, err
	}
	if task.Valid {
		d.WorkspaceTaskID = &task.Int64
	}
	if sess.Valid {
		d.PlanningSessionID = &sess.Int64
	}
	if phase.Valid {
		d.PhaseID = &phase.Int64
	}
	if decidedAt.Valid {
		d.DecidedAt = &decidedAt.String
	}
	if json.Unmarshal([]byte(optJSON), &d.OptionIDs) != nil || d.OptionIDs == nil {
		d.OptionIDs = []string{}
	}
	return d, nil
}

func queryDecisions(q Querier, where string, args ...any) ([]Decision, error) {
	rows, err := q.Query(`SELECT `+decisionCols+` FROM phase_decisions WHERE `+where+` ORDER BY seq, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Decision{}
	for rows.Next() {
		d, err := scanDecision(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListTaskDecisions returns a plan's decisions in D-n order.
func ListTaskDecisions(q Querier, workspaceTaskID int64) ([]Decision, error) {
	return queryDecisions(q, `workspace_task_id = ?`, workspaceTaskID)
}

// ListSessionDecisions returns the decisions PROCEED recorded for one planning
// session, adopted onto a task or not.
func ListSessionDecisions(q Querier, planningSessionID int64) ([]Decision, error) {
	return queryDecisions(q, `planning_session_id = ?`, planningSessionID)
}

// UpsertSessionDecision writes one interview decision keyed by
// (planning_session_id, decision_id) — PROCEED's write, and idempotent so a
// second PROCEED (a retried PHASE B) rewrites the same rows instead of adding.
func UpsertSessionDecision(q Querier, d Decision) error {
	if d.PlanningSessionID == nil {
		return errors.New("store: session decision without a planning session id")
	}
	opt, err := json.Marshal(nonNilStrings(d.OptionIDs))
	if err != nil {
		return err
	}
	_, err = q.Exec(`
		INSERT INTO phase_decisions
			(planning_session_id, decision_id, seq, source, question_id, question,
			 cost, cost_reason, status, answer, option_ids_json, created_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(planning_session_id, decision_id) DO UPDATE SET
			seq = excluded.seq, source = excluded.source,
			question_id = excluded.question_id, question = excluded.question,
			cost = excluded.cost, cost_reason = excluded.cost_reason,
			status = excluded.status, answer = excluded.answer,
			option_ids_json = excluded.option_ids_json, decided_at = excluded.decided_at`,
		*d.PlanningSessionID, d.DecisionID, d.Seq, orDefault(d.Source, DecisionSourceInterview),
		d.QuestionID, d.Question, orDefault(d.Cost, DecisionCostCheap), d.CostReason,
		d.Status, d.Answer, string(opt), d.CreatedAt, d.DecidedAt)
	return err
}

// SetSessionDecisionCost flips the cost of one recorded interview decision and
// re-derives the status of an unanswered one (cheap → recommended, expensive →
// undecided; an answered decision stays decided). Returns the updated row, or
// (nil, nil) when PROCEED has not recorded that decision yet.
func SetSessionDecisionCost(q Querier, planningSessionID int64, decisionID, cost, reason string) (*Decision, error) {
	cost = strings.TrimSpace(cost)
	if cost != DecisionCostCheap && cost != DecisionCostExpensive {
		return nil, fmt.Errorf("store: unknown decision cost %q", cost)
	}
	if _, err := q.Exec(`
		UPDATE phase_decisions
		   SET cost = ?,
		       cost_reason = CASE WHEN ? <> '' THEN ? ELSE cost_reason END,
		       status = CASE WHEN status = 'decided' THEN 'decided'
		                     WHEN ? = 'expensive' THEN 'undecided' ELSE 'recommended' END
		 WHERE planning_session_id = ? AND decision_id = ?`,
		cost, reason, reason, cost, planningSessionID, decisionID); err != nil {
		return nil, err
	}
	d, err := scanDecision(q.QueryRow(`SELECT `+decisionCols+` FROM phase_decisions
		WHERE planning_session_id = ? AND decision_id = ?`, planningSessionID, decisionID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// UpsertStopDecision records the open stop decision of one phase: while the
// phase already has an UNDECIDED stop decision, that row is refreshed (question,
// options, guard_json) instead of a second one being minted — a re-run that hits
// the same guard asks the same question once. Otherwise a new row continues the
// task's D-n sequence (MAX(seq)+1), undecided and expensive. Returns the row and
// whether it was created.
func UpsertStopDecision(q Querier, taskID, phaseID int64, question string, optionIDs []string, guardJSON, now string) (Decision, bool, error) {
	return upsertPhaseDecision(q, DecisionSourceStop, taskID, phaseID, question, optionIDs, guardJSON, now)
}

// UpsertReviewDecision records the open review decision of one phase (source
// 'review', verify-not-trust phase 9): a review cycle that could not settle on its
// own — an incomplete review, or one still failing after its automatic rounds —
// asks the operator to repeat the review or accept it as is. Same refresh-or-mint
// rule as UpsertStopDecision: one open review decision per phase.
func UpsertReviewDecision(q Querier, taskID, phaseID int64, question string, optionIDs []string, now string) (Decision, bool, error) {
	return upsertPhaseDecision(q, DecisionSourceReview, taskID, phaseID, question, optionIDs, "{}", now)
}

// upsertPhaseDecision is the shared body of the per-phase decisions (stop,
// review): refresh the phase's open decision of that source, else mint the next
// D-n, undecided and expensive.
func upsertPhaseDecision(q Querier, source string, taskID, phaseID int64, question string, optionIDs []string, guardJSON, now string) (Decision, bool, error) {
	opt, err := json.Marshal(nonNilStrings(optionIDs))
	if err != nil {
		return Decision{}, false, err
	}
	if strings.TrimSpace(guardJSON) == "" {
		guardJSON = "{}"
	}
	open, err := queryDecisions(q, `workspace_task_id = ? AND phase_id = ? AND source = ? AND status = ?`,
		taskID, phaseID, source, DecisionUndecided)
	if err != nil {
		return Decision{}, false, err
	}
	if len(open) > 0 {
		d := open[len(open)-1]
		if _, err := q.Exec(`UPDATE phase_decisions SET question = ?, option_ids_json = ?, guard_json = ? WHERE id = ?`,
			question, string(opt), guardJSON, d.ID); err != nil {
			return Decision{}, false, err
		}
		got, err := GetDecision(q, d.ID)
		return got, false, err
	}
	var seq int
	if err := q.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM phase_decisions WHERE workspace_task_id = ?`,
		taskID).Scan(&seq); err != nil {
		return Decision{}, false, err
	}
	res, err := q.Exec(`
		INSERT INTO phase_decisions
			(workspace_task_id, decision_id, seq, source, question, cost, status,
			 option_ids_json, phase_id, created_at, guard_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, DecisionID(seq), seq, source, question, DecisionCostExpensive,
		DecisionUndecided, string(opt), phaseID, now, guardJSON)
	if err != nil {
		return Decision{}, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Decision{}, false, err
	}
	got, err := GetDecision(q, id)
	return got, true, err
}

// GetDecision reads one phase_decisions row by its primary key.
func GetDecision(q Querier, id int64) (Decision, error) {
	return scanDecision(q.QueryRow(`SELECT `+decisionCols+` FROM phase_decisions WHERE id = ?`, id).Scan)
}

// GetTaskDecision reads a plan's decision by its `D-n`; sql.ErrNoRows when the
// plan has none of that id.
func GetTaskDecision(q Querier, taskID int64, decisionID string) (Decision, error) {
	return scanDecision(q.QueryRow(`SELECT `+decisionCols+` FROM phase_decisions
		WHERE workspace_task_id = ? AND decision_id = ?`, taskID, decisionID).Scan)
}

// DecideDecision stamps an undecided decision decided with answer. It reports
// false when the row was no longer undecided (someone answered first).
func DecideDecision(q Querier, id int64, answer, now string) (bool, error) {
	res, err := q.Exec(`UPDATE phase_decisions SET status = ?, answer = ?, decided_at = ?
		WHERE id = ? AND status = ?`, DecisionDecided, answer, now, id, DecisionUndecided)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
