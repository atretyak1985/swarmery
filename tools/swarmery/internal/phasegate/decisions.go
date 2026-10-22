package phasegate

import "strings"

// Decision is the slice of a phase_decisions row (migration 0120) the decision
// hold judges. Callers load it; this package stays pure (no DB, no clock).
type Decision struct {
	ID       string // "D-3"
	Question string
	Cost     string // cheap | expensive
	Status   string // decided | undecided | recommended
}

// DecisionHoldCode prefixes the hold detail, so every surface (the 409 body, the
// phase card) names the same reason the same way.
const DecisionHoldCode = "decision-undecided"

// holds reports whether one decision blocks the phases that depend on it:
// undecided AND expensive to change. A cheap undecided decision never holds —
// building on a guess that is cheap to reverse beats waiting for an answer.
func (d Decision) holds() bool {
	return d.Status == "undecided" && d.Cost == "expensive"
}

// DecisionsHolding returns, in the order the phase lists them, the decisions in
// deps that hold the phase. A listed id with no recorded decision does not hold:
// nobody could ever take it, so refusing on it would be a dead end, not a gate.
func DecisionsHolding(deps []string, decisions []Decision) []Decision {
	if len(deps) == 0 {
		return nil
	}
	byID := make(map[string]Decision, len(decisions))
	for _, d := range decisions {
		byID[strings.ToUpper(d.ID)] = d
	}
	var out []Decision
	for _, id := range deps {
		if d, ok := byID[strings.ToUpper(strings.TrimSpace(id))]; ok && d.holds() {
			out = append(out, d)
		}
	}
	return out
}

// HoldsForDecisions is the phase-run start gate's decision condition: whether a
// phase whose doc declares `**Depends on decisions:** deps` must wait, and the
// hold detail — `decision-undecided: D-3 (<question>)`, one entry per holding
// decision, comma-joined.
//
// It is a START gate, not part of Check: a decision taken late does not make
// finished work incomplete, it only stops new work being built on a guess.
func HoldsForDecisions(deps []string, decisions []Decision) (hold bool, reason string) {
	waiting := DecisionsHolding(deps, decisions)
	if len(waiting) == 0 {
		return false, ""
	}
	parts := make([]string, 0, len(waiting))
	for _, d := range waiting {
		q := strings.Join(strings.Fields(d.Question), " ")
		if q == "" {
			parts = append(parts, d.ID)
			continue
		}
		parts = append(parts, d.ID+" ("+q+")")
	}
	return true, DecisionHoldCode + ": " + strings.Join(parts, ", ")
}
