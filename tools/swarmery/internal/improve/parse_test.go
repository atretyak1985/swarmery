package improve

import (
	"errors"
	"strings"
	"testing"
)

const validOut = "## Diff\n" +
	"```diff\n" +
	"--- a/agents/tech-lead.md\n" +
	"+++ b/agents/tech-lead.md\n" +
	"@@ -10,3 +10,4 @@\n" +
	" existing line\n" +
	"+new guardrail line\n" +
	"```\n" +
	"## Rationale\n" +
	"Adds a guardrail because the evidence shows repeated exit-code failures.\n"

func TestSplitDiffRationaleValid(t *testing.T) {
	diff, rationale, err := splitDiffRationale(validOut)
	if err != nil {
		t.Fatalf("splitDiffRationale: %v", err)
	}
	if !strings.HasPrefix(diff, "--- a/agents/tech-lead.md") {
		t.Errorf("diff = %q, want unified diff body without fences", diff)
	}
	if strings.Contains(diff, "```") {
		t.Errorf("diff contains fence markers: %q", diff)
	}
	if !strings.Contains(diff, "+new guardrail line") {
		t.Errorf("diff lost hunk content: %q", diff)
	}
	if !strings.Contains(rationale, "guardrail") {
		t.Errorf("rationale = %q, want the rationale prose", rationale)
	}
}

// Leading model chatter before "## Diff" is tolerated — only the two-section
// contract from the first heading on is enforced.
func TestSplitDiffRationaleLeadingProse(t *testing.T) {
	if _, _, err := splitDiffRationale("Here is my minimal change.\n\n" + validOut); err != nil {
		t.Fatalf("leading prose should be tolerated: %v", err)
	}
}

func TestSplitDiffRationaleMissingRationale(t *testing.T) {
	out := "## Diff\n```diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n```\n"
	if _, _, err := splitDiffRationale(out); err == nil {
		t.Fatal("missing '## Rationale' section must be an error")
	}
}

func TestSplitDiffRationaleMissingDiffHeading(t *testing.T) {
	out := "```diff\n--- a/x\n+++ b/x\n```\n## Rationale\nwhy\n"
	if _, _, err := splitDiffRationale(out); err == nil {
		t.Fatal("missing '## Diff' section must be an error")
	}
}

// A raw diff in the Diff section without a ```diff fence violates the
// contract — errors, never a silent pass-through.
func TestSplitDiffRationaleDiffOutsideFence(t *testing.T) {
	out := "## Diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n## Rationale\nwhy\n"
	if _, _, err := splitDiffRationale(out); err == nil {
		t.Fatal("diff outside a ```diff fence must be an error")
	}
}

func TestSplitDiffRationaleTwoFences(t *testing.T) {
	out := "## Diff\n```diff\n-a\n+b\n```\n```diff\n-c\n+d\n```\n## Rationale\nwhy\n"
	if _, _, err := splitDiffRationale(out); err == nil {
		t.Fatal("two fenced blocks in the Diff section must be an error")
	}
}

// An empty diff block is the model's sanctioned "no justified change" answer —
// the sentinel, not a generic parse error.
func TestSplitDiffRationaleEmptyDiff(t *testing.T) {
	out := "## Diff\n```diff\n```\n## Rationale\nThe evidence does not justify any change.\n"
	_, _, err := splitDiffRationale(out)
	if !errors.Is(err, ErrNoChange) {
		t.Fatalf("err = %v, want ErrNoChange", err)
	}

	// Whitespace-only block counts as empty too.
	out = "## Diff\n```diff\n\n   \n```\n## Rationale\nNothing to fix.\n"
	if _, _, err := splitDiffRationale(out); !errors.Is(err, ErrNoChange) {
		t.Fatalf("whitespace-only diff: err = %v, want ErrNoChange", err)
	}
}

func TestSplitDiffRationaleEmptyRationale(t *testing.T) {
	out := "## Diff\n```diff\n-a\n+b\n```\n## Rationale\n   \n"
	if _, _, err := splitDiffRationale(out); err == nil {
		t.Fatal("empty rationale must be an error")
	}
}

func TestRenderPrompt(t *testing.T) {
	p := renderPrompt("/tmp/agents/tech-lead.md", "---\nname: tech-lead\n---\nbody", "## Scorecard\nruns: 3")
	for _, want := range []string{
		`<agent-file path="/tmp/agents/tech-lead.md">`,
		"name: tech-lead",
		"<evidence>## Scorecard",
		"## Diff",
		"## Rationale",
		"Vendor neutrality",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
