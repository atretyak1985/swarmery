package verify

import (
	"strings"
	"testing"
)

func TestBuildPrompt_ContainsContract(t *testing.T) {
	p := BuildPrompt("Add waypoint editing", "Criteria:\n- editable list", "swarm/T-abc", StrictnessNormal)
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

// The daemon-API rule names the port it is given (BuildPrompt: the default), and
// it sits among the rules, above the verdict line that must stay last.
func TestBuildPrompt_DaemonAPIRule(t *testing.T) {
	def := BuildPrompt("t", "c", "main", StrictnessNormal)
	if !strings.Contains(def, "- "+DaemonAPINotice(DefaultDaemonPort)+"\n") {
		t.Errorf("default prompt lacks the daemon-API rule for :%d:\n%s", DefaultDaemonPort, def)
	}
	p := BuildPromptForPort("t", "c", "main", StrictnessStrict, 8080)
	for _, want := range []string{"127.0.0.1:8080", "localhost:8080", "curl, wget, http"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt for :8080 missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, ":7777") {
		t.Errorf("prompt for :8080 still names the default port:\n%s", p)
	}
	if !strings.HasSuffix(p, "VERDICT: PASS | FAIL | INCONCLUSIVE") {
		t.Errorf("the verdict line is no longer last:\n%s", p)
	}
	if DaemonAPINotice(0) != DaemonAPINotice(DefaultDaemonPort) {
		t.Errorf("DaemonAPINotice(0) = %q, want the default port's", DaemonAPINotice(0))
	}
}

func TestBuildPrompt_EmptyStartPointFallback(t *testing.T) {
	p := BuildPrompt("t", "c", "", StrictnessNormal)
	if !strings.Contains(p, "the base branch") {
		t.Errorf("empty startPoint should fall back to a neutral phrase; got:\n%s", p)
	}
}

// The verify knob (fusion phase 13) moves ONLY the bar: normal omits the strict
// clause, strict injects it, and the fixed verdict vocabulary is present in both.
func TestBuildPrompt_StrictnessKnob(t *testing.T) {
	normal := BuildPrompt("t", "c", "main", StrictnessNormal)
	strict := BuildPrompt("t", "c", "main", StrictnessStrict)

	if strings.Contains(normal, "STRICT REVIEW") {
		t.Errorf("normal prompt should NOT contain the strict clause:\n%s", normal)
	}
	if !strings.Contains(strict, "STRICT REVIEW") {
		t.Errorf("strict prompt should contain the strict clause:\n%s", strict)
	}
	// The verdict contract is invariant across the knob (only the BAR moves).
	for _, p := range []string{normal, strict} {
		if !strings.Contains(p, "VERDICT: PASS | FAIL | INCONCLUSIVE") {
			t.Errorf("verdict line missing regardless of strictness:\n%s", p)
		}
		if !strings.Contains(p, "READ ONLY") {
			t.Errorf("read-only contract missing regardless of strictness:\n%s", p)
		}
	}
}
