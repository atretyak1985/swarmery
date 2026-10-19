// Package phasegate is THE completion gate for a plan phase: one derivation that
// answers "may this phase be recorded as complete?" and, when the answer is no,
// says why.
//
// It exists because completion used to be decided in four places from the same
// two columns (checkboxes_done / checkboxes_total) and nothing else — so a phase
// whose verification never ran read exactly like one that had been graded and
// passed. The store still holds the proof: a verification row 1.1 seconds long
// with an empty detail, and another reading "verifier did not run: fork/exec
// …/claude: no such file or directory". Both stamped INCONCLUSIVE, and both
// phases presented as done.
//
// Design notes, so this does not get widened by accident:
//
//   - The gate is ONE call returning ONE state plus reasons, not a set of
//     independent checks. An operator should see a single completion gate that can
//     cite more than one reason, rather than several half-gates each refusing for
//     its own cause. Later conditions (a mandatory Completion Report, a recorded
//     lesson) belong in Input and Result.Reasons — not in a second gate beside
//     this one.
//   - It is PURE: no DB, no clock, no git. Callers hand it the row's fields.
//   - It deliberately does NOT restate phasediag.Outcome. That vocabulary answers
//     "did work land?" and is closed on purpose (decision D5 rejected a
//     `completed-unverified` outcome, because it forks that one question in two).
//     This package answers a different question — "may we call it done?" — and
//     the two travel side by side.
//   - `fail` is NOT this gate's business. A phase graded FAIL keeps the behaviour
//     decision D5 settled: it reads as completed work with a verify-failed blocker
//     beside it. Turning FAIL into "not complete" here would silently re-open a
//     settled decision, and FAIL is already visible. The gate covers the case that
//     was invisible: an ABSENT verdict.
package phasegate

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Completion states. Closed set.
const (
	// StateComplete — every condition met; the phase may be recorded as complete.
	StateComplete = "complete"
	// StateUnverified — the work is there (criteria ticked) but the grade is
	// missing or could not conclude. Distinct from complete AND from failed: the
	// work may well be fine, and nobody knows.
	StateUnverified = "unverified"
	// StateIncomplete — the criteria themselves are not met. The pre-existing
	// answer, unchanged.
	StateIncomplete = "incomplete"
	// StateUnreported — the work is there and (where asked for) confirmed, but the
	// record of it is not: an empty or placeholder Completion Report, or no lesson.
	// Separate from StateUnverified because they call for different actions —
	// re-run the verifier versus write down what happened — and one label for both
	// would send the operator to the wrong one half the time. When BOTH are true
	// the state is StateUnverified: unconfirmed work is the more serious of the
	// two, and Reasons carries the rest.
	StateUnreported = "unreported"
)

// Verdict values as stamped on the row. Mirrors verify.Verdict without importing
// it: phasegate must stay leaf-level so both api and phaserun can depend on it.
const (
	VerdictPass         = "pass"
	VerdictFail         = "fail"
	VerdictInconclusive = "inconclusive"
)

// VerifyOff is the verify_mode value meaning "this doc never asked to be graded".
// A phase that did not opt in cannot be held to a verdict it never requested —
// gating those would stall every plan that does not use verification.
const VerifyOff = "off"

// Input is one phase's completion-relevant row fields.
type Input struct {
	CriteriaDone  int
	CriteriaTotal int
	// VerifyMode is the doc's `**Verify:**` opt-in: off | normal | strict. Empty
	// is treated as off, matching wsingest's own normalisation.
	VerifyMode string
	// VerifyVerdict is the stamped grade: "" (never graded) | pass | fail |
	// inconclusive.
	VerifyVerdict string
	// LegacyDone marks a legacy activated board task that reached done/archived.
	// Those predate acceptance-criteria counting and prove completion by their
	// column; the gate honours that rather than retroactively re-opening them.
	LegacyDone bool
	// CompletionReport is the phase doc's `## Completion Report` body as
	// wsingest parsed it — "" when the section is absent OR present but empty,
	// which the parser already treats as the same thing.
	//
	// The contract has demanded this section in three places (the dispatcher's
	// execution contract, phaserun's prompt, the phase-doc template) and nothing
	// refused an empty one, so the instruction was advice: churn was measurable
	// for exactly two tasks in a fourteen-day window.
	CompletionReport string
	// Ran reports whether this phase was actually EXECUTED by the daemon —
	// epic_phases.run_ended_at is set.
	//
	// It gates the Completion Report condition, and the reason is fairness: the
	// contract that demands a report is the dispatcher's and phaserun's, given to
	// an executor when the daemon runs a phase. A phase a human worked through by
	// hand was never handed that contract, so holding it to one is not enforcing
	// an agreement — it is inventing one retroactively, for work that is finished.
	//
	// This is not a hypothetical softening. Without it the gate reopened 48 of 49
	// completed plans in the live store and emptied the dashboard's Done tab. With
	// it, the gate bites on the three dispatched phases that genuinely shipped
	// without a report, which is the case SC-11 exists for.
	Ran bool
	// Dispatched reports whether the daemon's phase runner ever STARTED this
	// phase — epic_phases.run_started_at is set. It is what makes a missing
	// verdict a debt rather than an impossibility.
	//
	// The verifier runs only behind a phaserun run (and Verify again needs that
	// run's branch), so a phase worked through by hand, by an interactive
	// session or by a plan run can never be graded, whatever its doc's
	// **Verify:** line asks for. Holding such a phase to a verdict left it
	// `unverified` forever — every criterion ticked, the code merged, no action
	// that could clear it (2026-10-19: all six phases of the phase-reverify and
	// reverify-rekeyed-phases plans).
	//
	// It is deliberately NOT Ran: during a run the executor ticks the last
	// criterion before the verifier grades, and run_ended_at is not yet set —
	// keying on Ran would let that phase read complete, and unblock its
	// dependents, before its grade landed.
	Dispatched bool
	// ClosureRequired is the closure gate's own opt-in, resolved by the caller
	// from ClosureGateEnabled(). Off ⇒ the report is not required, which is the
	// migration path for plans already in flight when the gate shipped.
	ClosureRequired bool
}

