package improve

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestMain sets HOME to a fresh temp dir for the whole package and then hard
// fails the run if any test in this package actually reached a real claude
// binary — however it was found (PATH, claudebin's machine-wide
// systemProbeDirs, or a home-relative candidate like ~/.local/bin/claude). A
// real `claude` subprocess inherits this HOME (ClaudeRunner.Run sets cmd.Env
// from os.Environ() either directly or via claudeacct.SpawnEnvResolved, both
// of which carry HOME through), so it would write its transcript under
// <dir>/.claude/projects — a tree nothing else in this suite ever touches.
// Comparing a before/after count of *.jsonl files under that tree turns "we
// reduced the odds of a leak" into "we catch a leak if one happens."
//
// This does NOT close any resolution path by itself — a test can still reach
// a real binary via PATH or claudebin's systemProbeDirs — it only guarantees
// that if one does, the run fails instead of silently writing real account
// transcripts that can trip the account breaker (the bug this guard exists
// for).
//
// runner_account_test.go's own t.Setenv("HOME", …) calls still work unchanged:
// they override/restore HOME per-test, composing fine with this baseline.
//
// os.Exit(code) below bypasses the deferred os.RemoveAll(dir) — pre-existing
// behavior, not a new regression: the temp dir is leaked to the OS tmp root
// on every run, same as before this change.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "improve-test-home-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	os.Setenv("HOME", dir)
	os.Unsetenv("SWARMERY_CLAUDE_BIN")

	before := countTranscripts(dir)
	code := m.Run()
	after := countTranscripts(dir)
	if after > before {
		fmt.Fprintf(os.Stderr,
			"guard: a real claude process wrote %d transcript file(s) under the test HOME (%s) — "+
				"some test in this package reached a real claude binary instead of a stub; "+
				"stub resolveClaudeBin or PATH explicitly in that test\n", after-before, dir)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// countTranscripts counts *.jsonl files under home's ~/.claude/projects tree
// (recursively) — zero, and an error from a nonexistent tree, both count as 0.
func countTranscripts(home string) int {
	root := filepath.Join(home, ".claude", "projects")
	n := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(path) == ".jsonl" {
			n++
		}
		return nil
	})
	return n
}
