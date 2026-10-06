package decide

import (
	"context"
	"database/sql"
	"errors"
)

// SessionEvidence is the text the D2 classifier was shown about a session: the
// opening line plus the digest (d2Digest) — exactly the Input of the task-type
// question d2Questions builds, the fullest of the three. The session is loaded
// in the labeler's own shape (evalSession) and its facts with the labeler's
// error policy (d2FactsFor). Read-only. sql.ErrNoRows for an unknown session.
func SessionEvidence(db *sql.DB, sessionUUID string) (string, error) {
	s, reason, err := evalSession(context.Background(), db, sessionUUID)
	if err != nil {
		return "", err
	}
	if reason == EvalSkipNoSession {
		return "", sql.ErrNoRows
	}
	facts := d2FactsFor(db, s.uuid)
	return openingLine(facts, s.title) + d2Digest(db, s, facts), nil
}

// ClearAgentTruth removes a label a triage run wrote, so the question returns
// to the label queue. It never touches an operator or observed label:
// sql.ErrNoRows when decision id has no agent label (unknown id, unlabelled,
// already cleared, or labelled by someone else).
func ClearAgentTruth(db *sql.DB, id int64) error {
	if db == nil {
		return errors.New("decide: nil db")
	}
	res, err := db.Exec(`UPDATE decisions SET ground_truth=NULL, ground_truth_at=NULL, ground_truth_source=''
		WHERE id=? AND ground_truth_source=?`, id, TruthAgent)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
