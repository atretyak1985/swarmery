package improve

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// validOut is the minimal well-formed agent answer the generation and API
// tests feed the mock runner: a FORM line, one lensed hunk, a rationale.
const validOut = "## Form\n" +
	"always-on — the evidence shows a missing guardrail.\n" +
	"## Diff\n" +
	"```diff\n" +
	"--- a/agents/tech-lead.md\n" +
	"+++ b/agents/tech-lead.md\n" +
	"@@ -10,3 +10,4 @@ lens: procedure\n" +
	" existing line\n" +
	"+new guardrail line\n" +
	"```\n" +
	"## Rationale\n" +
	"Adds a guardrail because the evidence shows repeated exit-code failures.\n"

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "parse", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The four D8 fixtures: good, missing lens, missing FORM, KEEP_BASE with and
// without a reason.
func TestParseOutputGood(t *testing.T) {
	p, err := parseOutput(fixture(t, "good.txt"))
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	if p.Form != FormAlwaysOn || p.KeepBase {
		t.Errorf("form = %q keepBase = %v", p.Form, p.KeepBase)
	}
	if p.Lens != LensMixed {
		t.Errorf("lens = %q, want mixed (one procedure + one knowledge hunk)", p.Lens)
	}
	if !strings.HasPrefix(p.Diff, "--- a/plugins/core/agents/tech-lead.md") || strings.Contains(p.Diff, "```") {
		t.Errorf("diff = %q, want the fence body", p.Diff)
	}
	if !strings.Contains(p.Rationale, "stale path") || strings.Contains(p.Rationale, "Follow-ups") {
		t.Errorf("rationale = %q, want it cut at ## Follow-ups", p.Rationale)
	}
	want := []Followup{
		{Form: FormScript, Text: "a bin/ helper that runs the exit-code check deterministically"},
		{Form: FormHook, Text: `block "done" while the last command failed`},
		{Form: "", Text: "keep an eye on it"},
	}
	if !reflect.DeepEqual(p.Followups, want) {
		t.Errorf("followups = %+v, want %+v", p.Followups, want)
	}
}

func TestParseOutputMissingLens(t *testing.T) {
	_, err := parseOutput(fixture(t, "missing_lens.txt"))
	if err == nil || !strings.Contains(err.Error(), "hunk 2 has no lens") {
		t.Fatalf("err = %v, want hunk 2 has no lens", err)
	}
}

func TestParseOutputMissingForm(t *testing.T) {
	_, err := parseOutput(fixture(t, "missing_form.txt"))
	if err == nil || !strings.Contains(err.Error(), `missing "## Form"`) {
		t.Fatalf("err = %v, want missing ## Form", err)
	}
}

func TestParseOutputKeepBaseWithReason(t *testing.T) {
	p, err := parseOutput(fixture(t, "keep_base_reason.txt"))
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	if !p.KeepBase || p.Form != FormKeepBase || p.Diff != "" || p.Lens != "" {
		t.Errorf("keep base parsed as %+v", p)
	}
	if !strings.Contains(p.Rationale, "line 12 already requires") {
		t.Errorf("reason = %q", p.Rationale)
	}
	if len(p.Followups) != 1 || p.Followups[0].Form != FormScript {
		t.Errorf("followups = %+v", p.Followups)
	}
}

func TestParseOutputKeepBaseWithoutReason(t *testing.T) {
	_, err := parseOutput(fixture(t, "keep_base_no_reason.txt"))
	if err == nil || !strings.Contains(err.Error(), "KEEP_BASE without a reason") {
		t.Fatalf("err = %v, want KEEP_BASE without a reason", err)
	}
	// No Rationale section at all is the same failure.
	_, err = parseOutput("## Form\nKEEP_BASE — nothing to do\n")
	if err == nil || !strings.Contains(err.Error(), "KEEP_BASE without a reason") {
		t.Fatalf("no rationale section: err = %v", err)
	}
}

