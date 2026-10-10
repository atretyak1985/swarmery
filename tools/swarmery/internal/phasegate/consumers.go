package phasegate

// Consumers is the recorded list of every code path that decides a phase is
// complete, and whether it goes through Check.
//
// It exists because fixing the reported instance is not the job: the defect was
// that FOUR paths derived completion from the same two columns, and patching one
// would have left the others answering the old way. So the sweep is written down
// here, and consumers_test.go fails when a path in this list stops matching what
// it claims — including a stale exemption whose reason has expired.
//
// Adding a new completion decision means adding a row here. That is the point:
// the list is short, and a completion decision that is not in it is a bug.
type Consumer struct {
	// Path is the file the decision lives in, relative to tools/swarmery.
	Path string
	// Symbol is the function or identifier that makes the decision.
	Symbol string
	// Gated is whether the path is subject to the gate.
	Gated bool
	// Via names the identifier through which a gated path RECEIVES the gate's
	// verdict when it does not call Check itself — a parameter fed by a caller
	// that does. Empty means the path calls Check directly. It exists so a
	// consumer that only forwards the answer still has to prove where the answer
	// came from, instead of being able to claim compliance without either.
	Via string
	// Why documents an exemption. Required when Gated is false, and it must state
	// what makes the path legitimately different — not merely that it was skipped.
	Why string
}

// Consumers lists them all. Keep it alphabetical by Path.
var Consumers = []Consumer{
	{
		Path:   "internal/api/epics.go",
		Symbol: "epicPhases",
		Gated:  true,
	},
	{
		Path:   "internal/api/epics.go",
		Symbol: "planStatus",
		Gated:  true,
		// A pure derivation over the plan's rollup: it takes the gate's verdict as
		// a parameter rather than querying phases itself, so both callers
		// (listEpics via the rollup, derivedPlanStatus via phasesAllComplete) have
		// to compute it.
		Via: "allPhasesComplete",
	},
	{
		Path:   "internal/phasediag/phasediag.go",
		Symbol: "Diagnose",
		Gated:  true,
	},
	{
		Path:   "internal/phaserun/service.go",
		Symbol: "depSatisfied",
		Gated:  true,
	},
	{
		Path:   "internal/planning/revise_seed.go",
		Symbol: "BuildEvidence",
		Gated:  false,
		Why: "Not a completion decision. It classifies which phase docs are HISTORY " +
			"and therefore immutable in the revise wizard. A phase whose criteria are " +
			"ticked has been executed against, verified or not, and re-opening its doc " +
			"for revision would let a revision rewrite work that already shipped. " +
			"Gating this on a verdict would make an ungraded phase editable again, " +
			"which is the opposite of what the immutability is for.",
	},
	{
		Path:   "internal/api/phase_reopen.go",
		Symbol: "reopenPhase",
		Gated:  false,
		Why: "Not a completion decision. It refuses to REOPEN a phase whose " +
			"criteria are not all ticked (409 phase-not-done): an open phase needs " +
			"no ledger row to be worked on. The verdict of the gate is irrelevant " +
			"here — an unverified but fully ticked phase can still have let a " +
			"defect through and must be reopenable; gating it on a verifier pass " +
			"would hide exactly the reopens the baseline report exists to count.",
	},
	{
		Path:   "internal/phasediag/outcome.go",
		Symbol: "Outcome",
		Gated:  false,
		Why: "Answers a different question — 'did work land?' — over a CLOSED " +
			"vocabulary that decision D5 deliberately kept free of verification " +
			"(a `completed-unverified` outcome was considered and rejected because it " +
			"forks that one question in two). The gate travels beside the outcome " +
			"instead: Diagnose calls both, so the modal shows what landed AND whether " +
			"it may be called done.",
	},
}
