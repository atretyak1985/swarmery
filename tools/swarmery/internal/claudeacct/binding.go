package claudeacct

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// BindingFile is the per-project, NOT-shared settings file the binding lives in.
// It is the same tier internal/hookcfg writes to (D2 there): machine-local,
// gitignored, never carrying a decision that belongs to the whole team. Two
// engineers on one repo legitimately have different Claude accounts.
const BindingFile = ".claude/settings.local.json"

// The namespaced key inside that file:
//
//	{ "swarmery": { "claudeAccount": "nabu-org" } }
//
// A namespace of our own, because the file's other keys belong to Claude Code's
// own settings schema and must not be shadowed.
const (
	bindingNamespace = "swarmery"
	bindingField     = "claudeAccount"
)

// bindingPath is <project>/.claude/settings.local.json.
func bindingPath(projectPath string) string {
	return filepath.Join(projectPath, filepath.FromSlash(BindingFile))
}

// Binding returns the account key bound to projectPath, or "" when the project
// has no binding. A missing or malformed file is NOT an error: an unmanaged or
// hand-broken settings file must mean "default account", never a failed spawn.
//
// A binding whose value fails ValidKey is treated exactly like a broken file —
// "" — so a hand-edited key can never reach a path join. So is a file the
// current user does not own, one group/other can write, a symlink, or one over
// maxSettingsBytes (readTrustedSettings).
func Binding(projectPath string) string {
	root := readTrustedSettings(bindingPath(projectPath))
	if root == nil {
		return ""
	}
	ns, _ := root[bindingNamespace].(map[string]any)
	key, _ := ns[bindingField].(string)
	key = strings.TrimSpace(key)
	if !ValidKey(key) {
		return ""
	}
	if why := bindingDistrusted(bindingPath(projectPath)); why != "" {
		logDistrusted(bindingPath(projectPath), why)
		return ""
	}
	return key
}

// SetBinding writes the binding, following internal/hookcfg's surgery rules
// verbatim: read-modify-write through map[string]any so every foreign key
// survives, abort WITHOUT writing on unparseable JSON, copy to .bak before the
// first write, and be idempotent (a second identical call produces no diff).
// An empty account clears the binding (and prunes the empty "swarmery" object).
//
// Clearing a binding that is not there — and setting the one already stored —
// touch the file not at all, so SetBinding never reformats a settings file it
// has nothing to say about.
func SetBinding(projectPath, account string) error {
	account = strings.TrimSpace(account)
	if account != "" && !ValidKey(account) {
		return fmt.Errorf("claudeacct: %q is not a valid account key", account)
	}

	path := bindingPath(projectPath)
	raw, root, existed, err := readSettings(path)
	if err != nil {
		return err
	}
	ns, _ := root[bindingNamespace].(map[string]any)

	if account == "" {
		if ns == nil {
			return nil // nothing of ours in there — leave the file alone
		}
		if _, ok := ns[bindingField]; !ok {
			return nil
		}
		delete(ns, bindingField)
		if len(ns) == 0 {
			delete(root, bindingNamespace)
		}
	} else {
		if cur, _ := ns[bindingField].(string); cur == account {
			return nil // already bound — no write, no reformat
		}
		if ns == nil {
			ns = map[string]any{}
			root[bindingNamespace] = ns
		}
		ns[bindingField] = account
	}

	return writeSettings(path, raw, root, existed)
}

// VerifyBinding reads the binding at projectPath back after a write and reports
// an error when it is not `want` — the writer's last line of defence against a
// write that "succeeded" into a file the READ side ignores. The typical cause is
// a file SetBinding had nothing to change in (already bound, so not rewritten)
// that group/other can write or another user owns; the error names the file and
// that reason, never its contents. want "" (a clear) always reads back.
func VerifyBinding(projectPath, want string) error {
	want = strings.TrimSpace(want)
	got := Binding(projectPath)
	if got == want {
		return nil
	}
	path := bindingPath(projectPath)
	if why := untrustedSettings(path); why != "" {
		return fmt.Errorf("the binding %s was written but does not take effect: %s — "+
			"fix the file (chmod go-w it, or replace one you do not own) and retry", want, why)
	}
	return fmt.Errorf("the binding %s was written to %s but reads back as %q", want, path, got)
}

// BindingFileUntrusted says why projectPath's OWN binding file exists but is
// ignored by every reader (a symlink, not a regular file, writable by group or
// other, over the size cap, not owned by you) — "" when it is absent or would
// be read. The reason names the path, never the contents.
func BindingFileUntrusted(projectPath string) string {
	if strings.TrimSpace(projectPath) == "" {
		return ""
	}
	return untrustedSettings(bindingPath(projectPath))
}

