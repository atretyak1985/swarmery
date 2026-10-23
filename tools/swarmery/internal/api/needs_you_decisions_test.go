package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const guardDecisionDoc = "# Phase 1\n\n## Acceptance Criteria\n\n- [ ] x\n\n## Guard\n\n```yaml\nkind: guard\n" +
	"protected_paths:\n  - \"**/*_test.go\"\nstop_if: [protected path]\n```\n\n## Completion Report\n"

// seedGuardDecision gives phaseID a real doc and an open guard decision about
// trap_test.go, the way phaserun.raiseGuardDecision writes it.
func seedGuardDecision(t *testing.T, db *sql.DB, taskID, phaseID int64, created string) (store.Decision, string) {
	t.Helper()
	doc := filepath.Join(t.TempDir(), "phase-1.md")
	if err := os.WriteFile(doc, []byte(guardDecisionDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE epic_phases SET doc_path = ? WHERE id = ?`, doc, phaseID); err != nil {
		t.Fatal(err)
	}
	guard := `{"hasGuard":true,"protectedPaths":["**/*_test.go"],"allow":[],"protectedChanged":["trap_test.go"],` +
		`"docCopyMismatch":false,"guardBlocks":[],"guardBlocksTotal":0,"malformedLogLines":0,"parseError":null,"base":"abc"}`
	d, created2, err := store.UpsertStopDecision(db, taskID, phaseID,
		"The run touched protected paths: trap_test.go. Allow (add to scope) or reject?",
		phaserun.GuardOptions, guard, created)
	if err != nil || !created2 {
		t.Fatalf("seed decision: %v (created %v)", err, created2)
	}
	return d, doc
}

func decisionItems(items []needsYouItem) []needsYouItem {
	var out []needsYouItem
	for _, it := range items {
		if it.Kind == needsYouPhaseDecision {
			out = append(out, it)
		}
	}
	return out
}

func TestNeedsYouPhaseDecision(t *testing.T) {
	srv, db := needsYouServerDB(t)
	alphaPhase, _, betaPhase := manualPhaseFixture(t, db)
	var alphaTask int64
	_ = db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, alphaPhase).Scan(&alphaTask)
	d, _ := seedGuardDecision(t, db, alphaTask, alphaPhase, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))

	resp := getNeedsYou(t, srv.URL+"/api/needs-you")
	got := decisionItems(resp.Items)
	if len(got) != 1 {
		t.Fatalf("phase_decision items = %+v", got)
	}
	it := got[0]
	if it.Decision == nil || it.Phase == nil || it.Phase.PhaseID != alphaPhase {
		t.Fatalf("item = %+v", it)
	}
	if it.Decision.DecisionID != d.DecisionID || it.Decision.TaskID != alphaTask || it.Decision.PhaseID != alphaPhase ||
		it.Decision.Question != d.Question {
		t.Errorf("decision = %+v", it.Decision)
	}
	if len(it.Decision.Options) != 2 || it.Decision.Options[0] != (needsYouDecisionOption{ID: "allow-add-to-scope", Label: "Allow (add to scope)"}) ||
		it.Decision.Options[1].ID != "reject" {
		t.Errorf("options = %+v", it.Decision.Options)
	}
	var g phaserun.GuardResult
	if err := json.Unmarshal(it.Decision.Guard, &g); err != nil || len(g.ProtectedChanged) != 1 {
		t.Errorf("guard = %s (%v)", it.Decision.Guard, err)
	}
	if it.SessionName != "Alpha plan · Phase 1: Phase name 1" || !strings.HasPrefix(it.Preview, "D-1: The run touched") || it.BlockingSeconds <= 0 {
		t.Errorf("name/preview/blocking = %q / %q / %d", it.SessionName, it.Preview, it.BlockingSeconds)
	}
	// One phase, one item: the decision replaces the phase's manual_phase item.
	for _, m := range manualItems(resp.Items) {
		if m.Phase.PhaseID == alphaPhase {
			t.Errorf("manual_phase item for a phase with an open decision: %+v", m)
		}
	}
	if n := len(manualItems(resp.Items)); n != 2 {
		t.Errorf("manual_phase items = %d, want the other 2", n)
	}

	// ?project= scopes it like every other source.
	if scoped := decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you?project=-work-beta").Items); len(scoped) != 0 {
		t.Errorf("beta scope lists alpha's decision: %+v", scoped)
	}
	_ = betaPhase

	// Answered ⇒ gone.
	if _, err := store.DecideDecision(db, d.ID, "reject", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if left := decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items); len(left) != 0 {
		t.Errorf("an answered decision is still listed: %+v", left)
	}
}

func TestNeedsYouPhaseDecisionSortAndDedupe(t *testing.T) {
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	id := func(n int64) *int64 { return &n }
	ph := func(n int64) *needsYouPhase { return &needsYouPhase{PhaseID: n} }
	items := []needsYouItem{
		{Kind: needsYouFailed, SessionID: 1, at: at},
		{Kind: needsYouAwaitingMerge, Phase: ph(3), at: at},
		{Kind: needsYouPhaseDecision, Phase: ph(4), at: at},
		{Kind: needsYouQuestion, SessionID: 6, RequestID: id(2), at: at},
		{Kind: needsYouAwaitingReply, SessionID: 2, at: at},
	}
	sortNeedsYou(items)
	var got []string
	for _, it := range items {
		got = append(got, it.Kind)
	}
	if want := "question phase_decision awaiting_reply failed awaiting_merge"; strings.Join(got, " ") != want {
		t.Errorf("order = %s\nwant    %s", strings.Join(got, " "), want)
	}

	deduped := dedupeNeedsYou([]needsYouItem{
		{Kind: needsYouManualPhase, Phase: ph(4)},
		{Kind: needsYouAwaitingMerge, Phase: ph(4)},
		{Kind: needsYouPhaseDecision, Phase: ph(4)},
		{Kind: needsYouAwaitingMerge, Phase: ph(5)},
	})
	got = got[:0]
	for _, it := range deduped {
		got = append(got, it.Kind+"#"+strconv.FormatInt(it.Phase.PhaseID, 10))
	}
	if want := "phase_decision#4 awaiting_merge#5"; strings.Join(got, " ") != want {
		t.Errorf("dedupe = %s, want %s", strings.Join(got, " "), want)
	}
}

func TestAnswerPhaseDecisionEndpoint(t *testing.T) {
	srv, db := needsYouServerDB(t)
	alphaPhase, _, betaPhase := manualPhaseFixture(t, db)
	var alphaTask int64
	_ = db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, alphaPhase).Scan(&alphaTask)
	d, doc := seedGuardDecision(t, db, alphaTask, alphaPhase, time.Now().UTC().Format(time.RFC3339))
	url := func(phase int64, decision string) string {
		return srv.URL + "/api/epics/" + strconv.FormatInt(alphaTask, 10) + "/phases/" + strconv.FormatInt(phase, 10) +
			"/decisions/" + decision
	}
	post := func(u, body string) int {
		t.Helper()
		resp, err := http.Post(u, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if c := post(url(alphaPhase, d.DecisionID), `{"option":"maybe"}`); c != http.StatusBadRequest {
		t.Errorf("unknown option: %d, want 400", c)
	}
	if c := post(url(alphaPhase, d.DecisionID), `not json`); c != http.StatusBadRequest {
		t.Errorf("bad body: %d, want 400", c)
	}
	if c := post(url(alphaPhase, "D-99"), `{"option":"reject"}`); c != http.StatusNotFound {
		t.Errorf("unknown decision: %d, want 404", c)
	}
	if c := post(url(betaPhase, d.DecisionID), `{"option":"reject"}`); c != http.StatusNotFound {
		t.Errorf("a phase of another plan: %d, want 404", c)
	}
	if c := post(url(alphaPhase, d.DecisionID), `{"option":"allow-add-to-scope","note":"the test was wrong"}`); c != http.StatusOK {
		t.Fatalf("allow: %d, want 200", c)
	}
	body, _ := os.ReadFile(doc)
	if !strings.Contains(string(body), "## Decisions\n\n- **D-1** (stop) — The run touched protected paths: trap_test.go. "+
		"Allow (add to scope) or reject? → allow (add to scope): trap_test.go — the test was wrong\n") {
		t.Errorf("doc lacks the D-1 line:\n%s", body)
	}
	if g, perr := mdfence.ParseGuard(string(body)); perr != nil || len(g.Allow) != 1 || g.Allow[0] != "trap_test.go" {
		t.Errorf("guard after allow = %+v, %v", g, perr)
	}
	if c := post(url(alphaPhase, d.DecisionID), `{"option":"reject"}`); c != http.StatusConflict {
		t.Errorf("second answer: %d, want 409", c)
	}
}

// An agent-declared stop (verify-not-trust phase 10) is a phase_decision item
// carrying its block — trigger, reason, the executor's two options — with the
// options as buttons plus the free-text `other`, and an empty guard. The
// endpoint answers it with those options.
func TestNeedsYouAgentStopDecision(t *testing.T) {
	srv, db := needsYouServerDB(t)
	alphaPhase, _, _ := manualPhaseFixture(t, db)
	var alphaTask int64
	_ = db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, alphaPhase).Scan(&alphaTask)
	_, doc := seedGuardDecision(t, db, alphaTask, alphaPhase, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	stop := `{"stop":{"trigger":"access-error","reason":"the staging console refuses the stored login",` +
		`"options":[{"id":"relogin","label":"Log in again","consequence":"the re-run does the check"},` +
		`{"id":"skip","label":"Skip the check","consequence":"the criterion stays open"}]}}`
	d, created, err := store.UpsertAgentStopDecision(db, alphaTask, alphaPhase, "the staging console refuses the stored login",
		[]string{"relogin", "skip", phaserun.StopOptionOther}, stop, time.Now().UTC().Format(time.RFC3339))
	if err != nil || !created {
		t.Fatalf("seed stop: %v (created %v)", err, created)
	}

	var it *needsYouItem
	for _, x := range decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items) {
		if x.Decision != nil && x.Decision.DecisionID == d.DecisionID {
			x := x
			it = &x
		}
	}
	if it == nil {
		t.Fatal("the agent stop is not listed")
	}
	dec := it.Decision
	if dec.Source != "stop" || dec.Stop == nil || dec.Stop.Trigger != mdfence.StopAccessError || len(dec.Stop.Options) != 2 {
		t.Fatalf("decision = %+v / stop %+v", dec, dec.Stop)
	}
	want := []needsYouDecisionOption{{ID: "relogin", Label: "Log in again"}, {ID: "skip", Label: "Skip the check"},
		{ID: phaserun.StopOptionOther, Label: stopOptionOtherLabel}}
	if len(dec.Options) != 3 || dec.Options[0] != want[0] || dec.Options[1] != want[1] || dec.Options[2] != want[2] {
		t.Errorf("options = %+v, want %+v", dec.Options, want)
	}
	if string(dec.Guard) != "{}" {
		t.Errorf("guard = %s, want {}", dec.Guard)
	}

	// The plan DTO carries it on the phase as stopDecision, apart from the guard's.
	h := &Handler{DB: db}
	stops, guards := h.phaseStopDecisions(alphaTask), h.phaseGuardDecisions(alphaTask)
	if stops[alphaPhase] == nil || stops[alphaPhase].DecisionID != d.DecisionID {
		t.Errorf("phaseStopDecisions = %+v", stops)
	}
	if guards[alphaPhase] == nil || guards[alphaPhase].Stop != nil {
		t.Errorf("phaseGuardDecisions = %+v, want the guard decision only", guards)
	}

	u := srv.URL + "/api/epics/" + strconv.FormatInt(alphaTask, 10) + "/phases/" + strconv.FormatInt(alphaPhase, 10) +
		"/decisions/" + d.DecisionID
	resp, err := http.Post(u, "application/json", strings.NewReader(`{"option":"allow-add-to-scope"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a guard option on a stop: %d, want 400", resp.StatusCode)
	}
	resp, err = http.Post(u, "application/json", strings.NewReader(`{"option":"other","note":"use the read-only token"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("free-text answer: %d, want 200", resp.StatusCode)
	}
	body, _ := os.ReadFile(doc)
	if !strings.Contains(string(body), "- **"+d.DecisionID+"** (stop: access-error) — the staging console refuses the stored login → other: use the read-only token") {
		t.Errorf("doc lacks the stop line:\n%s", body)
	}
}
