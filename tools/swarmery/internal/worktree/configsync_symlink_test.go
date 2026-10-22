package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSyncUntrackedConfigRefusesSymlinkedClaudeDir: a worktree whose .claude is
// a symlink (a repository can commit one) receives nothing — the lent config
// must land inside the worktree, never at the symlink's target.
func TestSyncUntrackedConfigRefusesSymlinkedClaudeDir(t *testing.T) {
	root := t.TempDir()
	repo, wt, outside := filepath.Join(root, "repo"), filepath.Join(root, "wt"), filepath.Join(root, "outside")
	for _, rel := range configFilesToSync {
		mustMkdir(t, filepath.Dir(filepath.Join(repo, rel)))
		mustWrite(t, filepath.Join(repo, rel), "{}\n")
	}
	mustMkdir(t, wt)
	mustMkdir(t, outside)
	if err := os.Symlink(outside, filepath.Join(wt, ".claude")); err != nil {
		t.Fatal(err)
	}

	syncUntrackedConfig(repo, wt)

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("sync wrote %d files through the symlinked .claude; want none", len(entries))
	}
}

// TestStripLentBindingsRefusesSymlinkedClaudeDir: the rewrite of a lent
// settings.local.json never reaches through a symlinked .claude to a file
// outside the worktree.
func TestStripLentBindingsRefusesSymlinkedClaudeDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "settings.local.json")
	mustWrite(t, target, lentWithBinding)
	wt := filepath.Join(root, "proj", "phase-5")
	mustMkdir(t, wt)
	if err := os.Symlink(outside, filepath.Join(wt, ".claude")); err != nil {
		t.Fatal(err)
	}

	m := &Manager{Root: root, Git: trackGit{wt: "untracked"}}
	n, err := m.StripLentBindings()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("stripped %d files through a symlinked .claude; want 0", n)
	}
	if got := readFile(t, target); got != lentWithBinding {
		t.Errorf("the file outside the worktree was rewritten: %q", got)
	}
}