// BindingFileExists reports whether projectPath's binding file exists at all
// (Lstat: a symlink counts as existing).
func BindingFileExists(projectPath string) bool {
	if strings.TrimSpace(projectPath) == "" {
		return false
	}
	_, err := os.Lstat(bindingPath(projectPath))
	return err == nil
}

// EnvFor is what a spawner actually needs: the env DELTA for this project, to be
// appended to os.Environ(). Returns nil for an unbound project and for the
// default account — see the package doc for why that must stay empty.
func EnvFor(projectPath string) []string {
	return EnvForAccount(Binding(projectPath))
}

// EnvForAccount is EnvFor when the caller already resolved the key. Dispatch and
// verify MUST use this one: they run in a worktree whose path has no project
// settings file, so resolving from cwd there would silently yield the default
// account (see plan A3).
//
// The dir comes from Discover when the account exists, so an account living in a
// non-canonical dir still gets pointed at its real credentials; ConfigDirFor is
// the fallback for an account being provisioned. An unresolvable key yields nil
// — the default account — because a silent spawn under the default beats a
// failed spawn.
func EnvForAccount(key string) []string {
	dir, ok := ConfigDirForAccount(key)
	if !ok {
		return nil
	}
	return []string{configDirEnv + "=" + dir}
}

// ConfigDirForAccount is the config dir a spawn under `key` actually reads —
// the same answer EnvForAccount encodes as CLAUDE_CONFIG_DIR, exposed for the
// callers that must touch that dir's files directly (a settings.json snapshot
// before a user-scope install, a plugin cache lookup). Discover wins over the
// canonical path so an account living in a non-canonical dir is still found.
// ok=false for the default account and for an unresolvable key: the caller then
// keeps the default ~/.claude it already holds, which is exactly what the spawn
// falls back to.
func ConfigDirForAccount(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" || key == ingest.DefaultAccount {
		return "", false
	}
	for _, a := range Discover() {
		if a.Key == key {
			return a.ConfigDir, true
		}
	}
	dir, err := ConfigDirFor(key)
	if err != nil {
		return "", false
	}
	return dir, true
}

// ConfigDirForProject is ConfigDirForAccount over the project's own binding.
func ConfigDirForProject(projectPath string) (string, bool) {
	return ConfigDirForAccount(Binding(projectPath))
}

// ── settings surgery helpers (internal/hookcfg's, verbatim in behaviour) ──────

// maxSettingsBytes caps how much of one settings file any reader takes. A real
// settings.local.json is a few KiB; anything past the cap is treated as
// unparseable, so no input — a symlink to /dev/zero, a runaway file — can make a
// read unbounded.
const maxSettingsBytes = 1 << 20

// settingsUntrustedBits are the permission bits that make a settings file
// untrusted to the READ side (group or other may write it). The writer never
// rewrites a file carrying them (ErrUntrustedSettings), so the two sides cannot
// disagree.
const settingsUntrustedBits = 0o022

// ErrUntrustedSettings is wrapped by every settings WRITE that refuses an
// existing file the read side ignores (untrustedSettings). Rewriting such a file
// would hand it a trusted mode and put everything else in it into effect — so
// it is the operator's to fix, never the writer's. Callers match it with
// errors.Is to tell "fix your file" from a malformed request.
var ErrUntrustedSettings = errors.New("untrusted settings file")

// currentUID is the uid every file the READ side trusts must be owned by, and
// the uid a secret store and its directory must be owned by. A package var only
// so a test can make "not owned by you" observable without root.
var currentUID = os.Geteuid

// fileOwner is fi's owning uid; ok=false where the platform does not say.
func fileOwner(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// openNoFollow opens path read-only, refusing a symlink as its FINAL component
// (ELOOP) and never blocking on a FIFO swapped in under it.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// readCapped reads at most limit bytes of f; ok=false when there were more.
func readCapped(f *os.File, limit int64) (raw []byte, ok bool, err error) {
	raw, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, err
	}
	return raw, int64(len(raw)) <= limit, nil
}

