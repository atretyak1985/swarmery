package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// seedReviewDecision gives phaseID a real doc and an open review decision, the
// way phaserun.raiseReviewDecision writes it.
func seedReviewDecision(t *testing.T, db *sql.DB, taskID, phaseID int64) (store.Decision, string) {
	t.Helper()
	doc := filepath.Join(t.TempDir(), "phase-1.md")
	if err := os.WriteFile(doc, []byte("# Phase 1\n\n## Acceptance Criteria\n\n- [x] x\n\n## Completion Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE epic_phases SET doc_path = ? WHERE id = ?`, doc, phaseID); err != nil {
		t.Fatal(err)
	}
	d, created, err := store.UpsertReviewDecision(db, taskID, phaseID,
		"Review round 2 did not settle: the review still fails after round 2. Repeat the review or accept it as is?",
		[]string{phaserun.ReviewOptionRepeat, phaserun.ReviewOptionAccept}, time.Now().UTC().Format(time.RFC3339))
	if err != nil || !created {
		t.Fatalf("seed review decision: %v (created %v)", err, created)
	}
	return d, doc
}

// A review decision is a needs-you phase_decision with the two review options,
// answered through the same endpoint: repeat-review without a phase runner is
// refused and leaves it open; accept-as-is appends the (review) D-n line and
// closes it.
func TestReviewDecisionNeedsYouAndAnswer(t *testing.T) {
	srv, db := needsYouServerDB(t)
	alphaPhase, _, _ := manualPhaseFixture(t, db)
	var alphaTask int64
	_ = db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, alphaPhase).Scan(&alphaTask)
	d, doc := seedReviewDecision(t, db, alphaTask, alphaPhase)

	got := decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items)
	if len(got) != 1 || got[0].Decision == nil {
		t.Fatalf("phase_decision items = %+v", got)
	}
	dec := got[0].Decision
	if dec.Source != store.DecisionSourceReview || dec.DecisionID != d.DecisionID || len(dec.Options) != 2 ||
		dec.Options[0] != (needsYouDecisionOption{ID: phaserun.ReviewOptionRepeat, Label: "Repeat the review"}) ||
		dec.Options[1] != (needsYouDecisionOption{ID: phaserun.ReviewOptionAccept, Label: "Accept as is"}) {
		t.Errorf("review decision item = %+v", dec)
	}

	url := srv.URL + "/api/epics/" + strconv.FormatInt(alphaTask, 10) + "/phases/" + strconv.FormatInt(alphaPhase, 10) +
		"/decisions/" + d.DecisionID
	post := func(body string) (int, string) {
		t.Helper()
		resp, err := http.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, _ := post(`{"option":"allow-add-to-scope"}`); c != http.StatusBadRequest {
		t.Errorf("a guard option on a review decision: %d, want 400", c)
	}
	if c, _ := post(`{"option":"repeat-review"}`); c != http.StatusServiceUnavailable {
		t.Errorf("repeat-review without a phase runner: %d, want 503", c)
	}
	if open := decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items); len(open) != 1 {
		t.Fatalf("a refused repeat review closed the decision: %+v", open)
	}
	c, body := post(`{"option":"accept-as-is","note":"reviewed by hand"}`)
	if c != http.StatusOK {
		t.Fatalf("accept-as-is: %d %s", c, body)
	}
	var decided store.Decision
	if err := json.Unmarshal([]byte(body), &decided); err != nil || decided.Status != store.DecisionDecided ||
		decided.Answer != "accept as is — reviewed by hand" {
		t.Errorf("decided = %+v (%v)", decided, err)
	}
	b, _ := os.ReadFile(doc)
	if !strings.Contains(string(b), "## Decisions\n\n- **"+d.DecisionID+"** (review) — Review round 2 did not settle") ||
		!strings.Contains(string(b), "→ accept as is — reviewed by hand\n") {
		t.Errorf("doc lacks the review D-n line:\n%s", b)
	}
	if left := decisionItems(getNeedsYou(t, srv.URL+"/api/needs-you").Items); len(left) != 0 {
		t.Errorf("an answered review decision is still listed: %+v", left)
	}
	if c, _ := post(`{"option":"accept-as-is"}`); c != http.StatusConflict {
		t.Errorf("second answer: %d, want 409", c)
	}
}

// On the epic payload: an open review decision rides on its phase as
// reviewDecision and holds it (review-incomplete) even with a complete latest
// round; accept-as-is lifts both the decision and an incomplete round's hold.
func TestEpicPayloadReviewDecisionHoldsUntilAccepted(t *testing.T) {
	srv, db, taskID, phases := findingsFixture(t)
	p1 := phases[1]
	if _, err := db.Exec(`UPDATE epic_phases SET checkboxes_total = 1, checkboxes_done = 1 WHERE id = ?`, p1); err != nil {
		t.Fatal(err)
	}
	d, created, err := store.UpsertReviewDecision(db, taskID, p1, "Review round 2 did not settle: x. Repeat the review or accept it as is?",
		[]string{phaserun.ReviewOptionRepeat, phaserun.ReviewOptionAccept}, "2026-10-22T12:00:00Z")
	if err != nil || !created {
		t.Fatal(err)
	}
	type phaseShape struct {
		ID                 int64             `json:"id"`
		CompletionState    string            `json:"completionState"`
		CompletionBlockers []string          `json:"completionBlockers"`
		ReviewDecision     *needsYouDecision `json:"reviewDecision"`
	}
	phase := func() phaseShape {
		t.Helper()
		var epics []struct {
			TaskID int64        `json:"taskId"`
			Phases []phaseShape `json:"phases"`
		}
		getJSON(t, srv.URL+"/api/epics", &epics)
		for _, e := range epics {
			for _, p := range e.Phases {
				if e.TaskID == taskID && p.ID == p1 {
					return p
				}
			}
		}
		t.Fatal("phase not in the payload")
		return phaseShape{}
	}
	p := phase()
	if p.ReviewDecision == nil || p.ReviewDecision.DecisionID != d.DecisionID || p.ReviewDecision.Source != "review" ||
		p.CompletionState != "unverified" || len(p.CompletionBlockers) != 1 ||
		!strings.Contains(p.CompletionBlockers[0], "review decision "+d.DecisionID+" is open") {
		t.Fatalf("held phase = %+v", p)
	}

	// The round is incomplete too; accepting lifts both.
	if _, err := db.Exec(`UPDATE review_blocks SET complete = 0, scope_left_json = '["review-incomplete: no-block"]'
		 WHERE round = '2' AND doc_path = (SELECT doc_path FROM epic_phases WHERE id = ?)`, p1); err != nil {
		t.Fatal(err)
	}
	if _, err := phaserun.AnswerReviewDecision(db, taskID, p1, d.DecisionID,
		phaserun.GuardAnswer{Option: phaserun.ReviewOptionAccept}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if p := phase(); p.ReviewDecision != nil || p.CompletionState != "complete" {
		t.Errorf("accepted phase = %+v, want complete and no open decision", p)
	}
}
