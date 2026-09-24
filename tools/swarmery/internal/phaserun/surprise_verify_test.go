package phaserun

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// A run the scorer flags is graded by the verifier AFTER actuals (which is what
// scores it) and BEFORE the worktree goes, at the normal bar, with the focus hint.
func TestSurpriseVerifyGradesAFlaggedRunWithTheHint(t *testing.T) {
	db, _, p1, _ := fixture(t)
	wt := &stubWt{}
	s := newTestService(db, &stubRunner{}, wt)

	var order []string
	v := &stubVerifier{onVerify: func() { order = append(order, "verify") }}
	s.Verify = v
	wt.onRemove = func() { order = append(order, "remove") }
	s.Actuals = func(int64, string, string) { order = append(order, "actuals") }
	var askedID int64
	var askedUUID string
	s.SurpriseVerify = func(phaseID int64, uuid string) (string, bool) {
		askedID, askedUUID = phaseID, uuid
		return "look at internal/api", true
	}

	uuid, err := s.Start(p1, "", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := []string{"actuals", "verify", "remove"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("exit order = %v, want %v", order, want)
	}
	if askedID != p1 || askedUUID != uuid {
		t.Errorf("SurpriseVerify asked (%d, %q), want (%d, %q)", askedID, askedUUID, p1, uuid)
	}
	reqs := v.calls()
	if len(reqs) != 1 {
		t.Fatalf("verifier called %d times, want 1", len(reqs))
	}
	if reqs[0].FocusHint != "look at internal/api" || reqs[0].Mode != wsingest.VerifyNormal {
		t.Errorf("request = hint %q mode %q, want the hint at the normal bar", reqs[0].FocusHint, reqs[0].Mode)
	}
}

// The scorer saying no (the default: auto-verify is off unless configured) means
// no verification at all for a doc that never asked for one.
func TestSurpriseVerifyOffByDefault(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	v := &stubVerifier{}
	s.Verify = v
	s.SurpriseVerify = func(int64, string) (string, bool) { return "", false }
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := len(v.calls()); n != 0 {
		t.Errorf("verifier called %d times with auto-verify declined, want 0", n)
	}

	// And with no hook wired at all.
	s2 := newTestService(db, &stubRunner{}, &stubWt{})
	v2 := &stubVerifier{}
	s2.Verify = v2
	if _, err := s2.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := len(v2.calls()); n != 0 {
		t.Errorf("verifier called %d times with no surprise hook, want 0", n)
	}
}

// A doc that opted into verification is graded once, by its own opt-in — the
// surprise path does not grade the same tree a second time (and is not asked).
func TestSurpriseVerifySkipsADocThatAlreadyVerifies(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	setVerifyMode(t, db, p1, "strict")
	v := &stubVerifier{}
	s.Verify = v
	asked := false
	s.SurpriseVerify = func(int64, string) (string, bool) { asked = true; return "hint", true }
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := len(v.calls()); n != 1 {
		t.Fatalf("verifier called %d times, want exactly the doc's own 1", n)
	}
	if v.calls()[0].FocusHint != "" {
		t.Errorf("the doc's own verification carried a focus hint %q", v.calls()[0].FocusHint)
	}
	if asked {
		t.Error("SurpriseVerify was consulted (and its claim spent) for a doc that already verifies")
	}
}
