package api

import (
	"net/http"
	"testing"
)

// The labelling queue lists answered, unlabelled decisions newest first, with
// each question's options and the session's title; errored and labelled rows
// are left out, since= bounds the window, and ground truth outside a
// question's options is refused (it could never agree, and would skew the
// agreement the promotion rule reads).
func TestDecisionsQueueAndTruthValidation(t *testing.T) {
	srv, db := testServerWithDB(t)
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, title, started_at) VALUES ((SELECT MIN(id) FROM projects), 's-q1', 'Refactor the parser', '2026-09-24T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ q, sess, answer, errText, truth, at string }{
		{"d2.task_type", "s-q1", "refactor", "", "", "2026-09-24T18:00:00Z"},           // id 1: in queue
		{"d2.outcome", "s-q1", "shipped", "", "", "2026-09-24T18:00:01Z"},              // id 2: in queue
		{"d2.failure_cause", "s-q1", "", "local: timeout", "", "2026-09-24T18:00:02Z"}, // id 3: errored, out
		{"d2.task_type", "s-q2", "bugfix", "", "bugfix", "2026-09-24T18:00:03Z"},       // id 4: labelled, out
		{"d2.task_type", "s-q3", "docs", "", "", "2026-09-24T09:00:00Z"},               // id 5: before since
	} {
		var truth any
		if row.truth != "" {
			truth = row.truth
		}
		if _, err := db.Exec(`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, error, backend, ground_truth, created_at)
			VALUES (?, ?, ?, 'h', ?, 0.8, ?, 'local', ?, ?)`, row.q, row.sess, row.sess, row.answer, row.errText, truth, row.at); err != nil {
			t.Fatal(err)
		}
	}

	var q struct {
		Items []struct {
			ID           int64    `json:"id"`
			QuestionID   string   `json:"questionId"`
			SessionTitle string   `json:"sessionTitle"`
			Answer       string   `json:"answer"`
			Options      []string `json:"options"`
		} `json:"items"`
	}
	getJSON(t, srv.URL+"/api/decisions/queue", &q)
	if len(q.Items) != 3 || q.Items[0].ID != 5 || q.Items[2].ID != 1 {
		t.Fatalf("queue = %+v, want ids 5, 2, 1 (newest first; errored and labelled rows out)", q.Items)
	}
	if q.Items[2].SessionTitle != "Refactor the parser" || len(q.Items[2].Options) != 9 {
		t.Errorf("item 1 = %+v, want the session title and the 9 task types", q.Items[2])
	}
	getJSON(t, srv.URL+"/api/decisions/queue?since=2026-09-24T17:57:51Z", &q)
	if len(q.Items) != 2 {
		t.Errorf("since-bounded queue = %d items, want 2", len(q.Items))
	}

	if r := decisionReq(t, http.MethodPost, srv.URL+"/api/decisions/1/ground-truth", `{"value":"a-typo"}`); r.StatusCode != 400 {
		t.Errorf("off-option truth = %d, want 400", r.StatusCode)
	}
	if r := decisionReq(t, http.MethodPost, srv.URL+"/api/decisions/1/ground-truth", `{"value":"refactor"}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("valid truth = %d, want 204", r.StatusCode)
	}
	getJSON(t, srv.URL+"/api/decisions/queue", &q)
	if len(q.Items) != 2 {
		t.Errorf("labelled row still queued: %d items, want 2", len(q.Items))
	}
}
