package phaserun

import (
	"strings"
	"testing"
)

// The executor is told to cite the ids it satisfies and how to write a
// deliberate gap — the shape wsingest.ScanEvidence reads (verify-not-trust
// phase 12, D19). Exactly once.
func TestPromptCarriesCitationRule(t *testing.T) {
	p := BuildPrompt("plan/phase-1.md", "phase-1.md", "# Phase 1\n")
	const rule = "Cite the IDs you satisfy (D-n, SC-n) in the Completion Report; a decision you deliberately do not implement is written as `not implemented: D-n — <why>`."
	if n := strings.Count(p, rule); n != 1 {
		t.Errorf("citation rule appears %d times, want 1:\n%s", n, p)
	}
}
