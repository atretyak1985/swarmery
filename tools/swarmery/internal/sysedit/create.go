package sysedit

// Step-11: exclusive create + restore-from-history. WriteFile (sysedit.go)
// replaces the content of a file the registry already knows; CreateFile is
// its birth twin — no row yet, so the path comes from the API layer (still
// fenced under the known config roots) and there is no baseHash: the ONLY
// precondition is that the destination name must not exist. The atomic
// commit is a hard link instead of a rename, because a rename over an
// existing (unscanned, orphan) file would silently clobber it — os.Link
// fails with EEXIST, which the API maps to 409 "run a rescan".
//
// RestoreFile is the undo of DeleteFile: the latest stored version is
// exclusive-created back at the row's file_path and the forced rescan flips
// deleted back to 0 (the scanner upserts by (name, scope, project) and
// un-deletes on sight) — this is why a soft delete never frees the name
// under UNIQUE(name, scope, project_id).

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Typed errors of the create/restore surface (API mapping: both → 409).
var (
	ErrExists     = errors.New("sysedit: file already exists on disk")
	ErrNotDeleted = errors.New("sysedit: item is not soft-deleted")
)

// ClaudeDir exposes the config-root anchor so the API layer can compose
// create paths (<ClaudeDir>/agents/<name>.md). CreateFile re-fences the
// result under the known roots regardless.
func (e *Editor) ClaudeDir() string { return e.cfg.ClaudeDir }

// CreateFile creates one NEW config file: kill-switch, root fence, parent
// mkdir 0755, tmp+fsync in the target directory, exclusive commit, forced
// rescan (the scanner mints the registry row and its FIRST version).
func (e *Editor) CreateFile(path string, content []byte) error {
	if readonly() {
		return ErrReadOnly
	}
	path = filepath.Clean(path)
	roots, err := e.roots()
	if err != nil {
		return err
	}
	if !underAny(path, roots) {
		return fmt.Errorf("%s: %w", path, ErrPathOutsideRoots)
	}
	if err := e.createExclusive(path, content); err != nil {
		return err
	}
	if _, err := e.scanner.Scan(); err != nil {
		return fmt.Errorf("sysedit: %s created but rescan failed: %w", path, err)
	}
	return nil
}

// RestoreFile brings one soft-deleted item back: the latest version's content
// is exclusive-created at the stored path and the forced rescan un-deletes
// the row. Unversioned kinds (commands) have no restore source → ErrNotFound.
func (e *Editor) RestoreFile(ref ItemRef) error {
	if readonly() {
		return ErrReadOnly
	}
	spec, ok := kinds[ref.Kind]
	if !ok {
		return fmt.Errorf("sysedit: unknown item kind %q: %w", ref.Kind, ErrNotFound)
	}
	if spec.verTable == "" {
		return fmt.Errorf("sysedit: %s has no version history to restore from: %w", ref.Kind, ErrNotFound)
	}

	// resolve() is not reused here on purpose: it rejects deleted rows, while
	// restore REQUIRES one. Same source of truth otherwise — path by row id.
	var path, origin string
	var deleted int
	err := e.db.QueryRow(fmt.Sprintf(
		`SELECT %s, origin, deleted FROM %s WHERE id = ?`, spec.pathCol, spec.table), ref.ID).
		Scan(&path, &origin, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", ref, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("sysedit: resolve %s: %w", ref, err)
	}
	if origin == "plugin" {
		return fmt.Errorf("%s: %w", ref, ErrPluginManaged)
	}
	if deleted == 0 {
		return fmt.Errorf("%s: %w", ref, ErrNotDeleted)
	}

	var content string
	err = e.db.QueryRow(fmt.Sprintf(
		`SELECT content FROM %s WHERE %s = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		spec.verTable, spec.fkCol), ref.ID).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s has no stored versions: %w", ref, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("sysedit: restore source of %s: %w", ref, err)
	}

	if spec.dirBased {
		path = filepath.Join(path, "SKILL.md")
	}
	return e.CreateFile(path, []byte(content))
}

// createExclusive is the O_EXCL analogue of atomicWrite: parents are created
// (0755 — §0: ~/.claude/agents may not exist yet), the content lands in a
// tmp file (fsync BEFORE commit), and the commit is os.Link — atomic, and it
// FAILS with EEXIST when the destination appeared meanwhile. The leftover
// tmp hard link is dropped by the same by-prefix sweep atomicWrite relies
// on; nothing here ever removes a destination file.
func (e *Editor) createExclusive(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("sysedit: mkdir %s: %w", dir, err)
	}
	sweepStaleTmp(dir)

	// Fast pre-check for the caller's error message; the link below is the
	// authoritative race-free gate.
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s: %w", path, ErrExists)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sysedit: stat %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, tmpPrefix)
	if err != nil {
		return fmt.Errorf("sysedit: tmp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("sysedit: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sysedit: fsync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sysedit: close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("sysedit: chmod %s: %w", tmpName, err)
	}
	if err := os.Link(tmpName, path); err != nil {
		sweepStaleTmp(dir)
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s: %w", path, ErrExists)
		}
		return fmt.Errorf("sysedit: link %s → %s: %w", tmpName, path, err)
	}
	sweepStaleTmp(dir) // drops the tmp link; path keeps the inode
	return nil
}
