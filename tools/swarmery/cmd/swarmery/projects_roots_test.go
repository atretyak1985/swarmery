package main

// Resolution order for the ingest transcript roots (defaultProjectsRoots):
// SWARMERY_PROJECTS_ROOTS (plural, 'auto' = glob) → SWARMERY_PROJECTS_ROOT
// (legacy singular) → ~/.claude/projects alone. The last rule is the
// configure-nothing default and must stay byte-identical to the single-root
// daemon these tests replaced.

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// isolatedHome points HOME at a temp dir (os.UserHomeDir reads it) and clears
// both root env vars, so each case starts from "nothing configured".
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SWARMERY_PROJECTS_ROOTS", "")
	t.Setenv("SWARMERY_PROJECTS_ROOT", "")
	return home
}

func TestDefaultProjectsRootsFallsBackToTheStockRoot(t *testing.T) {
	home := isolatedHome(t)
	want := []string{filepath.Join(home, ".claude", "projects")}
	if got := defaultProjectsRoots(); !slices.Equal(got, want) {
		t.Errorf("defaultProjectsRoots() = %v, want %v", got, want)
	}
}

func TestDefaultProjectsRootsHonorsLegacySingularEnv(t *testing.T) {
	isolatedHome(t)
	t.Setenv("SWARMERY_PROJECTS_ROOT", "/srv/transcripts")
	want := []string{"/srv/transcripts"}
	if got := defaultProjectsRoots(); !slices.Equal(got, want) {
		t.Errorf("defaultProjectsRoots() = %v, want %v", got, want)
	}
}

// The plural env is a comma-separated list: blanks dropped, order kept,
// duplicates collapsed — and it outranks the legacy singular.
func TestDefaultProjectsRootsParsesPluralEnv(t *testing.T) {
	isolatedHome(t)
	t.Setenv("SWARMERY_PROJECTS_ROOT", "/legacy/ignored")
	t.Setenv("SWARMERY_PROJECTS_ROOTS", " /a/projects , /b/projects ,, /a/projects ")
	want := []string{"/a/projects", "/b/projects"}
	if got := defaultProjectsRoots(); !slices.Equal(got, want) {
		t.Errorf("defaultProjectsRoots() = %v, want %v", got, want)
	}
}

// 'auto' globs $HOME/.claude*/projects and keeps only existing DIRECTORIES,
// sorted — the CLAUDE_CONFIG_DIR multi-subscription layout.
func TestDefaultProjectsRootsAutoGlobsClaudeConfigDirs(t *testing.T) {
	home := isolatedHome(t)
	for _, d := range []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude-work", "projects"),
		filepath.Join(home, ".claude-empty"),       // config dir with no projects/ yet
		filepath.Join(home, ".config", "projects"), // not a .claude* dir
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A FILE named projects under a .claude* dir must not be taken for a root.
	if err := os.MkdirAll(filepath.Join(home, ".claude-file"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude-file", "projects"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SWARMERY_PROJECTS_ROOTS", "auto")
	want := []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude-work", "projects"),
	}
	if got := defaultProjectsRoots(); !slices.Equal(got, want) {
		t.Errorf("defaultProjectsRoots() = %v, want %v", got, want)
	}
}

// 'auto' on a machine with no Claude Code config dir at all degrades to the
// stock root, so the failure names a concrete path instead of an empty list.
func TestDefaultProjectsRootsAutoFallsBackWhenNothingMatches(t *testing.T) {
	home := isolatedHome(t)
	t.Setenv("SWARMERY_PROJECTS_ROOTS", "auto")
	want := []string{filepath.Join(home, ".claude", "projects")}
	if got := defaultProjectsRoots(); !slices.Equal(got, want) {
		t.Errorf("defaultProjectsRoots() = %v, want %v", got, want)
	}
}