func TestParseOutputSingleLensAndFormVariants(t *testing.T) {
	p, err := parseOutput(validOut)
	if err != nil {
		t.Fatalf("parseOutput: %v", err)
	}
	if p.Lens != LensProcedure || p.Form != FormAlwaysOn {
		t.Errorf("lens/form = %q/%q", p.Lens, p.Form)
	}
	for in, want := range map[string]string{
		"skill":                      FormSkill,
		"`script` — a bin helper":    FormScript,
		"**Hook** — must not skip":   FormHook,
		"subagent":                   FormSubagent,
		"keep base — already there":  FormKeepBase,
		"Keep_Base":                  FormKeepBase,
		"ALWAYS-ON — last rung only": FormAlwaysOn,
	} {
		if got, err := parseForm(in); err != nil || got != want {
			t.Errorf("parseForm(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"rewrite — everything", "", "   "} {
		if _, err := parseForm(bad); err == nil {
			t.Errorf("parseForm(%q) accepted", bad)
		}
	}
}

func TestParseOutputKeepBaseWithDiffRejected(t *testing.T) {
	out := "## Form\nKEEP_BASE — fine\n## Diff\n```diff\n@@ -1 +1 @@ lens: procedure\n-a\n+b\n```\n## Rationale\nwhy\n"
	if _, err := parseOutput(out); err == nil || !strings.Contains(err.Error(), "non-empty diff") {
		t.Fatalf("err = %v, want KEEP_BASE with a non-empty diff", err)
	}
	// An empty diff block under KEEP_BASE is tolerated.
	out = "## Form\nKEEP_BASE — fine\n## Diff\n```diff\n```\n## Rationale\nwhy\n"
	if p, err := parseOutput(out); err != nil || !p.KeepBase {
		t.Fatalf("empty diff under KEEP_BASE: %+v %v", p, err)
	}
}

func TestParseOutputNoHunkHeader(t *testing.T) {
	out := "## Form\nalways-on — x\n## Diff\n```diff\n--- a/x\n+++ b/x\n-a\n+b\n```\n## Rationale\nwhy\n"
	if _, err := parseOutput(out); err == nil || !strings.Contains(err.Error(), "no @@ hunk") {
		t.Fatalf("err = %v, want no @@ hunk header", err)
	}
	out = "## Form\nalways-on — x\n## Diff\n```diff\n@@ broken @@ lens: procedure\n-a\n```\n## Rationale\nwhy\n"
	if _, err := parseOutput(out); err == nil || !strings.Contains(err.Error(), "malformed hunk header") {
		t.Fatalf("err = %v, want malformed hunk header", err)
	}
}

// Leading model chatter before the first heading is tolerated.
func TestParseOutputLeadingProse(t *testing.T) {
	if _, err := parseOutput("Here is my minimal change.\n\n" + validOut); err != nil {
		t.Fatalf("leading prose should be tolerated: %v", err)
	}
}

func TestParseOutputMissingRationale(t *testing.T) {
	out := "## Form\nalways-on — x\n## Diff\n```diff\n@@ -1 +1 @@ lens: procedure\n-a\n+b\n```\n"
	if _, err := parseOutput(out); err == nil {
		t.Fatal("missing '## Rationale' section must be an error")
	}
}

func TestParseOutputMissingDiffHeading(t *testing.T) {
	out := "## Form\nalways-on — x\n```diff\n@@ -1 +1 @@ lens: procedure\n```\n## Rationale\nwhy\n"
	if _, err := parseOutput(out); err == nil || !strings.Contains(err.Error(), `missing "## Diff"`) {
		t.Fatalf("err = %v, want missing ## Diff", err)
	}
}

// A raw diff in the Diff section without a ```diff fence violates the
// contract — errors, never a silent pass-through.
func TestParseOutputDiffOutsideFence(t *testing.T) {
	out := "## Form\nalways-on — x\n## Diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@ lens: procedure\n-a\n+b\n## Rationale\nwhy\n"
	if _, err := parseOutput(out); err == nil {
		t.Fatal("diff outside a ```diff fence must be an error")
	}
}

func TestParseOutputTwoFences(t *testing.T) {
	out := "## Form\nalways-on — x\n## Diff\n```diff\n-a\n+b\n```\n```diff\n-c\n+d\n```\n## Rationale\nwhy\n"
	if _, err := parseOutput(out); err == nil {
		t.Fatal("two fenced blocks in the Diff section must be an error")
	}
}

// An empty diff block under a ladder form is still the no-change sentinel.
func TestParseOutputEmptyDiff(t *testing.T) {
	out := "## Form\nalways-on — x\n## Diff\n```diff\n```\n## Rationale\nThe evidence does not justify any change.\n"
	if _, err := parseOutput(out); !errors.Is(err, ErrNoChange) {
		t.Fatalf("err = %v, want ErrNoChange", err)
	}
	out = "## Form\nalways-on — x\n## Diff\n```diff\n\n   \n```\n## Rationale\nNothing to fix.\n"
	if _, err := parseOutput(out); !errors.Is(err, ErrNoChange) {
		t.Fatalf("whitespace-only diff: err = %v, want ErrNoChange", err)
	}
}

func TestParseOutputEmptyRationale(t *testing.T) {
	out := "## Form\nalways-on — x\n## Diff\n```diff\n@@ -1 +1 @@ lens: procedure\n-a\n+b\n```\n## Rationale\n   \n"
	if _, err := parseOutput(out); err == nil {
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

func TestHeadingOf(t *testing.T) {
	for in, want := range map[string]string{
		"## Form":                  hForm,
		"## form\n":                hForm,
		"## Follow-ups (optional)": hFollowups,
		"  ## Rationale  ":         hRationale,
		"## Diffusion":             "",
		"### Diff":                 "",
		" existing ## Diff":        "",
	} {
		if got := headingOf(in); got != want {
			t.Errorf("headingOf(%q) = %q, want %q", in, got, want)
		}
	}
}
