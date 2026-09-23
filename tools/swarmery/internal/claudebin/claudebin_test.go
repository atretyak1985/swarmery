package claudebin

// Every test here is hermetic: no real `claude` binary is required and none is
// consulted. isolate() redirects all three resolution inputs (the override env,
// PATH, HOME) plus the machine-wide probe dirs into a temp tree, so the outcome
// depends only on files the test itself wrote — on any OS, with no
// build-target branch anywhere.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// isolate points every ambient lookup at hermetic temp state and returns the
// fake HOME and the single fake system probe dir.
func isolate(t *testing.T) (home, sysDir string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	sysDir = filepath.Join(root, "sys")
	mkdir(t, home)
	mkdir(t, sysDir)

	t.Setenv("SWARMERY_CLAUDE_BIN", "")
	// A directory that exists but holds nothing: LookPath must miss.
	emptyPath := filepath.Join(root, "emptypath")
	mkdir(t, emptyPath)
	t.Setenv("PATH", emptyPath)
	t.Setenv("HOME", home)
	// The shim dir defaults under the fake HOME; clear any ambient override.
	t.Setenv("SWARMERY_BIN_DIR", "")

	prev := systemProbeDirs
	systemProbeDirs = []string{sysDir}
	t.Cleanup(func() { systemProbeDirs = prev })
	return home, sysDir
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeBin creates dir/claude with mode and returns its path.
func writeBin(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	mkdir(t, dir)
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile honours umask, so force the mode we actually mean to test.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveOverrideWins(t *testing.T) {
	home, sysDir := isolate(t)
	// Plant a resolvable claude everywhere else; the override must still win.
	writeBin(t, sysDir, 0o755)
	writeBin(t, filepath.Join(home, ".local", "bin"), 0o755)

	t.Setenv("SWARMERY_CLAUDE_BIN", "  /custom/claude  ")
	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/custom/claude" {
		t.Errorf("Resolve = %q, want the trimmed override /custom/claude", got)
	}
}

func TestResolveFromPATH(t *testing.T) {
	home, _ := isolate(t)
	pathDir := t.TempDir()
	want := writeBin(t, pathDir, 0o755)
	t.Setenv("PATH", pathDir)
	// A home candidate exists too — PATH is checked first, so it must not win.
	writeBin(t, filepath.Join(home, ".local", "bin"), 0o755)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want the PATH hit %q", got, want)
	}
}

func TestResolveFromHomeCandidate(t *testing.T) {
	home, _ := isolate(t)
	want := writeBin(t, filepath.Join(home, ".local", "bin"), 0o755)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want the home candidate %q", got, want)
	}
}

// TestResolveProbeOrder pins the documented order: the system dirs are probed
// before the home-relative ones.
func TestResolveProbeOrder(t *testing.T) {
	home, sysDir := isolate(t)
	want := writeBin(t, sysDir, 0o755)
	writeBin(t, filepath.Join(home, ".local", "bin"), 0o755)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want the system-dir hit %q (system dirs precede home)", got, want)
	}
}

// TestResolveSkipsUnusableCandidates proves the executability and
// not-a-directory guards: a 0o644 file and a directory named `claude` are both
// rejected, and probing continues to the next candidate.
func TestResolveSkipsUnusableCandidates(t *testing.T) {
	home, sysDir := isolate(t)
	// Candidate 1 (system dir): a directory named claude.
	mkdir(t, filepath.Join(sysDir, "claude"))
	// Candidate 2 (~/.claude/local): present but not executable.
	notExec := writeBin(t, filepath.Join(home, ".claude", "local"), 0o644)
	// Candidate 3 (~/.local/bin): the first usable one.
	want := writeBin(t, filepath.Join(home, ".local", "bin"), 0o755)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == notExec {
		t.Fatalf("Resolve returned a non-executable candidate %q", got)
	}
	if got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

func TestResolveNotFound(t *testing.T) {
	isolate(t)

	got, err := Resolve()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve error = %v, want ErrNotFound", err)
	}
	if got != "" {
		t.Errorf("Resolve path = %q, want empty on failure", got)
	}
	if want := "claude not found in PATH or common install locations"; err.Error() != want {
		t.Errorf("ErrNotFound message = %q, want the message the ported resolver used: %q", err.Error(), want)
	}
}

