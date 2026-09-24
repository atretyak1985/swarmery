// An EXTERNAL test package: internal/lessons reaches internal/phaserun through
// surprise → actuals, so an in-package test importing lessons is a cycle.
package phaserun_test

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/textdiff"
)

// Golden prompts for lesson injection (learning-loop phase 15.6).
//
// phase_prompt.golden is the phase-run prompt with NO lessons. It must never
// change because of injection: zero lessons ⇒ the prompt is byte-identical to
// the prompt before injection existed. -update does not rewrite it once it
// exists; a deliberate change to the prompt itself deletes it and reruns.
//
// phase_prompt_lessons.golden is the same prompt with two lessons appended.
// Regenerate with `go test ./internal/phaserun -run TestPhasePromptGolden -update`.
var updateLessonsGolden = flag.Bool("update", false, "rewrite the recorded phase prompt with lessons")

const (
	goldenPhasePrompt        = "testdata/phase_prompt.golden"
	goldenPhasePromptLessons = "testdata/phase_prompt_lessons.golden"
)

const goldenDoc = "# Phase 1 — Line items\n\n## Steps\n\n- [ ] **1.1** Add the table.\n\n## Completion Report\n"

func checkGolden(t *testing.T, file, got string, update bool) {
	t.Helper()
	if update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s (%d bytes)", file, len(got))
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read golden: %v — regenerate with -update", err)
	}
	if string(want) != got {
		t.Fatalf("prompt changed (%d bytes, golden has %d):\n%s", len(got), len(want),
			textdiff.UnifiedDiff(file, "BuildPrompt()", string(want), got))
	}
}

func TestPhasePromptGolden(t *testing.T) {
	base := phaserun.BuildPrompt("plan/phase-1-line-items.md", "phase-1-line-items.md", goldenDoc)

	_, statErr := os.Stat(goldenPhasePrompt)
	checkGolden(t, goldenPhasePrompt, base+lessons.Render(nil), *updateLessonsGolden && os.IsNotExist(statErr))

	with := base + lessons.Render([]lessons.Active{
		{ID: 12, Guidance: "Read the schema before writing the migration."},
		{ID: 31, Guidance: "Index every child column a prune deletes through."},
	})
	checkGolden(t, goldenPhasePromptLessons, with, *updateLessonsGolden)
	if !strings.HasPrefix(with, base) {
		t.Error("the lessons block must be appended after the unchanged prompt")
	}
	if !strings.Contains(with, "[L-12]") || !strings.Contains(with, "[L-31]") {
		t.Error("the golden with lessons misses a lesson id")
	}
}
