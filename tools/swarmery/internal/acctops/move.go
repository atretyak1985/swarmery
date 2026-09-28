package acctops

// `swarmery account move-session <uuid> --to <key>` — the operator's
// copy-and-resume runbook as one idempotent command.
//
// # The destination directory name is LOCATED, never computed
//
// The transcript is found by globbing <srcConfigDir>/projects/*/<uuid>.jsonl,
// and the directory that holds it is carried to the target config dir
// BYTE-FOR-BYTE: no re-encoding, no normalisation, no Clean applied to the
// name. Nothing here encodes a path into a directory name, and nothing inverts
// a name back into a path. Reasons, each sufficient on its own:
//
//   - sessions.cwd is the FIRST non-empty cwd a session recorded and is never
//     revised; a session Claude Code relocated mid-flight (its worktree
//     feature) lives in the directory of a LATER cwd. Encoding the stored cwd
//     names a real, populated, wrong directory.
//   - CLAUDE_CODE_PROJECT_DIR_NAME overrides the directory name outright, and
//     no encoder can know it.
//   - The encoding is many-to-one, so a name cannot be inverted to a path.
//
// sessions.cwd and --cwd therefore feed exactly one thing: the --path of the
// printed resume command.
//
// # What is copied — COPY, never move
//
//	<src>/projects/<name>/<uuid>.jsonl  → <dst>/projects/<name>/<uuid>.jsonl
//	<src>/projects/<name>/<uuid>/       → <dst>/projects/<name>/<uuid>/   (recursive)
//	<src>/projects/<name>/memory/       → <dst>/projects/<name>/memory/   (merge, when present)
//
// Every file is classified BEFORE anything is written: new (copied),
// identical to the destination by size and SHA-256 (left alone — which is what
// makes a re-run a no-op), or DIFFERENT. A single differing destination
// refuses the whole move, listing every differing path, unless --overwrite;
// with --overwrite each differing destination is first renamed to
// <name>.pre-move-<UTC timestamp> (never deleted) and then copied over. A file
// is written to a temp file in its destination directory, fsynced and renamed
// into place, so a failure never leaves a partial file under the final name.
//
// Symlinks are never followed and never recreated: one inside <uuid>/ or
// memory/ is refused (listed, nothing copied for it), a symlinked <uuid>/ or
// memory/ is skipped (listed), and a destination directory that is a symlink
// refuses the move. Scratchpads and task outputs live outside both config dirs
// (under the system temp tree, shared by both accounts) and are never touched.
// Then store.SetSessionAccount re-points the sessions row — ingest's
// first-writer-wins stamp cannot, by design.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// defaultAccountKey is the default account's registry key (ingest's
// DefaultAccount — spelled here so this file needs no ingest import).
const defaultAccountKey = "default"

// ForkWarning is printed beside every resume command this package produces.
const ForkWarning = "resume in place with --resume and never --fork-session: Workflow's resumeFromRunId " +
	"is same-session-only and keys off the session id, so a fork throws away the run cache"

// FindProcessesCommand is the runbook's command for finding a live `claude`
// process and the config dir it runs under.
const FindProcessesCommand = `for p in $(pgrep -f "^claude "); do ps eww -p $p | tr ' ' '\n' | grep CLAUDE_CONFIG_DIR; done`

// preMoveStamp is the UTC timestamp layout of a preserved destination file.
const preMoveStamp = "20060102T150405Z"

// ErrSessionLive is returned (wrapped) when the session looks live and --force
// was not given.
var ErrSessionLive = errors.New("session is live")

// ErrDestinationDiffers is returned (wrapped) when a destination file exists
// with content different from the source and --overwrite was not given.
// Nothing has been written when it is returned.
var ErrDestinationDiffers = errors.New("destination differs")

// ErrCopyFailed is returned (wrapped) when the copy started and then failed;
// the report says what was written before the failure.
var ErrCopyFailed = errors.New("copy failed")

