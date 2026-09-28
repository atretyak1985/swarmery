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

// The accounts-pack PATH shim lives in the shim dir, which the operator's
// profile puts FIRST on PATH. resolveExecBin must look past the shim — a
// `claude` there — or every terminal launch re-enters it. Every OTHER binary
// in that dir (the swarmery CLI itself lives there) resolves normally.
func TestResolveExecBinSkipsTheShimFirstOnPath(t *testing.T) {
	shim := t.TempDir()
	t.Setenv("SWARMERY_BIN_DIR", shim)
	t.Setenv("SWARMERY_CLAUDE_BIN", "")
	realDir := t.TempDir()
	for _, dir := range []string{shim, realDir} {
		for _, name := range []string{"claude", "swarmery"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+realDir)

	if got, err := resolveExecBin("claude"); err != nil || got != filepath.Join(realDir, "claude") {
		t.Errorf("resolveExecBin(claude) = %q, %v; want the real binary, never the shim", got, err)
	}
	// `account exec -- swarmery …`: the shim dir's swarmery is the first PATH
	// hit and is NOT the shim.
	if got, err := resolveExecBin("swarmery"); err != nil || got != filepath.Join(shim, "swarmery") {
		t.Errorf("resolveExecBin(swarmery) = %q, %v; want the shim dir's swarmery", got, err)
	}
	// An absolute path to a non-claude binary in the shim dir runs verbatim;
	// the absolute path of the shim itself does not.
	abs := filepath.Join(shim, "swarmery")
	if got, err := resolveExecBin(abs); err != nil || got != abs {
		t.Errorf("resolveExecBin(%s) = %q, %v; want it verbatim", abs, got, err)
	}
	if got, err := resolveExecBin(filepath.Join(shim, "claude")); err == nil && got == filepath.Join(shim, "claude") {
		t.Errorf("resolveExecBin(<shim dir>/claude) = %q; the shim must never be the answer", got)
	}

	// The shim alone on PATH: "claude" falls through to the probe (here the
	// explicit override); a non-claude name in the dir still resolves there.
	t.Setenv("PATH", shim)
	fixture := filepath.Join(realDir, "claude")
	t.Setenv("SWARMERY_CLAUDE_BIN", fixture)
	if got, err := resolveExecBin("claude"); err != nil || got != fixture {
		t.Errorf("resolveExecBin(claude) with only the shim on PATH = %q, %v; want the probed %q", got, err, fixture)
	}
	if got, err := resolveExecBin("swarmery"); err != nil || got != filepath.Join(shim, "swarmery") {
		t.Errorf("resolveExecBin(swarmery) with only the shim dir on PATH = %q, %v; want the shim dir's swarmery", got, err)
	}
}

// A symlink elsewhere named anything that resolves to the shim is still the shim.
func TestResolveExecBinSkipsASymlinkToTheShim(t *testing.T) {
	shim := t.TempDir()
	t.Setenv("SWARMERY_BIN_DIR", shim)
	t.Setenv("SWARMERY_CLAUDE_BIN", "")
	if err := os.WriteFile(filepath.Join(shim, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "cc")
	if err := os.Symlink(filepath.Join(shim, "claude"), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if got, err := resolveExecBin(link); err == nil {
		t.Errorf("resolveExecBin(%s) = %q; a symlink to the shim must be refused", link, got)
	}
}

func TestWithoutEnvKeyDropsEveryCopyAndCopies(t *testing.T) {
	in := []string{"A=1", launchPathEnv + "=/old", "B=2", launchPathEnv + "=/older"}
	got := withoutEnvKey(in, launchPathEnv)
	if len(got) != 2 || got[0] != "A=1" || got[1] != "B=2" {
		t.Errorf("withoutEnvKey = %v", got)
	}
	if in[1] != launchPathEnv+"=/old" {
		t.Error("withoutEnvKey mutated its input")
	}
}
