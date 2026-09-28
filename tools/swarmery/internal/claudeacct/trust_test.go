package claudeacct

// Tests for the trust boundary around the files this package reads and writes:
// the settings writer (S1), the walk's reader (S2) and the secret store's
// loader (S4). Every tree lives in a t.TempDir(), every value is a literal
// non-secret, and "not owned by you" is made observable through the currentUID
// seam — a test cannot chown without root.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeUID makes every file look foreign to the package for one test.
func fakeUID(t *testing.T) {
	t.Helper()
	prev := currentUID
	currentUID = func() int { return os.Geteuid() + 4242 }
	t.Cleanup(func() { currentUID = prev })
}

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// ── S1: the writer ───────────────────────────────────────────────────────────

// A settings file that is a SYMLINK is refused by name, by every writer — the
// account pin, the estate, and pin clearing — and the link's target is never
// touched.
func TestWriteSettings_RefusesASymlinkedSettingsFile(t *testing.T) {
	proj := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	const body = `{"swarmery":{"claudeAccount":"work"}}`
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, filepath.Join(proj, ".claude"))
	if err := os.Symlink(target, bindingPath(proj)); err != nil {
		t.Fatal(err)
	}
	for name, write := range map[string]func() error{
		"SetBinding":  func() error { return SetBinding(proj, "other") },
		"SetEstate":   func() error { return SetEstate(proj, "acme") },
		"clear a pin": func() error { return SetBinding(proj, "") },
	} {
		err := write()
		if err == nil || !strings.Contains(err.Error(), bindingPath(proj)) || !strings.Contains(err.Error(), "symlink") {
			t.Errorf("%s through a symlinked settings file: err = %v, want a refusal naming %s", name, err, bindingPath(proj))
		}
	}
	if got := string(readFile(t, target)); got != body {
		t.Fatalf("the link target was rewritten: %q", got)
	}
	if _, err := os.Lstat(bindingPath(proj) + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("a .bak was created beside a refused symlink: %v", err)
	}
}

// A .bak that is a symlink (planted before the first write) is refused rather
// than written through, and the settings file is left as it was.
func TestWriteSettings_RefusesASymlinkedBackup(t *testing.T) {
	proj := t.TempDir()
	path := writeSettingsFile(t, proj, `{"permissions":{}}`)
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".bak"); err != nil {
		t.Fatal(err)
	}
	err := SetBinding(proj, "work")
	if err == nil || !strings.Contains(err.Error(), path+".bak") {
		t.Fatalf("SetBinding with a symlinked .bak: err = %v, want a refusal naming the .bak", err)
	}
	if got := string(readFile(t, target)); got != "untouched" {
		t.Fatalf("the .bak link's target was written: %q", got)
	}
	if got := string(readFile(t, path)); got != `{"permissions":{}}` {
		t.Fatalf("the settings file changed after a refused write: %q", got)
	}
}

// The .bak is created 0600 (it may hold anything the settings file held); the
// file itself keeps its existing mode across the atomic replace, a new file is
// 0644, and no temp file is left behind.
func TestWriteSettings_BackupIsOwnerOnlyAndReplaceKeepsTheMode(t *testing.T) {
	proj := t.TempDir()
	path := writeSettingsFile(t, proj, `{"permissions":{}}`)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SetBinding(proj, "work"); err != nil {
		t.Fatal(err)
	}
	if m := mustMode(t, path+".bak"); m != 0o600 {
		t.Errorf(".bak mode = %04o, want 0600", m)
	}
	if m := mustMode(t, path); m != 0o640 {
		t.Errorf("settings mode after the replace = %04o, want the existing 0640 kept", m)
	}
	if Binding(proj) != "work" {
		t.Errorf("Binding after write = %q", Binding(proj))
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}

	fresh := t.TempDir()
	if err := SetEstate(fresh, "acme"); err != nil {
		t.Fatal(err)
	}
	if m := mustMode(t, bindingPath(fresh)); m != 0o644 {
		t.Errorf("a new settings file is %04o, want 0644", m)
	}
}