// MoveOptions are move-session's inputs, already parsed.
type MoveOptions struct {
	UUID       string
	To         string // target account key
	From       string // source account key when there is no database row
	Cwd        string // --cwd: ONLY the --path of the printed resume command
	ProjectDir string // --project-dir: selects among located hits, never builds a path
	Force      bool
	Overwrite  bool // --overwrite: preserve then replace differing destination files
	DryRun     bool

	DBPath string // "" → store.DefaultDBPath()
	// ConfigDir resolves an account key to its config dir; nil → AccountConfigDir.
	ConfigDir func(key string) (dir string, installed bool)
}

// CopyItem is one artefact to copy.
type CopyItem struct {
	Kind string // "transcript" | "session dir" | "memory"
	Src  string
	Dst  string
}

// MoveReport is what a move did (or, on --dry-run, would do).
type MoveReport struct {
	UUID         string
	From, To     string
	SrcConfigDir string
	DstConfigDir string
	Name         string // the located directory name, byte-for-byte
	Items        []CopyItem
	Copied       int      // files written (0 on a re-run)
	Kept         int      // files already at the destination with identical content
	Conflicts    []string // destination files whose content differs from the source
	Preserved    []string // "<dst> -> <dst>.pre-move-<stamp>" renames done by --overwrite
	Refused      []string // symlinks inside a copied directory — nothing copied for them
	Skipped      []string // a symlinked <uuid>/ or memory/ directory, skipped whole
	Warnings     []string
	Cwd          string
	RowFound     bool
	RowUpdated   bool
	DBNote       string // why the row was not updated, when it was not
	DryRun       bool
	Failed       bool   // the copy started and failed; Copied counts what landed
	NoOp         bool   // nothing to do; Note says why
	Note         string // the single line a no-op prints
}

// sessionRow is what the sessions table knows about the uuid.
type sessionRow struct {
	found                           bool
	account, status, procState, cwd string
}

