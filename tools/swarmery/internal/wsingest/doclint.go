package wsingest

// Phase-doc header lints — the plan-side twin of the forecast lints
// (forecast.go): a thing to show the author, never a thing that can refuse an
// ingest or a run. The scan reports what the doc says; this judges it.

import "log"

// PhaseDocLint is one thing wrong with a phase doc's header. Code is stable
// (the lint's identity); Message says what to write.
type PhaseDocLint struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LintPhaseEffortMissing: the doc declares no `**Effort:**` header, so its run
// takes the daemon's default effort instead of the depth the work needs.
const LintPhaseEffortMissing = "phase_effort_missing"

// LintPhaseDoc reports what is wrong with ONE phase doc's header block.
//
// A doc that already carries a Completion Report is history — its run is over
// and an effort header written now would change nothing — so it is never
// linted. The header is read through ParseEffort, the one definition of the
// format: a value the CLI would reject is phaserun's to refuse loudly
// (DocEffortError), not this lint's.
//
// Pure; unit-tested.
func LintPhaseDoc(md string) []PhaseDocLint {
	out := []PhaseDocLint{}
	if ParseCompletionReport(md) != "" {
		return out
	}
	if ParseEffort(md) == "" {
		out = append(out, PhaseDocLint{
			Code: LintPhaseEffortMissing,
			Message: "no `**Effort:**` header (low | medium | high | xhigh) — the run falls back to the daemon's default effort; " +
				"add the line beside `**Verify:**` / `**Review:**`",
		})
	}
	return out
}

// logDocLints writes each phase's header lints to the daemon log. Deliberately
// NOT the scan's warn channel: a scan warning counts toward ScanStats.Warnings
// and means the scan itself degraded, while a lint is advice about a doc that
// indexed fine. It fires when a plan is (re)parsed — the hash gate skips an
// unchanged plan — so each lint is logged once per edit of its plan, not on
// every scan.
func logDocLints(planDir string, phases []epicPhase) {
	for _, p := range phases {
		for _, l := range p.docLints {
			log.Printf("lint: epics plan %s: phase doc %s: %s — %s", planDir, p.docPath, l.Code, l.Message)
		}
	}
}
