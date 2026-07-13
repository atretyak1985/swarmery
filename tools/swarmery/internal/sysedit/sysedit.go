// Package sysedit is the Stage 2 write base (phase 4 step-08): the ONLY code
// path allowed to modify agent-system config files (agents, skills, commands).
// A corrupted settings.json breaks Claude Code on the whole machine, so every
// write goes through the same strict pipeline:
//
//	a) kill-switch   — SWARMERY_SYSTEM_READONLY=1 → ErrReadOnly
//	b) resolve       — file path comes ONLY from the DB by row id (never from
//	                   an API request) and must lie under a known config root
//	                   (ClaudeDir or a project's .claude dir) → ErrPathOutsideRoots
//	c) provenance    — origin='plugin' rows are marketplace-managed → ErrPluginManaged
//	d) conflict      — sha256(disk) != baseHash → ErrConflict (with diff), NO overwrite
//	e) backup        — full copy into ~/.swarmery/config-backups/<ts>/<orig path>,
//	                   verified byte-for-byte BEFORE any change; last 50 kept
//	f) atomic write  — tmp file in the SAME directory → fsync → rename,
//	                   original permissions preserved
//	g) forced rescan — sysscan converges the registry; the new version id of
//	                   the item is returned
//
// Deletes are soft: the file is MOVED into config-backups (content is never
// destroyed) and the row gets deleted=1. settings.json is out of scope here
// (step-10 goes through the hookcfg surgical pattern).
package sysedit

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/textdiff"
)

// EnvReadOnly is the kill-switch: set to 1 (or true) to refuse every write —
// the API layer maps ErrReadOnly to 403 "readonly mode". Same env-override
// pattern as SWARMERY_PRICING / SWARMERY_LINT_* (config/config.go, lint.go).
const EnvReadOnly = "SWARMERY_SYSTEM_READONLY"

// DefaultKeepBackups is how many timestamp backup directories rotation keeps.
const DefaultKeepBackups = 50

// tmpPrefix marks sysedit's in-flight atomic-write files; stale ones (from a
// crash between tmp and rename) are swept on the next write into the same dir.
const tmpPrefix = ".sysedit-"

// Typed errors — errors.Is-friendly. The API mapping is step-09's job:
// ErrReadOnly/ErrPluginManaged → 403, ErrConflict → 409, ErrNotFound → 404,
// ErrPathOutsideRoots → 500 (registry corruption, never a user error).
var (
	ErrReadOnly         = errors.New("sysedit: readonly mode (" + EnvReadOnly + ")")
	ErrPluginManaged    = errors.New("sysedit: item is plugin-managed — edit it in the plugin's repo")
	ErrPathOutsideRoots = errors.New("sysedit: file path outside known config roots")
	ErrConflict         = errors.New("sysedit: content changed on disk since baseHash")
	ErrNotFound         = errors.New("sysedit: item not found")
)

// ConflictError is the ErrConflict carrier — the step-09 409 body
// {disk_hash, base_hash, diff} is built from these fields.
type ConflictError struct {
	DiskHash string // sha256 of the content currently on disk
	BaseHash string // the caller's stale base hash
	Diff     string // unified diff base→disk (or proposed→disk when the base
	// content is not in *_versions); canonical Myers diff (internal/textdiff)
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: disk=%s base=%s", ErrConflict, e.DiskHash, e.BaseHash)
}

// Unwrap makes errors.Is(err, ErrConflict) match.
func (e *ConflictError) Unwrap() error { return ErrConflict }

// ItemRef identifies one registry item by kind + row id. The row id is the
// ONLY input — the file path is resolved from the DB (artifacts pattern:
// paths never travel through API requests).
type ItemRef struct {
	Kind string // sysscan.KindAgent | KindSkill | KindCommand
	ID   int64
}

func (r ItemRef) String() string { return fmt.Sprintf("%s:%d", r.Kind, r.ID) }

// kindSpec maps one item kind onto its registry tables (mirrors the
// api.systemKind shape).
type kindSpec struct {
	table    string // agents | skills | commands
	verTable string // agent_versions | skill_versions; "" = unversioned kind
	fkCol    string // *_versions FK column
	pathCol  string // file_path | dir_path
	dirBased bool   // skills: the edited file is <dir_path>/SKILL.md
}

var kinds = map[string]kindSpec{
	sysscan.KindAgent:   {table: "agents", verTable: "agent_versions", fkCol: "agent_id", pathCol: "file_path"},
	sysscan.KindSkill:   {table: "skills", verTable: "skill_versions", fkCol: "skill_id", pathCol: "dir_path", dirBased: true},
	sysscan.KindCommand: {table: "commands", pathCol: "file_path"},
}

// Config tunes the editor. Zero values fall back to defaults.
type Config struct {
	ClaudeDir   string // config root allowlist anchor (default ~/.claude)
	BackupsDir  string // default ~/.swarmery/config-backups
	KeepBackups int    // rotation depth in timestamp dirs (default 50)
}

