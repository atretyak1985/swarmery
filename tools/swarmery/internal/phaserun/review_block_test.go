package phaserun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/workaround"
)

// The block example in reviewerRole is what a reviewer copies: it must parse with
// the parser later phases read the doc's `## Review` section with.
func TestReviewerRoleBlockParses(t *testing.T) {
	rs, errs := mdfence.ParseReviews(reviewerRole)
	if len(errs) != 0 || len(rs) != 1 {
		t.Fatalf("reviewerRole's kind: review example: reviews %+v, errors %+v; want exactly one parsable block", rs, errs)
	}
	r := rs[0]
	if r.Round != "1" || r.Verdict != mdfence.ReviewFail || !r.Complete || len(r.Findings) != 1 {
		t.Errorf("example block = %+v", r)
	}
	for _, want := range append([]string{
		"kind: review", "scope_left", "complete: false", "resolves", "checked_callers", "REVIEW ROUND",
		"Immediately before the verdict line",
	}, mdfence.FindingTypes...) {
		if !strings.Contains(reviewerRole, want) {
			t.Errorf("reviewerRole lacks %q", want)
		}
	}
	// The verdict line stays the last thing the contract asks for.
	if strings.LastIndex(reviewerRole, "kind: review") > strings.LastIndex(reviewerRole, "VERDICT: PASS | FAIL | INCONCLUSIVE") {
		t.Error("the block contract comes after the VERDICT line")
	}
}

// code-reviewer.md and reviewerRole restate one contract; the yaml example must
// be the same text in both so neither drifts alone.
func TestReviewerRoleBlockMatchesAgentFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "plugins", "core", "agents", "code-reviewer.md"))
	if err != nil {
		t.Skipf("code-reviewer.md not reachable from this checkout: %v", err)
	}
	example := func(doc string) string {
		for _, b := range mdfence.Blocks(doc) {
			if b.Info == "yaml" && strings.HasPrefix(b.Content, "kind: review\nround: 1                 #") {
				return b.Content
			}
		}
		return ""
	}
	agent, role := example(string(raw)), example(reviewerRole)
	if agent == "" || role == "" {
		t.Fatalf("contract example missing: agent file %t, reviewerRole %t", agent != "", role != "")
	}
	// The one line allowed to differ: where the round comes from.
	norm := func(s string) string {
		lines := strings.Split(s, "\n")
		lines[1] = "round: 1"
		return strings.Join(lines, "\n")
	}
	if norm(agent) != norm(role) {
		t.Errorf("code-reviewer.md and reviewerRole disagree on the kind: review block:\n--- agent\n%s\n--- role\n%s", agent, role)
	}
}

func TestReviewPromptCarriesRound(t *testing.T) {
	p := reviewPrompt("Phase 1 — X", "# doc", "DIFF: none", 7777, 2, workaround.PhaseHits{})
	if !strings.Contains(p, "\n\nREVIEW ROUND: 2\n\nPHASE: Phase 1 — X") {
		t.Errorf("prompt lacks the round before the phase:\n%s", p)
	}
	if !strings.Contains(planReviewPrompt("Plan", "", "", 7777), "REVIEW ROUND: plan") {
		t.Error("the plan branch review prompt lacks REVIEW ROUND: plan")
	}
}

// The block's own `verdict:` key sits inside a fence: verdict handling is
// unchanged whatever the block says.
func TestVerdictUnaffectedByReviewBlock(t *testing.T) {
	out := "P1 a.go:3 — wrong.\n\n```yaml\nkind: review\nround: 1\nverdict: FAIL\nfindings: []\n```\nVERDICT: PASS\n"
	if v, _ := verify.ParseVerdict(out); v != verify.VerdictPass {
		t.Errorf("ParseVerdict = %q, want PASS from the final line", v)
	}
	if !verify.HasVerdictLine(out) {
		t.Error("HasVerdictLine = false")
	}
	if v, _ := verify.ParseVerdict("```yaml\nkind: review\nround: 1\nverdict: FAIL\n```\n"); v != verify.VerdictInconclusive {
		t.Errorf("a block alone is not a verdict line; ParseVerdict = %q", v)
	}
}
