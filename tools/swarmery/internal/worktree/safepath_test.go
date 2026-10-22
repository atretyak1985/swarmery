package worktree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCheckNoSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "a", "b", "file"), "x")
	mustSymlink(t, outside, filepath.Join(root, "linkdir"))
	mustSymlink(t, filepath.Join(root, "a", "b", "file"), filepath.Join(root, "a", "linkfile"))

	for rel, wantSymlink := range map[string]bool{
		"a/b/file":         false,
		"a/b/missing":      false, // a missing tail is not a symlink
		"missing/x/y":      false,
		"linkdir":          true,
		"linkdir/anything": true,
		"a/linkfile":       true,
	} {
		err := checkNoSymlink(root, rel)
		if got := errors.Is(err, ErrSymlinkInWorktree); got != wantSymlink {
			t.Errorf("checkNoSymlink(%q) = %v, want symlink refusal %v", rel, err, wantSymlink)
		}
		if !wantSymlink && err != nil {
			t.Errorf("checkNoSymlink(%q) = %v, want nil", rel, err)
		}
	}
	for _, rel := range []string{"../x", "/abs", ""} {
		if err := checkNoSymlink(root, rel); err == nil {
			t.Errorf("checkNoSymlink(%q) = nil, want a refusal for a non-local path", rel)
		}
	}
}

func TestOpenNoFollowWriteRefusesSymlinkAndHardLink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	writeFile(t, victim, "original")

	link := filepath.Join(dir, "link")
	mustSymlink(t, victim, link)
	if f, err := openNoFollowWrite(link, 0o644); err == nil {
		f.Close()
		t.Error("openNoFollowWrite opened through a symlink")
	} else if !errors.Is(err, ErrSymlinkInWorktree) {
		t.Errorf("symlink err = %v, want ErrSymlinkInWorktree", err)
	}

	hard := filepath.Join(dir, "hard")
	if err := os.Link(victim, hard); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	if f, err := openNoFollowWrite(hard, 0o644); err == nil {
		f.Close()
		t.Error("openNoFollowWrite opened a hard-linked file")
	}
	if got := readFile(t, victim); got != "original" {
		t.Errorf("victim truncated or rewritten: %q", got)
	}

	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err == nil {
		if f, err := openNoFollowWrite(fifo, 0o644); err == nil {
			f.Close()
			t.Error("openNoFollowWrite opened a FIFO")
		}
	}

	fresh := filepath.Join(dir, "fresh")
	f, err := openNoFollowWrite(fresh, 0o644)
	if err != nil {
		t.Fatalf("plain create: %v", err)
	}
	f.Close()
}

func TestReadRegular(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	plain := filepath.Join(dir, "plain")
	writeFile(t, plain, "0123456789")

	if got, err := readRegular(plain, 4); err != nil || string(got) != "01234" {
		t.Errorf("readRegular capped = (%q, %v), want 5 bytes (max+1)", got, err)
	}
	if got, err := readRegular(plain, 100); err != nil || string(got) != "0123456789" {
		t.Errorf("readRegular = (%q, %v)", got, err)
	}
	if _, err := readRegular(filepath.Join(dir, "missing"), 10); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing err = %v, want fs.ErrNotExist", err)
	}

	secret := filepath.Join(outside, "secret")
	writeFile(t, secret, "secret")
	link := filepath.Join(dir, "link")
	mustSymlink(t, secret, link)
	if _, err := readRegular(link, 10); !errors.Is(err, ErrSymlinkInWorktree) {
		t.Errorf("symlink err = %v, want ErrSymlinkInWorktree", err)
	}
	if _, err := readRegular(dir, 10); err == nil {
		t.Error("readRegular read a directory")
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(secret, hard); err == nil {
		if _, err := readRegular(hard, 10); err == nil {
			t.Error("readRegular read a hard-linked file")
		}
	}
}
