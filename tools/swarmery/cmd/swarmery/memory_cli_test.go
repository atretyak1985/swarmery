package main

// The CLI's write guard. `swarmery memory consolidate` moves a user's real,
// un-versioned memory files, so planning — not applying — has to be what a bare
// invocation does. These cases drive cmdMemoryConsolidate against a throwaway
// claude dir under t.TempDir(); nothing here touches a real memory root.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memconsolidate"
)

// consolidateFixture builds <claudeDir>/projects/<slug>/memory with one closed
// entry, and returns the claude dir, the project path and the memory dir.
func consolidateFixture(t *testing.T) (claudeDir, project, memDir string) {
	t.Helper()
	claudeDir = t.TempDir()
	project = filepath.Join(t.TempDir(), "example-project")
	memDir = memconsolidate.AutoMemoryDirIn(claudeDir, project)
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", memDir, err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(memDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("MEMORY.md", "- [Search indexer](search-indexer.md) — DONE 2026-07-27: slugs name-derived.\n")
	write("search-indexer.md", "---\nname: search-indexer\n---\n\nbody\n")
	return claudeDir, project, memDir
}

// TestMemoryConsolidateDefaultsToPlanning is the guard itself: with no --yes, a
// bare invocation must leave every file exactly where it was. The API defaults
// dry_run ON for the same reason, and this command is reachable from the /land
// ritual, so an agent can run it.
func TestMemoryConsolidateDefaultsToPlanning(t *testing.T) {
	claudeDir, project, memDir := consolidateFixture(t)

	if err := cmdMemoryConsolidate([]string{"--project", project, "--claude-dir", claudeDir}); err != nil {
		t.Fatalf("bare invocation: %v", err)
	}

	if _, err := os.Stat(filepath.Join(memDir, "search-indexer.md")); err != nil {
		t.Fatalf("a bare invocation MOVED the memory file — planning is not the default: %v", err)
	}
	if _, err := os.Stat(memconsolidate.ClosedDir(memDir)); !os.IsNotExist(err) {
		t.Fatalf("a bare invocation created %s: %v", memconsolidate.ClosedDir(memDir), err)
	}
	idx, err := os.ReadFile(memconsolidate.IndexPath(memDir))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(idx), "search-indexer.md") {
		t.Fatalf("a bare invocation rewrote the index:\n%s", idx)
	}
}

// TestMemoryConsolidateDryRunIsStillAccepted: --dry-run keeps working as the
// explicit spelling of the default (the /land ritual documents it).
func TestMemoryConsolidateDryRunIsStillAccepted(t *testing.T) {
	claudeDir, project, memDir := consolidateFixture(t)

	if err := cmdMemoryConsolidate([]string{
		"--project", project, "--claude-dir", claudeDir, "--dry-run"}); err != nil {
		t.Fatalf("--dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, "search-indexer.md")); err != nil {
		t.Fatalf("--dry-run moved the memory file: %v", err)
	}
}

// TestMemoryConsolidateRefusesContradictoryFlags: --dry-run with --yes is a
// confused instruction about an irreversible move. Refuse instead of guessing.
func TestMemoryConsolidateRefusesContradictoryFlags(t *testing.T) {
	claudeDir, project, memDir := consolidateFixture(t)

	err := cmdMemoryConsolidate([]string{
		"--project", project, "--claude-dir", claudeDir, "--dry-run", "--yes"})
	if err == nil {
		t.Fatal("--dry-run --yes was accepted; one of them silently won")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error does not explain the conflict: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, "search-indexer.md")); err != nil {
		t.Fatalf("the refused invocation still moved the file: %v", err)
	}
}

// TestMemoryConsolidateAppliesWithYes: the affirmative still works, so the guard
// has not simply broken the command.
func TestMemoryConsolidateAppliesWithYes(t *testing.T) {
	claudeDir, project, memDir := consolidateFixture(t)
	// Keep the CLI's backup snapshots inside the test's own temp tree.
	t.Setenv("HOME", t.TempDir())

	if err := cmdMemoryConsolidate([]string{
		"--project", project, "--claude-dir", claudeDir, "--yes"}); err != nil {
		t.Fatalf("--yes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, "search-indexer.md")); !os.IsNotExist(err) {
		t.Fatalf("--yes did not move the memory file: %v", err)
	}
	moved := filepath.Join(memconsolidate.ClosedDir(memDir), "search-indexer.md")
	body, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("read %s: %v", moved, err)
	}
	if !strings.Contains(string(body), "status: closed") {
		t.Errorf("the moved file was not stamped:\n%s", body)
	}
}
