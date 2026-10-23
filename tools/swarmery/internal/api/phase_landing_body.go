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
	phasePRNoRemain   = "_None — every id this phase claims is cited in its report._"
	phasePRNoHonoured = "_No id is cited in the Completion Report yet._"
)

// phasePRIDSections renders the `## Remaining` / `## Decisions honoured` (and,
// when any, `## Not implemented`) sections, each led by "\n\n"; "" when the doc
// neither claims nor cites an id — every doc written before ids existed renders
// exactly as before. report is the doc's parsed Completion Report ("" or the
// placeholder when unwritten: neither cites an id).
func phasePRIDSections(doc, report string) string {
	var claimed []string
	seen := map[string]bool{}
	for _, raw := range append(wsingest.ParseCovers(doc), wsingest.ParseDecisionDeps(doc)...) {
		if id := wsingest.NormalizeRefID(raw); id != "" && !seen[id] {
			seen[id] = true
			claimed = append(claimed, id)
		}
	}
	mentions := wsingest.ScanEvidence(report)
	if len(claimed) == 0 && len(mentions) == 0 {
		return ""
	}
	cited := map[string]bool{}
	var honoured, notImpl []string
	for _, m := range mentions {
		cited[m.ID] = true
		if m.Status == wsingest.EvidenceStatusNotImplemented {
			line := "- " + m.ID
			if m.Reason != "" {
				line += " — " + m.Reason
			}
			notImpl = append(notImpl, line)
			continue
		}
		honoured = append(honoured, "- "+m.ID)
	}
	var remaining []string
	for _, id := range claimed {
		if !cited[id] {
			remaining = append(remaining, "- "+id)
		}
	}
	list := func(lines []string, empty string) string {
		if len(lines) == 0 {
			return empty
		}
		return strings.Join(lines, "\n")
	}
	var b strings.Builder
	b.WriteString("\n\n## Remaining\n\n")
	b.WriteString(list(remaining, phasePRNoRemain))
	b.WriteString("\n\n## Decisions honoured\n\n")
	b.WriteString(list(honoured, phasePRNoHonoured))
	if len(notImpl) > 0 {
		b.WriteString("\n\n## Not implemented\n\n")
		b.WriteString(strings.Join(notImpl, "\n"))
	}
	return b.String()
}

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
//   - `## Remaining` / `## Decisions honoured` (verify-not-trust phase 12, D19) —
//     only when the doc claims ids (`**Covers:**`, `**Depends on decisions:**`)
//     or its report cites any: Remaining lists the claimed ids the report does
//     not cite, honoured every id it cites as done, and a `## Not implemented`
//     list follows when the report marks one `not implemented: <id> — <why>`.
//     Per phase, from this doc alone; the plan-level union is the dashboard's
//     decision table.
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
	b.WriteString(phasePRIDSections(doc, report))
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "Swarm-Phase: %d/%d\n", taskID, phaseID)
	fmt.Fprintf(&b, "Plan: %s\n", planRel)
	return b.String()
}
