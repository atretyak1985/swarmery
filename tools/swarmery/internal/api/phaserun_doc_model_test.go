package api

// Rung 2 of the phase-run model ladder at the HTTP seam: a `**Model:**` the phase
// doc declares (epic_phases.doc_model, migration 0069).
//
// The two properties worth an http-level test rather than a service-level one are
// the STATUS and the BODY. Rung 2 answers 409 `doc-model-unknown` and not rung 1's
// 400 — the operator did not type the value, the plan did — and the body has to
// name the document, which is only true if the *DocModelError arm sits ABOVE the
// generic planning.ErrUnknownModel arm it wraps.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The doc's declaration reaches the spawned run when the request carries none.
func TestPhaseRun_DocModel_ReachesTheSpawn(t *testing.T) {
	// Set to something else: the doc must outrank the env knob, not merge with it.
	t.Setenv(phaserunModelEnv, "claude-opus-5[1m]")
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	mustExecEpics(t, db, `UPDATE epic_phases SET doc_model='sonnet' WHERE id=?`, p1)
	r := &phaseStubRunner{}
	attachPhaseRun(t, db, r, true)

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	specs := r.dispatchedSpecs()
	if len(specs) != 1 {
		t.Fatalf("dispatched %d runs, want 1", len(specs))
	}
	if got := specs[0].Model; got != "claude-sonnet-5" {
		t.Errorf("RunSpec.Model = %q, want the doc's declaration resolved to claude-sonnet-5", got)
	}
}

// An unknown model in a DOCUMENT: 409 with a code of its own, a body that names
// the file, and nothing started. Never the silent fallback to the daemon default
// that internal/dispatch/service.go:979 records for playbook `model:` chips.
func TestPhaseRun_UnknownDocModel_409_NamesTheDoc(t *testing.T) {
	t.Setenv(phaserunModelEnv, "claude-opus-5[1m]")
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	mustExecEpics(t, db, `UPDATE epic_phases SET doc_model='gpt-9' WHERE id=?`, p1)
	r := &phaseStubRunner{}
	attachPhaseRun(t, db, r, true)

	resp := postPhase(t, phaseRunURL(srv, taskID, p1))
	// NOT 400: rung 1's status would tell the operator to fix a request they never
	// sent a model on.
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error    string `json:"error"`
		Code     string `json:"code"`
		Doc      string `json:"doc"`
		Declared string `json:"declared"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "doc-model-unknown" {
		t.Errorf("code = %q, want doc-model-unknown", body.Code)
	}
	var docPath string
	if err := db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, p1).Scan(&docPath); err != nil {
		t.Fatal(err)
	}
	if body.Doc != docPath {
		t.Errorf("doc = %q, want the phase doc %q", body.Doc, docPath)
	}
	if body.Declared != "gpt-9" {
		t.Errorf("declared = %q, want the verbatim declaration gpt-9", body.Declared)
	}
	// The prose has to carry the path too — a client that only shows `error` must
	// still tell the author which file to open.
	if !strings.Contains(body.Error, docPath) {
		t.Errorf("error %q does not name the document", body.Error)
	}
	if specs := r.dispatchedSpecs(); len(specs) != 0 {
		t.Errorf("dispatched %d runs on a rejected doc model, want 0", len(specs))
	}
	var state string
	if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "idle" {
		t.Errorf("run_state = %q, want an untouched idle row", state)
	}
}

// The operator's override rescues a plan whose header is broken: rung 1 still wins,
// so one bad line cannot make a phase unrunnable until someone edits the file.
func TestPhaseRun_RequestModelOverridesABrokenDocModel(t *testing.T) {
	t.Setenv(phaserunModelEnv, "")
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	mustExecEpics(t, db, `UPDATE epic_phases SET doc_model='gpt-9' WHERE id=?`, p1)
	r := &phaseStubRunner{}
	attachPhaseRun(t, db, r, true)

	resp := postPhaseBody(t, phaseRunURL(srv, taskID, p1), `{"model":"opus"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	specs := r.dispatchedSpecs()
	if len(specs) != 1 {
		t.Fatalf("dispatched %d runs, want 1", len(specs))
	}
	if got := specs[0].Model; got != "claude-opus-5-5" {
		t.Errorf("RunSpec.Model = %q, want claude-opus-5-5", got)
	}
}

// The phase DTO carries the declaration so the Plans page can show what the doc
// asks for. A SEPARATE field from runModel, and this test pins that they are read
// independently: docModel is what the plan ASKS FOR, runModel is what a past run
// USED, and a phase that has never run has the first without the second.
func TestListEpics_PhaseDocModel(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	mustExecEpics(t, db, `UPDATE epic_phases SET doc_model='sonnet'
		WHERE workspace_task_id=? AND seq=1`, taskID)

	e := firstEpic(t, srv)
	if len(e.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(e.Phases))
	}
	if e.Phases[0].DocModel == nil {
		t.Fatal("phase 1 docModel = null, want the doc's declaration")
	}
	if got := *e.Phases[0].DocModel; got != "sonnet" {
		t.Errorf("phase 1 docModel = %q, want sonnet", got)
	}
	// Never run, so no runModel — the two fields are not the same fact.
	if e.Phases[0].RunModel != nil {
		t.Errorf("phase 1 runModel = %q, want null on a phase that never ran", *e.Phases[0].RunModel)
	}
	// The sibling declares nothing and must say so as null, not as "".
	if e.Phases[1].DocModel != nil {
		t.Errorf("phase 2 docModel = %q, want null", *e.Phases[1].DocModel)
	}
}

// An unknown value survives all the way to the DTO, verbatim. The run refuses such
// a phase, so the operator has to be able to SEE the offending text to fix it —
// blanking it here would leave a 409 with no visible cause on the page.
func TestListEpics_PhaseDocModel_UnknownValueShownVerbatim(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	mustExecEpics(t, db, `UPDATE epic_phases SET doc_model='gpt-9'
		WHERE workspace_task_id=? AND seq=1`, taskID)

	e := firstEpic(t, srv)
	if e.Phases[0].DocModel == nil || *e.Phases[0].DocModel != "gpt-9" {
		t.Errorf("phase 1 docModel = %v, want the verbatim gpt-9", e.Phases[0].DocModel)
	}
}
