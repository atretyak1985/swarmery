package verify

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// probedTrees is a Trees that also answers the completeness probe, the way
// *worktree.Manager does — so NewService must pick it up as the Checkout gate.
type probedTrees struct {
	stubTrees
	completeErr error
	probed      *int
}

func (p probedTrees) CheckComplete(string) error {
	*p.probed++
	return p.completeErr
}

// A half-deleted checkout (a killed `git worktree remove`) is not the executor's
// work: grading it would stamp FAIL on code that never existed that way. The
// verifier must stamp INCONCLUSIVE/unverifiable and never spawn a run.
func TestVerifyRefusesIncompleteCheckout(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: FAIL"}
	probed := 0
	trees := probedTrees{
		stubTrees:   stubTrees{hash: "t-incomplete"},
		completeErr: errors.New("worktree: checkout is missing tracked files: /wt has 222 tracked file(s) missing"),
		probed:      &probed,
	}
	s := newTestService(t, db, r, trees)
	if s.Checkout == nil {
		t.Fatal("NewService did not wire a Trees that implements CheckoutProbe as the Checkout gate")
	}
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if probed != 1 {
		t.Errorf("CheckComplete called %d times, want 1", probed)
	}
	if r.count() != 0 {
		t.Errorf("verifier spawned %d run(s) on an incomplete checkout, want 0", r.count())
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Fatalf("verdict = %q, want inconclusive (never fail)", got)
	}
	detail := detailOf(t, db, id)
	if !strings.HasPrefix(detail, ClassUnverifiable+":") || !strings.Contains(detail, "222 tracked file(s)") {
		t.Errorf("detail = %q, want %s carrying the probe's reason", detail, ClassUnverifiable)
	}
}

// A complete checkout passes the gate untouched: the run proceeds as before.
func TestVerifyGradesCompleteCheckout(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "VERDICT: PASS"}
	probed := 0
	s := newTestService(t, db, r, probedTrees{stubTrees: stubTrees{hash: "t-ok"}, probed: &probed})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if probed != 1 || r.count() != 1 {
		t.Errorf("probed=%d runs=%d, want 1 and 1", probed, r.count())
	}
	if got := verdictOf(t, db, id); got != "pass" {
		t.Errorf("verdict = %q, want pass", got)
	}
}