// ── the PATH shim is never the answer (risk R5) ─────────────────────────────
//
// plugins/accounts-pack installs a `claude` shim into ShimDir, which the
// operator's profile puts FIRST on PATH. A daemon spawn that resolved it would
// re-enter `swarmery account exec` and discard the account the Go code already
// resolved. These cases pin every path around it.

// shimDir points ShimDir at a fresh dir via SWARMERY_BIN_DIR and returns it.
func shimDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "shimbin")
	mkdir(t, d)
	t.Setenv("SWARMERY_BIN_DIR", d)
	return d
}

func TestShimDirDefaultsUnderHome(t *testing.T) {
	home, _ := isolate(t)
	if got, want := ShimDir(), filepath.Join(home, ".swarmery", "bin"); got != want {
		t.Errorf("ShimDir = %q, want %q", got, want)
	}
	t.Setenv("SWARMERY_BIN_DIR", "/x/y/")
	if got := ShimDir(); got != "/x/y" {
		t.Errorf("ShimDir with override = %q, want /x/y", got)
	}
}

func TestResolveSkipsShimAsPathHit(t *testing.T) {
	isolate(t)
	shim := shimDir(t)
	writeBin(t, shim, 0o755)
	realDir := t.TempDir()
	want := writeBin(t, realDir, 0o755)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+realDir)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want the next PATH hit %q (the shim must be skipped)", got, want)
	}
}

func TestResolveSkipsShimAsProbeCandidate(t *testing.T) {
	home, sysDir := isolate(t)
	// The shim dir IS the first probe dir.
	t.Setenv("SWARMERY_BIN_DIR", sysDir)
	writeBin(t, sysDir, 0o755)
	want := writeBin(t, filepath.Join(home, ".local", "bin"), 0o755)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want the next probe candidate %q", got, want)
	}
}

func TestResolveSkipsSymlinkInsideShimDir(t *testing.T) {
	home, _ := isolate(t)
	shim := shimDir(t)
	elsewhere := writeBin(t, t.TempDir(), 0o755)
	if err := os.Symlink(elsewhere, filepath.Join(shim, "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim)
	want := writeBin(t, filepath.Join(home, ".local", "bin"), 0o755)

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want %q (a symlink found in the shim dir is skipped)", got, want)
	}
}

func TestResolveSkipsShimDirReachedThroughSymlinkedPathEntry(t *testing.T) {
	isolate(t)
	shim := shimDir(t)
	writeBin(t, shim, 0o755)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(shim, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", alias)

	if got, err := Resolve(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve = %q, %v; want ErrNotFound (the alias dir IS the shim dir)", got, err)
	}
}

func TestResolveHonoursOverrideIntoShimDir(t *testing.T) {
	isolate(t)
	shim := shimDir(t)
	p := writeBin(t, shim, 0o755)
	t.Setenv("SWARMERY_CLAUDE_BIN", p)

	got, err := Resolve()
	if err != nil || got != p {
		t.Errorf("Resolve = %q, %v; want the explicit override %q honoured", got, err, p)
	}
}

func TestResolveShimOnlyIsNotFound(t *testing.T) {
	isolate(t)
	shim := shimDir(t)
	writeBin(t, shim, 0o755)
	t.Setenv("PATH", shim)

	got, err := Resolve()
	if !errors.Is(err, ErrNotFound) || got != "" {
		t.Errorf("Resolve = %q, %v; want ErrNotFound when the shim is the only candidate", got, err)
	}
}

func TestLookPathSkippingShim(t *testing.T) {
	isolate(t)
	shim := shimDir(t)
	inShim := writeBin(t, shim, 0o755)

	if _, err := LookPathSkippingShim(inShim); err == nil {
		t.Errorf("LookPathSkippingShim(%q) = nil error, want a refusal for a path in the shim dir", inShim)
	}
	realBin := writeBin(t, t.TempDir(), 0o755)
	if got, err := LookPathSkippingShim(realBin); err != nil || got != realBin {
		t.Errorf("LookPathSkippingShim(abs real) = %q, %v; want it verbatim", got, err)
	}
	t.Setenv("PATH", "relative/dir"+string(os.PathListSeparator)+shim)
	if _, err := LookPathSkippingShim("claude"); err == nil {
		t.Error("LookPathSkippingShim resolved through a relative PATH entry or the shim")
	}
	if UnderShimDir("") {
		t.Error("UnderShimDir(\"\") = true")
	}
}
