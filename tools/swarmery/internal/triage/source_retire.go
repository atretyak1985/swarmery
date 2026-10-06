package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
)

// retireInstruction is what the judge is asked about a retirement proposal.
const retireInstruction = "stop when the evidence shows the lesson no longer helps; " +
	"keep when it is still plausibly useful and the reason is only low traffic."

// RetireSource offers open lesson retirement proposals (lesson_retirements,
// state proposed) to a run. Its kind is suggest-only: the operator's accept
// confirms or keeps through the lessons functions, never through Apply.
// Retirements are fleet-wide, so they are collected for every scope.
type RetireSource struct {
	DB  *sql.DB
	Cfg lessons.VerifyConfig
}

// Kind is the policy key of retirement verdicts.
func (RetireSource) Kind() string { return "retire" }

// Collect lists every open proposal, newest first; limit > 0 caps the list.
func (s RetireSource) Collect(_ context.Context, _ Scope, limit int) ([]Item, error) {
	ps, err := lessons.ListProposals(s.DB, s.Cfg, false)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(ps))
	for _, p := range ps {
		ref := strconv.FormatInt(p.ID, 10)
		title := "Stop using “" + p.Title + "”?"
		items = append(items, Item{
			Kind:         "retire",
			Key:          ref,
			Title:        title,
			WaitingSince: p.ProposedAt,
			Instruction:  retireInstruction,
			Evidence:     retireEvidence(p),
			Parts:        []Part{{Ref: ref, Label: title, Allowed: []string{"stop", "keep"}}},
		})
	}
	return capItems(items, limit), nil
}

// retireEvidence is what the judge is shown about one proposal.
func retireEvidence(p lessons.Proposal) string {
	var b strings.Builder
	line(&b, "Lesson", p.Title)
	line(&b, "Guidance", p.Guidance)
	line(&b, "Reason", p.Reason)
	line(&b, "Detail", p.Detail)
	if len(p.Evidence) > 0 {
		if raw, err := json.Marshal(p.Evidence); err == nil {
			line(&b, "Reason numbers", string(raw))
		}
	}
	if e := p.Effectiveness; e != nil {
		line(&b, "Effectiveness", effectivenessText(*e))
	}
	return strings.TrimRight(b.String(), "\n")
}

// effectivenessText renders a lesson's stored verification row on one line.
func effectivenessText(e lessons.EffectivenessRow) string {
	parts := []string{
		fmt.Sprintf("runs before %d, after %d (window %d, min %d)", e.BeforeN, e.AfterN, e.WindowN, e.MinRuns),
	}
	if e.MedianBefore != nil {
		parts = append(parts, "median off-plan index before "+fmtNum(*e.MedianBefore))
	}
	if e.MedianAfter != nil {
		parts = append(parts, "after "+fmtNum(*e.MedianAfter))
	}
	if e.MedianDrop != nil {
		parts = append(parts, "drop "+fmtNum(*e.MedianDrop))
	}
	parts = append(parts, fmt.Sprintf("used %d times, relied on %d", e.Uses, e.Relied))
	if e.ReliedRate != nil {
		parts = append(parts, "relied rate "+fmtNum(*e.ReliedRate))
	}
	return strings.Join(parts, "; ")
}

// Apply is never reached: the policy makes retirement verdicts suggestions only.
func (RetireSource) Apply(context.Context, Item, Part, string, string, json.RawMessage) (Applied, error) {
	return Applied{}, errSuggestOnly("retire")
}

// Undo is never reached: nothing is ever applied for a retirement.
func (RetireSource) Undo(context.Context, Verdict) error { return errSuggestOnly("retire") }

// Open reports whether the proposal still waits for the operator (state proposed).
// An unknown or malformed ref is not open.
func (s RetireSource) Open(ctx context.Context, ref string) (bool, error) {
	return rowStateIs(ctx, s.DB, `SELECT state FROM lesson_retirements WHERE id=?`, ref, lessons.ProposalOpen)
}
