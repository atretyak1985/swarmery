package phasegate

import (
	"net/url"
	"strconv"
	"strings"
)

// Decision is the slice of a phase_decisions row (migration 0122) the decision
// hold judges. Callers load it; this package stays pure (no DB, no clock).
type Decision struct {
	ID       string // "D-3"
	Question string
	Cost     string // cheap | expensive
	Status   string // decided | undecided | recommended
	// Seq and PlanningSessionID locate the interview turn that asked it (the
	// question's planning_turns.seq in that session); PlanningSessionID is 0 for
	// a decision born in the README, which has no turn to open.
	Seq               int
	PlanningSessionID int64
}

// HoldRef is one decision holding a phase, with where the operator takes it: the
// planning page opened on the interview turn that asked it — the same link the
// dashboard's phase card and Needs you open ("decide now").
type HoldRef struct {
	DecisionID        string `json:"decisionId"`
	Question          string `json:"question"`
	Seq               int    `json:"seq"`
	PlanningSessionID int64  `json:"planningSessionId,omitempty"`
	// PlanningTurn is the dashboard path of that turn; "" when the decision has
	// no planning session or the project slug is unknown.
	PlanningTurn string `json:"planningTurn,omitempty"`
}

// PlanningTurnLink is the dashboard path of a planning session's turn — the same
// URL web/src/pages/Plans.tsx decisionTurnHref builds. "" when any part is
// missing.
func PlanningTurnLink(projectSlug string, planningSessionID int64, seq int) string {
	if strings.TrimSpace(projectSlug) == "" || planningSessionID <= 0 || seq <= 0 {
		return ""
	}
	s := strconv.Itoa(seq)
	return "/p/" + url.PathEscape(projectSlug) + "/plans?tab=new&planningSession=" +
		strconv.FormatInt(planningSessionID, 10) + "&turn=" + s + "#planning-turn-" + s
}

// HoldRefs is DecisionsHolding with each holding decision's planning-turn link.
func HoldRefs(projectSlug string, deps []string, decisions []Decision) []HoldRef {
	waiting := DecisionsHolding(deps, decisions)
	if len(waiting) == 0 {
		return nil
	}
	out := make([]HoldRef, 0, len(waiting))
	for _, d := range waiting {
		out = append(out, HoldRef{
			DecisionID: d.ID, Question: strings.Join(strings.Fields(d.Question), " "), Seq: d.Seq,
			PlanningSessionID: d.PlanningSessionID,
			PlanningTurn:      PlanningTurnLink(projectSlug, d.PlanningSessionID, d.Seq),
		})
	}
	return out
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
