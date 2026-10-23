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
	phasePRNoHonoured = "_None yet._"
	phasePRNoRemains  = "_Nothing — every id this phase claims has evidence._"
)

// phaseEvidence is what the plan knows about the ids a phase claims
// (wsingest.LoadDecisionTable, verify-not-trust phase 12).
type phaseEvidence struct {
	// Declared are the ids the plan declares (README `## Decisions`, spec.md). nil
	// keeps every id the doc claims; an empty map keeps none, which is what a plan
	// without decisions or a spec gets — its body has no evidence sections.
	Declared map[string]bool
	// Held are the ids evidenced anywhere in the plan (another phase's report, the
	// plan review), on top of what this doc's own report cites.
	Held map[string]bool
}

// phaseEvidenceFrom reduces the plan's decision table to phaseEvidence.
func phaseEvidenceFrom(t wsingest.DecisionTable) phaseEvidence {
	ev := phaseEvidence{Declared: map[string]bool{}, Held: map[string]bool{}}
	for _, r := range t.Rows {
		ev.Declared[r.ID] = true
		if r.Status == wsingest.RowEvidence {
			ev.Held[r.ID] = true
		}
	}
	return ev
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
//   - `## Decisions honoured` / `## Remaining` — the D-n / SC-n ids the doc's
//     header claims (`**Depends on decisions:**`, `**Covers:**`) and the plan
//     declares: honoured when this report cites them (or ev holds evidence for
//     them), with `not implemented: <why>` when the report says so; remaining
//     otherwise. Both are absent when the phase claims no declared id.
//   - a trailer naming the phase (`Swarm-Phase: <taskID>/<phaseID>`) and the plan
//     doc (`Plan: <planRel>`), so a change request can be traced back to its phase.
func phasePRBody(doc string, taskID, phaseID int64, planRel string, ev phaseEvidence) string {
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
	if honoured, remaining, ok := phasePRDecisions(doc, report, ev); ok {
		b.WriteString("\n\n## Decisions honoured\n\n")
		b.WriteString(honoured)
		b.WriteString("\n\n## Remaining\n\n")
		b.WriteString(remaining)
	}
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "Swarm-Phase: %d/%d\n", taskID, phaseID)
	fmt.Fprintf(&b, "Plan: %s\n", planRel)
	return b.String()
}

// phasePRDecisions renders the bodies of the `## Decisions honoured` and
// `## Remaining` sections; ok is false when the doc claims no declared id. The
// report's citations come from wsingest.ScanEvidence — the very scan that fills
// decision_evidence — so the body and the plan tab agree.
func phasePRDecisions(doc, report string, ev phaseEvidence) (honoured, remaining string, ok bool) {
	var claimed []string
	seen := map[string]bool{}
	for _, id := range append(wsingest.ParseDecisionDeps(doc), wsingest.ParseCovers(doc)...) {
		if seen[id] || (ev.Declared != nil && !ev.Declared[id]) {
			continue
		}
		seen[id] = true
		claimed = append(claimed, id)
	}
	if len(claimed) == 0 {
		return "", "", false
	}
	cited := map[string]wsingest.EvidenceMention{}
	for _, m := range wsingest.ScanEvidence(report) {
		cited[m.ID] = m
	}
	var h, r []string
	for _, id := range claimed {
		m, ok := cited[id]
		switch {
		case ok && m.Status == wsingest.EvidenceNotImplemented:
			h = append(h, "- "+id+" — not implemented: "+m.Excerpt)
		case ok || ev.Held[id]:
			h = append(h, "- "+id)
		default:
			r = append(r, "- "+id)
		}
	}
	honoured, remaining = phasePRNoHonoured, phasePRNoRemains
	if len(h) > 0 {
		honoured = strings.Join(h, "\n")
	}
	if len(r) > 0 {
		remaining = strings.Join(r, "\n")
	}
	return honoured, remaining, true
}
