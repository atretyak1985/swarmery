package api

// Tests for term.go's account resolution: resolveTermCwd must hand back the
// PROJECT path that governs a terminal session's Claude account, not the raw
// cwd. A task's worktree_path is a fresh git worktree with no
// .claude/settings.local.json of its own, so resolving the account from cwd
// there silently falls back to the default account — the same A3 trap
// dispatch/runner_account_test.go and provision/runner_account_test.go guard
// against on their own spawn paths. This file is term.go's mirror of both.
//
// # Isolation contract
//
// unsetConfigDir + attachHomeAccounts (accounts_test.go, same package) match
// dispatch/runner_account_test.go exactly: without them a developer's shell
// (or a daemon under a non-default account) could leak CLAUDE_CONFIG_DIR or
// point resolution at the operator's real ~/.claude, and skew the result.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// unsetConfigDir removes CLAUDE_CONFIG_DIR from the TEST process's environment
// for the duration of the test — see dispatch/runner_account_test.go's twin
// for why this matters (os.Environ() is the base every spawn site appends to).
func unsetConfigDir(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv("CLAUDE_CONFIG_DIR")
	if !had {
		return
	}
	if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
		t.Fatalf("unset CLAUDE_CONFIG_DIR: %v", err)
	}
	t.Cleanup(func() { os.Setenv("CLAUDE_CONFIG_DIR", prev) })
}

// termDB opens a throwaway store with one projects row per given path (ids
// 1..N in argument order, mirroring accountsTestDB's seeding) and no HTTP
// server — these tests call resolveTermCwd/termAccountEnv directly, so the
// full NewServer wiring (Improve/Provision services, mux, …) is not needed.
func termDB(t *testing.T, name string, projectPaths ...string) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC().Format("2006-01-02T15:04:05")
	for i, p := range projectPaths {
		if _, err := db.Exec(
			`INSERT INTO projects (id, path, slug, first_seen) VALUES (?, ?, ?, ?)`,
			i+1, p, fmt.Sprintf("p%d", i+1), now); err != nil {
			t.Fatalf("insert project %s: %v", p, err)
		}
	}
	return db
}

// termTaskWithWorktree inserts a dispatched task row holding worktreePath,
// owned by projectID — the same insert shape as term_test.go's
// termTestServer, which is the JOIN target termWorktreeRoots reads.
func termTaskWithWorktree(t *testing.T, db *sql.DB, id, projectID int64, worktreePath string) {
	t.Helper()
	now := time.Now().UTC().Format("2006-01-02T15:04:05")
	if _, err := db.Exec(
		`INSERT INTO tasks (id, project_id, title, prompt, status, source, external_id, created_at, started_at, worktree_path)
		 VALUES (?, ?, 'wt task', 'do it', 'running', 'queue', ?, ?, ?, ?)`,
		id, projectID, fmt.Sprintf("T-%d", id), now, now, worktreePath); err != nil {
		t.Fatalf("insert task: %v", err)
	}
}

// termOrphanedTaskWithWorktree inserts a worktree task whose project_id
// resolves to NO row in projects — the state termWorktreeRoots' LEFT JOIN
// must tolerate. tasks.project_id is FK-enforced (store.Open sets
// _pragma=foreign_keys(1)), so this state cannot be reached through a normal
// insert; flipping PRAGMA foreign_keys off for the one write (and back on
// immediately after) is what lets the test prove termWorktreeRoots is
// defensive about the DB state, not merely "never observed to be violated".
func termOrphanedTaskWithWorktree(t *testing.T, db *sql.DB, id int64, worktreePath string) {
	t.Helper()
	const noSuchProjectID = 999999
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("foreign_keys off: %v", err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05")
	_, insertErr := db.Exec(
		`INSERT INTO tasks (id, project_id, title, prompt, status, source, external_id, created_at, started_at, worktree_path)
		 VALUES (?, ?, 'orphan wt task', 'do it', 'running', 'queue', ?, ?, ?, ?)`,
		id, noSuchProjectID, fmt.Sprintf("T-orphan-%d", id), now, now, worktreePath)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("foreign_keys back on: %v", err)
	}
	if insertErr != nil {
		t.Fatalf("insert orphaned task: %v", insertErr)
	}
}

// TestResolveTermCwdProjectPathUsesOwnAccount: cwd IS the project path, so
// resolveTermCwd hands back that same path as projectPath, and the terminal
// picks up the project's OWN binding — the unchanged, non-worktree case.
func TestResolveTermCwdProjectPathUsesOwnAccount(t *testing.T) {
	unsetConfigDir(t)
	_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project := t.TempDir()
	db := termDB(t, "term-project.db", project)
	h := &Handler{DB: db}

	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	cwd, projectPath, ok := h.resolveTermCwd(project)
	if !ok {
		t.Fatalf("resolveTermCwd(%s) = not ok, want ok", project)
	}
	if projectPath != project {
		t.Errorf("projectPath = %q, want %q (the cwd itself)", projectPath, project)
	}

	got := termConfigDirs(termAccountEnv(projectPath))
	want := []string{"CLAUDE_CONFIG_DIR=" + dirs["nabu-org"]}
	if !slices.Equal(got, want) {
		t.Errorf("CLAUDE_CONFIG_DIR entries = %v, want %v, cwd = %v", got, want, cwd)
	}
}

