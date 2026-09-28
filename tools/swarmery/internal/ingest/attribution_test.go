package ingest

// Project-attribution regressions (live bug: every dispatcher worktree cwd
// and every in-repo subdirectory cwd minted its own phantom project, so the
// Projects page drifted away from the real workspace):
//   - a cwd under <worktreeRoot>/<parentSlug>/<task> attributes to the parent
//     project whose slug is the path segment, never minting a new row;
//   - a cwd inside a registered project's tree attributes to that project
//     (deepest registered ancestor wins);
//   - unknown standalone paths still mint their own project (no invention);
//   - HealProjectAttribution re-points pre-existing phantom rows and drops
//     the phantom project.
//
// …and the follow-up bug the ancestor rule itself caused (one session run at
// the machine's onboarding root minted a projects row for it, and the rule
// then swallowed all 36 sessions of every sibling repo under it):
//   - a configured onboarding root is never an ancestor-rule target;
//   - an archived row is never an ancestor-rule target (so archiving a trap
//     row actually defuses it);
//   - a pinned row is never folded away by the heal.
//
// …and the same trap one level up (one session started at cwd=$HOME minted a
// projects row for the home dir, and the next restart's heal folded 22
// unpinned projects — the System row among them — into it and deleted them):
//   - the home dir, and every ancestor of it, is never an ancestor-rule target;
//   - a cwd that IS a registered project resolves to that project even when a
//     registered ancestor exists (exact row first), which is what the
//     handoff and drift lookups need.

import (
	"testing"
)

func seedProject(t *testing.T, db dbtx, path string) int64 {
	t.Helper()
	id, _, err := UpsertProject(db, path, "2026-07-01T00:00:00.000Z", "")
	if err != nil {
		t.Fatalf("seed project %s: %v", path, err)
	}
	return id
}

func TestUpsertProjectWorktreeCwdAttributesToParent(t *testing.T) {
	db := testDB(t)
	old := worktreeRootOverride
	worktreeRootOverride = "/home/dev/.swarmery/worktrees"
	t.Cleanup(func() { worktreeRootOverride = old })

	parent := seedProject(t, db, "/Volumes/Work/Nytka")

	id, created, err := UpsertProject(db,
		"/home/dev/.swarmery/worktrees/-Volumes-Work-Nytka/T-5n66kj",
		"2026-07-26T00:00:00.000Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if created || id != parent {
		t.Errorf("worktree cwd: id=%d created=%v, want parent id=%d created=false", id, created, parent)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects`); n != 1 {
		t.Errorf("projects = %d, want 1 (no phantom minted)", n)
	}
}

func TestUpsertProjectSubdirCwdAttributesToAncestor(t *testing.T) {
	db := testDB(t)
	parent := seedProject(t, db, "/Volumes/Work/swarmery")

	id, created, err := UpsertProject(db,
		"/Volumes/Work/swarmery/tools/swarmery/internal/api",
		"2026-07-26T00:00:00.000Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if created || id != parent {
		t.Errorf("subdir cwd: id=%d created=%v, want ancestor id=%d created=false", id, created, parent)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects`); n != 1 {
		t.Errorf("projects = %d, want 1", n)
	}
}

func TestUpsertProjectDeepestAncestorWins(t *testing.T) {
	db := testDB(t)
	// Inner first: seeded the other way round, the ancestor rule would fold
	// the inner path into the outer row and there would be one row to win.
	inner := seedProject(t, db, "/Volumes/Work/swarmery")
	seedProject(t, db, "/Volumes/Work")

	id, _, err := UpsertProject(db, "/Volumes/Work/swarmery/web", "2026-07-26T00:00:00.000Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if id != inner {
		t.Errorf("id=%d, want deepest ancestor %d", id, inner)
	}
}

func TestUpsertProjectUnknownPathsStillMint(t *testing.T) {
	db := testDB(t)
	seedProject(t, db, "/Volumes/Work/swarmery")

	// Standalone path outside any registered tree → its own project.
	id, created, err := UpsertProject(db, "/Volumes/Work/Fusion", "2026-07-26T00:00:00.000Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created || id == 0 {
		t.Errorf("standalone path: created=%v id=%d, want fresh row", created, id)
	}
	// An existing exact row keeps resolving to itself.
	again, created, err := UpsertProject(db, "/Volumes/Work/Fusion", "2026-07-27T00:00:00.000Z", "")
	if err != nil || created || again != id {
		t.Errorf("re-upsert: id=%d created=%v err=%v, want id=%d created=false", again, created, err, id)
	}
}

