package wsingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const effortFixture = "testdata/effort-plan"

func readFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(effortFixture, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestLintPhaseDocEffortMissing(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want []string
	}{
		// The quoted `**Effort:** high` sits below the first `## ` heading —
		// someone else's header, not this doc's.
		{"phase-1-no-effort.md", []string{LintPhaseEffortMissing}},
		{"phase-2-declared.md", nil},
		{"phase-3-done.md", nil}, // has a Completion Report: history
	} {
		var codes []string
		for _, l := range LintPhaseDoc(readFixture(t, tc.doc)) {
			codes = append(codes, l.Code)
			if !strings.Contains(l.Message, "**Effort:**") {
				t.Errorf("%s: message %q does not say what to write", tc.doc, l.Message)
			}
		}
		if fmt.Sprint(codes) != fmt.Sprint(tc.want) {
			t.Errorf("%s: lints = %v, want %v", tc.doc, codes, tc.want)
		}
	}
	if got := LintPhaseDoc("# P\n**Effort:**\n\n## Goal\n"); len(got) != 1 {
		t.Errorf("an empty **Effort:** value must lint as missing, got %v", got)
	}
}

// The parse attaches the lint to the phase that lacks the header — and only
// to it — without counting it as a scan warning.
func TestParsePlanCarriesPhaseEffortMissing(t *testing.T) {
	warnings := 0
	phases := parsePlan(effortFixture, func(string, ...any) { warnings++ })
	if warnings != 0 {
		t.Errorf("scan warnings = %d, want 0 — a lint is not a scan degradation", warnings)
	}
	var hits []string
	for _, p := range phases {
		for _, l := range p.docLints {
			if l.Code == LintPhaseEffortMissing {
				hits = append(hits, filepath.Base(p.docPath))
			}
		}
	}
	if fmt.Sprint(hits) != "[phase-1-no-effort.md]" {
		t.Fatalf("phase_effort_missing on %v, want [phase-1-no-effort.md]", hits)
	}
}
