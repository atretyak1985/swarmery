package wsingest

import (
	"strings"
	"testing"
)

func TestParseDocBranch(t *testing.T) {
	// header builds a phase doc whose 4th line is the given header line.
	header := func(line string) string {
		return "# Phase 5 — Planner queue\nStatus: Done\n**Covers:** SC-7\n" + line + "\n**Verify:** strict\n\n## Goal\nship it\n"
	}
	tooFar := "# Phase 1\n" + strings.Repeat("filler\n", docStatusHeaderLines) + "**Branch:** `feat/late`\n"

	cases := []struct {
		name string
		text string
		want string
	}{
		{"backticked, inside the repo line",
			header("**Repo:** `/Volumes/Work/app` · **Branch:** `feat/tutor-5-planner` · **Depends on:** 1, 10"),
			"feat/tutor-5-planner"},
		{"bare, without backticks",
			header("**Repo:** `/abs/app` · **Branch:** feat/x · **Depends on:** —"),
			"feat/x"},
		{"on its own line, end of line", header("**Branch:** `main`"), "main"},
		{"absent", header("**Repo:** `/abs/app` · **Depends on:** —"), ""},
		{"only in the body, after the first section",
			"# Phase 3\nStatus: Pending\n\n## Prompt\n**Branch:** `feat/someone-else`\n", ""},
		{"Cyrillic prose is not a branch",
			header("**Repo:** `/abs/app` · **Branch:** призначає демон (`swarm/phase-<id>`); PR-гілка — на розсуд оператора"),
			""},
		{"a <id> placeholder is not a ref",
			header("**Branch:** `swarm/phase-<id>`"), ""},
		{"beyond the header line budget", tooFar, ""},
		{"digits only is not a branch", header("**Branch:** `1234`"), ""},
		{"dotted and underscored ref", header("**Branch:** `release/v1.2_rc`"), "release/v1.2_rc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseDocBranch(c.text); got != c.want {
				t.Errorf("ParseDocBranch = %q, want %q", got, c.want)
			}
		})
	}
}