func TestUpsertProjectRootAttributesToSystem(t *testing.T) {
	db := testDB(t)
	old := systemBaseOverride
	systemBaseOverride = "/home/dev/.swarmery"
	t.Cleanup(func() { systemBaseOverride = old })

	// cwd "/" (daemon-spawned headless runs under launchd) mints the System
	// project on first sight…
	id, created, err := UpsertProject(db, "/", "2026-07-26T00:00:00.000Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("first root upsert should mint the System project")
	}
	var path, name string
	if err := db.QueryRow(`SELECT path, name FROM projects WHERE id = ?`, id).Scan(&path, &name); err != nil {
		t.Fatal(err)
	}
	if path != "/home/dev/.swarmery" || name != "System" {
		t.Errorf("system row = (%q, %q), want (/home/dev/.swarmery, System)", path, name)
	}
	// …and both "/" and the system dir itself resolve to it afterwards.
	if id2, created, err := UpsertProject(db, "/", "2026-07-27T00:00:00.000Z", ""); err != nil || created || id2 != id {
		t.Errorf("second root upsert: id=%d created=%v err=%v, want id=%d created=false", id2, created, err, id)
	}
	if id3, created, err := UpsertProject(db, "/home/dev/.swarmery", "2026-07-27T00:00:00.000Z", ""); err != nil || created || id3 != id {
		t.Errorf("system-dir upsert: id=%d created=%v err=%v, want id=%d created=false", id3, created, err, id)
	}
}

func TestHealProjectAttributionMergesRootIntoSystem(t *testing.T) {
	db := testDB(t)
	old := systemBaseOverride
	systemBaseOverride = "/home/dev/.swarmery"
	t.Cleanup(func() { systemBaseOverride = old })

	// Legacy "/" row with sessions, minted before the system rule existed.
	mustExecT(t, db, `INSERT INTO projects (path, slug, name, first_seen) VALUES ('/', '-', '/', '2026-07-01T00:00:00.000Z')`)
	var rootID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE path = '/'`).Scan(&rootID); err != nil {
		t.Fatal(err)
	}
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, 'u-root', '2026-07-20T00:00:00.000Z')`, rootID)

	moved, err := HealProjectAttribution(db)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Errorf("moved = %d, want 1", moved)
	}
	var sysID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE path = '/home/dev/.swarmery'`).Scan(&sysID); err != nil {
		t.Fatalf("system project not minted by heal: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM sessions WHERE project_id = ?`, sysID); n != 1 {
		t.Errorf("system sessions = %d, want 1 (re-pointed from /)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects WHERE path = '/'`); n != 0 {
		t.Errorf("legacy / row still present")
	}
}

func TestHealProjectAttributionMergesPhantoms(t *testing.T) {
	db := testDB(t)
	old := worktreeRootOverride
	worktreeRootOverride = "/home/dev/.swarmery/worktrees"
	t.Cleanup(func() { worktreeRootOverride = old })

	parent := seedProject(t, db, "/Volumes/Work/swarmery")

	// Phantom rows minted by the pre-fix code path: insert directly.
	mustExecT(t, db, `INSERT INTO projects (path, slug, name, first_seen) VALUES (?, ?, ?, ?)`,
		"/Volumes/Work/swarmery/tools/swarmery/internal/api",
		"-Volumes-Work-swarmery-tools-swarmery-internal-api", "api", "2026-07-20T00:00:00.000Z")
	mustExecT(t, db, `INSERT INTO projects (path, slug, name, first_seen) VALUES (?, ?, ?, ?)`,
		"/home/dev/.swarmery/worktrees/-Volumes-Work-swarmery/T-abc123",
		"-home-dev-.swarmery-worktrees--Volumes-Work-swarmery-T-abc123", "T-abc123", "2026-07-24T00:00:00.000Z")
	var subdirID, wtID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE name = 'api'`).Scan(&subdirID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM projects WHERE name = 'T-abc123'`).Scan(&wtID); err != nil {
		t.Fatal(err)
	}
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, 'u-sub', '2026-07-20T00:00:00.000Z')`, subdirID)
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, 'u-wt', '2026-07-24T00:00:00.000Z')`, wtID)

	moved, err := HealProjectAttribution(db)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 2 {
		t.Errorf("moved = %d, want 2", moved)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects`); n != 1 {
		t.Errorf("projects = %d, want 1 (phantoms dropped)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM sessions WHERE project_id = ?`, parent); n != 2 {
		t.Errorf("parent sessions = %d, want 2 (re-pointed)", n)
	}
}