// MoveSession runs the command. Every refusal returns before anything is
// created under the destination.
func MoveSession(opts MoveOptions) (MoveReport, error) {
	rep := MoveReport{UUID: opts.UUID, To: strings.TrimSpace(opts.To), DryRun: opts.DryRun}
	if !validUUID(opts.UUID) {
		return rep, fmt.Errorf("%q is not a session id (letters, digits and '-' only)", opts.UUID)
	}
	if !claudeacct.ValidKey(rep.To) {
		return rep, fmt.Errorf("%q is not a valid account key", rep.To)
	}
	configDir := opts.ConfigDir
	if configDir == nil {
		configDir = AccountConfigDir
	}

	db, dbNote := openSessionsDB(opts.DBPath)
	if db != nil {
		defer db.Close()
	}
	row, err := readSessionRow(db, opts.UUID)
	if err != nil {
		return rep, err
	}
	rep.RowFound = row.found

	from := strings.TrimSpace(opts.From)
	switch {
	case !row.found && from == "":
		return rep, fmt.Errorf("session %s is not in the database (%s) — pass --from <key> to name the account that holds it",
			opts.UUID, dbNote)
	case !row.found:
		rep.Warnings = append(rep.Warnings, "no sessions row for this id, so whether it is still live is unchecked — "+
			"close it first; find a live process with: "+FindProcessesCommand)
	case row.account == rep.To && (from == "" || from == rep.To):
		return alreadyUnderTarget(rep, configDir, opts.ProjectDir)
	case from == "":
		from = row.account
	case from != row.account && row.account != rep.To:
		return rep, fmt.Errorf("--from %s disagrees with the database, which records session %s under %s",
			from, opts.UUID, row.account)
	}
	if !claudeacct.ValidKey(from) {
		return rep, fmt.Errorf("%q is not a valid account key", from)
	}
	rep.From = from
	if from == rep.To {
		return rep, fmt.Errorf("session %s is already under account %s", opts.UUID, from)
	}
	// The row already names the target and --from names where the files came
	// from: a re-run that only completes (or confirms) the copy.
	rerun := row.found && row.account == rep.To

	if row.found && !opts.Force &&
		(row.status == "active" || row.status == "waiting_approval" || row.procState == "running") {
		return rep, fmt.Errorf("%w: session %s is %s (process %s) — close it first; find the process with:\n  %s\n"+
			"or rerun with --force if it is not really running",
			ErrSessionLive, opts.UUID, row.status, orNone(row.procState), FindProcessesCommand)
	}

	src, srcOK := configDir(from)
	if src == "" || (!srcOK && from != defaultAccountKey) {
		return rep, fmt.Errorf("no config dir for source account %s on this machine", from)
	}
	dst, dstOK := configDir(rep.To)
	if dst == "" || (!dstOK && rep.To != defaultAccountKey) {
		return rep, fmt.Errorf("no config dir for target account %s on this machine", rep.To)
	}
	rep.SrcConfigDir, rep.DstConfigDir = src, dst

	name, err := locate(src, opts.UUID, opts.ProjectDir)
	if err != nil {
		return rep, err
	}
	rep.Name = name
	rep.Items, rep.Skipped = plan(src, dst, name, opts.UUID)

	mp, err := scanItems(rep.Items, projectDir(dst, name))
	if err != nil {
		return rep, err
	}
	rep.Refused = mp.refused
	rep.Conflicts = mp.conflicts()
	if len(rep.Conflicts) > 0 && !opts.Overwrite {
		return rep, fmt.Errorf("%w: %d destination file(s) already exist with content different from the source — "+
			"nothing was written:\n  %s\nrerun with --overwrite to keep each one as <name>.pre-move-<UTC timestamp> and copy over it",
			ErrDestinationDiffers, len(rep.Conflicts), safeJoin(rep.Conflicts))
	}

	rep.Cwd = strings.TrimSpace(opts.Cwd)
	if rep.Cwd == "" {
		rep.Cwd = row.cwd
	}

	if rerun && mp.allSame() && len(rep.Refused) == 0 && len(rep.Skipped) == 0 {
		rep.NoOp = true
		rep.Note = fmt.Sprintf("nothing to do: session %s is already under account %s and all %d file(s) are identical in %s",
			opts.UUID, rep.To, len(mp.files), dst)
		return rep, nil
	}

	if opts.DryRun {
		return rep, nil
	}
	if err := os.MkdirAll(filepath.Join(dst, "projects"), 0o700); err != nil {
		return failCopy(&rep, err)
	}
	if err := mp.apply(opts.Overwrite, time.Now().UTC().Format(preMoveStamp), &rep); err != nil {
		return failCopy(&rep, err)
	}
	switch {
	case db == nil:
		rep.DBNote = dbNote
	case !row.found:
		rep.DBNote = "no sessions row for this id"
	default:
		ok, err := store.SetSessionAccount(db, opts.UUID, rep.To)
		if err != nil {
			return rep, fmt.Errorf("files copied, but the sessions row was not updated: %w", err)
		}
		rep.RowUpdated = ok
	}
	return rep, nil
}

// failCopy marks the report as a copy that started and failed.
func failCopy(rep *MoveReport, err error) (MoveReport, error) {
	rep.Failed = true
	rep.DBNote = "the copy failed, so the row was left alone"
	return *rep, fmt.Errorf("%w after %d file(s) were written: %v", ErrCopyFailed, rep.Copied, err)
}

// alreadyUnderTarget answers a move whose row already names the target and no
// other source was given: a no-op when the target holds the transcript.
func alreadyUnderTarget(rep MoveReport, configDir func(string) (string, bool), selector string) (MoveReport, error) {
	rep.From = rep.To
	dst, ok := configDir(rep.To)
	if dst == "" || (!ok && rep.To != defaultAccountKey) {
		return rep, fmt.Errorf("session %s is recorded under account %s, which has no config dir on this machine",
			rep.UUID, rep.To)
	}
	name, err := locate(dst, rep.UUID, selector)
	if err != nil {
		return rep, fmt.Errorf("session %s is recorded under account %s, but its config dir does not hold it (%v) — "+
			"pass --from <key> to copy it from the account that does", rep.UUID, rep.To, err)
	}
	rep.DstConfigDir, rep.Name, rep.NoOp = dst, name, true
	rep.Note = fmt.Sprintf("nothing to do: session %s is already under account %s (transcript in %s)",
		rep.UUID, rep.To, filepath.Join(dst, "projects")+string(os.PathSeparator)+name)
	return rep, nil
}

