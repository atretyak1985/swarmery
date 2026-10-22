package phaserun

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
)

// Two automatic rounds, then nothing automatic: a third review exists only as
// the operator's repeat-review answer, which runs the reviewer once on the run
// branch, starts no fix re-run and — still failing — hands over again.
func TestReviewThirdRoundOnlyViaRepeatReview(t *testing.T) {
	const finding = "[{id: R1-1, type: correctness, file: a.go, line: 3, evidence: broken}]"
	s, db, p1, runner, rv, _, dec := startReviewedWithDecisions(t,
		reviewerOutput("FAIL", finding), reviewerOutput("FAIL", finding), reviewerOutput("FAIL", finding))
	if n := len(rv.calls()); n != 2 {
		t.Fatalf("automatic reviews = %d, want 2", n)
	}
	if got := reviewFixRound(t, db, p1); got != maxAutoReviewRounds {
		t.Fatalf("review_fix_round = %d, want %d", got, maxAutoReviewRounds)
	}

	ew := &existingStubWt{stubWt: s.Wt.(*stubWt)}
	s.Wt = ew
	s.Git = &stubGit{head: stubBranchTip}
	start, err := s.AdmitRepeatReview(p1)
	if err != nil {
		t.Fatalf("AdmitRepeatReview: %v", err)
	}
	start()

	calls := rv.calls()
	if len(calls) != 3 {
		t.Fatalf("reviews = %d, want 3 (two automatic + the repeat)", len(calls))
	}
	if p := calls[2].Prompt; !strings.Contains(p, "REVIEW ROUND: 3\n") || !strings.Contains(p, "PHASE DOCUMENT:") {
		t.Errorf("the repeat review is not a full round 3:\n%s", p)
	}
	if n := runner.specCount(); n != 2 {
		t.Errorf("executor runs = %d, want 2 — a repeated review starts no fix", n)
	}
	if got := dec.all(); len(got) != 2 || got[1].Round != 3 {
		t.Errorf("decisions = %+v, want a second one after round 3", got)
	}
	if len(ew.existing) != 1 {
		t.Errorf("run-branch checkouts = %d, want 1", len(ew.existing))
	}
	if s.Slots.IsActive(s.slotKey(p1)) {
		t.Error("the run slot is still held after the repeat review")
	}
}

func TestAdmitRepeatReviewRefusals(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.AdmitRepeatReview(p1); asRefusal(t, err).Code != ReviewCodeOff {
		t.Errorf("review off: %v", err)
	}
	setReviewMode(t, db, p1, "on")
	if _, err := s.AdmitRepeatReview(p1); asRefusal(t, err).Code != ReverifyCodeNoRunBranch {
		t.Errorf("no run branch: %v", err)
	}
	mustExec(t, db, `UPDATE epic_phases SET run_branch='swarm/phase-1', run_state='done' WHERE id=?`, p1)
	if _, err := s.AdmitRepeatReview(p1); asRefusal(t, err).Code != ReviewCodeNotWired {
		t.Errorf("no reviewer: %v", err)
	}
}

// D20 at the dependency gate: a finished dependency whose latest review round is
// incomplete does not unblock its dependents (phasegate.ReviewHoldSQL against the
// real schema); a complete later round, or a D21 legacy phase, does.
func TestDepSatisfiedHeldByAnIncompleteReview(t *testing.T) {
	db, taskID, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	mustExec(t, db, `UPDATE epic_phases SET checkboxes_done = checkboxes_total WHERE id = ?`, p1)
	ok, err := s.depSatisfied(taskID, 1)
	if err != nil || !ok {
		t.Fatalf("ticked dependency without reviews = (%v, %v), want satisfied", ok, err)
	}

	doc := phaseDocPath(t, db, p1)
	mustExec(t, db, `INSERT INTO review_blocks (workspace_task_id, phase_id, doc_path, round, verdict, complete, scope_left_json, written_at)
		VALUES (?, ?, ?, '1', 'FAIL', 1, '[]', 'now'),
		       (?, ?, ?, '2', 'INCONCLUSIVE', 0, '["web/", "review-incomplete: reviewer-timed-out — killed"]', 'now'),
		       (?, ?, ?, 'plan', 'PASS', 1, '[]', 'now')`,
		taskID, p1, doc, taskID, p1, doc, taskID, p1, doc)
	var hold string
	if err := db.QueryRow(`SELECT `+phasegate.ReviewHoldSQL+` FROM epic_phases e WHERE e.id = ?`, p1).Scan(&hold); err != nil {
		t.Fatal(err)
	}
	if hold != "round 2: review-incomplete: reviewer-timed-out — killed" {
		t.Errorf("ReviewHoldSQL = %q, want round 2's last scope_left entry", hold)
	}
	if ok, err := s.depSatisfied(taskID, 1); err != nil || ok {
		t.Errorf("incomplete latest review = (%v, %v), want held", ok, err)
	}

	mustExec(t, db, `UPDATE epic_phases SET legacy_review = 1 WHERE id = ?`, p1)
	if ok, _ := s.depSatisfied(taskID, 1); !ok {
		t.Error("a legacy phase is held by the review rule")
	}
	mustExec(t, db, `UPDATE epic_phases SET legacy_review = 0 WHERE id = ?`, p1)

	mustExec(t, db, `INSERT INTO review_blocks (workspace_task_id, phase_id, doc_path, round, verdict, complete, written_at)
		VALUES (?, ?, ?, '10', 'PASS', 1, 'now')`, taskID, p1, doc)
	if ok, _ := s.depSatisfied(taskID, 1); !ok {
		t.Error("a complete later round (10, numeric order) does not lift the hold")
	}
}