// setOnboardRoots installs the onboarding allow-list for one test and restores
// the previous value (the var is process-global, like worktreeRootOverride).
func setOnboardRoots(t *testing.T, roots ...string) {
	t.Helper()
	old := onboardRootsOverride
	SetOnboardRoots(roots)
	t.Cleanup(func() { onboardRootsOverride = old })
}

func TestCanonicalProjectPathSkipsOnboardRoot(t *testing.T) {
	db := testDB(t)
	// Trailing slash on purpose: the setter cleans before comparing.
	setOnboardRoots(t, "/Users/dev/projects/")

	// The trap row: one session once ran at the onboarding root itself, so a
	// projects row exists for the parent dir of every repo on the machine.
	root := seedProject(t, db, "/Users/dev/projects")

	if got := CanonicalProjectPath(db, "/Users/dev/projects/swarmery"); got != "/Users/dev/projects/swarmery" {
		t.Errorf("cwd under onboarding root = %q, want it unchanged", got)
	}
	// …so a repo under the root mints its OWN row instead of folding in.
	id, created, err := UpsertProject(db, "/Users/dev/projects/swarmery", "2026-07-26T00:00:00.000Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created || id == root {
		t.Errorf("repo under onboarding root: id=%d created=%v, want a fresh row (root id=%d)", id, created, root)
	}
	// A real project deeper than the root is still a valid ancestor — the ban
	// is on the root row alone, not on the subtree.
	if got := CanonicalProjectPath(db, "/Users/dev/projects/swarmery/web"); got != "/Users/dev/projects/swarmery" {
		t.Errorf("subdir of a real repo = %q, want /Users/dev/projects/swarmery", got)
	}
}

func TestCanonicalProjectPathSkipsArchivedAncestor(t *testing.T) {
	db := testDB(t)
	seedProject(t, db, "/Volumes/Work/swarmery")

	// Unarchived, the ancestor rule folds the subdir in (baseline).
	if got := CanonicalProjectPath(db, "/Volumes/Work/swarmery/web"); got != "/Volumes/Work/swarmery" {
		t.Fatalf("baseline: got %q, want /Volumes/Work/swarmery", got)
	}
	mustExecT(t, db, `UPDATE projects SET archived = 1 WHERE path = ?`, "/Volumes/Work/swarmery")
	if got := CanonicalProjectPath(db, "/Volumes/Work/swarmery/web"); got != "/Volumes/Work/swarmery/web" {
		t.Errorf("archived ancestor = %q, want the cwd unchanged", got)
	}
}

