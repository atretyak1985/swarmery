package decide

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// OptionsFor is a question's closed answer set: the values ground truth may
// take. nil for an unknown question.
func OptionsFor(questionID string) []string {
	switch questionID {
	case QD1:
		return D1Options
	case QD2TaskType:
		return TaskTypes
	case QD2Outcome:
		return Outcomes
	case QD2Failure:
		return FailureCauses
	case QD3Cause:
		return DivergenceCauses
	}
	return nil
}

// QueueItem is one answered, not-yet-labelled decision, as the Decisions page's
// labelling queue shows it. The decisions table stores an input HASH, never the
// input, so the operator judges from the subject itself (the session or run);
// SessionTitle is there to recognise it without opening it.
type QueueItem struct {
	ID           int64    `json:"id"`
	QuestionID   string   `json:"questionId"`
	Subject      string   `json:"subject"`
	SessionUUID  string   `json:"sessionUuid"`
	SessionTitle string   `json:"sessionTitle"`
	Answer       string   `json:"answer"`
	Confidence   *float64 `json:"confidence"`
	CreatedAt    string   `json:"createdAt"`
	Options      []string `json:"options"`
}

// LabelQueue lists answered decisions that have no ground truth yet, newest
// first. Errored rows are excluded (there is no answer to judge), and so are
// questions outside KnownQuestions. since ("" ⇒ no bound) keeps the queue to a
// window, e.g. the one a measurement counts.
func LabelQueue(db *sql.DB, limit int, since string) ([]QueueItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`
		SELECT d.id, d.question_id, d.subject, d.session_uuid,
		       COALESCE(NULLIF(s.custom_title, ''), s.title, ''),
		       d.answer, d.confidence, d.created_at
		  FROM decisions d
		  LEFT JOIN sessions s ON s.session_uuid = d.session_uuid AND d.session_uuid <> ''
		 WHERE d.ground_truth IS NULL AND d.error = '' AND d.answer <> ''
		   AND (? = '' OR d.created_at >= ?)
		 ORDER BY d.id DESC
		 LIMIT ?`, since, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueueItem{}
	for rows.Next() {
		var it QueueItem
		var conf sql.NullFloat64
		if err := rows.Scan(&it.ID, &it.QuestionID, &it.Subject, &it.SessionUUID, &it.SessionTitle,
			&it.Answer, &conf, &it.CreatedAt); err != nil {
			return nil, err
		}
		it.Options = OptionsFor(it.QuestionID)
		if it.Options == nil {
			continue
		}
		if conf.Valid {
			c := conf.Float64
			it.Confidence = &c
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ValidateTruth reports whether truth is an allowed ground-truth value for
// decision id's question. sql.ErrNoRows when the decision does not exist.
func ValidateTruth(db *sql.DB, id int64, truth string) error {
	var q string
	if err := db.QueryRow(`SELECT question_id FROM decisions WHERE id=?`, id).Scan(&q); err != nil {
		return err
	}
	opts := OptionsFor(q)
	if opts == nil {
		return fmt.Errorf("decision %d has an unknown question %q", id, q)
	}
	if !slices.Contains(opts, strings.TrimSpace(truth)) {
		return fmt.Errorf("%q is not an answer to %s; want one of %s", truth, q, strings.Join(opts, ", "))
	}
	return nil
}
