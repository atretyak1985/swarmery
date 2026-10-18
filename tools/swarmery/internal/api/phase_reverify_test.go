package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// reverifyWt is the no-op phase worktree manager plus AcquireExisting — the
// extension a re-verification checks the existing run branch out through.
type reverifyWt struct{ phaseStubWt }

func (reverifyWt) AcquireExisting(repoRoot, projectSlug, taskID string) (worktree.Acquired, error) {
	return worktree.Acquired{Path: "/wt/" + projectSlug + "/" + taskID, Branch: "swarm/" + taskID, StartPoint: "branch-tip"}, nil
}

// reverifyStub is a PhaseVerifier that hands every request to the test.
type reverifyStub struct {
	got chan runcore.PhaseVerifyRequest
}

func (v *reverifyStub) VerifyPhase(_ context.Context, req runcore.PhaseVerifyRequest) error {
	v.got <- req
	return nil
}

// makeReverifiable stamps what a finished, graded-inconclusive run leaves behind.
func makeReverifiable(t *testing.T, db *sql.DB, phaseID int64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE epic_phases
		SET run_state='done', run_branch=?, run_start_point='start-sha',
		    verify_mode='normal', verify_verdict='inconclusive', checkboxes_done=checkboxes_total
		WHERE id=?`, runcore.PhaseBranch(phaseID), phaseID); err != nil {
		t.Fatal(err)
	}
}

func attachReverify(t *testing.T, db *sql.DB) (*phaserun.Service, *reverifyStub) {
	t.Helper()
	svc := attachPhaseRunWt(t, db, &phaseStubRunner{}, true, reverifyWt{})
	svc.Git = planGitStub{} // every branch resolves
	v := &reverifyStub{got: make(chan runcore.PhaseVerifyRequest, 1)}
	svc.Verify = v
	return svc, v
}

func phaseReverifyURL(srv *httptest.Server, taskID, phaseID int64) string {
	return srv.URL + "/api/epics/" + i64(taskID) + "/phases/" + i64(phaseID) + "/verify"
}

func TestPhaseReverify_Accepted_202(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	makeReverifiable(t, db, p1)
	svc, v := attachReverify(t, db)

	resp := postPhase(t, phaseReverifyURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var body struct {
		Status  string `json:"status"`
		PhaseID int64  `json:"phaseId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "verifying" || body.PhaseID != p1 {
		t.Errorf("body = %+v, want verifying for phase %d", body, p1)
	}
	select {
	case req := <-v.got:
		if req.PhaseID != p1 || req.StartPoint != "start-sha" || req.Branch != runcore.PhaseBranch(p1) {
			t.Errorf("request = %+v, want phase %d on its run branch from start-sha", req, p1)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the verifier was never called")
	}
	deadline := time.Now().Add(5 * time.Second)
	for svc.InFlight(p1) {
		if time.Now().After(deadline) {
			t.Fatal("run slot still held after the re-verification finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var state string
	if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "done" {
		t.Errorf("run_state = %q, want done unchanged", state)
	}
}

func TestPhaseReverify_PhaseOfAnotherTask_404(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	makeReverifiable(t, db, p1)
	attachReverify(t, db)
	resp := postPhase(t, phaseReverifyURL(srv, taskID+999, p1))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPhaseReverify_NotAttached_503(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	detachPhaseRun(t)
	resp := postPhase(t, phaseReverifyURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

// TestPhaseReverify_CodedRefusals: a refusal renders {"error","code"} with the
// status its code maps to, and the verifier is never called.
func TestPhaseReverify_CodedRefusals(t *testing.T) {
	cases := []struct {
		name, setup, code string
		status            int
	}{
		{"verify off", `UPDATE epic_phases SET verify_mode='off' WHERE id=?`, phaserun.ReverifyCodeVerifyOff, http.StatusUnprocessableEntity},
		{"criteria open", `UPDATE epic_phases SET checkboxes_done=0 WHERE id=?`, phaserun.ReverifyCodeCriteriaOpen, http.StatusUnprocessableEntity},
		{"already passed", `UPDATE epic_phases SET verify_verdict='pass' WHERE id=?`, phaserun.ReverifyCodeAlreadyPassed, http.StatusConflict},
		{"already running", `UPDATE epic_phases SET run_state='running' WHERE id=?`, phaserun.ReverifyCodeAlreadyRunning, http.StatusConflict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, db, taskID, _ := epicFixture(t)
			p1, _ := fixturePhaseIDs(t, db, taskID)
			makeReverifiable(t, db, p1)
			_, v := attachReverify(t, db)
			if _, err := db.Exec(c.setup, p1); err != nil {
				t.Fatal(err)
			}
			resp := postPhase(t, phaseReverifyURL(srv, taskID, p1))
			if resp.StatusCode != c.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.status)
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != c.code {
				t.Errorf("code = %v, want %q", body["code"], c.code)
			}
			if msg, _ := body["error"].(string); msg == "" {
				t.Errorf("error = %v, want a message", body["error"])
			}
			select {
			case req := <-v.got:
				t.Errorf("verifier called on a refused request: %+v", req)
			default:
			}
		})
	}
	// A whole-plan run is a live refusal, not a row fact.
	t.Run("plan running", func(t *testing.T) {
		srv, db, taskID, _ := epicFixture(t)
		p1, _ := fixturePhaseIDs(t, db, taskID)
		makeReverifiable(t, db, p1)
		attachReverify(t, db)
		if _, err := db.Exec(`INSERT INTO plan_runs (workspace_task_id, run_state) VALUES (?, 'running')`, taskID); err != nil {
			t.Fatal(err)
		}
		resp := postPhase(t, phaseReverifyURL(srv, taskID, p1))
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != phaserun.ReverifyCodePlanRunning {
			t.Errorf("code = %v, want plan-running", body["code"])
		}
	})
}

// TestPhaseReverify_PhaseJSONCarriesCanReverifyAndVerifyRunning (SC-5): the
// phase JSON answers canReverify from the endpoint's own predicate and
// verifyRunning from verification_runs.
func TestPhaseReverify_PhaseJSONCarriesCanReverifyAndVerifyRunning(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, p2 := fixturePhaseIDs(t, db, taskID)
	makeReverifiable(t, db, p1)

	list := func() map[int64]epicPhaseDTO {
		t.Helper()
		resp, err := http.Get(srv.URL + "/api/epics?projectId=1")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var epics []epicDTO
		if err := json.NewDecoder(resp.Body).Decode(&epics); err != nil {
			t.Fatal(err)
		}
		if len(epics) != 1 {
			t.Fatalf("epics = %d, want 1", len(epics))
		}
		out := map[int64]epicPhaseDTO{}
		for _, p := range epics[0].Phases {
			out[p.ID] = p
		}
		return out
	}

	got := list()
	if !got[p1].CanReverify {
		t.Error("phase 1 canReverify = false, want true (ticked, verify normal, inconclusive, run branch)")
	}
	if got[p2].CanReverify {
		t.Error("phase 2 canReverify = true, want false (never ran, verify off)")
	}
	if got[p1].VerifyRunning || got[p2].VerifyRunning {
		t.Error("verifyRunning = true with no verification in flight")
	}

	// A running verification of phase 1 (and a finished one of phase 2).
	if _, err := db.Exec(`INSERT INTO verification_runs (target_key, status, started_at) VALUES (?, 'running', '2026-10-18T00:00:00Z'), (?, 'fail', '2026-10-18T00:00:00Z')`,
		verify.PhaseKey(p1), verify.PhaseKey(p2)); err != nil {
		t.Fatal(err)
	}
	got = list()
	if !got[p1].VerifyRunning {
		t.Error("phase 1 verifyRunning = false, want true")
	}
	if got[p2].VerifyRunning {
		t.Error("phase 2 verifyRunning = true, want false (its run is finished)")
	}

	// A passed phase is no longer offered.
	if _, err := db.Exec(`UPDATE epic_phases SET verify_verdict='pass' WHERE id=?`, p1); err != nil {
		t.Fatal(err)
	}
	if list()[p1].CanReverify {
		t.Error("phase 1 canReverify = true after pass, want false")
	}

	// The wire names phase 2 consumes.
	resp, err := http.Get(srv.URL + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	ph := raw[0]["phases"].([]any)[0].(map[string]any)
	if _, ok := ph["canReverify"]; !ok {
		t.Error("phase JSON has no canReverify key")
	}
	if _, ok := ph["verifyRunning"]; !ok {
		t.Error("phase JSON has no verifyRunning key")
	}
}