func TestHealProjectAttributionSkipsPinnedRow(t *testing.T) {
	db := testDB(t)
	parent := seedProject(t, db, "/Volumes/Work/swarmery")

	// Two rows the ancestor rule would fold into the parent, identical except
	// for the pin: neither is workspace-linked, so only `pinned` decides.
	mustExecT(t, db, `INSERT INTO projects (path, slug, name, first_seen, pinned) VALUES (?, ?, ?, ?, 1)`,
		"/Volumes/Work/swarmery/web", "-Volumes-Work-swarmery-web", "web", "2026-07-20T00:00:00.000Z")
	mustExecT(t, db, `INSERT INTO projects (path, slug, name, first_seen, pinned) VALUES (?, ?, ?, ?, 0)`,
		"/Volumes/Work/swarmery/docs", "-Volumes-Work-swarmery-docs", "docs", "2026-07-20T00:00:00.000Z")
	var pinnedID, looseID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE name = 'web'`).Scan(&pinnedID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM projects WHERE name = 'docs'`).Scan(&looseID); err != nil {
		t.Fatal(err)
	}
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, 'u-pinned', '2026-07-20T00:00:00.000Z')`, pinnedID)
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, 'u-loose', '2026-07-20T00:00:00.000Z')`, looseID)

	moved, err := HealProjectAttribution(db)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Errorf("moved = %d, want 1 (the unpinned row only)", moved)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects WHERE id = ?`, pinnedID); n != 1 {
		t.Errorf("pinned row was merged away")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM sessions WHERE project_id = ?`, pinnedID); n != 1 {
		t.Errorf("pinned sessions = %d, want 1 (kept)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects WHERE id = ?`, looseID); n != 0 {
		t.Errorf("unpinned row survived the heal")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM sessions WHERE project_id = ?`, parent); n != 1 {
		t.Errorf("parent sessions = %d, want 1 (unpinned row re-pointed)", n)
	}
}

// setHomeDir pins the home dir the ancestor rule refuses, for one test (the
// var is process-global, like worktreeRootOverride).
func setHomeDir(t *testing.T, dir string) {
	t.Helper()
	old := homeDirOverride
	homeDirOverride = dir
	t.Cleanup(func() { homeDirOverride = old })
}

func TestCanonicalProjectPathNeverFoldsIntoHome(t *testing.T) {
	db := testDB(t)
	setHomeDir(t, "/Users/dev")

	// The trap rows: one session once ran at cwd=$HOME, another at /Users.
	home := seedProject(t, db, "/Users/dev")
	users := seedProject(t, db, "/Users")

	if got := CanonicalProjectPath(db, "/Users/dev/projects/am"); got != "/Users/dev/projects/am" {
		t.Errorf("cwd under the home dir = %q, want it unchanged", got)
	}
	if got := CanonicalProjectPath(db, "/Users/other/repo"); got != "/Users/other/repo" {
		t.Errorf("cwd under an ancestor of the home dir = %q, want it unchanged", got)
	}
	// …so a repo under the home dir mints its OWN row instead of folding in.
	id, created, err := UpsertProject(db, "/Users/dev/projects/am", "2026-09-28T00:00:00.000Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created || id == home || id == users {
		t.Errorf("repo under the home dir: id=%d created=%v, want a fresh row (home id=%d)", id, created, home)
	}
	// The home row still owns a session that really ran at cwd=$HOME.
	if again, created, err := UpsertProject(db, "/Users/dev", "2026-09-28T00:00:00.000Z", ""); err != nil || created || again != home {
		t.Errorf("home cwd: id=%d created=%v err=%v, want the home row %d", again, created, err, home)
	}
	// A real project under the home dir is still a valid ancestor.
	if got := CanonicalProjectPath(db, "/Users/dev/projects/am/web"); got != "/Users/dev/projects/am" {
		t.Errorf("subdir of a repo under the home dir = %q, want /Users/dev/projects/am", got)
	}
}

func TestHealProjectAttributionNeverMergesIntoHome(t *testing.T) {
	db := testDB(t)
	setHomeDir(t, "/Users/dev")

	// Real projects first, then the trap row minted by a later cwd=$HOME session.
	am := seedProject(t, db, "/Users/dev/projects/am")
	tools := seedProject(t, db, "/Users/dev/.local/tools")
	seedProject(t, db, "/Users/dev")
	mustExecT(t, db, `INSERT INTO sessions (project_id, session_uuid, started_at) VALUES (?, 'u-am', '2026-09-20T00:00:00.000Z')`, am)

	moved, err := HealProjectAttribution(db)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 {
		t.Errorf("moved = %d, want 0 — nothing may fold into the home dir", moved)
	}
	for _, id := range []int64{am, tools} {
		if n := count(t, db, `SELECT COUNT(*) FROM projects WHERE id = ?`, id); n != 1 {
			t.Errorf("project %d was merged away into the home dir", id)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM sessions WHERE project_id = ?`, am); n != 1 {
		t.Errorf("am sessions = %d, want 1 (kept)", n)
	}
}

func TestCanonicalProjectPathExactRowWinsOverAncestor(t *testing.T) {
	db := testDB(t)
	// Inner first (see TestUpsertProjectDeepestAncestorWins), then an
	// ancestor registered by a later session.
	seedProject(t, db, "/Volumes/Work/swarmery")
	seedProject(t, db, "/Volumes/Work")

	if got := CanonicalProjectPath(db, "/Volumes/Work/swarmery"); got != "/Volumes/Work/swarmery" {
		t.Errorf("registered cwd = %q, want its own row, not the ancestor", got)
	}
	if got := CanonicalProjectPath(db, "/Volumes/Work/swarmery/web"); got != "/Volumes/Work/swarmery" {
		t.Errorf("subdir = %q, want the deepest registered ancestor", got)
	}
	// An archived exact row still resolves to itself, as in UpsertProject:
	// archiving stops a row from swallowing others, never from owning its own.
	mustExecT(t, db, `UPDATE projects SET archived = 1 WHERE path = ?`, "/Volumes/Work/swarmery")
	if got := CanonicalProjectPath(db, "/Volumes/Work/swarmery"); got != "/Volumes/Work/swarmery" {
		t.Errorf("archived registered cwd = %q, want its own row", got)
	}
}

func mustExecT(t *testing.T, db dbtx, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v\n%s", err, q)
	}
}
