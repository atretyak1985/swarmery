package verify

import (
	"context"
	"strings"
	"testing"
)

// ── step 3.3: one resume when the verdict line is missing ──
//
// The shape this fixes: a verifier that ran the checks, wrote a perfectly good
// report, and ended its turn with a narrative summary instead of the token the
// parser reads. Before this, that was INCONCLUSIVE with the whole read-only pass
// paid for and thrown away.

// TestVerdictRetry_RecoversAPass: the retry resumes the SAME session, asks only
// for the missing line, and the recovered verdict is stamped as a real one.
func TestVerdictRetry_RecoversAPass(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	r.outFn = func(spec RunSpec) *Run {
		if spec.Resume {
			return &Run{Output: "VERDICT: PASS", ExitCode: 0}
		}
		return &Run{Output: "- build is green\n- tests pass\nThat is everything I checked.", ExitCode: 0}
	}
	s := newTestService(t, db, r, stubTrees{hash: "t-retry-pass"})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "pass" {
		t.Fatalf("verdict = %q, want pass — the retry recovered it", got)
	}
	if r.count() != 2 {
		t.Fatalf("spawned %d times, want 2 (the pass + one resume)", r.count())
	}

	// The reasons from the FIRST message must survive: the model puts its bullets
	// above the verdict, and the verdict arrived in the second message.
	if d := detailOf(t, db, id); !strings.Contains(d, "build is green") {
		t.Errorf("detail = %q, want the reason bullets from the original report", d)
	}

	// The retry is a resume of the same session, and it forbids more work.
	retry := r.specs[1]
	if !retry.Resume {
		t.Error("the verdict retry must resume the session, not start a new verification")
	}
	if retry.SessionUUID != r.specs[0].SessionUUID {
		t.Errorf("retry uuid = %q, want the original %q", retry.SessionUUID, r.specs[0].SessionUUID)
	}
	if retry.Model != r.specs[0].Model || retry.Cwd != r.specs[0].Cwd || retry.Account != r.specs[0].Account {
		t.Error("the verdict retry must keep the original run's model, cwd and account")
	}
	for _, want := range []string{"Do not re-read files", "VERDICT: PASS | FAIL | INCONCLUSIVE", "Do not invent one"} {
		if !strings.Contains(retry.Prompt, want) {
			t.Errorf("retry prompt missing %q:\n%s", want, retry.Prompt)
		}
	}
}

// TestVerdictRetry_RecoversAFail: a recovered FAIL is a real FAIL — it caches and
// drives the fail follow-up exactly as a first-pass FAIL does.
func TestVerdictRetry_RecoversAFail(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	r.outFn = func(spec RunSpec) *Run {
		if spec.Resume {
			return &Run{Output: "VERDICT: FAIL", ExitCode: 0}
		}
		return &Run{Output: "- the migration has no rollback", ExitCode: 0}
	}
	s := newTestService(t, db, r, stubTrees{hash: "t-retry-fail"})
	id := insertTask(t, db, taskOpts{externalID: "T-root1"})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "fail" {
		t.Fatalf("verdict = %q, want fail", got)
	}
	if countFixTasks(t, db, "T-root1") == 0 {
		t.Error("a recovered FAIL must drive the fail follow-up like any other FAIL")
	}
}

// TestVerdictRetry_HappensOnlyOnce: a verifier that ignores the instruction twice
// is not going to answer on the third ask, and every attempt is a billed turn on
// the highest-frequency spawn in the fleet.
func TestVerdictRetry_HappensOnlyOnce(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "I looked at the diff and then ran out of budget."}
	s := newTestService(t, db, r, stubTrees{hash: "t-retry-once"})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := verdictOf(t, db, id); got != "inconclusive" {
		t.Fatalf("verdict = %q, want inconclusive", got)
	}
	if r.count() != 2 {
		t.Fatalf("spawned %d times, want exactly 2 (the pass + ONE resume)", r.count())
	}
	if d := detailOf(t, db, id); !strings.Contains(d, "no VERDICT: line") {
		t.Errorf("detail = %q, want it to still name the missing verdict line", d)
	}
}

// TestVerdictRetry_NotAttemptedWhenNothingWasWritten: an empty-output run is a
// spawn failure, not a forgotten line. Resuming a session that produced nothing
// buys nothing and costs a turn.
func TestVerdictRetry_NotAttemptedWhenNothingWasWritten(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: &Run{Output: "", ExitCode: 0}}
	s := newTestService(t, db, r, stubTrees{hash: "t-retry-empty"})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Fatalf("spawned %d times, want 1 — an empty run must not be resumed", r.count())
	}
	if d := detailOf(t, db, id); !strings.Contains(d, "written nothing") {
		t.Errorf("detail = %q, want the start-up-failure classification", d)
	}
}

// TestVerdictRetry_NotAttemptedWhenTheVerdictIsPresent: the happy path must not
// have gained a second spawn.
func TestVerdictRetry_NotAttemptedWhenTheVerdictIsPresent(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{out: "- all green\nVERDICT: PASS"}
	s := newTestService(t, db, r, stubTrees{hash: "t-retry-none"})
	id := insertTask(t, db, taskOpts{})

	if err := s.VerifyTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Fatalf("spawned %d times, want 1", r.count())
	}
	if got := verdictOf(t, db, id); got != "pass" {
		t.Fatalf("verdict = %q, want pass", got)
	}
}
