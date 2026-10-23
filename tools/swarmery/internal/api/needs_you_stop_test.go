package api

import (
	"database/sql"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// seedAgentStop turns the phase's seeded guard decision into an agent-declared
// protected-path stop, the way phaserun.raiseStopDecision refreshes it.
func seedAgentStop(t *testing.T, db *sql.DB, taskID, phaseID int64) (store.Decision, string) {
	t.Helper()
	_, doc := seedGuardDecision(t, db, taskID, phaseID, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	guard := `{"hasGuard":true,"protectedChanged":["trap_test.go"],"stop":{"trigger":"protected-path",` +
		`"reason":"The fix needs trap_test.go.","paths":["pkg/x_test.go"],"flags":["stop_options_count"],"options":[` +
		`{"id":"allow-add-to-scope","label":"Let me edit the test","consequence":"the test changes"}]}}`
	d, created, err := store.UpsertStopDecision(db, taskID, phaseID, "The fix needs trap_test.go.",
		[]string{"allow-add-to-scope"}, guard, time.Now().UTC().Format(time.RFC3339))
	if err != nil || created {
		t.Fatalf("refresh into a stop: created=%v err=%v", created, err)
	}
	return d, doc
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
	want := []needsYouDecisionOption{{ID: "allow-add-to-scope", Label: "Let me edit the test", Consequence: "the test changes"}}
	if !reflect.DeepEqual(dec.Options, want) {
		t.Fatalf("options = %+v, want %+v", dec.Options, want)
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
	if c := post(`{"option":"reject"}`); c != http.StatusBadRequest {
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