// DefaultBackupsDir resolves ~/.swarmery/config-backups (the canonical app
// dir — installer.DBPath, store.DefaultDBPath).
func DefaultBackupsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".swarmery", "config-backups")
	}
	return filepath.Join(home, ".swarmery", "config-backups")
}

func (c Config) withDefaults() Config {
	if c.ClaudeDir == "" {
		c.ClaudeDir = sysscan.DefaultClaudeDir()
	}
	if c.BackupsDir == "" {
		c.BackupsDir = DefaultBackupsDir()
	}
	if c.KeepBackups <= 0 {
		c.KeepBackups = DefaultKeepBackups
	}
	return c
}

// Editor performs guarded writes against one DB + one scanner. The scanner is
// the same instance the daemon runs — the post-write rescan converges the
// registry and publishes system_item_updated notes for the UI.
type Editor struct {
	db      *sql.DB
	scanner *sysscan.Scanner
	cfg     Config

	// commit finalizes the atomic write (default os.Rename). Injectable so
	// tests can simulate a crash BETWEEN tmp-write and rename.
	commit func(tmp, dst string) error
	// now feeds backup timestamp dirs (injectable for rotation tests).
	now func() time.Time
}

// New builds an editor. scanner must run against the same db.
func New(db *sql.DB, scanner *sysscan.Scanner, cfg Config) *Editor {
	return &Editor{db: db, scanner: scanner, cfg: cfg.withDefaults(), commit: os.Rename, now: time.Now}
}

// readonly reports the kill-switch state, read per call (flipping the env on
// a live daemon takes effect on the next write).
func readonly() bool {
	v := os.Getenv(EnvReadOnly)
	return v == "1" || strings.EqualFold(v, "true")
}

// WriteFile replaces the content of one registry item's file, running the
// full a)–g) pipeline documented on the package. Returns the item's new
// version id in *_versions (0 for unversioned kinds, i.e. commands).
func (e *Editor) WriteFile(ref ItemRef, newContent []byte, baseHash string) (int64, error) {
	// a) kill-switch.
	if readonly() {
		return 0, ErrReadOnly
	}

	// b) resolve the path from the DB by id, then fence it into known roots.
	spec, path, origin, err := e.resolve(ref)
	if err != nil {
		return 0, err
	}

	// c) plugin-managed items are read-only mirrors of the marketplace cache.
	if origin == "plugin" {
		return 0, fmt.Errorf("%s: %w", ref, ErrPluginManaged)
	}

	// d) conflict detection — NEVER overwrite concurrent edits.
	disk, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("sysedit: read %s: %w", path, err)
	}
	diskHash := sha256Hex(disk)
	if diskHash != baseHash {
		return 0, e.conflict(spec, ref, baseHash, diskHash, disk, newContent)
	}

	// e) backup the original BEFORE any change, byte-verified.
	if _, err := e.backupFile(path); err != nil {
		return 0, fmt.Errorf("sysedit: backup %s: %w", path, err)
	}

	// f) atomic write: tmp in the SAME directory → fsync → rename, original
	// permissions preserved. Stale tmps from a previous crash are swept first.
	if err := e.atomicWrite(path, newContent); err != nil {
		return 0, err
	}

	// g) forced rescan — the scanner versions the new content into *_versions
	// (full pass; unchanged files are hash-skipped, so it is cheap).
	if _, err := e.scanner.Scan(); err != nil {
		return 0, fmt.Errorf("sysedit: %s written but rescan failed: %w", path, err)
	}
	return e.currentVersionID(spec, ref)
}

// DeleteFile soft-deletes one item: the file is MOVED into config-backups
// (content preserved — originals are never destroyed) and the registry row is
// flagged deleted=1. The next scan pass converges the rest.
func (e *Editor) DeleteFile(ref ItemRef) error {
	if readonly() {
		return ErrReadOnly
	}
	spec, path, origin, err := e.resolve(ref)
	if err != nil {
		return err
	}
	if origin == "plugin" {
		return fmt.Errorf("%s: %w", ref, ErrPluginManaged)
	}
	if err := e.moveToBackups(path); err != nil {
		return fmt.Errorf("sysedit: move %s to backups: %w", path, err)
	}
	if _, err := e.db.Exec(fmt.Sprintf(`UPDATE %s SET deleted = 1 WHERE id = ?`, spec.table), ref.ID); err != nil {
		return fmt.Errorf("sysedit: mark %s deleted: %w", ref, err)
	}
	return nil
}

