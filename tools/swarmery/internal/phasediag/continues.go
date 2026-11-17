package phasediag

import (
	"encoding/json"
	"strings"
)

// The guard's allow answer, as a stop decision records it (the option id) and as
// a plain guard decision records it (the label prefix). Spelled here because
// phaserun — which owns the decision flow — imports this package, not the other
// way round; phaserun's parity test pins all three to its own constants.
const (
	GuardOptionAllowID     = "allow-add-to-scope"
	GuardAnswerAllowPrefix = "allow (add to scope)"
	// StopOptionOtherPrefix opens an answer in the operator's own words — the
	// recorded form of mdfence.StopOptionOther. Exported only so phaserun's parity
	// test can pin it; a drift would make an own-words answer read as an option id.
	StopOptionOtherPrefix = "other: "
)

// AnswerContinuesBranch reports whether a DECIDED stop-source decision (its
// stored guard_json and its recorded answer) lets the phase's re-run build on the
// stopped run's branch. It is the one rule both the run engine
// (phaserun.stopAnsweredSinceBlock) and the diagnosis apply, so the modal can
// never advise deleting a branch the next run would have continued on — or call
// a branch safe that the run engine will refuse.
//
//   - The branch holds no changed protected file (protectedChanged empty): any
//     answer continues. A guard check flagged only by REFUSED attempts (the hook
//     blocked the write, so it never reached the branch) has nothing on the
//     branch for an answer to accept or reject.
//   - Otherwise the branch holds protected edits, and only the guard's allow
//     accepts them: the stop's exact allow-add-to-scope option, or a plain guard
//     decision's "allow (add to scope)…" answer. Any other answer — reject, an
//     agent option, the operator's own words — leaves them unaccepted.
func AnswerContinuesBranch(guardJSON, answer string) bool {
	var rec struct {
		ProtectedChanged []string        `json:"protectedChanged"`
		Stop             json.RawMessage `json:"stop"`
	}
	_ = json.Unmarshal([]byte(guardJSON), &rec)
	if len(rec.ProtectedChanged) == 0 {
		return true
	}
	if len(rec.Stop) == 0 || string(rec.Stop) == "null" {
		return strings.HasPrefix(answer, GuardAnswerAllowPrefix)
	}
	return stopAnswerOption(answer) == GuardOptionAllowID
}

// stopAnswerOption is the option id a stop's recorded answer chose: `<id>` or
// `<id> — <note>`, "" for the operator's own words (`other: <text>`).
func stopAnswerOption(answer string) string {
	if strings.HasPrefix(answer, StopOptionOtherPrefix) {
		return ""
	}
	id, _, _ := strings.Cut(answer, " — ")
	return id
}
