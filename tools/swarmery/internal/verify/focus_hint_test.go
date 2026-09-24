package verify

import (
	"context"
	"strings"
	"testing"
)

// An empty hint leaves the contract byte-identical — every verification that
// carries none (all of them unless surprise auto-verify is on) is unchanged.
func TestWithFocusHintEmptyIsIdentity(t *testing.T) {
	const contract = "- [ ] criterion one\n"
	if got := WithFocusHint(contract, "  \n"); got != contract {
		t.Errorf("WithFocusHint with no hint changed the contract: %q", got)
	}
	got := WithFocusHint(contract, "look at internal/api")
	if !strings.HasPrefix(got, "- [ ] criterion one\n\n"+focusHintHeading) || !strings.HasSuffix(got, "look at internal/api") {
		t.Errorf("WithFocusHint = %q, want the contract then the labelled hint", got)
	}
}

// A phase request's hint reaches the verifier's prompt, after the criteria and
// labelled as advisory.
func TestVerifyPhase_CarriesTheFocusHint(t *testing.T) {
	db := testDB(t)
	runner := &stubRunner{out: "fine\nVERDICT: PASS"}
	s := newTestService(t, db, runner, stubTrees{hash: "hinttree"})
	phaseID, epicID := insertPhase(t, db, "off")

	req := phaseReq(phaseID, epicID, "normal")
	req.FocusHint = "It changed areas the forecast did not name: web/src."
	if err := s.VerifyPhase(context.Background(), req); err != nil {
		t.Fatalf("VerifyPhase: %v", err)
	}
	p := runner.lastPrompt()
	ci, hi := strings.Index(p, req.Prompt), strings.Index(p, focusHintHeading)
	if ci < 0 || hi < ci || !strings.Contains(p, "web/src") {
		t.Errorf("prompt does not carry the hint after the criteria:\n%s", p)
	}
}