// Result is the gate's answer. Reasons is empty exactly when State is
// StateComplete.
type Result struct {
	State   string
	Reasons []string
}

// Complete reports whether the phase may be recorded as complete.
func (r Result) Complete() bool { return r.State == StateComplete }

// HoldsPlanBack reports whether this phase should stop its PLAN from reading
// done. It is Complete() with one exception: a phase with NO acceptance criteria.
//
// Such a phase is unprovable — it is `incomplete` and says so on its own row,
// which is right and predates this gate. But a plan is measured by its checkbox
// ROLLUP, and a phase with nothing to count contributes nothing to that rollup in
// either direction. Letting it veto the plan means one prose-only phase doc keeps
// a finished plan out of Done forever, with no action available that would ever
// clear it: the doc has no criteria to tick, and writing some retroactively would
// be inventing an agreement nobody made.
//
// That is not hypothetical — it is what two live plans looked like at 102/102 and
// 13/13 ticked while reading `active`. The dependency gate in phaserun keeps the
// stricter rule (a 0/0 dependency cannot unblock a dependent), because there the
// question is "may work START on top of this", where unprovable must mean no.
func (r Result) HoldsPlanBack(in Input) bool {
	if in.CriteriaTotal == 0 && !in.VerificationOwed() {
		return false
	}
	return !r.Complete()
}

// VerificationRequired reports whether this phase opted into being graded.
func (in Input) VerificationRequired() bool {
	return in.VerifyMode != "" && in.VerifyMode != VerifyOff
}

// VerificationOwed reports whether the gate holds this phase to a verdict: it
// asked to be graded AND the daemon dispatched it, so a grade can land. See
// Input.Dispatched.
func (in Input) VerificationOwed() bool {
	return in.VerificationRequired() && in.Dispatched
}

// criteriaMet is the pre-existing derivation, inlined here so this package stays
// leaf-level. phasediag.CriteriaMet remains the exported name other callers use;
// the two must agree, and a test pins that.
func criteriaMet(done, total int) bool { return total > 0 && done >= total }

// CriteriaMetForDisplay is criteriaMet, exported for callers that need the same
// "has this phase's work landed?" answer WITHOUT running the whole gate — an
// advisory blocker, for instance, that should only speak about finished work.
func CriteriaMetForDisplay(done, total int) bool { return criteriaMet(done, total) }