// readTrustedSettings is the READ side's loader — Binding, the upward walk, the
// pin scan. It yields the parsed root only for a file that is a REGULAR file
// (not a symlink, a FIFO or a device), owned by currentUID, not writable by
// group or other, and no larger than maxSettingsBytes. Anything else is "nothing
// declared here" (nil), never an error: a file someone else could have written
// must not choose the account or the credentials this user's spawn runs with.
// The checks are made on the OPENED file, so nothing can be swapped in between.
func readTrustedSettings(path string) map[string]any {
	f, err := openNoFollow(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&settingsUntrustedBits != 0 {
		return nil
	}
	if uid, ok := fileOwner(fi); !ok || uid != currentUID() {
		return nil
	}
	raw, ok, err := readCapped(f, maxSettingsBytes)
	if err != nil || !ok {
		return nil
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil
	}
	return root
}

// untrustedSettings reports why an EXISTING settings file would be ignored by
// readTrustedSettings ("" when it exists and would be read, or does not exist).
func untrustedSettings(path string) string {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("%s cannot be inspected (%v)", path, err)
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Sprintf("%s is a symlink, which the walk never follows", path)
	case !fi.Mode().IsRegular():
		return fmt.Sprintf("%s is not a regular file", path)
	case fi.Mode().Perm()&settingsUntrustedBits != 0:
		return fmt.Sprintf("%s is writable by group or other (mode %04o), so the walk ignores it", path, fi.Mode().Perm())
	case fi.Size() > maxSettingsBytes:
		return fmt.Sprintf("%s is larger than %d bytes, so the walk ignores it", path, maxSettingsBytes)
	}
	if uid, ok := fileOwner(fi); !ok || uid != currentUID() {
		return fmt.Sprintf("%s is not owned by you, so the walk ignores it", path)
	}
	return ""
}

// readSettings loads and parses the settings file for a WRITE. A missing file
// yields an empty root; a symlink, a non-regular file, an over-cap file or a
// parse failure aborts (never write over a file we cannot read, and never write
// THROUGH a link to somewhere else).
func readSettings(path string) (raw []byte, root map[string]any, existed bool, err error) {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, map[string]any{}, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, nil, true, fmt.Errorf(
			"%s is a symlink — refusing to read or rewrite it; replace it with a regular file and retry", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, true, fmt.Errorf("%s is not a regular file — aborting without writing", path)
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, nil, true, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	raw, ok, err := readCapped(f, maxSettingsBytes)
	if err != nil {
		return nil, nil, true, fmt.Errorf("read %s: %w", path, err)
	}
	if !ok {
		return nil, nil, true, fmt.Errorf(
			"%s is larger than %d bytes — aborting without writing; fix or remove the file and retry", path, maxSettingsBytes)
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, true, fmt.Errorf(
			"%s is not valid JSON (%v) — aborting without writing; fix or remove the file and retry", path, err)
	}
	return raw, root, true, nil
}

// writeSettings marshals root (2-space indent, trailing newline) and writes it
// if it differs from the original bytes. The original is preserved as .bak
// (created exclusively, 0600 — it can hold whatever the settings file held)
// before the FIRST swarmery write. The file itself is replaced ATOMICALLY: a
// temp file in the same directory, fsync'd, then renamed over it, keeping the
// existing file's mode (0644 for a new one). An EXISTING file the read side
// would ignore — writable by group or other, not owned by you, over the cap
// (untrustedSettings) — is REFUSED with ErrUntrustedSettings, never rewritten:
// the rewrite would promote it to trusted and activate whatever else it holds.
// So a file this writer produces is always one readTrustedSettings accepts, and
// the kept mode can never carry group/other write. A settings file or .bak that
// is a symlink is refused by name — the write would otherwise land wherever it
// points.
func writeSettings(path string, raw []byte, root map[string]any, existed bool) error {
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if existed && bytes.Equal(out, raw) {
		return nil
	}
	mode := os.FileMode(0o644)
	fi, err := os.Lstat(path)
	switch {
	case err == nil && fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink — refusing to write through it", path)
	case err == nil && !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file — refusing to write it", path)
	case err == nil:
		if why := untrustedSettings(path); why != "" {
			return fmt.Errorf("%w: refusing to rewrite it — %s; rewriting would make it trusted and put "+
				"everything in it into effect. Fix it yourself (chmod go-w %s, or replace a file you do "+
				"not own) and re-run", ErrUntrustedSettings, why, path)
		}
		mode = fi.Mode().Perm()
	case !os.IsNotExist(err):
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if existed {
		if err := writeBackupOnce(path+".bak", raw); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, out, mode)
}

// writeBackupOnce creates bak holding raw, exclusively and owner-only. An
// existing regular .bak is the FIRST write's backup and is kept; an existing
// symlink is refused rather than written through.
func writeBackupOnce(bak string, raw []byte) error {
	fi, err := os.Lstat(bak)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup %s is a symlink — refusing to write through it", bak)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat backup %s: %w", bak, err)
	}
	f, err := os.OpenFile(bak, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write backup %s: %w", bak, err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fmt.Errorf("write backup %s: %w", bak, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write backup %s: %w", bak, err)
	}
	return nil
}

// writeAtomic replaces path with data: temp file in the same directory, chmod,
// fsync, close, rename. A reader sees the old file or the new one, never half.
func writeAtomic(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