// TestResolveTermCwdWorktreeUsesProjectAccount is the fix itself: cwd is a
// task's worktree_path, which carries no .claude/settings.local.json of its
// own (asserted below as a precondition, so this test fails loudly if that
// ever stops being true) — yet the terminal must still run under the
// PROJECT's bound account, because resolveTermCwd resolves it via
// tasks.project_id instead of from cwd.
//
// This is the test that must bite: reverting termWorktreeRoots to hand back
// the worktree path itself as projectPath (the pre-fix "resolve from cwd"
// behaviour) makes it fail. See the task's verification report for the
// captured red run proving that.
func TestResolveTermCwdWorktreeUsesProjectAccount(t *testing.T) {
	unsetConfigDir(t)
	_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project, worktree := t.TempDir(), t.TempDir()
	db := termDB(t, "term-worktree.db", project)
	h := &Handler{DB: db}

	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	termTaskWithWorktree(t, db, 10, 1, worktree)

	// Precondition: the trap this test guards against is real — a worktree cwd
	// genuinely resolves no binding on its own.
	if env := claudeacct.EnvFor(worktree); env != nil {
		t.Fatalf("precondition: EnvFor(worktree) = %v, want nil — the trap this test guards is gone", env)
	}

	cwd, projectPath, ok := h.resolveTermCwd(worktree)
	if !ok {
		t.Fatalf("resolveTermCwd(%s) = not ok, want ok", worktree)
	}
	if real, err := filepath.EvalSymlinks(worktree); err != nil || cwd != real {
		t.Errorf("cwd = %q, want the (resolved) worktree %q — the PTY must still chdir there, only the account lookup moves", cwd, worktree)
	}
	if projectPath != project {
		t.Errorf("projectPath = %q, want %q (the task's project, not the worktree)", projectPath, project)
	}

	got := termConfigDirs(termAccountEnv(projectPath))
	want := []string{"CLAUDE_CONFIG_DIR=" + dirs["nabu-org"]}
	if !slices.Equal(got, want) {
		t.Errorf("CLAUDE_CONFIG_DIR entries = %v, want %v — the account was not resolved from the project", got, want)
	}
}

// TestResolveTermCwdWorktreeUnboundProjectYieldsNoAccount: the worktree's
// project resolves fine, it is simply not BOUND — env must be empty, byte for
// byte the same as an unbound project cwd has always produced.
func TestResolveTermCwdWorktreeUnboundProjectYieldsNoAccount(t *testing.T) {
	unsetConfigDir(t)
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project, worktree := t.TempDir(), t.TempDir()
	db := termDB(t, "term-worktree-unbound.db", project)
	h := &Handler{DB: db}

	termTaskWithWorktree(t, db, 11, 1, worktree)
	// project is deliberately left unbound.

	_, projectPath, ok := h.resolveTermCwd(worktree)
	if !ok {
		t.Fatalf("resolveTermCwd(%s) = not ok, want ok", worktree)
	}
	if projectPath != project {
		t.Errorf("projectPath = %q, want %q", projectPath, project)
	}

	if got := termAccountEnv(projectPath); !slices.Equal(got, os.Environ()) {
		t.Errorf("env = %v, want the daemon's own environment byte for byte for an unbound project", got)
	}
}

// TestResolveTermCwdOrphanedWorktreeTaskYieldsNoAccount: a worktree task whose
// project_id no longer resolves (an orphaned row) must behave exactly like "no
// project" — empty projectPath, nil env, and no panic walking the result.
func TestResolveTermCwdOrphanedWorktreeTaskYieldsNoAccount(t *testing.T) {
	unsetConfigDir(t)
	attachHomeAccounts(t, ingest.DefaultAccount)
	worktree := t.TempDir()
	db := termDB(t, "term-worktree-orphan.db") // no projects at all
	h := &Handler{DB: db}

	termOrphanedTaskWithWorktree(t, db, 20, worktree)

	cwd, projectPath, ok := h.resolveTermCwd(worktree)
	if !ok {
		t.Fatalf("resolveTermCwd(%s) = not ok, want ok — an orphaned task's worktree is still a live worktree_path", worktree)
	}
	if projectPath != "" {
		t.Errorf("projectPath = %q, want empty for an orphaned task", projectPath)
	}

	got := termAccountEnv(projectPath) // must not panic
	if got != nil {
		t.Errorf("env = %v, want nil (no project ⇒ the daemon's own environment), cwd = %v", got, cwd)
	}
}

// TestTermAccountEnvGuardsEmptyProjectPath is the mandatory guard: an empty
// projectPath must short-circuit to nil BEFORE claudeacct.EnvFor is ever
// called. claudeacct.Binding joins its argument with
// ".claude/settings.local.json" unconditionally, so EnvFor("") resolves that
// RELATIVE path against the daemon's OWN process working directory — proven
// here by making that relative path a real, bound settings file (mirrors
// provision/runner_account_test.go's TestProvisionEmptyDirResolvesNoAccount)
// and confirming termAccountEnv("") still returns nil.
func TestTermAccountEnvGuardsEmptyProjectPath(t *testing.T) {
	unsetConfigDir(t)
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")

	// A directory bound to a REAL account, made the process's cwd for the
	// duration of the test: if termAccountEnv ever called claudeacct.EnvFor("")
	// instead of short-circuiting, this is what it would pick up (Binding("")
	// joins to the plain relative ".claude/settings.local.json").
	bound := t.TempDir()
	if err := claudeacct.SetBinding(bound, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(bound); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prevWd) })

	// Non-vacuity: calling EnvFor("") directly WOULD pick up the relative file —
	// proving the scenario is real, not a guard against an impossible input.
	if env := claudeacct.EnvFor(""); env == nil {
		t.Fatalf("precondition: EnvFor(\"\") = nil — the relative-path trap this test guards is gone")
	}

	if got := termAccountEnv(""); got != nil {
		t.Errorf("termAccountEnv(\"\") = %v, want nil — it must never resolve a binding for an empty project path", got)
	}
}
