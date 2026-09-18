package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The accounts-pack shell function is itself named `claude` and hands the bare
// word here. With the official local install (`claude migrate-installer`) that
// word is a shell ALIAS and nothing is on PATH — execve cannot see an alias, so
// the fallback has to probe the install locations the way every daemon spawn
// does. Any other name gets the plain PATH answer, error included.
func TestResolveExecBinFallsBackToTheProbedClaudeForTheBareName(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing resolvable on PATH
	fixture := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", fixture) // claudebin.Resolve's explicit override

	if got, err := resolveExecBin("claude"); err != nil || got != fixture {
		t.Errorf("resolveExecBin(claude) = %q, %v; want the probed %q", got, err, fixture)
	}
	if _, err := resolveExecBin("definitely-not-a-command"); err == nil {
		t.Errorf("resolveExecBin(other) = nil error, want the PATH lookup failure")
	}
}
