package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// GET /api/epics/{taskId}/evidence lists every declared id with its chip, the
// claiming phases and the evidence behind it; the same table rides on the epic
// payload with planReviewStatus lifted; an INCONCLUSIVE plan review turns the
// uncited rows not-verified; an unknown task is 404.
func TestEpicEvidenceEndpoint(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var p1, p2 int64
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`, taskID).Scan(&p1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id = ? AND seq = 2`, taskID).Scan(&p2); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, question, status, answer, created_at)
		VALUES (?, 'D-1', 1, 'Q1', 'decided', 'A1', 'x'), (?, 'D-2', 2, 'Q2', 'decided', 'A2', 'x')`, taskID, taskID)
	exec(`INSERT INTO spec_criteria (workspace_task_id, pos, cid, text) VALUES (?, 0, 'SC-3', 's3'), (?, 1, 'SC-4', 's4')`, taskID, taskID)
	exec(`UPDATE epic_phases SET covers = '["SC-3","SC-4"]', decision_deps = '["D-1","D-2"]' WHERE id = ?`, p1)
	exec(`INSERT INTO decision_evidence (workspace_task_id, ref_id, phase_id, doc_path, kind, status, reason, excerpt, created_at) VALUES
		(?, 'SC-3', ?, '/d/phase-1.md', 'report', 'evidence', '', 'SC-3 shipped', 'x'),
		(?, 'D-2', ?, '/d/phase-1.md', 'report', 'not-implemented', 'moved to pricing', 'not implemented: D-2 — moved to pricing', 'x'),
		(?, 'D-9', ?, '/d/phase-1.md', 'report', 'evidence', '', 'an undeclared id', 'x')`,
		taskID, p1, taskID, p1, taskID, p2)

	var got epicEvidenceResponse
	getJSON(t, srv.URL+"/api/epics/"+strconv.FormatInt(taskID, 10)+"/evidence", &got)
	if got.TaskID != taskID || len(got.Rows) != 4 {
		t.Fatalf("evidence = %+v, want 4 rows", got)
	}
	chips := map[string]wsingest.DecisionTableRow{}
	for _, r := range got.Rows {
		chips[r.ID] = r
	}
	want := map[string]string{"D-1": "remaining", "D-2": "not-implemented", "SC-3": "evidence", "SC-4": "remaining"}
	for id, chip := range want {
		if chips[id].Chip != chip {
			t.Errorf("%s chip = %q, want %q (%+v)", id, chips[id].Chip, chip, chips[id])
		}
	}
	if r := chips["SC-3"]; len(r.Phases) != 1 || r.Phases[0] != 1 || len(r.Evidence) != 1 || r.Evidence[0].Excerpt != "SC-3 shipped" ||
		r.Evidence[0].PhaseSeq == nil || *r.Evidence[0].PhaseSeq != 1 {
		t.Errorf("SC-3 row = %+v", r)
	}
	if chips["D-2"].Reason != "moved to pricing" {
		t.Errorf("D-2 reason = %q", chips["D-2"].Reason)
	}
	if got.PlanReview.Status != "none" || got.Counts != (wsingest.DecisionTableCounts{Evidence: 1, Remaining: 2, NotImplemented: 1}) {
		t.Errorf("planReview=%+v counts=%+v", got.PlanReview, got.Counts)
	}

	// The wire shape the web DecisionTable reads.
	resp, err := http.Get(srv.URL + "/api/epics/" + strconv.FormatInt(taskID, 10) + "/evidence")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"taskId", "planReview", "rows", "counts"} {
		if _, ok := shape[k]; !ok {
			t.Errorf("response lacks %q: %s", k, raw)
		}
	}

	// An INCONCLUSIVE daemon plan review.
	exec(`INSERT INTO phase_reviews (scope, phase_id, workspace_task_id, session_uuid, run_session_uuid, tree_before, tree_after,
		verdict, detail, findings, fix_round, started_at, finished_at)
		VALUES ('plan', NULL, ?, 's', 'branchset:k', 't', 't', 'inconclusive', 'reviewer-timed-out: killed', '', 0, '2026-10-23T00:00:00Z', '2026-10-23T00:01:00Z')`,
		strconv.FormatInt(taskID, 10))
	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	if len(epics) != 1 {
		t.Fatalf("epics = %d", len(epics))
	}
	e := epics[0]
	if e.PlanReviewStatus != wsingest.PlanReviewNotVerified || e.Evidence.PlanReview.Reason != "reviewer-timed-out" {
		t.Errorf("epic planReviewStatus = %q, planReview = %+v", e.PlanReviewStatus, e.Evidence.PlanReview)
	}
	for _, r := range e.Evidence.Rows {
		switch r.ID {
		case "D-1", "SC-4":
			if r.Chip != wsingest.ChipNotVerified {
				t.Errorf("%s on the epic payload = %q, want not-verified", r.ID, r.Chip)
			}
		case "SC-3":
			if r.Chip != wsingest.ChipEvidence {
				t.Errorf("SC-3 = %q, want evidence", r.Chip)
			}
		}
	}

	resp, err = http.Get(srv.URL + "/api/epics/99999/evidence")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown task = %d, want 404", resp.StatusCode)
	}
}

// A plan with neither decisions nor criteria carries an empty table, not null.
func TestEpicEvidenceEmptyPlan(t *testing.T) {
	srv, _, taskID, _ := epicFixture(t)
	resp, err := http.Get(srv.URL + "/api/epics/" + strconv.FormatInt(taskID, 10) + "/evidence")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var shape struct {
		Rows       json.RawMessage `json:"rows"`
		PlanReview struct {
			Status string `json:"status"`
		} `json:"planReview"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	if string(shape.Rows) != "[]" || shape.PlanReview.Status != "none" {
		t.Errorf("empty plan = %s", raw)
	}
}
