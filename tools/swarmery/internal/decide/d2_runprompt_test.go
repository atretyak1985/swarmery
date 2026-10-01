package decide_test

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// D2 recognises a plan engine's run by the prompt the engine sends. The engines
// import internal/decide, so their prompt text cannot be imported there; this
// holds the two spellings together: a reworded prompt fails here instead of
// silently turning every unlinked run back into an ordinary session.
func TestRunPromptHeadsMatchEngines(t *testing.T) {
	phaseDoc := "# Phase 3 — Cache\n\nStatus: Pending\n\n## Goal\n\nAdd the read-through\ncache.\n\n## Files to Modify\n\n- cache.go\n"
	readme := "# Cache plan\n\n## Objective\n\nShip the cache.\n\n## Risks\n\nNone.\n"
	budget := runcore.Budget{}

	cases := []struct {
		name, prompt, kind, title, goal string
	}{
		{"phase run", phaserun.BuildPrompt("plan/phase-3-cache.md", "plan/phase-3-cache.md", phaseDoc),
			"phase", "Phase 3 — Cache", "Add the read-through cache."},
		{"phase run in a multi-repo project", phaserun.BuildPromptIn("plan/phase-3-cache.md", "plan/phase-3-cache.md", phaseDoc,
			"/work/umbrella/app", "/work/umbrella", "/work/umbrella/app/.wt/run", budget),
			"phase", "Phase 3 — Cache", "Add the read-through cache."},
		{"plan run", planrun.BuildPrompt("/ws/plan", readme, nil, planrun.ValidMode("auto")),
			"plan", "Cache plan", "Ship the cache."},
		{"plan run in a multi-repo project", planrun.BuildPromptIn("/ws/plan", readme, nil, planrun.ValidMode("inline"),
			"/work/umbrella/app", "/work/umbrella", "/work/umbrella/app/.wt/run", budget),
			"plan", "Cache plan", "Ship the cache."},
		// A stacked run carries one more note between the contract and the
		// document; it must neither move the head nor hide the marker line.
		// Last in the list: the head check below reads cases[0] and cases[2].
		{"phase run stacked on a dependency branch", phaserun.BuildPromptStacked("plan/phase-3-cache.md", "plan/phase-3-cache.md", phaseDoc,
			"/work/umbrella/app", "/work/umbrella", "/work/umbrella/app/.wt/run", "swarm/phase-2-store", budget),
			"phase", "Phase 3 — Cache", "Add the read-through cache."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, doc := decide.EnginePromptForTest(tc.prompt)
			if kind != tc.kind {
				t.Fatalf("kind = %q, want %q — the engine's prompt no longer opens with the head D2 knows:\n%.200s", kind, tc.kind, tc.prompt)
			}
			if doc == "" {
				t.Fatalf("the embedded document was not found — the marker line changed:\n%s", tc.prompt)
			}
			if got := decide.TitleOfForTest(doc); got != tc.title {
				t.Errorf("title = %q, want %q", got, tc.title)
			}
			if got := decide.GoalOfForTest(doc); got != tc.goal {
				t.Errorf("goal = %q, want %q", got, tc.goal)
			}
		})
	}
	if !strings.HasPrefix(cases[0].prompt, decide.PhaseRunPromptHead) || !strings.HasPrefix(cases[2].prompt, decide.PlanRunPromptHead) {
		t.Error("the exported heads are not the prompts' opening words")
	}
	// Anything else is no engine prompt.
	for _, text := range []string{"", "fix the parser", "Please explain: " + decide.PhaseRunPromptHead} {
		if kind, doc := decide.EnginePromptForTest(text); kind != "" || doc != "" {
			t.Errorf("EnginePrompt(%q) = %q, %q; want none", text, kind, doc)
		}
	}
}