// Check runs the gate.
func Check(in Input) Result {
	if in.LegacyDone {
		return Result{State: StateComplete}
	}

	var reasons []string

	if !criteriaMet(in.CriteriaDone, in.CriteriaTotal) {
		if in.CriteriaTotal == 0 {
			reasons = append(reasons,
				"this phase has no acceptance-criteria checkboxes, so its completion cannot be proven")
		} else {
			reasons = append(reasons, fmt.Sprintf("%d of %d acceptance criteria are ticked",
				in.CriteriaDone, in.CriteriaTotal))
		}
		return Result{State: StateIncomplete, Reasons: reasons}
	}

	// ONE gate, several reasons. The verification condition and the closure
	// conditions are collected together rather than short-circuiting, so the
	// operator sees everything standing between this phase and done in one
	// answer instead of fixing one thing and being refused for the next.
	verificationUnmet := false
	if in.VerificationOwed() {
		switch in.VerifyVerdict {
		case "":
			verificationUnmet = true
			reasons = append(reasons,
				"this phase asked to be verified (verify: "+in.VerifyMode+
					") and carries no verdict — the grade never landed, so the ticked criteria are unconfirmed")
		case VerdictInconclusive:
			verificationUnmet = true
			reasons = append(reasons,
				"verification could not conclude, so the ticked criteria are unconfirmed — "+
					"see the verify detail for whether the verifier ran at all")
		}
	}

	// The report is owed by a phase the daemon RAN — see Input.Ran.
	//
	// A recorded lesson is deliberately NOT a condition here, and that is a
	// reversal of this gate's first shape, made against data: lessons live in
	// retro_lessons, fed from a task's phases/09-retrospective.md, and across 80
	// plans in the live store exactly ONE had an entry. Gating completion on it
	// marked 79 plans unfinished — it was not measuring a fleet that skips
	// lessons, it was measuring a convention the fleet does not use. It also
	// inverts the order of work: a retrospective is written after the work is
	// done, so requiring it to call the work done can never be satisfied in
	// sequence. The lesson survives as an advisory blocker on the phase
	// (phasediag) and as a measurable (advisor.Closure) — visible, not blocking.
	if in.ClosureRequired && in.Ran {
		if why := reportProblem(in.CompletionReport); why != "" {
			reasons = append(reasons, why)
		}
	}

	if len(reasons) > 0 {
		state := StateUnreported
		if verificationUnmet {
			state = StateUnverified
		}
		return Result{State: state, Reasons: reasons}
	}
	return Result{State: StateComplete}
}

// substantiveReportRe is the "names something concrete" test: a path with an
// extension or a directory separator, a git SHA, or a PR/commit reference.
// Deliberately crude — see reportProblem.
var substantiveReportRe = regexp.MustCompile(
	`(?i)([\w./-]+\.(go|ts|tsx|js|jsx|py|sh|md|json|yaml|yml|sql|tf|rs|java|rb|css|html)\b|` +
		`[\w-]+/[\w./-]+|\b[0-9a-f]{7,40}\b|#\d+)`)

// minReportRunes is the length floor. "done" and "ok" are not reports; the
// floor is low enough that a genuine two-sentence report clears it easily.
const minReportRunes = 80

// placeholderReportRe catches the stub a template leaves behind and the words
// an agent reaches for when it has nothing to say.
var placeholderReportRe = regexp.MustCompile(
	`(?i)^\s*(tbd|todo|n/?a|none|pending|-{1,3}|_+|<[^>]*>|\(?to be (filled|written)[^)]*\)?|nothing to report|see (above|reply|below))\s*\.?\s*$`)

// reportProblem returns "" when the Completion Report passes, else the reason.
//
// It gates on SUBSTANCE, not presence: an empty section, whitespace, a template
// placeholder, or prose that names nothing concrete does not close a phase. The
// substance test is simply "does it name a file, a commit, or a PR" — a
// deliberately crude check, because a prose analyser here would be a
// hard-to-explain oracle that agents learn to game in more elaborate ways.
//
// KNOWN LIMIT, recorded rather than engineered around: a report can satisfy this
// by naming one file and saying nothing useful. That is not preventable at this
// layer and trying would cost more than it saves. What the gate buys is that the
// section can no longer be EMPTY, which is what made churn unmeasurable.
func reportProblem(report string) string {
	trimmed := strings.TrimSpace(report)
	if trimmed == "" {
		return "the phase doc's `## Completion Report` section is empty — " +
			"write what shipped, the files and commits, the verification output, and any deviation. " +
			"That section is the only summary the dashboard shows for this phase"
	}
	if placeholderReportRe.MatchString(trimmed) {
		return "the `## Completion Report` is a placeholder (" + firstWords(trimmed, 6) + ") — " +
			"a blocked phase still owes a real report describing how far it got and what stopped it"
	}
	if len([]rune(trimmed)) < minReportRunes {
		return fmt.Sprintf(
			"the `## Completion Report` is %d characters — too short to be a report of anything; "+
				"name what shipped and where", len([]rune(trimmed)))
	}
	if !substantiveReportRe.MatchString(trimmed) {
		return "the `## Completion Report` names nothing concrete — cite the files you changed, " +
			"the commits you made, or the PR, so the work can be found later"
	}
	return ""
}

// firstWords is a short echo of the offending text for the refusal message.
func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

// ClosureGateEnabled reports the closure conditions' kill switch:
// SWARMERY_CLOSURE_GATE=0/false/off disables them. Default ON.
//
// The switch exists for one situation and is documented as such: plans already
// in flight when the gate shipped have phases that were closed under the old
// contract, and an operator who needs them to read as done while the reports are
// backfilled should be able to say so explicitly — rather than discovering the
// gate by having a finished plan reopen itself.
func ClosureGateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SWARMERY_CLOSURE_GATE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}
