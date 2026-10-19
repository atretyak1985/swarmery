package phasegate

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in        Input
		want      string
		wantWords string
	}{
		{
			name: "criteria met, verification never asked for",
			in:   Input{CriteriaDone: 5, CriteriaTotal: 5, VerifyMode: "off"},
			want: StateComplete,
		},
		{
			name: "criteria met, verify_mode empty is the same as off",
			in:   Input{CriteriaDone: 5, CriteriaTotal: 5},
			want: StateComplete,
		},
		{
			name: "criteria met and graded pass",
			in:   Input{CriteriaDone: 5, CriteriaTotal: 5, VerifyMode: "normal", VerifyVerdict: VerdictPass},
			want: StateComplete,
		},
		{
			// THE case this package exists for: the doc asked to be graded, the grade
			// never landed, and the phase used to read as done anyway.
			name:      "criteria met, verification asked for, no verdict",
			in:        Input{CriteriaDone: 5, CriteriaTotal: 5, VerifyMode: "normal", Dispatched: true},
			want:      StateUnverified,
			wantWords: "no verdict",
		},
		{
			// Mid-run: the executor ticked the last criterion, the verifier has not
			// graded yet and run_ended_at is unset. Dispatched, not Ran, is what
			// holds it — the phase must not unblock its dependents here.
			name:      "dispatched, not yet ended, no verdict",
			in:        Input{CriteriaDone: 5, CriteriaTotal: 5, VerifyMode: "strict", Dispatched: true, Ran: false},
			want:      StateUnverified,
			wantWords: "no verdict",
		},
		{
			// Worked through by hand, an interactive session or a plan run: no
			// verifier can ever grade it and Verify again has no run branch, so a
			// missing verdict is not a debt the operator could pay.
			name: "never dispatched, verification asked for, no verdict",
			in:   Input{CriteriaDone: 5, CriteriaTotal: 5, VerifyMode: "strict"},
			want: StateComplete,
		},
		{
			name:      "criteria met, verification could not conclude",
			in:        Input{CriteriaDone: 5, CriteriaTotal: 5, VerifyMode: "strict", VerifyVerdict: VerdictInconclusive, Dispatched: true},
			want:      StateUnverified,
			wantWords: "could not conclude",
		},
		{
			// Decision D5 stands: a FAILED grade is completed work with a
			// verify-failed blocker beside it. This gate covers the ABSENCE of a
			// grade, not a grade against the work.
			name: "criteria met, graded fail — still complete, blocker lives elsewhere",
			in:   Input{CriteriaDone: 5, CriteriaTotal: 5, VerifyMode: "strict", VerifyVerdict: VerdictFail},
			want: StateComplete,
		},
		{
			name:      "criteria not met",
			in:        Input{CriteriaDone: 2, CriteriaTotal: 5, VerifyMode: "normal", VerifyVerdict: VerdictPass},
			want:      StateIncomplete,
			wantWords: "2 of 5",
		},
		{
			name:      "no criteria at all is unprovable, not complete",
			in:        Input{CriteriaTotal: 0},
			want:      StateIncomplete,
			wantWords: "cannot be proven",
		},
		{
			// Legacy activated board tasks predate criteria counting; re-opening them
			// would rewrite history rather than gate new work.
			name: "legacy board-done phase",
			in:   Input{CriteriaTotal: 0, LegacyDone: true},
			want: StateComplete,
		},
		{
			name: "legacy done wins over a missing verdict",
			in:   Input{CriteriaDone: 5, CriteriaTotal: 5, VerifyMode: "strict", LegacyDone: true},
			want: StateComplete,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(tc.in)
			if got.State != tc.want {
				t.Fatalf("State = %q, want %q (reasons: %v)", got.State, tc.want, got.Reasons)
			}
			if got.Complete() != (tc.want == StateComplete) {
				t.Errorf("Complete() = %v for state %q", got.Complete(), got.State)
			}
			if tc.want == StateComplete && len(got.Reasons) != 0 {
				t.Errorf("a complete result must carry no reasons; got %v", got.Reasons)
			}
			if tc.want != StateComplete && len(got.Reasons) == 0 {
				t.Error("a refused result must say why")
			}
			if tc.wantWords != "" && !strings.Contains(strings.Join(got.Reasons, " "), tc.wantWords) {
				t.Errorf("reasons %v do not mention %q", got.Reasons, tc.wantWords)
			}
		})
	}
}

