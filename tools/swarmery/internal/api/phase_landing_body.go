package api

import (
	"fmt"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Placeholders the change-request body falls back to. Exact strings: the golden
// test pins them, and a reviewer reads them as "the doc is missing this", which
// is more useful than an empty heading.
const (
	phasePRNoGoal     = "_The phase doc has no `## Goal` section._"
	phasePRNoReport   = "_Completion report not written yet._"
	phasePRNoCriteria = "_No acceptance criteria are ticked yet._"
)

// phasePRTitle is the change-request title for one landed plan phase:
// "<plan title>: Phase <seq> — <name>". Pure.
func phasePRTitle(planTitle string, seq int, name string) string {
	return fmt.Sprintf("%s: Phase %d — %s",
		strings.TrimSpace(planTitle), seq, strings.TrimSpace(name))
}

// phasePRBody renders a phase doc into the change-request body. Pure: it reads
// only doc, and everything it shows is the doc's own text — the operator already
// reviewed that text on the Review screen, so the body adds nothing they did not see.
//
//   - `## Goal` — the doc's Goal section, verbatim.
//   - `## Completion Report` — the section exactly as wsingest parses it for
//     epic_phases.completion_report (ParseCompletionReport), so the body and the
//     dashboard can never disagree about what the report says; a placeholder when
//     it is absent or empty.
//   - `### How to verify` — every TICKED acceptance criterion, verbatim, on the
//     same fence-aware walker the done count uses.
//   - a trailer naming the phase (`Swarm-Phase: <taskID>/<phaseID>`) and the plan
//     doc (`Plan: <planRel>`), so a change request can be traced back to its phase.
func phasePRBody(doc string, taskID, phaseID int64, planRel string) string {
	goal := wsingest.Section(doc, "Goal")
	if goal == "" {
		goal = phasePRNoGoal
	}
	report := wsingest.ParseCompletionReport(doc)
	if report == "" {
		report = phasePRNoReport
	}
	verify := phasePRNoCriteria
	if ticked := wsingest.TickedCheckboxes(doc); len(ticked) > 0 {
		verify = strings.Join(ticked, "\n")
	}

	var b strings.Builder
	b.WriteString("## Goal\n\n")
	b.WriteString(goal)
	b.WriteString("\n\n## Completion Report\n\n")
	b.WriteString(report)
	b.WriteString("\n\n### How to verify\n\n")
	b.WriteString(verify)
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "Swarm-Phase: %d/%d\n", taskID, phaseID)
	fmt.Fprintf(&b, "Plan: %s\n", planRel)
	return b.String()
}
