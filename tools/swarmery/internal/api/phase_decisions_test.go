package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func patchCost(t *testing.T, url, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// PATCH …/turns/{seq}/cost flips the cost of a recorded decision, and GET
// /api/epics/{taskId}/decisions reflects it.
func TestPatchTurnCostReflectedInEpicDecisions(t *testing.T) {
	srv, db, _ := serverWithPlanning(t, &planStubRunner{})
	seedWizard(t, db, "uuid-cost", planning.StatusDone, true)
	var sid int64
	if err := db.QueryRow(`SELECT id FROM planning_sessions WHERE session_uuid='uuid-cost'`).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, source, external_id)
		VALUES (11, 1, 'plan', 'p', 'running', 'x', 'workspace', 'ws-11')`); err != nil {
		t.Fatal(err)
	}
	// PROCEED's row, already adopted onto the task (no README to rewrite: the
	// task has no plan artifact, which the toggle tolerates).
	if err := store.UpsertSessionDecision(db, store.Decision{PlanningSessionID: &sid, DecisionID: "D-1", Seq: 1,
		Question: "Which direction?", Cost: "cheap", Status: store.DecisionRecommended, CreatedAt: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE phase_decisions SET workspace_task_id = 11`); err != nil {
		t.Fatal(err)
	}

	url := srv.URL + "/api/planning/sessions/" + itoa(sid) + "/turns/1/cost"
	if code := patchCost(t, url, `{"cost":"expensive","reason":"a public contract"}`); code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", code)
	}

	var got struct {
		ProjectID int64            `json:"projectId"`
		Decisions []store.Decision `json:"decisions"`
	}
	getJSON(t, srv.URL+"/api/epics/11/decisions", &got)
	if got.ProjectID != 1 || len(got.Decisions) != 1 {
		t.Fatalf("decisions = %+v", got)
	}
	d := got.Decisions[0]
	if d.DecisionID != "D-1" || d.Cost != "expensive" || d.Status != "undecided" || d.CostReason != "a public contract" {
		t.Errorf("D-1 after PATCH = %+v, want expensive/undecided", d)
	}
	var q struct {
		CostToChange string `json:"costToChange"`
	}
	var qJSON string
	db.QueryRow(`SELECT question FROM planning_turns WHERE planning_session_id = ? AND seq = 1`, sid).Scan(&qJSON)
	json.Unmarshal([]byte(qJSON), &q)
	if q.CostToChange != "expensive" {
		t.Errorf("stored turn costToChange = %q", q.CostToChange)
	}

	for body, want := range map[string]int{
		`{"cost":"pricey"}`: http.StatusBadRequest,
		`not json`:          http.StatusBadRequest,
	} {
		if code := patchCost(t, url, body); code != want {
			t.Errorf("PATCH %s = %d, want %d", body, code, want)
		}
	}
	if code := patchCost(t, srv.URL+"/api/planning/sessions/"+itoa(sid)+"/turns/9/cost", `{"cost":"cheap"}`); code != http.StatusNotFound {
		t.Errorf("PATCH unknown turn = %d, want 404", code)
	}
	resp, err := http.Get(srv.URL + "/api/epics/999/decisions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown task = %d, want 404", resp.StatusCode)
	}
}

// The phase card's hold: epicPhases carries decisionHold exactly while the
// start gate would refuse, with a link back to the planning turn.
func TestEpicPhasesCarryDecisionHold(t *testing.T) {
	_, db := testServerWithDB(t)
	h := &Handler{DB: db}
	mustExecAPI := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var projectID int64
	if err := db.QueryRow(`SELECT id FROM projects LIMIT 1`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	mustExecAPI(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, source, external_id)
		VALUES (21, ?, 'plan', 'p', 'running', 'x', 'workspace', 'ws-21')`, projectID)
	mustExecAPI(`INSERT INTO epic_phases (id, workspace_task_id, seq, name, doc_path, decision_deps)
		VALUES (301, 21, 1, 'One', '/p/plan/phase-1.md', '["D-3"]'), (302, 21, 2, 'Two', '/p/plan/phase-2.md', '[]')`)
	mustExecAPI(`INSERT INTO planning_sessions (id, project_id, session_uuid, idea, created_at, updated_at)
		VALUES (44, ?, 'u-44', 'idea', 'x', 'x')`, projectID)
	mustExecAPI(`INSERT INTO phase_decisions (workspace_task_id, planning_session_id, decision_id, seq, question, cost, status, created_at)
		VALUES (21, 44, 'D-3', 3, 'Which store?', 'expensive', 'undecided', 'x')`)

	phases, _, _, err := h.epicPhases(21, "/p/plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 {
		t.Fatalf("phases = %d", len(phases))
	}
	hold := phases[0].DecisionHold
	if hold == nil || len(hold.Decisions) != 1 ||
		hold.Decisions[0].Seq != 3 || hold.Decisions[0].PlanningSessionID == nil || *hold.Decisions[0].PlanningSessionID != 44 {
		t.Fatalf("phase 1 hold = %+v", hold)
	}
	// The hold names the question and links the planning turn that asked it —
	// the card's "decide now" action and the 409's detail say the same.
	var slug string
	_ = db.QueryRow(`SELECT slug FROM projects WHERE id = ?`, projectID).Scan(&slug)
	link := phasegate.PlanningTurnLink(slug, 44, 3)
	if link == "" || hold.Decisions[0].TurnHref != link {
		t.Errorf("turnHref = %q, want %q", hold.Decisions[0].TurnHref, link)
	}
	if want := "decision-undecided: D-3 (Which store?) — decide now: " + link; hold.Detail != want {
		t.Errorf("detail = %q, want %q", hold.Detail, want)
	}
	if len(phases[0].DecisionsRecommended) != 0 {
		t.Errorf("recommended = %+v, want none while D-3 is undecided", phases[0].DecisionsRecommended)
	}
	if phases[1].DecisionHold != nil {
		t.Errorf("phase 2 declares nothing but holds: %+v", phases[1].DecisionHold)
	}

	mustExecAPI(`UPDATE phase_decisions SET cost = 'cheap'`)
	phases, _, _, _ = h.epicPhases(21, "/p/plan")
	if phases[0].DecisionHold != nil {
		t.Errorf("cheap undecided decision holds: %+v", phases[0].DecisionHold)
	}

	// Skipped as cheap ⇒ recommended: never held, marked «no answer» with the
	// recommended option on the card.
	mustExecAPI(`UPDATE phase_decisions SET status = 'recommended', answer = 'sqlite'`)
	phases, _, _, _ = h.epicPhases(21, "/p/plan")
	if phases[0].DecisionHold != nil {
		t.Errorf("recommended decision holds: %+v", phases[0].DecisionHold)
	}
	if rec := phases[0].DecisionsRecommended; len(rec) != 1 || rec[0].DecisionID != "D-3" || rec[0].Answer != "sqlite" || rec[0].TurnHref != link {
		t.Errorf("recommended = %+v", rec)
	}
}
