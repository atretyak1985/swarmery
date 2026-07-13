package sysedit

// Backups (pipeline step e) and soft-delete moves. Layout:
//
//	<BackupsDir>/<timestamp>/<full original path>
//	~/.swarmery/config-backups/2026-07-14T10-22-33Z/Users/me/.claude/agents/x.md
//
// The timestamp is RFC3339 UTC with `-` instead of `:` in the time part
// (colons break some tools); a same-second collision gets a -2/-3… suffix so
// every backup is its own directory. Rotation keeps the newest KeepBackups
// timestamp dirs, ordered by directory mtime (suffix names are not
// chronologically sortable within one second), and deletes STRICTLY under
// BackupsDir — removeBackupDir asserts the prefix before any RemoveAll.

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// backupTimeFormat: RFC3339 UTC, `-` for `:` in the time part.
const backupTimeFormat = "2006-01-02T15-04-05Z"

// backupFile copies src into a fresh timestamp dir (mirroring the absolute
// path), fsyncs, verifies the copy byte-for-byte, then rotates. Returns the
// backup file path. Runs BEFORE any modification of src.
func (e *Editor) backupFile(src string) (string, error) {
	dst, err := e.backupDst(src)
	if err != nil {
		return "", err
	}
	if err := copyVerify(src, dst); err != nil {
		return "", err
	}
	if err := e.rotateBackups(); err != nil {
		return "", err
	}
	return dst, nil
}

// moveToBackups relocates src into a fresh timestamp dir — the soft-delete
// primitive. Same-FS moves are a pure rename; across filesystems (a project
// on another volume vs ~/.swarmery) rename fails with EXDEV, so the fallback
// is copy + fsync + byte-for-byte verify, and ONLY a verified backup permits
// removing the original — content is never destroyed.
func (e *Editor) moveToBackups(src string) error {
	dst, err := e.backupDst(src)
	if err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return e.rotateBackups()
	}
	if err := copyVerify(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove original after verified backup: %w", err)
	}
	return e.rotateBackups()
}

// backupDst allocates a fresh timestamp dir and returns the mirrored
// destination path for src, with parents created.
func (e *Editor) backupDst(src string) (string, error) {
	tsDir, err := e.newBackupDir()
	if err != nil {
		return "", err
	}
	mirror := strings.TrimPrefix(filepath.Clean(src), string(os.PathSeparator))
	dst := filepath.Join(tsDir, mirror)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	return dst, nil
}

// newBackupDir creates <BackupsDir>/<timestamp>[ -N ] — each backup gets its
// own directory (rotation counts dirs, not files).
func (e *Editor) newBackupDir() (string, error) {
	if err := os.MkdirAll(e.cfg.BackupsDir, 0o755); err != nil {
		return "", err
	}
	base := e.now().UTC().Format(backupTimeFormat)
	name := base
	for i := 2; ; i++ {
		dir := filepath.Join(e.cfg.BackupsDir, name)
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
		name = fmt.Sprintf("%s-%d", base, i) // same-second collision
	}
}

// rotateBackups keeps the newest KeepBackups timestamp dirs (mtime order —
// creation order regardless of suffix naming) and deletes the rest, each
// through the prefix-asserting removeBackupDir.
func (e *Editor) rotateBackups() error {
	entries, err := os.ReadDir(e.cfg.BackupsDir)
	if err != nil {
		return err
	}
	type dirent struct {
		name string
		info fs.FileInfo
	}
	var dirs []dirent
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue // vanished mid-rotation — skip
		}
		dirs = append(dirs, dirent{name: en.Name(), info: info})
	}
	if len(dirs) <= e.cfg.KeepBackups {
		return nil
	}
	sort.Slice(dirs, func(i, j int) bool { // oldest first; name as tiebreak
		ti, tj := dirs[i].info.ModTime(), dirs[j].info.ModTime()
		if ti.Equal(tj) {
			return dirs[i].name < dirs[j].name
		}
		return ti.Before(tj)
	})
	for _, d := range dirs[:len(dirs)-e.cfg.KeepBackups] {
		if err := removeBackupDir(e.cfg.BackupsDir, d.name); err != nil {
			return err
		}
	}
	return nil
}

// removeBackupDir deletes one rotation victim, asserting it resolves to a
// path STRICTLY under root before RemoveAll ever runs — the only recursive
// delete in the package stays fenced inside config-backups.
func removeBackupDir(root, name string) error {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return err
	}
	victim, err := filepath.Abs(filepath.Join(rootAbs, name))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(victim, rootAbs+string(os.PathSeparator)) {
		return fmt.Errorf("sysedit: refusing to remove %q — outside backups root %q", victim, rootAbs)
	}
	return os.RemoveAll(victim)
}

// copyVerify copies src → dst (original permissions, capped at owner rw for
// safety of secrets), fsyncs, and re-reads dst to confirm a byte-for-byte
// match — a backup that cannot be trusted is an error, not a warning.
func copyVerify(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode(src))
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	check, err := os.ReadFile(dst)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, check) {
		return fmt.Errorf("backup verification failed: %s does not match %s", dst, src)
	}
	return nil
}
