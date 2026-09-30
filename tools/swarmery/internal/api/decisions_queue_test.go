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
	// The queue hides decisions of a session with no turns (nothing to judge
	// them from), so the session under test has one.
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at, text)
		VALUES ((SELECT id FROM sessions WHERE session_uuid = 's-q1'), 1, 'assistant', '2026-09-24T10:05:00Z', 'Refactored.')`); err != nil {
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

// ?project= narrows the labelling queue to decisions about that project's
// sessions: the Inbox shows it under the project switcher, so a project view
// must not list the whole fleet's classifier questions. A decision with no
// session is fleet-level and drops out; an unknown project is an empty queue.
func TestDecisionsQueueProjectScope(t *testing.T) {
	srv, db := testServerWithDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(901, '/work/alpha', '-work-alpha', 'Alpha', '2026-09-24T00:00:00Z'),
		(902, '/work/beta',  '-work-beta',  'Beta',  '2026-09-24T00:00:00Z')`)
	exec(`INSERT INTO sessions (project_id, session_uuid, title, started_at) VALUES
		(901, 's-alpha', 'Alpha work', '2026-09-24T10:00:00Z'),
		(902, 's-beta',  'Beta work',  '2026-09-24T10:00:00Z')`)
	// One turn each: the queue hides decisions of a session that has none.
	exec(`INSERT INTO turns (session_id, seq, role, started_at, text)
		SELECT id, 1, 'assistant', '2026-09-24T10:05:00Z', 'Done.' FROM sessions WHERE session_uuid IN ('s-alpha', 's-beta')`)
	for _, row := range []struct{ sess, at string }{
		{"s-alpha", "2026-09-24T18:00:00Z"}, // id 1
		{"s-alpha", "2026-09-24T18:00:01Z"}, // id 2
		{"s-beta", "2026-09-24T18:00:02Z"},  // id 3
		{"", "2026-09-24T18:00:03Z"},        // id 4: no session — fleet-level
	} {
		exec(`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, error, backend, created_at)
			VALUES ('d2.task_type', 'x', ?, 'h', 'refactor', 0.8, '', 'local', ?)`, row.sess, row.at)
	}

	var q struct {
		Items []struct {
			SessionUUID string `json:"sessionUuid"`
		} `json:"items"`
	}
	sessions := func(path string) []string {
		t.Helper()
		getJSON(t, srv.URL+path, &q)
		out := []string{}
		for _, it := range q.Items {
			out = append(out, it.SessionUUID)
		}
		return out
	}

	if got := sessions("/api/decisions/queue"); len(got) != 4 {
		t.Errorf("unscoped queue = %v, want all 4 decisions", got)
	}
	for _, scope := range []string{"-work-alpha", "901", "alpha"} {
		got := sessions("/api/decisions/queue?project=" + scope)
		if len(got) != 2 || got[0] != "s-alpha" || got[1] != "s-alpha" {
			t.Errorf("project=%s queue = %v, want only the two s-alpha decisions", scope, got)
		}
	}
	if got := sessions("/api/decisions/queue?project=-work-beta"); len(got) != 1 || got[0] != "s-beta" {
		t.Errorf("project=-work-beta queue = %v, want only s-beta", got)
	}
	if got := sessions("/api/decisions/queue?project=-work-ghost"); len(got) != 0 {
		t.Errorf("unknown project queue = %v, want empty (never the fleet)", got)
	}
}

// limit=all returns the whole open queue — the Inbox lists every question, not
// the first page — while a missing limit keeps the 100 default and an
// out-of-range number still falls back to it.
func TestDecisionsQueueLimitAll(t *testing.T) {
	srv, db := testServerWithDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 620; i++ {
		if _, err := tx.Exec(`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, error, backend, created_at)
			VALUES ('d2.task_type', 'x', '', 'h', 'refactor', 0.8, '', 'local', '2026-09-24T18:00:00Z')`); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var q struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	for _, c := range []struct {
		query string
		want  int
	}{
		{"", 100},
		{"?limit=all", 620},
		{"?limit=250", 250},
		{"?limit=9999", 100},
	} {
		getJSON(t, srv.URL+"/api/decisions/queue"+c.query, &q)
		if len(q.Items) != c.want {
			t.Errorf("queue%s = %d items, want %d", c.query, len(q.Items), c.want)
		}
	}
}