// locate globs <src>/projects/*/<uuid>.jsonl and returns the holding
// directory's name exactly as found. Zero hits, or several with no selector,
// are refused.
func locate(src, uuid, selector string) (string, error) {
	pattern := filepath.Join(src, "projects", "*", uuid+".jsonl")
	hits, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("glob %s: %w", safe(pattern), err)
	}
	var regular []string
	for _, h := range hits {
		if fi, err := os.Lstat(h); err == nil && fi.Mode().IsRegular() {
			regular = append(regular, h)
		}
	}
	if len(regular) == 0 {
		return "", fmt.Errorf("session %s is not held by the config dir %s: glob %s matched nothing — "+
			"no directory is invented for it", uuid, safe(src), safe(pattern))
	}
	if selector != "" {
		for _, h := range regular {
			if filepath.Base(filepath.Dir(h)) == selector {
				return selector, nil
			}
		}
		return "", fmt.Errorf("--project-dir %q is not among the located transcripts:\n  %s",
			selector, safeJoin(regular))
	}
	if len(regular) > 1 {
		return "", fmt.Errorf("session %s has %d transcripts under %s — pick one with --project-dir <name>:\n  %s",
			uuid, len(regular), safe(src), safeJoin(regular))
	}
	return filepath.Base(filepath.Dir(regular[0])), nil
}

// projectDir is <configDir>/projects/<name>, the name joined verbatim —
// string concatenation, never Clean.
func projectDir(configDir, name string) string {
	return filepath.Join(configDir, "projects") + string(os.PathSeparator) + name
}

// plan lists the artefacts to copy; only artefacts that exist are listed. A
// <uuid>/ or memory/ that is a symlink is never followed: it is reported in
// skipped and nothing is copied for it.
func plan(src, dst, name, uuid string) (items []CopyItem, skipped []string) {
	sep := string(os.PathSeparator)
	srcDir, dstDir := projectDir(src, name), projectDir(dst, name)
	items = []CopyItem{{Kind: "transcript", Src: srcDir + sep + uuid + ".jsonl", Dst: dstDir + sep + uuid + ".jsonl"}}
	for _, sub := range []struct{ kind, base string }{{"session dir", uuid}, {"memory", "memory"}} {
		p := srcDir + sep + sub.base
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
		case fi.Mode()&os.ModeSymlink != 0:
			skipped = append(skipped, fmt.Sprintf("%s %s is a symlink — not followed, nothing copied for it", sub.kind, p))
		case fi.IsDir():
			items = append(items, CopyItem{Kind: sub.kind, Src: p + sep, Dst: dstDir + sep + sub.base + sep})
		}
	}
	return items, skipped
}

// fileState classifies one source file against its destination.
type fileState int

const (
	stateNew     fileState = iota // no destination file: copy
	stateSame                     // identical size and SHA-256: leave alone
	stateDiffers                  // anything else at the destination path
)

type fileOp struct {
	src, dst string
	fi       os.FileInfo
	state    fileState
}

type dirOp struct {
	dst  string
	perm os.FileMode
}

// movePlan is the whole move, classified before anything is written.
type movePlan struct {
	dirs    []dirOp // destination directories, parents first
	files   []fileOp
	refused []string // symlinks inside a copied directory
}

// scanItems classifies every file of every item. It writes nothing. A
// destination directory that is a symlink, or a non-directory where a
// directory must go, refuses the move.
func scanItems(items []CopyItem, dstRoot string) (movePlan, error) {
	var mp movePlan
	if err := mp.addDir(dstRoot, 0o700); err != nil {
		return mp, err
	}
	sep := string(os.PathSeparator)
	for _, it := range items {
		if err := mp.scan(strings.TrimSuffix(it.Src, sep), strings.TrimSuffix(it.Dst, sep)); err != nil {
			return mp, fmt.Errorf("%s: %w", it.Kind, err)
		}
	}
	return mp, nil
}

func (mp *movePlan) addDir(dst string, perm os.FileMode) error {
	if err := checkDestDir(dst); err != nil {
		return err
	}
	mp.dirs = append(mp.dirs, dirOp{dst: dst, perm: perm | 0o700})
	return nil
}

