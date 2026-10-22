package phaserun

import (
	"strings"
	"testing"
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
