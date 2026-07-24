package verify

import (
	"strings"
	"testing"
)

func TestBuildPrompt_ContainsContract(t *testing.T) {
	p := BuildPrompt("Add waypoint editing", "Criteria:\n- editable list", "swarm/T-abc")
	for _, want := range []string{
		"read-only verification agent",
		"Add waypoint editing",
		"editable list",
		"READ ONLY",
		"INCONCLUSIVE — not FAIL",
		"VERDICT: PASS | FAIL | INCONCLUSIVE",
		"swarm/T-abc", // startPoint interpolated into the diff instruction
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, p)
		}
	}
}

func TestBuildPrompt_EmptyStartPointFallback(t *testing.T) {
	p := BuildPrompt("t", "c", "")
	if !strings.Contains(p, "the base branch") {
		t.Errorf("empty startPoint should fall back to a neutral phrase; got:\n%s", p)
	}
}
