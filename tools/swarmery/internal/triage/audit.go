package triage

import (
	"database/sql"
	"slices"
	"strings"

	dec "github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
)

// AuditRow is how often the agent's label on an audit-sample question matched the operator's.
type AuditRow struct {
	QuestionID string `json:"questionId"`
	Answered   int    `json:"answered"`
	Agree      int    `json:"agree"`
}

// ClassifierAudit compares the agent's answers kept back as the audit sample with the labels
// the operator later gave for the same decisions. It reads only; nothing is copied.
//
// Only the newest sample verdict per decision counts, and only a label the operator wrote:
// an agent or observed label says nothing about how often the agent is right. Agreement is
// decide.Agrees, so a question's own matching rules apply. The result is never nil.
func ClassifierAudit(db *sql.DB) ([]AuditRow, error) {
	rows, err := db.Query(`
		SELECT d.question_id, d.ground_truth, v.value
		  FROM triage_verdicts v
		  JOIN decisions d ON d.id = CAST(v.ref AS INTEGER)
		 WHERE v.id IN (SELECT MAX(id) FROM triage_verdicts
		                 WHERE kind = ? AND state IN (?, ?) GROUP BY ref)
		   AND d.ground_truth IS NOT NULL AND d.ground_truth_source = ?`,
		kindClassifier, StateSample, StateAudited, dec.TruthOperator)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byQuestion := map[string]*AuditRow{}
	for rows.Next() {
		var question, truth, value string
		if err := rows.Scan(&question, &truth, &value); err != nil {
			return nil, err
		}
		r, ok := byQuestion[question]
		if !ok {
			r = &AuditRow{QuestionID: question}
			byQuestion[question] = r
		}
		r.Answered++
		if dec.Agrees(question, truth, value, false) {
			r.Agree++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]AuditRow, 0, len(byQuestion))
	for _, r := range byQuestion {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b AuditRow) int { return strings.Compare(a.QuestionID, b.QuestionID) })
	return out, nil
}