func (mp *movePlan) scan(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		mp.refused = append(mp.refused, src)
		return nil
	case fi.IsDir():
		if err := mp.addDir(dst, fi.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := mp.scan(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	case fi.Mode().IsRegular():
		state, err := compareDest(src, dst, fi)
		if err != nil {
			return err
		}
		mp.files = append(mp.files, fileOp{src: src, dst: dst, fi: fi, state: state})
		return nil
	default:
		return nil // sockets, devices, fifos: never part of a session
	}
}

func (mp movePlan) conflicts() []string {
	var out []string
	for _, f := range mp.files {
		if f.state == stateDiffers {
			out = append(out, f.dst)
		}
	}
	return out
}

func (mp movePlan) allSame() bool {
	for _, f := range mp.files {
		if f.state != stateSame {
			return false
		}
	}
	return true
}

// apply performs the classified move. Differing destinations are only reached
// with overwrite (the caller refused otherwise); each is renamed aside first.
func (mp movePlan) apply(overwrite bool, stamp string, rep *MoveReport) error {
	for _, d := range mp.dirs {
		if err := ensureDir(d.dst, d.perm); err != nil {
			return err
		}
	}
	for _, f := range mp.files {
		switch f.state {
		case stateSame:
			rep.Kept++
			continue
		case stateDiffers:
			if !overwrite {
				return fmt.Errorf("%s differs from the source", safe(f.dst))
			}
			backup, err := preserve(f.dst, stamp)
			if err != nil {
				return err
			}
			rep.Preserved = append(rep.Preserved, f.dst+" -> "+backup)
		}
		if err := copyFile(f.src, f.dst, f.fi); err != nil {
			return err
		}
		rep.Copied++
	}
	return nil
}

// compareDest classifies dst against the source file: absent, identical (size
// first, then SHA-256), or different. A destination that is not a regular file
// (a symlink, a directory) is different.
func compareDest(src, dst string, sfi os.FileInfo) (fileState, error) {
	dfi, err := os.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return stateNew, nil
	}
	if err != nil {
		return 0, err
	}
	if !dfi.Mode().IsRegular() || dfi.Size() != sfi.Size() {
		return stateDiffers, nil
	}
	a, err := fileSHA256(src)
	if err != nil {
		return 0, err
	}
	b, err := fileSHA256(dst)
	if err != nil {
		return 0, err
	}
	if a == b {
		return stateSame, nil
	}
	return stateDiffers, nil
}

func fileSHA256(p string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(p)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// checkDestDir refuses a destination directory that is a symlink or not a
// directory; an absent one is fine (it will be created).
func checkDestDir(p string) error {
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("destination directory %s is a symlink — refusing to write through it", safe(p))
	case !fi.IsDir():
		return fmt.Errorf("destination %s exists and is not a directory", safe(p))
	}
	return nil
}

// ensureDir creates p (its parent already ensured) or confirms it is a real
// directory — re-checked at write time, never trusted from the scan.
func ensureDir(p string, perm os.FileMode) error {
	if err := checkDestDir(p); err != nil {
		return err
	}
	if err := os.Mkdir(p, perm); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return checkDestDir(p)
}

// maxNameBytes is the longest file name (one path element) the common
// filesystems accept; maxPreserveAttempts caps the -N suffixes preserve tries.
const (
	maxNameBytes        = 255
	maxPreserveAttempts = 100
)

// preserve renames dst aside to <dst>.pre-move-<stamp> (with a -N suffix when
// that name is taken) and returns the new name. Nothing is deleted. A name too
// long to carry the suffix is shortened deterministically (backupBaseName); a
// probe error other than not-exist stops the search instead of skipping past
// it, and so does running out of attempts.
func preserve(dst, stamp string) (string, error) {
	suffix := ".pre-move-" + stamp
	room := maxNameBytes - len(suffix) - len(fmt.Sprintf("-%d", maxPreserveAttempts-1))
	base := filepath.Join(filepath.Dir(dst), backupBaseName(filepath.Base(dst), room)+suffix)
	for n := 0; n < maxPreserveAttempts; n++ {
		backup := base
		if n > 0 {
			backup = fmt.Sprintf("%s-%d", base, n)
		}
		_, err := os.Lstat(backup)
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("probe backup name %s: %w", safe(backup), err)
		}
		if err := os.Rename(dst, backup); err != nil {
			return "", err
		}
		return backup, nil
	}
	return "", fmt.Errorf("no free backup name for %s after %d attempts", safe(dst), maxPreserveAttempts)
}

