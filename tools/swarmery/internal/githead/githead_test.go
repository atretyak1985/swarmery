package githead_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/githead"
)

const fakesha = "aabbccddee112233445566778899001122334455"
const fakesha2 = "1122334455aabbccddee112233445566778899ff"

// makeGitDir creates a real .git directory at root and returns (root, gitDir).
func makeGitDir(t *testing.T) (root, gitDir string) {
	t.Helper()
	root = t.TempDir()
	gitDir = filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, gitDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	t.Run("loose ref", func(t *testing.T) {
		root, gitDir := makeGitDir(t)
		writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
		writeFile(t, filepath.Join(gitDir, "refs", "heads", "main"), fakesha+"\n")

		got, ok := githead.Resolve(root)
		if !ok {
			t.Fatal("Resolve returned ok=false, want true")
		}
		if got != fakesha {
			t.Errorf("sha = %q, want %q", got, fakesha)
		}
	})

	t.Run("packed ref", func(t *testing.T) {
		root, gitDir := makeGitDir(t)
		writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
		// No loose ref file — only packed-refs.
		packedContent := "# pack-refs with: peeled fully-peeled sorted\n" +
			fakesha + " refs/heads/main\n" +
			fakesha2 + " refs/heads/other\n"
		writeFile(t, filepath.Join(gitDir, "packed-refs"), packedContent)

		got, ok := githead.Resolve(root)
		if !ok {
			t.Fatal("Resolve returned ok=false, want true")
		}
		if got != fakesha {
			t.Errorf("sha = %q, want %q", got, fakesha)
		}
	})

	t.Run("detached HEAD", func(t *testing.T) {
		root, gitDir := makeGitDir(t)
		writeFile(t, filepath.Join(gitDir, "HEAD"), fakesha+"\n")

		got, ok := githead.Resolve(root)
		if !ok {
			t.Fatal("Resolve returned ok=false, want true")
		}
		if got != fakesha {
			t.Errorf("sha = %q, want %q", got, fakesha)
		}
	})

	t.Run("worktree gitdir pointer file", func(t *testing.T) {
		// The "real" bare .git directory.
		realRoot, realGitDir := makeGitDir(t)
		writeFile(t, filepath.Join(realGitDir, "HEAD"), "ref: refs/heads/main\n")
		writeFile(t, filepath.Join(realGitDir, "refs", "heads", "main"), fakesha+"\n")

		// A worktree whose .git is a file pointing to realGitDir.
		worktreeRoot := t.TempDir()
		writeFile(t, filepath.Join(worktreeRoot, ".git"), "gitdir: "+realGitDir+"\n")
		_ = realRoot // suppress unused warning

		got, ok := githead.Resolve(worktreeRoot)
		if !ok {
			t.Fatal("Resolve returned ok=false, want true")
		}
		if got != fakesha {
			t.Errorf("sha = %q, want %q", got, fakesha)
		}
	})

	t.Run("no .git", func(t *testing.T) {
		root := t.TempDir()
		got, ok := githead.Resolve(root)
		if ok {
			t.Errorf("Resolve returned ok=true for dir without .git, got sha=%q", got)
		}
	})

	t.Run("malformed HEAD", func(t *testing.T) {
		root, gitDir := makeGitDir(t)
		// Not a sha and not "ref: " prefix.
		writeFile(t, filepath.Join(gitDir, "HEAD"), "not-a-valid-head\n")

		got, ok := githead.Resolve(root)
		if ok {
			t.Errorf("Resolve returned ok=true for malformed HEAD, got sha=%q", got)
		}
	})
}
