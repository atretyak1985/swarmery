package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// reviewsFixture seeds phase_reviews on top of epicFixture: two phase-scope
// reviews of phase 1, one plan-scope review that is still unacked, and one that
// is acked. It returns the ids in insertion order.
func reviewsFixture(t *testing.T) (srvURL string, db *sql.DB, taskID, phase1 int64, ids []int64) {
	t.Helper()
	srv, db, taskID, _ := epicFixture(t)
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id=? AND seq=1`, taskID).Scan(&phase1); err != nil {
		t.Fatal(err)
	}
	tid := strconv.FormatInt(taskID, 10)
	insert := func(scope string, phaseID any, runUUID, verdict, acked string) int64 {
		t.Helper()
		var a any
		if acked != "" {
			a = acked
		}
		res, err := db.Exec(`INSERT INTO phase_reviews
			(scope, phase_id, workspace_task_id, session_uuid, run_session_uuid, verdict, detail, findings,
			 fix_round, started_at, finished_at, acked_at)
			VALUES (?, ?, ?, 'rev-uuid', ?, ?, 'reasons', '- P1 a.go:1 — wrong', 0,
			        '2026-10-10T10:00:00Z', '2026-10-10T10:05:00Z', ?)`,
			scope, phaseID, tid, runUUID, verdict, a)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	ids = []int64{
		insert("phase", phase1, "run-1", "fail", ""),
		insert("phase", phase1, "run-2", "pass", ""),
		insert("plan", nil, "branchset:abc123", "fail", ""),
		insert("plan", nil, "branchset:def456", "pass", "2026-10-10T11:00:00Z"),
	}
	return srv.URL, db, taskID, phase1, ids
}

func getReviews(t *testing.T, url string) (int, []reviewDTO) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if strings.TrimSpace(string(body)) == "null" {
		t.Fatalf("GET %s encoded null, want []", url)
	}
	var out []reviewDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v\n%s", url, err, body)
	}
	return resp.StatusCode, out
}

func TestListReviewsPlanUnacked(t *testing.T) {
	url, _, taskID, _, ids := reviewsFixture(t)

	code, got := getReviews(t, url+"/api/reviews?scope=plan&unacked=1")
	if code != http.StatusOK || len(got) != 1 {
		t.Fatalf("plan&unacked: status %d, %d rows, want 200 and 1", code, len(got))
	}
	r := got[0]
	if r.ID != ids[2] || r.Scope != "plan" || r.PhaseID != nil || r.TaskID != taskID || r.PlanTitle != "My Epic" {
		t.Errorf("plan review = %+v", r)
	}
	if r.BranchSetKey != "abc123" || r.RunSessionUUID != "" {
		t.Errorf("branchSetKey=%q runSessionUuid=%q, want the key without its prefix and no run uuid", r.BranchSetKey, r.RunSessionUUID)
	}
	if r.Verdict != "fail" || r.Findings == "" || r.AckedAt != nil || r.FinishedAt == nil || r.CostUSD != nil {
		t.Errorf("plan review fields = %+v", r)
	}

	// All plan reviews, newest first.
	_, got = getReviews(t, url+"/api/reviews?scope=plan")
	if len(got) != 2 || got[0].ID != ids[3] || got[1].ID != ids[2] {
		t.Errorf("plan reviews = %v, want ids [%d %d]", reviewIDs(got), ids[3], ids[2])
	}
	if got[0].AckedAt == nil {
		t.Error("the acked review has no ackedAt")
	}
	// No filter: everything, newest first.
	_, got = getReviews(t, url+"/api/reviews")
	if len(got) != 4 || got[0].ID != ids[3] {
		t.Errorf("all reviews = %v", reviewIDs(got))
	}
	// taskId filter: an unknown plan has none, and that is [] not null.
	_, got = getReviews(t, url+"/api/reviews?taskId=99999")
	if len(got) != 0 {
		t.Errorf("reviews of an unknown plan = %v", reviewIDs(got))
	}
	_, got = getReviews(t, fmt.Sprintf("%s/api/reviews?taskId=%d&limit=1", url, taskID))
	if len(got) != 1 || got[0].ID != ids[3] {
		t.Errorf("limit=1 = %v, want [%d]", reviewIDs(got), ids[3])
	}

	for _, bad := range []string{"scope=board", "unacked=maybe", "taskId=x", "limit=0"} {
		if code, _ := getReviews(t, url+"/api/reviews?"+bad); code != http.StatusBadRequest {
			t.Errorf("?%s: status %d, want 400", bad, code)
		}
	}
}

func TestAckReview(t *testing.T) {
	url, db, _, _, ids := reviewsFixture(t)
	ack := func(id string, origin string) (int, reviewDTO) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, url+"/api/reviews/"+id+"/ack", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var d reviewDTO
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, d
	}
	plan := strconv.FormatInt(ids[2], 10)

	code, d := ack(plan, "")
	if code != http.StatusOK || d.ID != ids[2] || d.AckedAt == nil {
		t.Fatalf("ack: status %d, %+v", code, d)
	}
	first := *d.AckedAt
	// Idempotent: the first acknowledgement's time stays.
	mustExecAPI(t, db, `UPDATE phase_reviews SET acked_at='2026-10-10T12:00:00Z' WHERE id=?`, ids[2])
	code, d = ack(plan, "")
	if code != http.StatusOK || d.AckedAt == nil || *d.AckedAt != "2026-10-10T12:00:00Z" {
		t.Errorf("second ack: status %d, ackedAt %v, want the stored time kept (first was %s)", code, d.AckedAt, first)
	}
	if _, got := getReviews(t, url+"/api/reviews?scope=plan&unacked=1"); len(got) != 0 {
		t.Errorf("unacked plan reviews after the ack = %v, want none", reviewIDs(got))
	}

	if code, _ := ack("424242", ""); code != http.StatusNotFound {
		t.Errorf("ack unknown: status %d, want 404", code)
	}
	if code, _ := ack("abc", ""); code != http.StatusBadRequest {
		t.Errorf("ack bad id: status %d, want 400", code)
	}
	if code, _ := ack(plan, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("cross-origin ack: status %d, want 403", code)
	}
}

func TestListPhaseReviews(t *testing.T) {
	url, db, taskID, phase1, ids := reviewsFixture(t)

	code, got := getReviews(t, fmt.Sprintf("%s/api/epics/%d/phases/%d/reviews", url, taskID, phase1))
	if code != http.StatusOK || len(got) != 2 {
		t.Fatalf("phase reviews: status %d, %d rows, want 200 and 2", code, len(got))
	}
	if got[0].ID != ids[1] || got[1].ID != ids[0] {
		t.Errorf("phase reviews = %v, want newest first [%d %d]", reviewIDs(got), ids[1], ids[0])
	}
	r := got[1]
	if r.Scope != "phase" || r.PhaseID == nil || *r.PhaseID != phase1 || r.PhaseName != "Phase 1 — Schema" ||
		r.RunSessionUUID != "run-1" || r.BranchSetKey != "" || r.Verdict != "fail" {
		t.Errorf("phase review = %+v", r)
	}

	// Phase 2 has none: [] not null.
	var phase2 int64
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id=? AND seq=2`, taskID).Scan(&phase2); err != nil {
		t.Fatal(err)
	}
	if code, got := getReviews(t, fmt.Sprintf("%s/api/epics/%d/phases/%d/reviews", url, taskID, phase2)); code != http.StatusOK || len(got) != 0 {
		t.Errorf("phase 2 reviews: status %d, %v", code, reviewIDs(got))
	}
	// A phase outside the epic is 404.
	if code, _ := getReviews(t, fmt.Sprintf("%s/api/epics/%d/phases/%d/reviews", url, taskID+1, phase1)); code != http.StatusNotFound {
		t.Errorf("phase of another epic: status %d, want 404", code)
	}
}

func reviewIDs(rs []reviewDTO) []int64 {
	out := make([]int64, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func mustExecAPI(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}
