package phaserun

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/textdiff"
)

// The stop-trigger prompt (verify-not-trust phase 10). Regenerate the golden with
// `go test ./internal/phaserun -run TestPromptStopGolden -update-stop-golden`.
var updateStopGolden = flag.Bool("update-stop-golden", false, "rewrite testdata/phase_prompt_stop.golden")

const goldenStopPrompt = "testdata/phase_prompt_stop.golden"

// stopGuardedDoc carries the planner's default stop_if list.
const stopGuardedDoc = "# Phase 3 — Export\nStatus: Pending\n**Depends on decisions:** D-2\n\n## Acceptance Criteria\n\n- [ ] the export lands\n\n## Guard\n\n" +
	"```yaml\nkind: guard\nprotected_paths:\n  - \"**/*_test.go\"\nstop_if:\n  - out-of-scope change\n  - protected path\n" +
	"  - access or login error\n  - criterion impossible\n```\n\n## Completion Report\n"

var stopPromptDecisions = []PromptDecision{
	{ID: "D-4", Question: "the criterion needs scripts/tests/run.sh", Answer: "allow: Allow editing scripts/tests/run.sh", Trigger: "protected-path"},
	{ID: "D-2", Question: "Which cache backs the export?", Answer: "in-memory", Recommended: true},
}

func TestPromptStopGolden(t *testing.T) {
	got := buildPromptDecided("plan/phase-3-export.md", "phase-3-export.md", stopGuardedDoc, "", "", "", "", "", stopPromptDecisions, runcore.Budget{})
	if *updateStopGolden {
		if err := os.WriteFile(goldenStopPrompt, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenStopPrompt)
	if err != nil {
		t.Fatalf("read golden: %v — regenerate with -update-stop-golden", err)
	}
	if string(want) != got {
		t.Fatalf("prompt changed:\n%s", textdiff.UnifiedDiff(goldenStopPrompt, "buildPromptDecided()", string(want), got))
	}
}

// Every stop_if entry appears verbatim with its trigger id, the contract once,
// and both kinds of decision in their own wording.
func TestPromptStopContract(t *testing.T) {
	got := buildPromptDecided("plan/p.md", "p.md", stopGuardedDoc, "", "", "", "", "", stopPromptDecisions, runcore.Budget{})
	for _, want := range []string{
		"- out-of-scope change (trigger: out-of-scope)",
		"- protected path (trigger: protected-path)",
		"- access or login error (trigger: access-error)",
		"- criterion impossible (trigger: criterion-impossible)",
		"When a trigger fires, STOP: write the kind: stop block with exactly two options, then end with PHASE BLOCKED: <trigger>.",
		"- Decision D-4: allow: Allow editing scripts/tests/run.sh (stop: protected-path — the criterion needs scripts/tests/run.sh)",
		"- D-2 (recommended, no operator answer): Which cache backs the export? → in-memory",
	} {
		if strings.Count(got, want) != 1 {
			t.Errorf("prompt carries %q %d times, want once", want, strings.Count(got, want))
		}
	}
	// The contract sits before the document, so it is read before the doc's own
	// copy of the guard.
	if strings.Index(got, "STOP TRIGGERS:") > strings.Index(got, "PHASE DOCUMENT (") {
		t.Error("the stop contract comes after the phase document")
	}
}

// A doc without a guard (or with a guard that lists no trigger) and a run with
// no decisions render the prompt exactly as before phase 10.
func TestPromptStopAbsentIsByteIdentical(t *testing.T) {
	plain := "# Phase 1\n\n## Acceptance Criteria\n\n- [ ] x\n"
	if a, b := BuildPrompt("plan/p.md", "p.md", plain),
		buildPromptDecided("plan/p.md", "p.md", plain, "", "", "", "", "", nil, runcore.Budget{}); a != b {
		t.Fatal("an unguarded, undecided prompt differs from BuildPrompt")
	}
	noStop := "# Phase 1\n\n## Guard\n\n```yaml\nkind: guard\nprotected_paths: [\"a.go\"]\nstop_if: []\n```\n"
	if got := BuildPrompt("plan/p.md", "p.md", noStop); strings.Contains(got, "STOP TRIGGERS") {
		t.Error("a guard with no stop_if still renders the stop contract")
	}
}