// backupBaseName returns name when it fits in room bytes; otherwise a prefix
// of it (cut on a UTF-8 boundary) plus "-" and 8 hex digits of its SHA-256,
// exactly room bytes or fewer — the same name always shortens the same way.
func backupBaseName(name string, room int) string {
	if len(name) <= room {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	tag := "-" + hex.EncodeToString(sum[:4])
	keep := max(room-len(tag), 0)
	for keep > 0 && !utf8.RuneStart(name[keep]) {
		keep--
	}
	return name[:keep] + tag
}

// copyFile writes src to a temp file in dst's directory (O_EXCL), gives it
// src's mode and mtime, fsyncs it and hard-links it into place (then drops the
// temp name). The temp file is removed on any error, so a partial copy never
// exists under dst's name; a dst that appeared since the scan is never
// replaced. Where hard links are unsupported it falls back to check+rename.
func copyFile(src, dst string, fi os.FileInfo) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".move-tmp-*")
	if err != nil {
		return err
	}
	tmpName, closed := tmp.Name(), false
	defer func() {
		if err != nil {
			if !closed {
				_ = tmp.Close()
			}
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(fi.Mode().Perm()); err != nil {
		return err
	}
	if _, err = io.Copy(tmp, in); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	closed = true
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chtimes(tmpName, fi.ModTime(), fi.ModTime()); err != nil {
		return err
	}
	// Link, unlike Rename, fails with EEXIST instead of replacing whatever
	// holds dst's name — so no check-then-rename window.
	err = linkIntoPlace(tmpName, dst)
	switch {
	case err == nil:
		if rerr := os.Remove(tmpName); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return fmt.Errorf("%s copied, but the temp file %s was not removed: %w", safe(dst), safe(tmpName), rerr)
		}
		return nil
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%s appeared during the move — not replaced", safe(dst))
	case !errors.Is(err, errors.ErrUnsupported) && !errors.Is(err, fs.ErrPermission):
		return err
	}
	// A filesystem without hard links: the best available is check, then rename.
	if _, lerr := os.Lstat(dst); !errors.Is(lerr, fs.ErrNotExist) {
		return fmt.Errorf("%s appeared during the move — not replaced", safe(dst))
	}
	return os.Rename(tmpName, dst)
}

// linkIntoPlace is os.Link; a test swaps it to act inside the write window.
var linkIntoPlace = os.Link

// openSessionsDB opens the database without migrating it. A missing or
// unopenable database is not an error for a move (the files are the session);
// the note says why the row cannot be read or updated.
func openSessionsDB(path string) (*sql.DB, string) {
	if path == "" {
		p, err := store.DefaultDBPath()
		if err != nil {
			return nil, "no database path"
		}
		path = p
	}
	db, err := store.OpenNoMigrate(path)
	if err != nil {
		return nil, "database not available at " + path
	}
	return db, ""
}

func readSessionRow(db *sql.DB, uuid string) (sessionRow, error) {
	if db == nil {
		return sessionRow{}, nil
	}
	var r sessionRow
	err := db.QueryRow(`
		SELECT COALESCE(account, ''), COALESCE(status, ''), COALESCE(proc_state, ''), COALESCE(cwd, '')
		  FROM sessions WHERE session_uuid = ?`, uuid).Scan(&r.account, &r.status, &r.procState, &r.cwd)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionRow{}, nil
	}
	if err != nil {
		return sessionRow{}, fmt.Errorf("read the sessions row: %w", err)
	}
	r.found = true
	if r.account == "" {
		r.account = defaultAccountKey
	}
	return r, nil
}

