package repopath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorktreeRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt1"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(t *testing.T, dir, body string) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	abs := write(t, filepath.Join(t.TempDir(), "abs"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt1")+"\n")
	if got, ok := WorktreeRepo(abs); !ok || !SameDir(got, repo) {
		t.Errorf("absolute gitdir: got (%q, %v), want %q", got, ok, repo)
	}

	// git's worktree.useRelativePaths writes the gitdir relative to the worktree.
	relWt := filepath.Join(repo, "..", "rel-wt")
	rel, err := filepath.Rel(relWt, filepath.Join(repo, ".git", "worktrees", "wt1"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, relWt, "gitdir: "+rel+"\n")
	if got, ok := WorktreeRepo(relWt); !ok || !SameDir(got, repo) {
		t.Errorf("relative gitdir: got (%q, %v), want %q", got, ok, repo)
	}

	for name, dir := range map[string]string{
		"main checkout (.git is a dir)": repo,
		"no .git":                       t.TempDir(),
		"not a gitdir line":             write(t, filepath.Join(t.TempDir(), "junk"), "hello\n"),
		"gitdir outside .git/worktrees": write(t, filepath.Join(t.TempDir(), "odd"), "gitdir: "+filepath.Join(repo, ".git")+"\n"),
		"empty path":                    "",
	} {
		if got, ok := WorktreeRepo(dir); ok {
			t.Errorf("%s: got (%q, true), want not ok", name, got)
		}
	}
}