// --projects-root is comma-separated AND repeatable; the first occurrence
// REPLACES the env/HOME default so an explicit flag never inherits a root the
// caller did not name.
func TestProjectsRootFlagReplacesDefaultThenAppends(t *testing.T) {
	isolatedHome(t)

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := pipelineFlags(fs)
	if err := fs.Parse([]string{
		"--projects-root", "/a/projects,/b/projects",
		"--projects-root", "/c/projects",
		"--projects-root", "/a/projects", // duplicate: collapsed
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"/a/projects", "/b/projects", "/c/projects"}
	if !slices.Equal(cfg.ProjectsRoots, want) {
		t.Errorf("cfg.ProjectsRoots = %v, want %v", cfg.ProjectsRoots, want)
	}
}

// No flag at all → exactly the resolved default (the configure-nothing path).
func TestProjectsRootFlagOmittedKeepsTheDefault(t *testing.T) {
	home := isolatedHome(t)

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := pipelineFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{filepath.Join(home, ".claude", "projects")}
	if !slices.Equal(cfg.ProjectsRoots, want) {
		t.Errorf("cfg.ProjectsRoots = %v, want %v", cfg.ProjectsRoots, want)
	}
}

// makeAccountRoots creates ~/.claude/projects and ~/.claude-acct2/projects
// under home — a two-account machine.
func makeAccountRoots(t *testing.T, home string) []string {
	t.Helper()
	roots := []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude-acct2", "projects"),
	}
	for _, d := range roots {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return roots
}

// A shell-run backfill carries no SWARMERY_PROJECTS_ROOTS=auto (only the
// launchd plist sets it), so the CLI default must cover every account on its
// own — the 2026-09-24 replay silently skipped the second account.
func TestCLIDefaultProjectsRootsCoversEveryAccount(t *testing.T) {
	home := isolatedHome(t)
	want := makeAccountRoots(t, home)
	if got := cliDefaultProjectsRoots(); !slices.Equal(got, want) {
		t.Errorf("cliDefaultProjectsRoots() = %v, want %v", got, want)
	}
}

// An explicit env value wins over the CLI auto default, exactly as for serve.
func TestCLIDefaultProjectsRootsHonorsEnv(t *testing.T) {
	home := isolatedHome(t)
	makeAccountRoots(t, home)

	t.Setenv("SWARMERY_PROJECTS_ROOTS", "/only/this")
	if got := cliDefaultProjectsRoots(); !slices.Equal(got, []string{"/only/this"}) {
		t.Errorf("with SWARMERY_PROJECTS_ROOTS: got %v, want [/only/this]", got)
	}

	t.Setenv("SWARMERY_PROJECTS_ROOTS", "")
	t.Setenv("SWARMERY_PROJECTS_ROOT", "/legacy/root")
	if got := cliDefaultProjectsRoots(); !slices.Equal(got, []string{"/legacy/root"}) {
		t.Errorf("with SWARMERY_PROJECTS_ROOT: got %v, want [/legacy/root]", got)
	}
}

// No Claude config dir at all → the stock root, so the failure names a path.
func TestCLIDefaultProjectsRootsFallsBackToTheStockRoot(t *testing.T) {
	home := isolatedHome(t)
	want := []string{filepath.Join(home, ".claude", "projects")}
	if got := cliDefaultProjectsRoots(); !slices.Equal(got, want) {
		t.Errorf("cliDefaultProjectsRoots() = %v, want %v", got, want)
	}
}

// serve's default (pipelineFlags → defaultProjectsRoots) stays the single
// stock root on the same two-account HOME: configure nothing, get exactly
// ~/.claude/projects, byte-identical to before.
func TestServeDefaultProjectsRootsUnchangedOnMultiAccountHome(t *testing.T) {
	home := isolatedHome(t)
	makeAccountRoots(t, home)

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfg := pipelineFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{filepath.Join(home, ".claude", "projects")}
	if !slices.Equal(cfg.ProjectsRoots, want) {
		t.Errorf("serve cfg.ProjectsRoots = %v, want %v", cfg.ProjectsRoots, want)
	}
}

// --projects-root still replaces the CLI auto default.
func TestCLIProjectsRootFlagReplacesAutoDefault(t *testing.T) {
	home := isolatedHome(t)
	makeAccountRoots(t, home)

	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	cfg := pipelineFlagsWithRoots(fs, cliDefaultProjectsRoots())
	if err := fs.Parse([]string{"--projects-root", "/x/projects"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(cfg.ProjectsRoots, []string{"/x/projects"}) {
		t.Errorf("cfg.ProjectsRoots = %v, want [/x/projects]", cfg.ProjectsRoots)
	}
}
