// Package phasediag turns a phase's DB row plus the repo's git state into a
// human-actionable diagnosis of what a headless phase run achieved. It is read-only
// and computed on demand: git state changes outside the daemon (a user merging a
// branch in a terminal), so a cached blocker would be stale exactly when it matters.
package phasediag

import "database/sql"

// Derived run outcomes. run_state describes how the PROCESS ended; the outcome
// describes whether WORK LANDED. The two diverge whenever an executor exits 0
// without ticking anything — a failed precondition, or refused work.
//
// This vocabulary is CLOSED. In particular verification adds nothing to it: a phase
// whose read-only grade came back FAIL is still `completed` if it ticked all its
// criteria, with a verify-failed blocker beside it (decision D5, §5.4). A
// `completed-unverified` state was considered and rejected — it would fork the one
// question this list answers ("did work land?") into two, and every consumer of the
// list chip would then have to decide which half it meant.
const (
	OutcomeIdle      = "idle"
	OutcomeRunning   = "running"
	OutcomeCompleted = "completed"
	OutcomePartial   = "partial"
	OutcomeNoop      = "noop"
	OutcomeFailed    = "failed"
)

// CriteriaMet is THE phase-done derivation: a phase counts as done when it has
// acceptance criteria and every one of them is ticked. Exported because three
// surfaces need the same answer about the same row — the Plans API's lifecycle
// rollup (internal/api.planStatus), the completed-outcome branch below, and the
// revise wizard's immutable-doc list (internal/planning.BuildEvidence) — and a
// second copy of `total > 0 && done >= total` is exactly the kind of drift that
// would let them disagree.
func CriteriaMet(done, total int) bool {
	return total > 0 && done >= total
}

// Outcome derives what a run achieved from the closed interval [before, after].
// It is the pure primitive: both edges are already resolved, so it admits no NULLs
// and holds no policy about what an unmeasured run means.
//
// Callers holding an epic_phases row must NOT call this directly — the row's two
// columns need OutcomeFromRow's policy first. Keep this pure and dependency-free.
func Outcome(runState string, total, before, after int) string {
	switch runState {
	case "running":
		return OutcomeRunning
	case "failed":
		return OutcomeFailed
	// `blocked` and `partial` join `done` here, and the vocabulary above stays
	// CLOSED. All three describe a process that exited 0 — the completion loop
	// (runcore.ClassifyEnd) merely says WHICH kind of clean exit it was — so the
	// question this function answers, "did work land?", is still answered by the
	// checkbox interval and by nothing else. Falling through to the default would
	// report a blocked run as `idle`, i.e. as a run that never happened.
	//
	// Note that `partial` the run_state and OutcomePartial the outcome are NOT the
	// same claim and are not derived from each other: a run nudged twice that then
	// ticked its last criterion is run_state=partial only if the loop ran out of
	// continuations, while a run that ticked everything is OutcomeCompleted
	// regardless of how it got there.
	case "done", "blocked", "partial":
		switch {
		case CriteriaMet(after, total):
			return OutcomeCompleted
		case after > before:
			return OutcomePartial
		default:
			return OutcomeNoop
		}
	default:
		return OutcomeIdle
	}
}

// OutcomeFromRow derives the outcome straight from a phase row's columns, applying
// the two policies that make the derivation honest: run_checkboxes_after (the count
// stamped when the run ended) wins over the live count, which keeps moving; and a
// NULL run_checkboxes_before means UNMEASURED, so it collapses to `after` and an
// unmeasured run can never be reported as partial progress it may not have made.
// Callers that have a row should use this, never Outcome directly.
//
// This is the SINGLE derivation both surfaces go through — Diagnose for the modal
// and the api layer for the phase DTO — so the list chip and the diagnosis can never
// disagree. `live` is checkboxes_done, the fallback right edge for a run that never
// stamped one (pre-0042 rows, and runs still in flight).
func OutcomeFromRow(runState string, total, live int, before, after sql.NullInt64) string {
	right := live
	if after.Valid {
		right = int(after.Int64)
	}
	left := right // NULL baseline ⇒ zero delta, never 'partial'
	if before.Valid {
		left = int(before.Int64)
	}
	return Outcome(runState, total, left, right)
}