// Unverified must be its own state — collapsing it into either neighbour is the
// bug. Into `complete` and the work ships ungraded; into `failed`/`incomplete`
// and an infra hiccup reads as a real test failure, which is the distinction
// verify.ParseVerdict's bias-to-reject exists to protect.
func TestUnverifiedIsDistinctFromCompleteAndIncomplete(t *testing.T) {
	unverified := Check(Input{CriteriaDone: 3, CriteriaTotal: 3, VerifyMode: "normal", Dispatched: true})
	complete := Check(Input{CriteriaDone: 3, CriteriaTotal: 3, VerifyMode: "normal", VerifyVerdict: VerdictPass, Dispatched: true})
	incomplete := Check(Input{CriteriaDone: 1, CriteriaTotal: 3, VerifyMode: "normal"})

	if unverified.State == complete.State {
		t.Error("an unverified phase reads the same as a verified one")
	}
	if unverified.State == incomplete.State {
		t.Error("an unverified phase reads the same as one with unticked criteria")
	}
	if unverified.Complete() {
		t.Error("an unverified phase must not pass the gate")
	}
}

// VerificationRequired is the knob that keeps the gate off every plan that never
// asked for grading — without it, this change would stall the fleet.
func TestVerificationRequired(t *testing.T) {
	for mode, want := range map[string]bool{"": false, "off": false, "normal": true, "strict": true} {
		if got := (Input{VerifyMode: mode}).VerificationRequired(); got != want {
			t.Errorf("VerificationRequired(%q) = %v, want %v", mode, got, want)
		}
		// Asking is not owing: only a dispatched phase can be graded.
		if (Input{VerifyMode: mode}).VerificationOwed() {
			t.Errorf("VerificationOwed(%q) without Dispatched = true", mode)
		}
		if got := (Input{VerifyMode: mode, Dispatched: true}).VerificationOwed(); got != want {
			t.Errorf("VerificationOwed(%q, dispatched) = %v, want %v", mode, got, want)
		}
	}
}

// ── the closure conditions (Phase 7) ──
// One gate, two more reasons. These compose with the verification condition
// rather than forming a second gate: the operator sees every reason at once.

