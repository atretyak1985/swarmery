package api

import "testing"

// The labelling queue leaves out the answers the rules gave — they follow from
// the session's own record, so there is nothing for the operator to judge — and
// returns them only when ?rules=1 asks (the Inbox never does).
func TestDecisionsQueueRulesParam(t *testing.T) {
	srv, db := testServerWithDB(t)
	for _, row := range []struct{ q, answer, backend string }{
		{"d2.task_type", "docs", "local"},     // id 1
		{"d2.outcome", "shipped", "rules"},    // id 2: a rule's answer
		{"d2.failure_cause", "none", "rules"}, // id 3: a rule's answer
	} {
		if _, err := db.Exec(`INSERT INTO decisions (question_id, subject, session_uuid, input_hash, answer, confidence, calibrated, error, backend, created_at)
			VALUES (?, 'x', '', 'h', ?, 1, 1, '', ?, '2026-09-24T18:00:00Z')`, row.q, row.answer, row.backend); err != nil {
			t.Fatal(err)
		}
	}
	var q struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	for _, c := range []struct {
		query string
		want  []int64
	}{
		{"", []int64{1}},
		{"?limit=all", []int64{1}},
		{"?rules=0", []int64{1}},
		{"?rules=nope", []int64{1}},
		{"?rules=1", []int64{3, 2, 1}},
		{"?rules=true&limit=all", []int64{3, 2, 1}},
	} {
		getJSON(t, srv.URL+"/api/decisions/queue"+c.query, &q)
		got := []int64{}
		for _, it := range q.Items {
			got = append(got, it.ID)
		}
		if len(got) != len(c.want) {
			t.Errorf("queue%s = %v, want %v", c.query, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("queue%s = %v, want %v", c.query, got, c.want)
				break
			}
		}
	}
}