// ── S2: the walk's reader ────────────────────────────────────────────────────

// A binding file that is group/other-WRITABLE, a symlink (here to a large
// regular file outside the tree — the /dev/zero shape, bounded), or larger than
// the cap is "nothing declared here": the walk continues past it to a trusted
// ancestor, and Binding reads nothing from it.
func TestResolve_SkipsUntrustedBindingFiles(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "acme"})

	// group-writable
	gw := filepath.Join(root, "gw")
	declare(t, gw, map[string]any{"claudeAccount": "evil", "estate": "evil"})
	if err := os.Chmod(bindingPath(gw), 0o664); err != nil {
		t.Fatal(err)
	}
	// a symlink to a large regular file outside the tree that declares things
	big := filepath.Join(t.TempDir(), "big.json")
	pad := strings.Repeat(" ", maxSettingsBytes+16)
	if err := os.WriteFile(big, []byte(`{"swarmery":{"claudeAccount":"evil","estate":"evil"}}`+pad), 0o644); err != nil {
		t.Fatal(err)
	}
	ln := filepath.Join(root, "ln")
	mkdirs(t, filepath.Join(ln, ".claude"))
	if err := os.Symlink(big, bindingPath(ln)); err != nil {
		t.Fatal(err)
	}
	// a symlink to a SMALL valid file: the link itself is what is refused
	small := filepath.Join(t.TempDir(), "small.json")
	if err := os.WriteFile(small, []byte(`{"swarmery":{"claudeAccount":"evil","estate":"evil"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sl := filepath.Join(root, "sl")
	mkdirs(t, filepath.Join(sl, ".claude"))
	if err := os.Symlink(small, bindingPath(sl)); err != nil {
		t.Fatal(err)
	}
	// an over-cap regular file
	huge := filepath.Join(root, "huge")
	writeSettingsFile(t, huge, `{"swarmery":{"claudeAccount":"evil","estate":"evil"}}`+pad)

	for _, dir := range []string{gw, ln, sl, huge} {
		proj := filepath.Join(dir, "p")
		mkdirs(t, proj)
		r := Resolve(proj)
		if r.Account != "work" || r.Estate != "acme" || r.AccountRoot != root || r.EstateRoot != root {
			t.Errorf("%s: resolved %q/%q at %q/%q, want work/acme from %s — an untrusted file was read",
				filepath.Base(dir), r.Account, r.Estate, r.AccountRoot, r.EstateRoot, root)
		}
		if got := Binding(dir); got != "" {
			t.Errorf("Binding(%s) = %q, want \"\"", filepath.Base(dir), got)
		}
		if key, _ := Estate(dir); key != "" {
			t.Errorf("Estate(%s) = %q, want \"\"", filepath.Base(dir), key)
		}
	}
	// ShadowingPins reads through Binding: none of the untrusted pins is listed.
	if pins := ShadowingPins(root); len(pins) != 0 {
		t.Errorf("ShadowingPins listed untrusted files: %v", pins)
	}
	// ScanPins is the same walk, and it reports every one it skipped — sorted,
	// by path and reason, never by content.
	pins, skipped := ScanPins(root)
	if len(pins) != 0 || len(skipped) != 4 {
		t.Fatalf("ScanPins = %v / %v, want no pins and the 4 untrusted files", pins, skipped)
	}
	for i, dir := range []string{gw, huge, ln, sl} { // sorted by path
		if !strings.HasPrefix(skipped[i], bindingPath(dir)+" ") {
			t.Errorf("skipped[%d] = %q, want the reason for %s", i, skipped[i], bindingPath(dir))
		}
		if strings.Contains(skipped[i], "evil") {
			t.Errorf("skipped[%d] leaked file contents: %q", i, skipped[i])
		}
	}
}

// A binding file not owned by the current user is not read, and the walk stops
// at the first existing directory not owned by the current user — a
// not-yet-created path below it still walks up to it.
func TestResolve_StopsAtWhatTheUserDoesNotOwn(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	declare(t, root, map[string]any{"claudeAccount": "work", "estate": "acme"})
	if r := Resolve(root); r.Estate != "acme" {
		t.Fatalf("precondition: %+v", r)
	}

	fakeUID(t)
	if got := Binding(root); got != "" {
		t.Errorf("Binding of a foreign-owned file = %q, want \"\"", got)
	}
	if r := Resolve(root); r.Account != "" || r.Estate != "" {
		t.Errorf("Resolve under a foreign-owned tree = %+v, want nothing", r)
	}
	missing := filepath.Join(root, "not", "yet")
	if got, want := ladder(missing), []string{missing, filepath.Join(root, "not")}; !slices.Equal(got, want) {
		t.Errorf("ladder = %v, want %v — the walk must stop at the first existing foreign directory", got, want)
	}
}

// For real: a root-owned system directory ends the walk (outside $HOME there is
// no other bound but the filesystem root).
func TestLadder_StopsBelowARootOwnedDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: every directory is owned by the current user")
	}
	fakeHome(t)
	p := filepath.Join("/usr", "bin", "no-such-swarmery-dir")
	if got := ladder(p); !slices.Equal(got, []string{p}) {
		t.Fatalf("ladder(%s) = %v, want only the path itself — /usr/bin is root-owned", p, got)
	}
}

// ── S4: the secret store's loader ────────────────────────────────────────────

// A symlinked store is refused (O_NOFOLLOW), logged by path, and nothing of its
// content — here a 0600 file the link points at — is loaded or logged.
func TestSecretEnv_RefusesASymlinkedStore(t *testing.T) {
	dir := seedStores(t, map[string]string{})
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "real.env")
	if err := os.WriteFile(target, []byte("LINKED_NAME=linked-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "work.env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var got []string
	logged := captureLog(t, func() { got = SecretEnvForStore("work") })
	if got != nil {
		t.Fatalf("a symlinked store yielded %d variables, want none", len(got))
	}
	if !strings.Contains(logged, link) || !strings.Contains(logged, "symlink") {
		t.Errorf("log %q does not name the refused link", logged)
	}
	if strings.Contains(logged, "LINKED_NAME") || strings.Contains(logged, "linked-value") {
		t.Errorf("log leaked store content: %q", logged)
	}
}

// A store DIRECTORY open to group or other is refused even when the file is
// 0600: whoever can write the directory can swap the file. Logged by path and
// mode only. A missing store in such a directory stays silent — nothing to load.
func TestSecretEnv_RefusesAnOpenStoreDirectory(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o750, 0o711, 0o770} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := seedStores(t, map[string]string{"work": "DIR_NAME=dir-value\n"})
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			var got []string
			logged := captureLog(t, func() { got = SecretEnvForStore("work") })
			if got != nil {
				t.Fatalf("store dir %04o yielded %d variables, want none", mode, len(got))
			}
			if !strings.Contains(logged, dir) || !strings.Contains(logged, "mode") {
				t.Errorf("log %q does not name the directory and its mode", logged)
			}
			if strings.Contains(logged, "DIR_NAME") || strings.Contains(logged, "dir-value") {
				t.Errorf("log leaked store content: %q", logged)
			}
			if logged := captureLog(t, func() { _ = SecretEnvForStore("absent") }); logged != "" {
				t.Errorf("a missing store logged %q — absence is healthy and silent", logged)
			}
		})
	}
}

// A store (and its directory) not owned by the current user is refused.
func TestSecretEnv_RefusesAForeignOwnedStore(t *testing.T) {
	dir := seedStores(t, map[string]string{"work": "OWN_NAME=own-value\n"})
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := SecretEnvForStore("work"); len(got) != 1 {
		t.Fatalf("precondition: an owner-only store yielded %d variables", len(got))
	}
	fakeUID(t)
	var got []string
	logged := captureLog(t, func() { got = SecretEnvForStore("work") })
	if got != nil {
		t.Fatalf("a foreign-owned store yielded %d variables, want none", len(got))
	}
	if !strings.Contains(logged, "not owned by the current user") {
		t.Errorf("log %q does not say why", logged)
	}
	if strings.Contains(logged, "OWN_NAME") || strings.Contains(logged, "own-value") {
		t.Errorf("log leaked store content: %q", logged)
	}
	// The FILE's owner check on its own: the directory check (made first) sees
	// the real uid and passes, the opened file's check sees a foreign one.
	calls := 0
	currentUID = func() int {
		calls++
		if calls == 1 {
			return os.Geteuid()
		}
		return os.Geteuid() + 4242
	}
	logged = captureLog(t, func() { got = SecretEnvForStore("work") })
	if got != nil || calls != 2 {
		t.Fatalf("file-owner check: %d variables after %d uid checks, want none after 2", len(got), calls)
	}
	if !strings.Contains(logged, filepath.Join(dir, "work.env")) || !strings.Contains(logged, "0600") {
		t.Errorf("file-owner refusal %q does not name the file and its mode", logged)
	}
}

// ── R1 support: DeclarationUnreadable ────────────────────────────────────────

func TestDeclarationUnreadable(t *testing.T) {
	home := fakeHome(t)
	proj := filepath.Join(home, "projects", "p")
	mkdirs(t, proj)
	if why := DeclarationUnreadable(proj); why != "" {
		t.Fatalf("an ordinary project: %q, want readable", why)
	}
	if why := DeclarationUnreadable(home); !strings.Contains(why, "home directory") {
		t.Errorf("$HOME: %q, want the home-directory reason", why)
	}
	if why := DeclarationUnreadable(string(filepath.Separator)); why == "" {
		t.Error("the filesystem root was accepted")
	}
	src := filepath.Join(home, "projects", "acme", "repo")
	mkdirs(t, src)
	wt := wtPath(home, src, "T-1")
	linkWorktree(t, src, wt, "T-1")
	if why := DeclarationUnreadable(wt); !strings.Contains(why, "daemon worktree") || !strings.Contains(why, src) {
		t.Errorf("a daemon worktree: %q, want the worktree reason naming %s", why, src)
	}
	writeSettingsFile(t, proj, `{}`)
	if err := os.Chmod(bindingPath(proj), 0o666); err != nil {
		t.Fatal(err)
	}
	if why := DeclarationUnreadable(proj); !strings.Contains(why, "group or other") {
		t.Errorf("a group-writable binding file: %q, want the mode reason", why)
	}
	if why := DeclarationUnreadable(" "); why == "" {
		t.Error("an empty path was accepted")
	}
}

// ── R3: the settings dedup is decided against the SOURCE for a worktree ──────

// A worktree cwd whose source checkout declares the estate on itself: the
// estate's settings file IS the project's own, so it must count once — decided
// against the source checkout the walk ran from, not the worktree path (which
// has no settings.json of its own).
func TestResolve_WorktreeDedupsAgainstItsSource(t *testing.T) {
	home := fakeHome(t)
	src := filepath.Join(home, "projects", "code")
	declare(t, src, map[string]any{"estate": "code"})
	writeFile(t, filepath.Join(src, ".claude", "settings.json"), "{}\n")
	anchorStore(t, "code", src) // D5: only an admitted estate contributes its settings file
	wt := wtPath(home, src, "phase-2")
	linkWorktree(t, src, wt, "phase-2")

	r := Resolve(wt)
	if r.Estate != "code" || r.SettingsFile == "" {
		t.Fatalf("worktree resolved %+v, want estate code with its settings file", r)
	}
	if !r.EstateSettingsIsProjects {
		t.Fatal("EstateSettingsIsProjects = false for a worktree of the estate root — compared against the worktree path, not its source")
	}
}