// ResumeCommand is the command that resumes the moved session. cwd is the only
// thing sessions.cwd / --cwd ever feed.
func (r MoveReport) ResumeCommand() string {
	path := r.Cwd
	if path == "" {
		path = "<the session's working directory>"
	}
	return "swarmery account exec --path " + shellQuote(path) + " -- claude --resume " + r.UUID
}

// ResumeAccountNote says what stands between the resume command and the moved
// session: a path that is not an existing directory (a mistyped --cwd, or a
// recorded cwd whose worktree is gone), or a path that resolves to another
// account, with how to bind it. "" when the path is fine or unknown.
func (r MoveReport) ResumeAccountNote() string {
	if r.Cwd == "" {
		return ""
	}
	// Checked first: binding a path that does not exist cannot help, and the
	// account note alone read as if the path were fine.
	if fi, err := os.Stat(r.Cwd); err != nil || !fi.IsDir() {
		return fmt.Sprintf("%s is not an existing directory — the resume command cannot run there; "+
			"rerun with --cwd <the directory the session ran in>", r.Cwd)
	}
	got := claudeacct.Resolve(r.Cwd).Account
	if got == "" {
		got = defaultAccountKey
	}
	if got == r.To {
		return ""
	}
	return fmt.Sprintf("%s resolves to account %s, not %s — bind it first: swarmery account use %s --path %s",
		r.Cwd, got, r.To, r.To, shellQuote(r.Cwd))
}

// Lines renders the report for stdout. Every line is passed through safe, so a
// directory name or cwd carrying control characters (an ESC sequence) cannot
// drive the terminal.
func (r MoveReport) Lines() []string {
	if r.NoOp {
		return []string{safe(r.Note)}
	}
	out := []string{
		fmt.Sprintf("session:     %s", r.UUID),
		fmt.Sprintf("account:     %s -> %s", r.From, r.To),
		fmt.Sprintf("located:     %s (directory name carried over byte-for-byte)", r.Name),
	}
	verb := "copied"
	if r.DryRun {
		verb = "would copy"
	}
	for _, it := range r.Items {
		out = append(out, fmt.Sprintf("%s %-11s %s -> %s", verb, it.Kind+":", it.Src, it.Dst))
	}
	for _, s := range r.Skipped {
		out = append(out, "skipped:     "+s)
	}
	for _, p := range r.Refused {
		out = append(out, "refused:     symlink not copied: "+p)
	}
	switch {
	case r.DryRun:
		out = append(out, "dry run:     nothing written")
	case r.Failed:
		out = append(out, fmt.Sprintf("files:       %d copied before the failure, %d already present (identical)", r.Copied, r.Kept))
		for _, p := range r.Preserved {
			out = append(out, "preserved:   "+p)
		}
		out = append(out, "database:    not updated — "+r.DBNote)
	default:
		out = append(out, fmt.Sprintf("files:       %d copied, %d already present (identical)", r.Copied, r.Kept))
		for _, p := range r.Preserved {
			out = append(out, "preserved:   "+p)
		}
		if r.RowUpdated {
			out = append(out, "database:    sessions row now under "+r.To)
		} else {
			out = append(out, "database:    not updated — "+r.DBNote)
		}
	}
	for _, w := range r.Warnings {
		out = append(out, "warning:     "+w)
	}
	out = append(out, "resume:      "+r.ResumeCommand(), "warning:     "+ForkWarning)
	if note := r.ResumeAccountNote(); note != "" {
		out = append(out, "note:        "+note)
	}
	for i := range out {
		out[i] = safe(out[i])
	}
	return out
}

// safe replaces every control character (ESC, CR, LF, C1 controls, DEL …)
// with '?' so a printed path cannot carry a terminal escape sequence.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// safeJoin renders a path list one per indented line, each path passed
// through safe (the separators are the only newlines that survive).
func safeJoin(paths []string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = safe(p)
	}
	return strings.Join(out, "\n  ")
}

func validUUID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func orNone(s string) string {
	if s == "" {
		return "state unknown"
	}
	return s
}

// shellQuote single-quotes s when it carries anything a shell would split or
// expand, so a printed command can be pasted as-is.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
