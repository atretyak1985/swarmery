package improve

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updatePromptGolden = flag.Bool("update", false, "rewrite the recorded improve prompts")

// TestPromptGolden pins both rendered prompts byte-for-byte: any wording
// change to the FORM ladder, the cut-to-pay rule, KEEP_BASE or the per-hunk
// lens contract shows up as a reviewed golden diff
// (go test ./internal/improve -run TestPromptGolden -update).
func TestPromptGolden(t *testing.T) {
	cases := []struct {
		golden string
		render func() string
	}{
		{"prompt_agent.golden", func() string {
			return renderPrompt("plugins/core/agents/tech-lead.md",
				"---\nname: tech-lead\ndescription: d\n---\nbody", "## Scorecard\nruns: 3")
		}},
		{"prompt_skill.golden", func() string {
			return renderSkillPrompt("plugins/core/skills/testing/SKILL.md",
				"---\nname: testing\ndescription: d\n---\nsteps", "## Recurring lesson\nidentity: x")
		}},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			got := c.render()
			path := filepath.Join("testdata", c.golden)
			if *updatePromptGolden {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("prompt drifted from %s — re-run with -update and review the diff", path)
			}
		})
	}
}

// TestPromptsCarryFormContract: both prompts state the form ladder in order,
// the cut-to-pay rule, KEEP_BASE, the per-hunk lens, follow-ups — and keep the
// "MINIMAL change" wording.
func TestPromptsCarryFormContract(t *testing.T) {
	for name, p := range map[string]string{
		"agent": renderPrompt("plugins/core/agents/a.md", "c", "b"),
		"skill": renderSkillPrompt("plugins/core/skills/s/SKILL.md", "c", "b"),
	} {
		for _, want := range []string{
			"Produce a MINIMAL change.",
			"# FORM",
			"Cut to pay",
			"KEEP_BASE",
			`"## Form"`,
			"lens: procedure",
			"knowledge = what is true",
			`"## Follow-ups"`,
			"under the owning plugin's bin/",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt missing %q", name, want)
			}
		}
		// The ladder order is the contract: skill → script → subagent → hook → always-on.
		last := -1
		for _, rung := range []string{"1. skill", "2. script", "3. subagent", "4. hook", "5. always-on"} {
			i := strings.Index(p, rung)
			if i <= last {
				t.Errorf("%s prompt: ladder rung %q out of order", name, rung)
			}
			last = i
		}
		if strings.Contains(p, "{form}") || strings.Contains(p, "{output}") || strings.Contains(p, "{path}") {
			t.Errorf("%s prompt has an unfilled placeholder", name)
		}
	}
}
