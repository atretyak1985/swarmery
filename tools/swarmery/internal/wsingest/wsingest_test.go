package wsingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const fixtureRoot = "../../testdata/workspace"

// TestRoot covers the env fallback chain: AGENT_WORKSPACE_ROOT (per-project
// runtime var) wins over SWARMERY_WORKSPACE_ROOT (machine-level var init.sh
// reads and the launchd installer bakes into the plist), which wins over the
// home default. Empty string counts as unset — Root() checks != "".
func TestRoot(t *testing.T) {
	cases := []struct {
		name, agent, swarmery, want string
	}{
		{"agent wins over swarmery", "/agent-root", "/swarmery-root", "/agent-root"},
		{"agent only", "/agent-root", "", "/agent-root"},
		{"swarmery only", "", "/swarmery-root", "/swarmery-root"},
		{"neither set falls back to default", "", "", DefaultWorkspaceRoot()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENT_WORKSPACE_ROOT", tc.agent)
			t.Setenv("SWARMERY_WORKSPACE_ROOT", tc.swarmery)
			if got := Root(); got != tc.want {
				t.Errorf("Root() = %q, want %q", got, tc.want)
			}
		})
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// seed inserts the telemetry side: projects + sessions the fixture workspace
// links against. Paths deliberately differ from overlay codePath in case and
// trailing slash to exercise normalization.
func seed(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/projalpha', 'projalpha', 'Proj Alpha', '2026-06-01T00:00:00Z'),
		(2, '/work/projbeta',  'projbeta',  'Proj Beta',  '2026-06-01T00:00:00Z'),
		(3, '/elsewhere/other','other',     'Other',      '2026-06-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO sessions (id, project_id, session_uuid, cwd, status, started_at, ended_at) VALUES
		-- s1: explicitly referenced by full uuid in full-card logs/sessions.md,
		--     cwd deliberately OUTSIDE the workspace code path.
		(1, 3, 'aabbccdd-1122-4333-8444-555566667777', '/elsewhere/other', 'completed',
		 '2026-07-02T10:00:00Z', '2026-07-02T11:00:00Z'),
		-- s2: explicitly referenced by an 8-hex PREFIX inside prose.
		(2, 1, 'deadbeef-0000-4000-8000-000000000001', '/work/ProjAlpha', 'completed',
		 '2026-07-02T12:00:00Z', '2026-07-02T13:00:00Z'),
		-- s3: heuristic — cwd under codePath, fully inside full-card's window.
		(3, 1, '11111111-1111-4111-8111-111111111111', '/work/ProjAlpha/apps/main', 'completed',
		 '2026-07-01T12:00:00Z', '2026-07-01T14:00:00Z'),
		-- s4: heuristic PARTIAL — 4h session, only ~2h inside done-card's
		--     [2026-06-20, 2026-06-20 23:59:59] window → confidence ≈ 0.5.
		(4, 1, '22222222-2222-4222-8222-222222222222', '/work/ProjAlpha', 'completed',
		 '2026-06-20T22:00:00Z', '2026-06-21T02:00:00Z'),
		-- s5: heuristic in projbeta (mapped via projects.path basename fallback).
		(5, 2, '33333333-3333-4333-8333-333333333333', '/work/projbeta', 'completed',
		 '2026-07-06T09:00:00Z', '2026-07-06T10:00:00Z'),
		-- s6: unlinked — cwd outside every workspace.
		(6, 3, '44444444-4444-4444-8444-444444444444', '/elsewhere/other', 'completed',
		 '2026-07-06T09:00:00Z', '2026-07-06T10:00:00Z')`)
}

// pinMtime makes the mtime fallback deterministic: git does not preserve
// mtimes, so the archived-without-date card gets an explicit old timestamp.
func pinMtime(t *testing.T) {
	t.Helper()
	dir := filepath.Join(fixtureRoot, "projalpha", "workspace", "archive", "2026", "06", "10", "no-date-card")
	when := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatalf("pin mtime: %v", err)
	}
}

func scan(t *testing.T, db *sql.DB) Stats {
	t.Helper()
	stats, err := New(db, Config{WorkspaceRoot: fixtureRoot}).Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return stats
}

func TestScanIndexesWorkspacesAndCards(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	pinMtime(t)
	stats := scan(t, db)

	// _shared is skipped; projalpha + projbeta + projgamma are indexed.
	if stats.Workspaces != 3 {
		t.Errorf("workspaces = %d, want 3", stats.Workspaces)
	}
	// full-card, bare-card, done-card, no-date-card, beta-task, gamma-task.
	if stats.Tasks != 6 {
		t.Errorf("tasks = %d, want 6", stats.Tasks)
	}

	// Mapping: overlay codePath (case + trailing slash) ↔ projects.path.
	if got := count(t, db, `SELECT COUNT(*) FROM workspaces WHERE slug='projalpha' AND project_id=1 AND code_path='/work/ProjAlpha/'`); got != 1 {
		t.Errorf("projalpha workspace mapping missing (normalized codePath match)")
	}
	// Fallback: empty overlay → projects.path basename match.
	if got := count(t, db, `SELECT COUNT(*) FROM workspaces WHERE slug='projbeta' AND project_id=2`); got != 1 {
		t.Errorf("projbeta fallback mapping missing")
	}
	// Workspace with no telemetry project → a projects row is created.
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE path='/work/gamma-app' AND slug='projgamma'`); got != 1 {
		t.Errorf("projgamma project row not created")
	}

	// Card fields: full card.
	var title, status, prompt string
	if err := db.QueryRow(`SELECT title, status, prompt FROM tasks WHERE external_id='2026-07-01-full-card'`).
		Scan(&title, &status, &prompt); err != nil {
		t.Fatalf("full-card row: %v", err)
	}
	if title != "Full card with every field" || status != "running" || prompt != "exercise the happy-path card parser" {
		t.Errorf("full-card = %q/%q/%q", title, status, prompt)
	}

	// Broken card (no README at all) still indexes, title falls back to slug.
	if got := count(t, db, `SELECT COUNT(*) FROM tasks WHERE external_id='2026-07-03-bare-card' AND title='bare-card' AND status='running'`); got != 1 {
		t.Errorf("bare-card not indexed tolerantly")
	}

	// Archive: completion date → archived_at + finished_at + done.
	if got := count(t, db, `SELECT COUNT(*) FROM tasks WHERE external_id='2026-06-20-done-card'
		AND status='done' AND archived_at LIKE '2026-06-20%' AND finished_at LIKE '2026-06-20%'`); got != 1 {
		t.Errorf("done-card archive fields wrong")
	}
	// Archive without a date → dir mtime fallback.
	if got := count(t, db, `SELECT COUNT(*) FROM tasks WHERE external_id='2026-06-10-no-date-card'
		AND status='done' AND archived_at LIKE '2026-06-11%'`); got != 1 {
		t.Errorf("no-date-card mtime fallback wrong")
	}
}

func TestExplicitLinks(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	pinMtime(t)
	stats := scan(t, db)

	if stats.Explicit != 2 {
		t.Errorf("explicit links = %d, want 2 (full uuid + 8-hex prefix)", stats.Explicit)
	}
	for _, sid := range []int{1, 2} {
		if got := count(t, db, `SELECT COUNT(*) FROM task_sessions ts
			JOIN tasks tk ON tk.id = ts.task_id
			WHERE ts.session_id = ? AND ts.link_source='explicit' AND tk.external_id='2026-07-01-full-card'`, sid); got != 1 {
			t.Errorf("session %d: explicit link to full-card missing", sid)
		}
	}
	// Junk ids (5-digit, phase-3) resolve to nothing.
	if got := count(t, db, `SELECT COUNT(*) FROM task_sessions WHERE link_source='explicit'`); got != 2 {
		t.Errorf("junk sessions.md ids produced links: total explicit = %d", got)
	}
	// s2 is explicitly linked → the heuristic pass must skip it.
	if got := count(t, db, `SELECT COUNT(*) FROM task_sessions WHERE session_id=2 AND link_source='heuristic'`); got != 0 {
		t.Errorf("explicitly linked session also got a heuristic link")
	}
}

func TestHeuristicLinks(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	pinMtime(t)
	stats := scan(t, db)

	if stats.Heuristic != 3 {
		t.Errorf("heuristic links = %d, want 3 (s3, s4, s5)", stats.Heuristic)
	}

	// s3: fully inside full-card's open window → confidence 1.0.
	var conf float64
	if err := db.QueryRow(`SELECT ts.confidence FROM task_sessions ts
		JOIN tasks tk ON tk.id = ts.task_id
		WHERE ts.session_id = 3 AND tk.external_id='2026-07-01-full-card' AND ts.link_source='heuristic'`).
		Scan(&conf); err != nil {
		t.Fatalf("s3 heuristic link: %v", err)
	}
	if conf < 0.99 {
		t.Errorf("s3 confidence = %f, want ~1.0", conf)
	}

	// s4: 4h session, ~2h overlap with done-card's archived window → ~0.5.
	if err := db.QueryRow(`SELECT ts.confidence FROM task_sessions ts
		JOIN tasks tk ON tk.id = ts.task_id
		WHERE ts.session_id = 4 AND tk.external_id='2026-06-20-done-card' AND ts.link_source='heuristic'`).
		Scan(&conf); err != nil {
		t.Fatalf("s4 heuristic link: %v", err)
	}
	if conf < 0.4 || conf > 0.6 {
		t.Errorf("s4 confidence = %f, want ≈0.5 (overlap fraction)", conf)
	}

	// s5: projbeta fallback mapping still yields a heuristic link.
	if got := count(t, db, `SELECT COUNT(*) FROM task_sessions ts
		JOIN tasks tk ON tk.id = ts.task_id
		WHERE ts.session_id = 5 AND tk.external_id='2026-07-05-beta-task'`); got != 1 {
		t.Errorf("s5 heuristic link to beta-task missing")
	}

	// s6: cwd outside every workspace stays unlinked.
	if got := count(t, db, `SELECT COUNT(*) FROM task_sessions WHERE session_id = 6`); got != 0 {
		t.Errorf("s6 must stay unlinked")
	}
}

func TestRescanIsIdempotent(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	pinMtime(t)
	first := scan(t, db)
	second := scan(t, db)

	if first != second {
		t.Errorf("re-scan drifted: first %+v, second %+v", first, second)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM workspaces`); got != 3 {
		t.Errorf("workspaces after rescan = %d, want 3", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM tasks`); got != 6 {
		t.Errorf("tasks after rescan = %d, want 6 (no dupes)", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM task_sessions`); got != 5 {
		t.Errorf("task_sessions after rescan = %d, want 5 (no dupes)", got)
	}
	// Explicit rows survive re-scans untouched (heuristic never downgrades).
	if got := count(t, db, `SELECT COUNT(*) FROM task_sessions WHERE link_source='explicit'`); got != 2 {
		t.Errorf("explicit links after rescan = %d, want 2", got)
	}
}

// TestParseCardPaused: the pause lifecycle action writes `**Статус**: paused`
// into the card README — parseCard must map it to tasks.status "paused" in the
// working zone, while the archive-zone override still wins with "done".
func TestParseCardPaused(t *testing.T) {
	dir := t.TempDir()
	readme := "# Task: Paused card\n\n- **Статус**: paused\n- **Ціль**: hold my beer\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	warn := func(string, ...any) {}

	if c := parseCard(dir, "working", "2026-07-26-paused-card", warn); c.status != "paused" {
		t.Errorf("working-zone paused card status = %q, want paused", c.status)
	}
	if c := parseCard(dir, "archive", "2026-07-26-paused-card", warn); c.status != "done" {
		t.Errorf("archive-zone paused card status = %q, want done (zone override wins)", c.status)
	}
}

// TestExplicitRefsHookWrittenRow pins the sessions.md row shape the two core
// hooks write — plugins/core/hooks/task-session-log.sh on SessionStart and
// session-summary.sh on SessionEnd.
//
// Both used to be unusable here: task-session-log.sh gated on AGENT_TASK_ID,
// which nothing exports, so it never wrote at all; session-summary.sh put $$
// in the session column, and a pid is exactly the "junk 5-digit id" the
// >=8-hex filter throws away. The explicit task<->session link therefore had
// no working writer. If this test goes red, that state has returned.
func TestExplicitRefsHookWrittenRow(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	const uuid = "3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b"
	doc := "| Дата | Сесія | Тулзи | Активність |\n" +
		"|---|---|---|---|\n" +
		"| 2026-09-02 | " + uuid + " | | |\n" + // SessionStart writer
		"| 2026-09-02 | " + uuid + " | 412 | edits:9 bash:31 |\n" + // SessionEnd writer
		"| 2026-09-02 | 48213 | 12 | legacy pid row |\n" // must be ignored
	if err := os.WriteFile(filepath.Join(dir, "logs", "sessions.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	refs := explicitRefs(dir)

	// explicitRefs deliberately also emits >=8-hex substrings, so a card that
	// only ever logged a short prefix still links; those extras match no
	// session and are harmless. The contract worth pinning is narrower.
	var sawUUID bool
	for _, r := range refs {
		if r == uuid {
			sawUUID = true
		}
		if r == "48213" {
			t.Errorf("explicitRefs returned the pid row %q — the >=8-hex filter "+
				"is what keeps junk ids out of the task<->session link", r)
		}
	}
	if !sawUUID {
		t.Fatalf("explicitRefs = %v, want it to contain the hook-written uuid %s", refs, uuid)
	}
	// Both writers logged the same session; the ref must not be duplicated.
	var n int
	for _, r := range refs {
		if r == uuid {
			n++
		}
	}
	if n != 1 {
		t.Errorf("uuid appears %d times, want 1 — SessionStart and SessionEnd "+
			"rows for one session must dedupe", n)
	}
}

// A workspace whose slug already belongs to another project must still mint
// its registry row (suffixed) and keep its cards. Before FreeSlug the INSERT
// hit projects.slug's UNIQUE index, Scan logged a warning, and every card of
// the workspace vanished from the board on every scan.
func TestScanSuffixesWorkspaceSlugTakenByAnotherProject(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (4, '/code/projgamma', 'projgamma', 'Gamma checkout', '2026-06-01T00:00:00Z')`)
	pinMtime(t)

	stats := scan(t, db)

	if stats.Tasks != 6 {
		t.Errorf("tasks = %d, want 6 (warnings: %v)", stats.Tasks, stats.Warnings)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE path='/work/gamma-app' AND slug='projgamma-2'`); got != 1 {
		t.Errorf("workspace project row not minted under the suffixed slug")
	}
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE id=4 AND slug='projgamma'`); got != 1 {
		t.Errorf("the existing owner of the slug was renamed")
	}
	if got := count(t, db, `SELECT COUNT(*) FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE p.path='/work/gamma-app'`); got != 1 {
		t.Errorf("the workspace's card is not attached to its own project")
	}
}
