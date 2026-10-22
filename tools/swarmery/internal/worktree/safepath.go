package worktree

// A worktree's tree belongs to the repository, and a repository can commit
// symlinks. Any file the daemon reads out of a worktree, or writes into one,
// must therefore be resolved WITHOUT following a symlink: a followed link lets
// the tree pick a file outside the worktree for the daemon to read (and show on
// a card, or copy into the workspace) or to overwrite.
//
// The helpers here are the one place that rule is spelled out:
//   - checkNoSymlink refuses a relative path whose existing components include a
//     symlink;
//   - openNoFollowWrite and readRegular open the final component with
//     O_NOFOLLOW and accept only a regular file with a single link, so a
//     symlink or hard link planted between the check and the open is refused
//     too.
//
// The worktree root itself is not inspected: it is created by this package, and
// its ancestors (macOS /var -> /private/var, a symlinked home) are the
// machine's, not the repository's.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrSymlinkInWorktree: a path inside a worktree resolves through a symlink, so
// the daemon refuses to read or write it.
var ErrSymlinkInWorktree = errors.New("worktree: path resolves through a symlink")

// errNotRegularFile: the path is a directory, device, FIFO, socket or a file
// with more than one hard link — nothing the daemon lends or collects.
var errNotRegularFile = errors.New("worktree: not a regular single-link file")

// checkNoSymlink Lstats every component of rel below root and refuses with
// ErrSymlinkInWorktree when one of them is a symlink. A component that does not
// exist ends the walk with nil: there is nothing below it to follow, and
// whether a missing file is an error is the caller's question. rel must be a
// local relative path (no "..", not absolute).
func checkNoSymlink(root, rel string) error {
	if !filepath.IsLocal(rel) {
		return fmt.Errorf("worktree: %q is not a path inside the worktree", rel)
	}
	cur := root
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlinkInWorktree, cur)
		}
	}
	return nil
}

// openNoFollowWrite opens path for writing, creating it with perm when absent,
// and truncates it — but only after proving the open landed on a regular file
// with one link. A symlink at path fails the open itself (O_NOFOLLOW); a FIFO
// cannot block it (O_NONBLOCK); a hard link to a file elsewhere is refused
// before a byte of it is truncated.
func openNoFollowWrite(path string, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlinkInWorktree, path)
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		err = requireRegularSingleLink(path, fi)
	}
	if err == nil {
		err = f.Truncate(0)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// readRegular reads path when it is a regular single-link file, returning at
// most max+1 bytes so a caller can tell "exactly max" from "more than max".
// A missing file surfaces as an fs.ErrNotExist error.
func readRegular(path string, max int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", ErrSymlinkInWorktree, path)
	}
	if err := requireRegularSingleLink(path, fi); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s", ErrSymlinkInWorktree, path)
		}
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// The file opened must be the file inspected: a swap between Lstat and
	// open is refused rather than read.
	if !os.SameFile(fi, opened) {
		return nil, fmt.Errorf("%w: %s changed while it was being opened", errNotRegularFile, path)
	}
	if err := requireRegularSingleLink(path, opened); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

// requireRegularSingleLink refuses anything but a regular file with exactly one
// directory entry. A second hard link means the same bytes live somewhere else
// too, and git never checks one out, so inside a worktree it can only have been
// planted.
func requireRegularSingleLink(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s (%s)", errNotRegularFile, path, fi.Mode().Type())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return fmt.Errorf("%w: %s has %d hard links", errNotRegularFile, path, st.Nlink)
	}
	return nil
}
