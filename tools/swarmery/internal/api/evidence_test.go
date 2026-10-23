package api

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// GET /api/epics/{taskId}/evidence serves the decision table with its chips,
// and an INCONCLUSIVE plan review reads not-verified on the epic payload.
func TestEpicEvidenceEndpoint(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	tid := strconv.FormatInt(taskID, 10)

	var empty wsingest.DecisionTable
	getJSON(t, srv.URL+"/api/epics/"+tid+"/evidence", &empty)
	if empty.Rows == nil || len(empty.Rows) != 0 {
		t.Fatalf("a plan without decisions or spec: rows = %#v, want []", empty.Rows)
	}

	mustExecAPI(t, db, `INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, source, question, status, answer, created_at)
		VALUES (?, 'D-1', 1, 'interview', 'Format?', 'decided', 'CSV', 'x'),
		       (?, 'D-2', 2, 'interview', 'Mirror?', 'decided', 'S3', 'x')`, taskID, taskID)
	mustExecAPI(t, db, `INSERT INTO spec_criteria (workspace_task_id, pos, cid, text) VALUES (?, 0, 'SC-1', 'exports')`, taskID)
	mustExecAPI(t, db, `UPDATE epic_phases SET covers = '["SC-1"]', decision_deps = '["D-1","D-2"]' WHERE workspace_task_id = ? AND seq = 1`, taskID)
	mustExecAPI(t, db, `INSERT INTO decision_evidence (workspace_task_id, decision_id, phase_id, doc_path, kind, status, excerpt, created_at)
		SELECT ?, 'D-1', id, doc_path, 'report', 'evidence', 'CSV writer per D-1', 'x' FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`,
		taskID, taskID)
	mustExecAPI(t, db, `INSERT INTO decision_evidence (workspace_task_id, decision_id, phase_id, doc_path, kind, status, excerpt, created_at)
		SELECT ?, 'D-2', id, doc_path, 'report', 'not-implemented', 'bucket policy pending', 'x' FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`,
		taskID, taskID)

	var tb wsingest.DecisionTable
	getJSON(t, srv.URL+"/api/epics/"+tid+"/evidence", &tb)
	got := map[string]string{}
	for _, r := range tb.Rows {
		got[r.ID] = r.Status
	}
	if got["D-1"] != "evidence" || got["D-2"] != "not-implemented" || got["SC-1"] != "remaining" || len(got) != 3 {
		t.Errorf("chips = %v", got)
	}
	if tb.Rows[0].Evidence[0].PhaseSeq != 1 || tb.Rows[0].ClaimedBy[0] != 1 {
		t.Errorf("D-1 row = %+v", tb.Rows[0])
	}

	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	if epics[0].PlanReview != nil {
		t.Errorf("planReview before any plan review = %+v, want null", epics[0].PlanReview)
	}

	mustExecAPI(t, db, `INSERT INTO phase_reviews (scope, phase_id, workspace_task_id, session_uuid, run_session_uuid,
		tree_before, tree_after, verdict, detail, findings, fix_round, started_at, finished_at)
		VALUES ('plan', NULL, ?, 's', 'branchset:k', '', '', 'inconclusive', 'reviewer-timed-out: killed', '', 0, 'a', 'b')`, tid)
	getJSON(t, srv.URL+"/api/epics", &epics)
	if pr := epics[0].PlanReview; pr == nil || pr.Status != "not-verified" || pr.ReasonClass != "reviewer-timed-out" {
		t.Errorf("planReview = %+v, want not-verified / reviewer-timed-out", pr)
	}
	getJSON(t, srv.URL+"/api/epics/"+tid+"/evidence", &tb)
	for _, r := range tb.Rows {
		if r.ID == "SC-1" && r.Status != "not-verified" {
			t.Errorf("SC-1 after an inconclusive plan review = %s, want not-verified", r.Status)
		}
	}

	resp, err := srv.Client().Get(srv.URL + "/api/epics/999999/evidence")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown task status = %d, want 404", resp.StatusCode)
	}
}
