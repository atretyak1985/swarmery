package api

import (
	"net/http"
	"strings"
	"testing"
)

func decisionReq(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// TestDecisionsEndpoints: stats list every known question (shadow by default,
// unconfigured without an engine), the mode switch persists, operator ground
// truth lands on the row, and a session's labels read as null until written.
func TestDecisionsEndpoints(t *testing.T) {
	srv, db := testServerWithDB(t)

	var got decisionsResponse
	getJSON(t, srv.URL+"/api/decisions", &got)
	if got.Configured || len(got.Questions) != 5 || got.Questions[0].QuestionID != "d1.run_end" || got.Questions[0].Mode != "shadow" {
		t.Fatalf("summary = %+v", got)
	}

	if r := decisionReq(t, http.MethodPut, srv.URL+"/api/decisions/d1.run_end/mode", `{"mode":"active"}`); r.StatusCode != 200 {
		t.Fatalf("PUT mode = %d", r.StatusCode)
	}
	getJSON(t, srv.URL+"/api/decisions", &got)
	if got.Questions[0].Mode != "active" {
		t.Errorf("mode after switch = %q", got.Questions[0].Mode)
	}
	if r := decisionReq(t, http.MethodPut, srv.URL+"/api/decisions/d1.run_end/mode", `{"mode":"loud"}`); r.StatusCode != 400 {
		t.Errorf("bad mode = %d, want 400", r.StatusCode)
	}
	if r := decisionReq(t, http.MethodPut, srv.URL+"/api/decisions/nope/mode", `{"mode":"off"}`); r.StatusCode != 400 {
		t.Errorf("unknown question = %d, want 400", r.StatusCode)
	}

	if _, err := db.Exec(`INSERT INTO decisions (question_id, subject, input_hash, answer, confidence, calibrated, backend, created_at)
		VALUES ('d1.run_end', 'phaserun:1', 'h', 'blocked', 0.93, 1, 'local', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if r := decisionReq(t, http.MethodPost, srv.URL+"/api/decisions/1/ground-truth", `{"value":"blocked"}`); r.StatusCode != http.StatusNoContent {
		t.Fatalf("POST truth = %d", r.StatusCode)
	}
	if r := decisionReq(t, http.MethodPost, srv.URL+"/api/decisions/99/ground-truth", `{"value":"blocked"}`); r.StatusCode != 404 {
		t.Errorf("unknown decision = %d, want 404", r.StatusCode)
	}
	if r := decisionReq(t, http.MethodPost, srv.URL+"/api/decisions/x/ground-truth", `{}`); r.StatusCode != 400 {
		t.Errorf("bad id = %d, want 400", r.StatusCode)
	}
	getJSON(t, srv.URL+"/api/decisions", &got)
	q := got.Questions[0]
	if q.Calls != 1 || q.WithTruth != 1 || q.Agreement == nil || *q.Agreement != 1 || q.Histogram[9] != 1 {
		t.Errorf("stats after truth = %+v", q)
	}

	var labels map[string]any
	getJSON(t, srv.URL+"/api/decisions/labels/nope", &labels)
	if v, ok := labels["labels"]; !ok || v != nil {
		t.Errorf("labels = %v, want null", labels)
	}
	if _, err := db.Exec(`INSERT INTO session_labels (session_uuid, task_type, outcome, failure_cause, labeled_at)
		VALUES ('s1', 'bugfix', 'shipped', 'none', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	getJSON(t, srv.URL+"/api/decisions/labels/s1", &labels)
	if l, _ := labels["labels"].(map[string]any); l["outcome"] != "shipped" {
		t.Errorf("labels = %v", labels)
	}
}
