package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withUmask022 runs the test under the common default umask, so a mode that
// comes out private does so because the code asked for it.
func withUmask022(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
}

func wantMode(t *testing.T, p string, want fs.FileMode) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s: mode %o, want %o", p, got, want)
	}
}

func TestOpenCreatesPrivateFiles(t *testing.T) {
	withUmask022(t)
	dir := filepath.Join(t.TempDir(), "store")
	path := filepath.Join(dir, "swarmery.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE perms_probe (x)`); err != nil {
		t.Fatal(err)
	}
	wantMode(t, dir, 0o700)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s missing after a write: %v", p, err)
		}
		wantMode(t, p, 0o600)
	}
}

// A store created by an older build (0755 dir, 0644 files) is tightened by
// the next Open: the default directory to 0700, the database and sidecars to
// 0600.
func TestOpenTightensAnExistingDefaultStore(t *testing.T) {
	withUmask022(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := DefaultDBPath()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE perms_probe (x)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, p := range []string{dir} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o644); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wantMode(t, dir, 0o700)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if _, err := os.Stat(p); err == nil {
			wantMode(t, p, 0o600)
		}
	}
}

func TestOpenInMemoryCreatesNoFile(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(":memory:"); err == nil {
		t.Error("Open(\":memory:\") created a file named :memory:")
	}
}

func TestBackupIsPrivate(t *testing.T) {
	withUmask022(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "swarmery.db")
	db, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t (v)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dest := filepath.Join(dir, "backups", "snap.db")
	if _, err := Backup(src, dest); err != nil {
		t.Fatal(err)
	}
	wantMode(t, filepath.Dir(dest), 0o700)
	wantMode(t, dest, 0o600)
}

func TestTightenPerms(t *testing.T) {
	withUmask022(t)
	root := t.TempDir()
	home := filepath.Join(root, ".swarmery")
	outside := filepath.Join(root, "outside.txt")
	mk := func(p string, mode fs.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(home, "swarmery.db"), 0o644)
	mk(filepath.Join(home, "hook.log"), 0o644)
	mk(filepath.Join(home, "backups", "swarmery-1.db"), 0o644)
	mk(filepath.Join(home, "config-backups", "20261022", "agents", "a.md"), 0o644)
	mk(filepath.Join(home, "worktrees", "repo", "run.sh"), 0o755)
	mk(outside, 0o644)
	if err := os.Symlink(outside, filepath.Join(home, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "backups", "link.db")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{home, filepath.Join(home, "backups"), filepath.Join(home, "config-backups", "20261022")} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := TightenPerms(home); err != nil {
		t.Fatalf("TightenPerms: %v", err)
	}
	wantMode(t, home, 0o700)
	wantMode(t, filepath.Join(home, "swarmery.db"), 0o600)
	wantMode(t, filepath.Join(home, "hook.log"), 0o600)
	wantMode(t, filepath.Join(home, "backups"), 0o700)
	wantMode(t, filepath.Join(home, "backups", "swarmery-1.db"), 0o600)
	wantMode(t, filepath.Join(home, "config-backups"), 0o700)
	wantMode(t, filepath.Join(home, "config-backups", "20261022"), 0o700)
	wantMode(t, filepath.Join(home, "config-backups", "20261022", "agents", "a.md"), 0o600)
	// Not the store's: other trees and symlink targets keep their modes.
	wantMode(t, filepath.Join(home, "worktrees"), 0o755)
	wantMode(t, filepath.Join(home, "worktrees", "repo", "run.sh"), 0o755)
	wantMode(t, outside, 0o644)

	if err := TightenPerms(filepath.Join(root, "missing")); err != nil {
		t.Errorf("TightenPerms on a missing dir: %v", err)
	}
}
