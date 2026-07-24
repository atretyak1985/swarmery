package dispatch

import (
	"strings"
	"testing"
)

func TestBuildPromptContainsContractVerbatim(t *testing.T) {
	got := BuildPrompt("Do the thing.", "swarm/T-abc123", "T-abc123", []string{"src/api", "web/src"})

	// The task body leads.
	if !strings.HasPrefix(got, "Do the thing.\n\n") {
		t.Errorf("prompt should start with the task body + blank line; got:\n%s", got)
	}
	// Contract markers + normative lines present verbatim.
	mustContain := []string{
		"--- EXECUTION CONTRACT (swarmery dispatcher) ---",
		"You are running unattended inside a dedicated git worktree on branch swarm/T-abc123. Work ONLY here.",
		"Swarm-Task-Id: T-abc123",
		"Stay within this file scope if declared: src/api, web/src.",
		"End your reply with: PREMISE STALE: <evidence>.",
		"If no changes are needed, end with: NO-OP: <reason>.",
		"end with: BLOCKED: <reason>.",
		"Do not push, do not create PRs, do not switch branches.",
		"--- END CONTRACT ---",
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("prompt missing contract line:\n%q\nfull prompt:\n%s", s, got)
		}
	}
}

func TestBuildPromptEmptyScopeText(t *testing.T) {
	got := BuildPrompt("body", "swarm/T-x", "T-x", nil)
	if !strings.Contains(got, "(none declared — the whole worktree)") {
		t.Errorf("empty scope should render explicit none-declared text; got:\n%s", got)
	}
}

func TestClassifySentinel(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantKind string
	}{
		{"none", "I made the change and committed it.", ""},
		{"premise stale", "PREMISE STALE: HEAD already has the guard at auth.go:42", "done"},
		{"noop hyphen form", "NO-OP: nothing to change here", "done"},
		{"noop no hyphen", "NOOP: already satisfied", "done"},
		{"duplicate", "DUPLICATE: T-999888", "done"},
		{"redundant", "REDUNDANT: covered by the other task", "done"},
		{"blocked", "BLOCKED: need a schema change outside my file scope", "blocked"},
		{"sentinel after prose on last line", "Looked into it.\n\nPREMISE STALE: nothing to do", "done"},
		{"markdown-wrapped bold blocked", "**BLOCKED:** waiting on API", "blocked"},
		{"case insensitive", "premise stale: lower case still counts", "done"},
		{"blocked wins when last", "NO-OP: x\nthen realized\nBLOCKED: actually stuck", "blocked"},
		{"last done wins over earlier", "DUPLICATE: T-1\nactually\nNO-OP: clean", "done"},
		{"not a leading sentinel", "This is NOT a NO-OP situation, I did work.", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifySentinel(tc.text)
			if got.Kind != tc.wantKind {
				t.Errorf("ClassifySentinel(%q).Kind = %q, want %q (line=%q)", tc.text, got.Kind, tc.wantKind, got.Line)
			}
			if tc.wantKind != "" && got.Line == "" {
				t.Errorf("ClassifySentinel(%q) matched but returned empty Line", tc.text)
			}
		})
	}
}
