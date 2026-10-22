package planning

import (
	"strings"
	"testing"
)

// The planner's half of the blast-radius contract (resources/blast-radius.md):
// before a phase doc's `## Risks`, find who calls what the phase changes and
// write one `Blast radius:` line. A headless planner cannot type /impact, so the
// prompt carries the chain as shell — every link of it, the Serena step, the cap,
// the line format and the fallback line the dashboard lint accepts.
var blastRadiusMust = []string{
	"## Risks",
	"resources/blast-radius.md",
	"graft callers",
	"graft/.graph/wiring.json",
	"graphify query",
	"graphify-out/graph.json",
	"rg -n --no-heading -w",
	"find_referencing_symbols",
	"lsp-pack",
	"Never build or refresh an index",
	"at most 20 callers",
	"Blast radius: <N> callers in <pkgs> — <file:line, …>",
	"Blast radius: not computed — <reason>",
}

func TestPlanPromptBlastRadiusStep(t *testing.T) {
	got := BuildPrompt("add line items to orders", "/ws")
	for _, must := range blastRadiusMust {
		if !strings.Contains(got, must) {
			t.Errorf("plan prompt lacks %q", must)
		}
	}
	// Once, inside PHASE B: the interview must not start computing callers.
	if n := strings.Count(got, blastRadiusStep); n != 1 {
		t.Errorf("blast-radius step appears %d times, want 1", n)
	}
	if strings.Index(got, blastRadiusStep) < strings.Index(got, "PHASE B — PLAN (") {
		t.Error("blast-radius step sits before PHASE B")
	}
}

func TestRevisePromptBlastRadiusStep(t *testing.T) {
	got := BuildRevisePrompt(ReviseInput{
		Reason: "scope changed", PlanDir: "/p/plan", ScratchDir: "/p/scratch", PlanTitle: "Epic",
	})
	if n := strings.Count(got, blastRadiusStep); n != 1 {
		t.Errorf("blast-radius step appears %d times in the revise prompt, want 1", n)
	}
	if !strings.Contains(got, "A phase doc you CREATE gets the blast-radius step too") {
		t.Error("the revise prompt does not say a NEW phase doc needs its blast radius")
	}
}
