package store

// File modes of the store. The database holds whole transcripts — commands,
// tool output, replies — so every file of it is private to the user: dirs
// 0700, files 0600. Open and Backup set them on what they create; TightenPerms
// fixes a store directory created by an older build.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	privateDirMode  fs.FileMode = 0o700
	privateFileMode fs.FileMode = 0o600
)

// onDisk reports whether path names a file (not ":memory:" or a URI).
func onDisk(path string) bool {
	return path != "" && path != ":memory:" && !strings.HasPrefix(path, "file:")
}

// isDefaultDir reports whether dir is the default store directory, the
// directory of DefaultDBPath.
func isDefaultDir(dir string) bool {
	def, err := DefaultDBPath()
	if err != nil {
		return false
	}
	a, errA := filepath.Abs(dir)
	b, errB := filepath.Abs(filepath.Dir(def))
	return errA == nil && errB == nil && a == b
}

// createPrivate creates an empty database file 0600 when none exists, so
// SQLite never creates it (or its sidecars, which copy its mode) with the
// umask's default.
func createPrivate(path string) error {
	if !onDisk(path) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// privateDBFiles sets the database file and its -wal / -shm sidecars, when
// they exist, to 0600.
func privateDBFiles(path string) error {
	if !onDisk(path) {
		return nil
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := chmodRegular(p, privateFileMode); err != nil {
			return fmt.Errorf("chmod %s: %w", p, err)
		}
	}
	return nil
}

// chmodRegular sets mode on p when p is a regular file (never through a
// symlink) whose mode differs; a missing p is not an error.
func chmodRegular(p string, mode fs.FileMode) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() == mode {
		return nil
	}
	return os.Chmod(p, mode)
}

// privateTrees are the subdirectories of the store directory TightenPerms
// walks: the database snapshots and the system-config backups. Every other
// subdirectory (worktrees, bin, …) is left alone — it holds checkouts and
// executables whose modes are not the store's to decide.
var privateTrees = []string{"backups", "config-backups"}

// TightenPerms makes the store directory home (normally ~/.swarmery) private:
// home itself 0700, the regular files directly in it 0600, and everything
// inside home/backups and home/config-backups dirs 0700 / files 0600. It never
// follows a symlink, and does nothing when home does not exist. Errors are
// collected, not fatal: one unreadable entry does not leave the rest open.
func TightenPerms(home string) error {
	fi, err := os.Lstat(home)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return nil // a symlinked or non-directory home is not ours to change
	}
	var errs []error
	if fi.Mode().Perm() != privateDirMode {
		errs = append(errs, os.Chmod(home, privateDirMode))
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, e := range entries {
		p := filepath.Join(home, e.Name())
		if e.Type().IsRegular() {
			errs = append(errs, chmodRegular(p, privateFileMode))
		}
	}
	for _, sub := range privateTrees {
		errs = append(errs, tightenTree(filepath.Join(home, sub)))
	}
	return errors.Join(errs...)
}

// tightenTree walks root without following symlinks: dirs 0700, files 0600.
func tightenTree(root string) error {
	fi, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return nil
	}
	var errs []error
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			info, ierr := d.Info()
			if ierr != nil {
				errs = append(errs, ierr)
			} else if info.Mode().Perm() != privateDirMode {
				errs = append(errs, os.Chmod(p, privateDirMode))
			}
		case d.Type().IsRegular():
			errs = append(errs, chmodRegular(p, privateFileMode))
		}
		return nil // symlinks and other types: left alone, never followed
	})
	return errors.Join(append(errs, walkErr)...)
}