// resolve maps an ItemRef onto (spec, absolute file path, origin) using ONLY
// the DB row, and enforces the config-root fence.
func (e *Editor) resolve(ref ItemRef) (kindSpec, string, string, error) {
	spec, ok := kinds[ref.Kind]
	if !ok {
		return spec, "", "", fmt.Errorf("sysedit: unknown item kind %q: %w", ref.Kind, ErrNotFound)
	}
	var path, origin string
	var deleted int
	err := e.db.QueryRow(fmt.Sprintf(
		`SELECT %s, origin, deleted FROM %s WHERE id = ?`, spec.pathCol, spec.table), ref.ID).
		Scan(&path, &origin, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return spec, "", "", fmt.Errorf("%s: %w", ref, ErrNotFound)
	}
	if err != nil {
		return spec, "", "", fmt.Errorf("sysedit: resolve %s: %w", ref, err)
	}
	if deleted == 1 {
		return spec, "", "", fmt.Errorf("%s (soft-deleted): %w", ref, ErrNotFound)
	}
	if spec.dirBased {
		path = filepath.Join(path, "SKILL.md")
	}
	path = filepath.Clean(path)
	roots, err := e.roots()
	if err != nil {
		return spec, "", "", err
	}
	if !underAny(path, roots) {
		return spec, "", "", fmt.Errorf("%s at %s: %w", ref, path, ErrPathOutsideRoots)
	}
	return spec, path, origin, nil
}

// roots lists every directory sysedit may write under: ClaudeDir plus each
// known project's .claude dir (same universe sysscan scans — loadProjects).
func (e *Editor) roots() ([]string, error) {
	roots := []string{filepath.Clean(e.cfg.ClaudeDir)}
	rows, err := e.db.Query(`SELECT path FROM projects WHERE archived = 0`)
	if err != nil {
		return nil, fmt.Errorf("sysedit: load projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if filepath.IsAbs(p) { // skip '(unknown)' placeholder rows
			roots = append(roots, filepath.Join(p, ".claude"))
		}
	}
	return roots, rows.Err()
}

// underAny reports whether path lies under (or is) one of the roots — the
// same lexical fence sysscan's sweep uses.
func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// conflict builds the ConflictError. The most useful diff is base→disk
// ("what changed under you"); the base content is recovered from *_versions
// by content_hash. When it is not there (or the kind is unversioned) the
// fallback is proposed→disk.
func (e *Editor) conflict(spec kindSpec, ref ItemRef, baseHash, diskHash string, disk, proposed []byte) error {
	aName, aText := "proposed", string(proposed)
	if spec.verTable != "" {
		var base string
		err := e.db.QueryRow(fmt.Sprintf(
			`SELECT content FROM %s WHERE %s = ? AND content_hash = ?`, spec.verTable, spec.fkCol),
			ref.ID, baseHash).Scan(&base)
		if err == nil {
			aName, aText = "base", base
		}
	}
	return &ConflictError{
		DiskHash: diskHash,
		BaseHash: baseHash,
		Diff:     textdiff.UnifiedDiff(aName, "disk", aText, string(disk)),
	}
}

// atomicWrite is pipeline step f: sweep stale tmps, write a tmp file in the
// target's own directory (same FS — rename stays atomic), fsync, restore the
// original mode, then commit (os.Rename; injectable for crash tests). On a
// crash between tmp and rename the original is untouched and the leftover
// tmp is swept by the next write.
func (e *Editor) atomicWrite(path string, content []byte) error {
	dir := filepath.Dir(path)
	sweepStaleTmp(dir)

	info, err := os.Stat(path) // capture permissions BEFORE writing
	if err != nil {
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
	if err := tmp.Sync(); err != nil { // fsync BEFORE rename — no empty-file window
		tmp.Close()
		return fmt.Errorf("sysedit: fsync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sysedit: close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return fmt.Errorf("sysedit: chmod %s: %w", tmpName, err)
	}
	if err := e.commit(tmpName, path); err != nil {
		os.Remove(tmpName) // best-effort; a crash-leftover is swept next run
		return fmt.Errorf("sysedit: rename %s → %s: %w", tmpName, path, err)
	}
	return nil
}

// sweepStaleTmp removes leftover sysedit tmp files in dir — the recovery half
// of the crash contract (only ever files WE created, by prefix).
func sweepStaleTmp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, en := range entries {
		if !en.IsDir() && strings.HasPrefix(en.Name(), tmpPrefix) {
			os.Remove(filepath.Join(dir, en.Name()))
		}
	}
}

// currentVersionID reads the item's current version id after the rescan.
// Unversioned kinds (commands) report 0.
func (e *Editor) currentVersionID(spec kindSpec, ref ItemRef) (int64, error) {
	if spec.verTable == "" {
		return 0, nil
	}
	var id sql.NullInt64
	err := e.db.QueryRow(fmt.Sprintf(
		`SELECT current_version_id FROM %s WHERE id = ?`, spec.table), ref.ID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("sysedit: version of %s: %w", ref, err)
	}
	return id.Int64, nil
}

// sha256Hex matches sysscan's content_hash encoding.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fileMode returns src's permission bits, defaulting to 0600 when unreadable
// (backup copies should never be MORE permissive than the original).
func fileMode(src string) fs.FileMode {
	if info, err := os.Stat(src); err == nil {
		return info.Mode().Perm()
	}
	return 0o600
}
