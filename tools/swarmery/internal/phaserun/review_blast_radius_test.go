package phaserun

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/workaround"
)

func blastDoc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "blast-radius", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The reviewer brief repeats the phase doc's `Blast radius:` line on its own,
// between the phase title and the doc, so the reviewer knows which call sites to
// read and record in checked_callers.
func TestReviewPromptRepeatsBlastRadius(t *testing.T) {
	t.Run("doc with the line", func(t *testing.T) {
		p := reviewPrompt("", "Phase 2 — Line-item totals", blastDoc(t, "phase-with-blast-radius.md"), "DIFF: none", 7777, 1, workaround.PhaseHits{})
		want := "\n\nBLAST RADIUS: 3 callers in internal/orders, internal/api — internal/orders/service.go:41, " +
			"internal/api/line_items.go:88, internal/orders/totals_test.go:17 (sources: rg, serena)\n" +
			"Read these call sites against the diff; list each one you read, as file:line, in the block's checked_callers."
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks the repeated line:\n%s", p[:min(len(p), 4000)])
		}
		phase, brief, doc := strings.Index(p, "\n\nPHASE: "), strings.Index(p, "\n\nBLAST RADIUS: "), strings.Index(p, "PHASE DOCUMENT:")
		if !(phase < brief && brief < doc) {
			t.Errorf("order PHASE=%d BLAST RADIUS=%d PHASE DOCUMENT=%d, want the line between title and doc", phase, brief, doc)
		}
		if strings.Contains(p, "BLAST RADIUS: Blast radius:") {
			t.Error("the brief repeats the label twice")
		}
	})
	t.Run("doc without one", func(t *testing.T) {
		p := reviewPrompt("", "Phase 2 — Line-item totals", blastDoc(t, "phase-with-risk.md"), "DIFF: none", 7777, 1, workaround.PhaseHits{})
		if !strings.Contains(p, "\n\nBLAST RADIUS: the phase document declares none") {
			t.Error("a doc without a line does not say it declares none")
		}
		// The fixture quotes a line inside its agent prompt fence; it is not the doc's.
		if strings.Contains(p, "BLAST RADIUS: 3 callers") {
			t.Error("a fenced quote reached the brief as the doc's line")
		}
	})
	t.Run("bold label", func(t *testing.T) {
		if got := blastRadiusBrief("## Risks\n- **Blast radius:** 1 caller in a — a/b.go:3\n"); !strings.HasPrefix(got, "BLAST RADIUS: 1 caller in a — a/b.go:3\n") {
			t.Errorf("brief = %q", got)
		}
	})
}

// What the brief asks for comes back parseable: a reviewer block whose
// checked_callers lists the briefed call sites (flow form, file:line values)
// reads into Review.CheckedCallers with the parser the `## Review` section uses.
func TestCheckedCallersFromBriefParse(t *testing.T) {
	out := "No findings.\n\n```yaml\nkind: review\nround: 1\nverdict: PASS\ncomplete: true\nscope_left: []\nfindings: []\n" +
		"resolves: []\nids: []\ndrift: []\n" +
		"checked_callers: [internal/orders/service.go:41, internal/api/line_items.go:88, internal/orders/totals_test.go:17]\n" +
		"```\nVERDICT: PASS\n"
	rs, errs := mdfence.ParseReviews(out)
	if len(errs) != 0 || len(rs) != 1 {
		t.Fatalf("reviews %+v, errors %+v; want one parsable block", rs, errs)
	}
	want := []string{"internal/orders/service.go:41", "internal/api/line_items.go:88", "internal/orders/totals_test.go:17"}
	if !reflect.DeepEqual(rs[0].CheckedCallers, want) {
		t.Errorf("checked_callers = %q, want %q", rs[0].CheckedCallers, want)
	}
	if !strings.Contains(reviewerRole, "BLAST RADIUS line") || !strings.Contains(reviewerRole, "checked_callers") {
		t.Error("reviewerRole does not tie the BLAST RADIUS line to checked_callers")
	}
}
