package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// seedAgentStop turns the phase's seeded guard decision into an agent-declared
// protected-path stop, the way phaserun.raiseStopDecision refreshes it.
func seedAgentStop(t *testing.T, db *sql.DB, taskID, phaseID int64) (store.Decision, string) {
	t.Helper()
	return seedAgentStopPaths(t, db, taskID, phaseID, `["trap_test.go"]`, `["pkg/x_test.go"]`)
}

// seedAgentStopPaths is seedAgentStop with the guard check's changed files and
// the stop's stored paths given as JSON arrays — stored as is, the way a row
// raised before the paths were vetted holds them.
func seedAgentStopPaths(t *testing.T, db *sql.DB, taskID, phaseID int64, changedJSON, pathsJSON string) (store.Decision, string) {
	t.Helper()
	_, doc := seedGuardDecision(t, db, taskID, phaseID, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	guard := `{"hasGuard":true,"protectedPaths":["**/*_test.go"],"protectedChanged":` + changedJSON +
		`,"stop":{"trigger":"protected-path",` +
		`"reason":"The fix needs trap_test.go.","paths":` + pathsJSON + `,"flags":["stop_options_count"],"options":[` +
		`{"id":"allow-add-to-scope","label":"Let me edit the test","consequence":"the test changes"}]}}`
	d, created, err := store.UpsertStopDecision(db, taskID, phaseID, "The fix needs trap_test.go.",
		[]string{"allow-add-to-scope"}, guard, time.Now().UTC().Format(time.RFC3339))
	if err != nil || created {
		t.Fatalf("refresh into a stop: created=%v err=%v", created, err)
	}
	return d, doc
}

// The decision-undecided 409 carries each holding decision with its planning
// turn ("holds") and a message that names the link.
func TestWritePhaseStartRefusal_DecisionHolds(t *testing.T) {
	const link = "/p/p/plans?tab=new&planningSession=7&turn=3#planning-turn-3"
	err := &phaserun.DecisionUndecidedError{Detail: "decision-undecided: D-3 (Which store?)", Decisions: []string{"D-3"},
		Refs: []phasegate.HoldRef{{DecisionID: "D-3", Question: "Which store?", Seq: 3, PlanningSessionID: 7, PlanningTurn: link}}}
	rec := httptest.NewRecorder()
	writePhaseStartRefusal(rec, err)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Code   string              `json:"code"`
		Error  string              `json:"error"`
		Detail string              `json:"detail"`
		Holds  []phasegate.HoldRef `json:"holds"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if body.Code != codeDecisionUndecided || body.Detail != err.Detail || len(body.Holds) != 1 || body.Holds[0].PlanningTurn != link {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if !strings.Contains(body.Error, link) {
		t.Errorf("409 message does not carry the planning-turn link: %q", body.Error)
	}
}

// The needs-you phase_decision item of an agent-declared stop carries what the
// web renders: trigger, reason, flags, the agent's options with consequences and
// the free-text switch; the stop is answered through the same endpoint.
func TestNeedsYouAgentStopDecision(t *testing.T) {
	srv, db := needsYouServerDB(t)
	alphaPhase, _, _ := manualPhaseFixture(t, db)
	var alphaTask int64
	_ = db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, alphaPhase).Scan(&alphaTask)
	d, doc := seedAgentStop(t, db, alphaTask, alphaPhase)

	got := decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items)
	if len(got) != 1 || got[0].Decision == nil {
		t.Fatalf("phase_decision items = %+v", got)
	}
	dec := got[0].Decision
	if dec.Source != store.DecisionSourceStop || dec.Trigger != mdfence.StopTriggerProtectedPath ||
		dec.Reason != "The fix needs trap_test.go." || !dec.FreeText ||
		!reflect.DeepEqual(dec.Flags, []string{mdfence.LintStopOptionsCount}) {
		t.Fatalf("decision = %+v", dec)
	}
	// The guard check flagged this run (trap_test.go changed), so the card offers
	// the guard's allow and reject even though the stored row predates merging
	// them in — and the reserved allow id carries the daemon's label, not the
	// agent's.
	want := []needsYouDecisionOption{
		{ID: "allow-add-to-scope", Label: "Allow (add to scope)", Consequence: "the files listed join the guard's allow list and the next run builds on this run's branch"},
		{ID: "reject", Label: "Reject", Consequence: "the guard stays as it is; no protected edit of this run is built on"},
	}
	if !reflect.DeepEqual(dec.Options, want) {
		t.Fatalf("options = %+v, want %+v", dec.Options, want)
	}
	if !reflect.DeepEqual(dec.Paths, []string{"trap_test.go", "pkg/x_test.go"}) {
		t.Fatalf("paths = %q, want what an allow would release", dec.Paths)
	}

	// The phase card's open decision carries the same fields.
	h := &Handler{DB: db}
	if card := h.phaseGuardDecisions(alphaTask)[alphaPhase]; card == nil || card.Trigger != dec.Trigger || !card.FreeText {
		t.Fatalf("phase card decision = %+v", card)
	}

	u := srv.URL + "/api/epics/" + strconv.FormatInt(alphaTask, 10) + "/phases/" + strconv.FormatInt(alphaPhase, 10) +
		"/decisions/" + d.DecisionID
	post := func(body string) int {
		t.Helper()
		resp, err := http.Post(u, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(`{"option":"skip-test-change"}`); c != http.StatusBadRequest {
		t.Errorf("an option the stop does not offer: %d, want 400", c)
	}
	if c := post(`{"option":"other"}`); c != http.StatusBadRequest {
		t.Errorf("other without text: %d, want 400", c)
	}
	if c := post(`{"option":"allow-add-to-scope"}`); c != http.StatusOK {
		t.Fatalf("allow: %d, want 200", c)
	}
	body, _ := os.ReadFile(doc)
	if !strings.Contains(string(body), "- **D-1** (stop: protected-path) — The fix needs trap_test.go. → allow-add-to-scope\n") {
		t.Errorf("doc lacks the D-1 line:\n%s", body)
	}
	if g, perr := mdfence.ParseGuard(string(body)); perr != nil || !reflect.DeepEqual(g.Allow, []string{"trap_test.go", "pkg/x_test.go"}) {
		t.Errorf("guard after allow = %+v, %v", g, perr)
	}
}

// The card's paths are exactly what an allow would release: an untrusted
// agent's globs, `..`, .swarmery and unprotected paths never reach it, even on
// a row whose stored paths were never vetted — and the allow matches the card.
func TestNeedsYouAgentStopPathsAreVetted(t *testing.T) {
	srv, db := needsYouServerDB(t)
	alphaPhase, _, _ := manualPhaseFixture(t, db)
	var alphaTask int64
	_ = db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, alphaPhase).Scan(&alphaTask)
	d, doc := seedAgentStopPaths(t, db, alphaTask, alphaPhase, `[]`,
		`["**/*", ".github/workflows/*.yml", "../x", ".swarmery/plan/p.md", "pkg/foo_test.go"]`)

	got := decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items)
	if len(got) != 1 || got[0].Decision == nil {
		t.Fatalf("phase_decision items = %+v", got)
	}
	if paths := got[0].Decision.Paths; !reflect.DeepEqual(paths, []string{"pkg/foo_test.go"}) {
		t.Fatalf("needs-you paths = %q, want [pkg/foo_test.go]", paths)
	}
	u := srv.URL + "/api/epics/" + strconv.FormatInt(alphaTask, 10) + "/phases/" + strconv.FormatInt(alphaPhase, 10) +
		"/decisions/" + d.DecisionID
	resp, err := http.Post(u, "application/json", strings.NewReader(`{"option":"allow-add-to-scope"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allow: %d, want 200", resp.StatusCode)
	}
	body, _ := os.ReadFile(doc)
	if g, perr := mdfence.ParseGuard(string(body)); perr != nil || !reflect.DeepEqual(g.Allow, []string{"pkg/foo_test.go"}) {
		t.Errorf("guard after allow = %+v, %v; want allow [pkg/foo_test.go]", g, perr)
	}
}

// A plain guard decision is vetted the same way: a changed file literally named
// `**/*_test.go` and a forged guard.log path `**/*` reach neither the card's
// paths nor the guard's `allow:` list, and the card flags the drop.
func TestNeedsYouGuardDecisionPathsAreVetted(t *testing.T) {
	srv, db := needsYouServerDB(t)
	alphaPhase, _, _ := manualPhaseFixture(t, db)
	var alphaTask int64
	_ = db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, alphaPhase).Scan(&alphaTask)
	d, doc := seedGuardDecision(t, db, alphaTask, alphaPhase, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	raw := `{"hasGuard":true,"protectedPaths":["**/*_test.go"],"allow":[],"protectedChanged":["**/*_test.go","trap_test.go"],` +
		`"guardBlocks":[{"at":"t","tool":"Bash","path":"**/*","reason":"r"}],"guardBlocksTotal":1}`
	if _, err := db.Exec(`UPDATE phase_decisions SET guard_json = ? WHERE id = ?`, raw, d.ID); err != nil {
		t.Fatal(err)
	}
	got := decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items)
	if len(got) != 1 || got[0].Decision == nil {
		t.Fatalf("phase_decision items = %+v", got)
	}
	dec := got[0].Decision
	if !reflect.DeepEqual(dec.Paths, []string{"trap_test.go"}) || !reflect.DeepEqual(dec.Flags, []string{mdfence.LintStopPathsRejected}) {
		t.Fatalf("guard decision paths = %q flags = %q", dec.Paths, dec.Flags)
	}
	if dec.Trigger != "" || dec.FreeText {
		t.Fatalf("a guard decision rendered as a stop: %+v", dec)
	}
	u := srv.URL + "/api/epics/" + strconv.FormatInt(alphaTask, 10) + "/phases/" + strconv.FormatInt(alphaPhase, 10) +
		"/decisions/" + d.DecisionID
	resp, err := http.Post(u, "application/json", strings.NewReader(`{"option":"allow-add-to-scope"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allow: %d, want 200", resp.StatusCode)
	}
	body, _ := os.ReadFile(doc)
	if g, perr := mdfence.ParseGuard(string(body)); perr != nil || !reflect.DeepEqual(g.Allow, []string{"trap_test.go"}) {
		t.Errorf("guard after allow = %+v, %v; want allow [trap_test.go]", g, perr)
	}
}