func TestCheck_ClosureConditions(t *testing.T) {
	// A report that passes: it names files and says what happened.
	goodReport := "Rewired the janitor sweep so a reclaimed worktree cannot strand a lock. " +
		"Files: internal/prune/sweep.go, internal/worktree/worktree.go. Tests: make test green."

	base := func() Input {
		return Input{
			CriteriaDone: 3, CriteriaTotal: 3,
			VerifyMode:       "off",
			CompletionReport: goodReport,
			Ran:              true,
			ClosureRequired:  true,
		}
	}

	t.Run("report and lesson present", func(t *testing.T) {
		if got := Check(base()); !got.Complete() {
			t.Fatalf("state = %q, reasons %v", got.State, got.Reasons)
		}
	})

	for _, tc := range []struct {
		name      string
		report    string
		wantWords string
	}{
		{"empty", "", "is empty"},
		{"whitespace only", "   \n\t  ", "is empty"},
		{"template placeholder", "TBD", "placeholder"},
		{"angle-bracket stub", "<what shipped>", "placeholder"},
		{"see the reply", "See above", "placeholder"},
		{"too short", "Did the thing.", "too short"},
		{"names nothing concrete", "The work is finished and everything went smoothly, no problems were encountered at all during this phase.", "names nothing concrete"},
	} {
		t.Run("report refused: "+tc.name, func(t *testing.T) {
			in := base()
			in.CompletionReport = tc.report
			got := Check(in)
			if got.State != StateUnreported {
				t.Fatalf("state = %q, want unreported (reasons %v)", got.State, got.Reasons)
			}
			joined := strings.Join(got.Reasons, " ")
			if !strings.Contains(joined, tc.wantWords) {
				t.Errorf("reasons %q do not say %q", joined, tc.wantWords)
			}
		})
	}

	// The condition binds only where the contract that demands a report was
	// actually given: a phase the daemon RAN. A hand-executed phase was never
	// handed that contract, and holding it to one reopened 48 of 49 finished
	// plans in the live store and emptied the dashboard's Done tab.
	t.Run("a phase that never ran owes no report", func(t *testing.T) {
		in := base()
		in.CompletionReport = ""
		in.Ran = false
		if got := Check(in); !got.Complete() {
			t.Errorf("a hand-executed phase must still close: %v", got.Reasons)
		}
	})

	// The blocked path is not a bypass: a phase that stopped early still owes a
	// report describing how far it got and what stopped it.
	t.Run("blocked phase still owes a report", func(t *testing.T) {
		in := base()
		in.CompletionReport = "BLOCKED"
		if got := Check(in); got.Complete() {
			t.Error("a one-word 'BLOCKED' closed the phase")
		}
		in.CompletionReport = "BLOCKED: the migration in internal/store/migrations/0058_x.sql needs a column " +
			"the API has not shipped yet; got as far as wiring the reader."
		if got := Check(in); !got.Complete() {
			t.Errorf("an honest blocked report must pass: %v", got.Reasons)
		}
	})

	// Off by default for plans already in flight — the migration path.
	t.Run("closure not required", func(t *testing.T) {
		in := base()
		in.CompletionReport = ""
		in.ClosureRequired = false
		if got := Check(in); !got.Complete() {
			t.Errorf("with the closure gate off, an unreported phase must still close: %v", got.Reasons)
		}
	})
}

// ONE gate citing several reasons — the property Phase 7 asked for explicitly,
// rather than two gates that each half-block.
func TestCheck_OneGateManyReasons(t *testing.T) {
	got := Check(Input{
		CriteriaDone: 4, CriteriaTotal: 4,
		VerifyMode:       "strict", // asked to be graded, never was
		CompletionReport: "",       // and, having run, never reported
		Ran:              true,
		Dispatched:       true,
		ClosureRequired:  true,
	})
	if got.Complete() {
		t.Fatal("a phase failing two conditions closed")
	}
	if len(got.Reasons) < 2 {
		t.Errorf("want every reason cited at once, got %d: %v", len(got.Reasons), got.Reasons)
	}
	// Unconfirmed work is the more serious of the two states, so it names the row.
	if got.State != StateUnverified {
		t.Errorf("state = %q, want unverified when verification is also unmet", got.State)
	}
}

// The two failure modes must not collapse into one label: the remedies differ
// (re-run the grader vs write down what happened).
func TestUnreportedIsDistinctFromUnverified(t *testing.T) {
	report := "Shipped the retry budget in internal/dispatch/service.go; make test green."
	unverified := Check(Input{CriteriaDone: 1, CriteriaTotal: 1, VerifyMode: "normal",
		CompletionReport: report, Ran: true, Dispatched: true, ClosureRequired: true})
	unreported := Check(Input{CriteriaDone: 1, CriteriaTotal: 1, VerifyMode: "off",
		CompletionReport: "", Ran: true, ClosureRequired: true})
	if unverified.State == unreported.State {
		t.Fatalf("both read as %q — the operator cannot tell which remedy applies", unverified.State)
	}
	if unverified.State != StateUnverified || unreported.State != StateUnreported {
		t.Errorf("states are swapped: %q / %q", unverified.State, unreported.State)
	}
}
