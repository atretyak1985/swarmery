package phasegate

import (
	"fmt"
	"net/url"
	"strings"
)

// Decision is the slice of a phase_decisions row (migration 0122) the decision
// hold judges. Callers load it; this package stays pure (no DB, no clock).
type Decision struct {
	ID       string // "D-3"
	Question string
	Cost     string // cheap | expensive
	Status   string // decided | undecided | recommended
	// Link is where the operator decides it — the planning session turn that
	// asked the question (PlanningTurnLink); "" for a decision born in the
	// README, which has no interview turn to open.
	Link string
}

// PlanningTurnLink is the dashboard path that opens a planning session on the
// turn that asked decision seq — the same URL the phase card's "waits on D-n"
// link builds (web Plans.tsx decisionTurnHref): the plans page's New tab,
// scoped by ?planningSession= and ?turn=, scrolled to the turn. "" when the
// slug or the session is unknown.
func PlanningTurnLink(projectSlug string, planningSessionID int64, seq int) string {
	if projectSlug == "" || planningSessionID <= 0 || seq <= 0 {
		return ""
	}
	return fmt.Sprintf("/p/%s/plans?tab=new&planningSession=%d&turn=%d#planning-turn-%d",
		url.PathEscape(projectSlug), planningSessionID, seq, seq)
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
// decision, comma-joined. A decision that carries a Link adds where to take it:
// `D-3 (<question>) — decide now: /p/<slug>/plans?…`.
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
		part := d.ID
		if q := strings.Join(strings.Fields(d.Question), " "); q != "" {
			part += " (" + q + ")"
		}
		if d.Link != "" {
			part += " — decide now: " + d.Link
		}
		parts = append(parts, part)
	}
	return true, DecisionHoldCode + ": " + strings.Join(parts, ", ")
}
