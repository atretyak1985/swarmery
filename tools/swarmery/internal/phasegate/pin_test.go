package phasegate

// Compile-time pin of the gate's contract (decision D2 of the phase-landing plan:
// the landing lifecycle lives BESIDE completion, and phasegate is not touched).
//
// Landing a phase — pushing its branch, opening a change request — must never
// become a completion condition by accident. Any change to Input or Result breaks
// the build HERE, in review, where the change has to be argued for, instead of
// silently widening the gate.
//
// Two pins, because each catches what the other cannot:
//   - the keyed literals name every field, so a removed or renamed field fails;
//   - the conversions to an anonymous struct demand IDENTICAL field sets (names,
//     types, order), so an ADDED field fails too — a keyed literal alone would
//     happily leave a new field at its zero value.
//
// If this file stops compiling, the gate's contract changed: update the pin in the
// same change, and say in its review why completion now depends on something new.

var _ = Input{
	CriteriaDone:     0,
	CriteriaTotal:    0,
	VerifyMode:       "",
	VerifyVerdict:    "",
	LegacyDone:       false,
	CompletionReport: "",
	Ran:              false,
	ClosureRequired:  false,
}

var _ = Result{
	State:   "",
	Reasons: nil,
}

var _ = struct {
	CriteriaDone     int
	CriteriaTotal    int
	VerifyMode       string
	VerifyVerdict    string
	LegacyDone       bool
	CompletionReport string
	Ran              bool
	ClosureRequired  bool
}(Input{})

var _ = struct {
	State   string
	Reasons []string
}(Result{})
