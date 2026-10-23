package phaserun

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/textdiff"
)

var updateStopGolden = flag.Bool("update-stop-golden", false, "rewrite testdata/phase_prompt_stop.golden")

const goldenStopPrompt = "testdata/phase_prompt_stop.golden"

// stopGuardDoc is a phase doc whose guard names two stop triggers.
const stopGuardDoc = "# Phase 3 — Export retry\n\n## Acceptance Criteria\n\n- [ ] retries are bounded\n\n## Guard\n\n" +
	"```yaml\nkind: guard\nprotected_paths:\n  - \"**/*_test.go\"\nstop_if:\n  - out-of-scope change\n  - protected path\n```\n\n## Completion Report\n"

// promptDecisions are one cheap skipped interview decision and one answered
// agent stop — what a re-run after a stop sees.
func promptDecisions() (recommended, answered []store.Decision) {
	recommended = []store.Decision{{DecisionID: "D-2", Source: store.DecisionSourceInterview,
		Question: "Which backoff?", Cost: store.DecisionCostCheap, Status: store.DecisionRecommended,
		Answer: "exponential (без відповіді, рекомендований)"}}
	answered = []store.Decision{{DecisionID: "D-5", Source: store.DecisionSourceStop, Status: store.DecisionDecided,
		Question: "The retry criterion needs pkg/export/writer_test.go, which the guard protects.",
		Answer:   "allow-add-to-scope",
		GuardJSON: `{"stop":{"trigger":"protected-path","reason":"r","options":[` +
			`{"id":"allow-add-to-scope","label":"Let me edit the writer test","consequence":"c"},` +
			`{"id":"skip-test-change","label":"Leave the test alone","consequence":"c"}]}}`}}
	return recommended, answered
}

// TestPromptStopGolden: the executor prompt carries the guard's stop_if list
// verbatim, the stop-block contract, the cheap undecided decision with its
// recommended option and the operator's answer to the earlier stop.
func TestPromptStopGolden(t *testing.T) {
	got := buildPromptNoted("plan/phase-3-export.md", "phase-3-export.md", stopGuardDoc, "", "", "", "", "",
		DecisionsNote(promptDecisions()), runcore.Budget{})
	for _, want := range []string{
		"- out-of-scope change\n- protected path\n",
		"When a trigger fires, STOP: write the kind: stop block with exactly two options, then end with PHASE BLOCKED: <trigger>.",
		"`trigger` is one of out-of-scope, protected-path, access-error, criterion-impossible",
		"the id `allow-add-to-scope`",
		"- D-2 (recommended, no operator answer): Which backoff? → exponential\n",
		"- Decision D-5: allow-add-to-scope (Let me edit the writer test) — answers your earlier stop (protected-path): The retry criterion needs pkg/export/writer_test.go, which the guard protects.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// Both blocks sit ahead of the turn contract, after the contract bullets.
	if i, j := strings.Index(got, "STOP TRIGGERS"), strings.Index(got, "PHASE DOCUMENT ("); i < 0 || j < i {
		t.Errorf("stop triggers at %d, document at %d — the note belongs before the document", i, j)
	}
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
		t.Fatalf("prompt changed:\n%s", textdiff.UnifiedDiff(goldenStopPrompt, "buildPromptNoted()", string(want), got))
	}
}

// A doc without a guard (or with an empty stop_if) and a run without decisions
// render the prompt byte for byte as before stop triggers existed.
func TestPromptStopAbsentIsByteIdentical(t *testing.T) {
	plain := "# Phase 1\n\n- [ ] a\n"
	empty := "# Phase 1\n\n```yaml\nkind: guard\nprotected_paths: []\nstop_if: []\n```\n"
	for name, doc := range map[string]string{"no guard": plain, "empty stop_if": empty, "broken guard": "```yaml\nkind: guard\nstop_if: [\n```\n"} {
		if n := stopNote(doc); n != "" {
			t.Errorf("%s: stopNote = %q, want empty", name, n)
		}
		got := buildPromptNoted("d", "d", doc, "", "", "", "", "", "", runcore.Budget{})
		if strings.Contains(got, "STOP TRIGGERS") || strings.Contains(got, "DECISIONS this run builds on") {
			t.Errorf("%s: prompt carries a stop or decisions block", name)
		}
		if want := BuildPromptRun("d", "d", doc, "", "", "", "", false, runcore.Budget{}); got != want {
			t.Errorf("%s: buildPromptNoted without decisions differs from BuildPromptRun", name)
		}
	}
	if DecisionsNote(nil, nil) != "" {
		t.Error("DecisionsNote(nil, nil) is not empty")
	}
}

// A cheap decision without a recorded recommendation still tells the executor it
// is building on a default.
func TestDecisionsNoteWithoutRecommendation(t *testing.T) {
	got := DecisionsNote([]store.Decision{{DecisionID: "D-1", Question: "Name\nthe flag?", Cost: "cheap", Status: "undecided"}}, nil)
	if !strings.Contains(got, "- D-1 (recommended, no operator answer): Name the flag? → no recommended option was recorded") {
		t.Fatalf("note = %q", got)
	}
	guard := DecisionsNote(nil, []store.Decision{{DecisionID: "D-4", Source: "stop", Status: "decided", Question: "Allow or reject?", Answer: "reject", GuardJSON: "{}"}})
	if !strings.Contains(guard, "- Decision D-4: reject — answers: Allow or reject?") {
		t.Fatalf("guard decision note = %q", guard)
	}
}
