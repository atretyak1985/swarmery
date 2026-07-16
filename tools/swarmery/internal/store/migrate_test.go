package store

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// openRaw opens a SQLite DB with the same pragmas as Open but WITHOUT running
// migrations, so tests control which migrations are applied.
func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// migrateUpTo mirrors Migrate's runner loop but stops after maxVersion —
// used to reconstruct a database as it existed before a later migration.
func migrateUpTo(t *testing.T, db *sql.DB, maxVersion int) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			t.Fatalf("parse version of %s: %v", name, err)
		}
		if version > maxVersion {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
		if _, err := db.Exec(
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			version, name, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			t.Fatalf("record migration %s: %v", name, err)
		}
	}
}

func columnSet(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table_info %s: %v", table, err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info %s: %v", table, err)
	}
	return cols
}

func mustHaveColumns(t *testing.T, db *sql.DB, table string, want ...string) {
	t.Helper()
	cols := columnSet(t, db, table)
	if len(cols) == 0 {
		t.Fatalf("table %s does not exist", table)
	}
	for _, c := range want {
		if !cols[c] {
			t.Errorf("table %s: missing column %s (has %v)", table, c, cols)
		}
	}
}

func mustHaveIndex(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name,
	).Scan(&n)
	if err != nil {
		t.Fatalf("query sqlite_master for index %s: %v", name, err)
	}
	if n != 1 {
		t.Errorf("index %s: want 1, got %d", name, n)
	}
}

// TestMigrate0008FreshDB verifies 0008 applies on a brand-new database:
// hooks/commands exist with their unique keys and indexes, and agents/skills
// carry the new provenance columns.
func TestMigrate0008FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}

	var name string
	err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 8`).Scan(&name)
	if err != nil {
		t.Fatalf("migration 8 not recorded: %v", err)
	}
	if name != "0008_system.sql" {
		t.Errorf("migration 8 name: want 0008_system.sql, got %s", name)
	}

	mustHaveColumns(t, db, "hooks",
		"id", "scope", "project_id", "event", "matcher", "command",
		"timeout", "status_message", "source_file", "seq", "enabled",
		"managed", "content_hash")
	mustHaveColumns(t, db, "commands",
		"id", "name", "scope", "project_id", "file_path", "description",
		"origin", "plugin_name", "content_hash", "deleted")
	mustHaveColumns(t, db, "agents", "origin", "plugin_name")
	mustHaveColumns(t, db, "skills", "origin", "plugin_name")
	mustHaveIndex(t, db, "idx_hooks_project")
	mustHaveIndex(t, db, "idx_commands_project")

	// UNIQUE(source_file, event, seq): second identical key must fail.
	const insHook = `INSERT INTO hooks (scope, event, matcher, command, source_file, seq, content_hash)
		VALUES ('global', 'Stop', NULL, 'swarmery hook stop', '/tmp/settings.local.json', 0, 'h1')`
	if _, err := db.Exec(insHook); err != nil {
		t.Fatalf("insert hook: %v", err)
	}
	if _, err := db.Exec(insHook); err == nil {
		t.Error("duplicate (source_file, event, seq) accepted; want UNIQUE violation")
	}

	// UNIQUE(name, scope, project_id) on commands (non-NULL project scope).
	if _, err := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/tmp/p', 'p', '2026-07-13T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	const insCmd = `INSERT INTO commands (name, scope, project_id, file_path, content_hash)
		VALUES ('dashboard', 'project', 1, '/tmp/p/.claude/commands/dashboard.md', 'c1')`
	if _, err := db.Exec(insCmd); err != nil {
		t.Fatalf("insert command: %v", err)
	}
	if _, err := db.Exec(insCmd); err == nil {
		t.Error("duplicate (name, scope, project_id) accepted; want UNIQUE violation")
	}
	// The plugin:name convention coexists with a same-named local command.
	if _, err := db.Exec(`INSERT INTO commands (name, scope, project_id, file_path, origin, plugin_name, content_hash)
		VALUES ('core:dashboard', 'project', 1, '/tmp/cache/core/commands/dashboard.md', 'plugin', 'core', 'c2')`); err != nil {
		t.Errorf("insert plugin-qualified command: %v", err)
	}
}

// TestMigrate0008OnPopulatedDB verifies 0008 applies cleanly on a database
// that already holds data written under the 0007 schema, and that existing
// agents/skills rows are backfilled with origin='local' / plugin_name NULL.
func TestMigrate0008OnPopulatedDB(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 7)

	// Pre-0008 sanity: provenance columns must not exist yet.
	if cols := columnSet(t, db, "agents"); cols["origin"] {
		t.Fatal("agents.origin exists before 0008 — migrateUpTo applied too much")
	}

	if _, err := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/tmp/p', 'p', '2026-07-13T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (name, scope, project_id, file_path)
		VALUES ('tech-lead', 'project', 1, '/tmp/p/.claude/agents/tech-lead.md')`); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO skills (name, scope, project_id, dir_path)
		VALUES ('deployment', 'project', 1, '/tmp/p/.claude/skills/deployment')`); err != nil {
		t.Fatalf("insert skill: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate populated db: %v", err)
	}

	var origin string
	var pluginName sql.NullString
	if err := db.QueryRow(`SELECT origin, plugin_name FROM agents WHERE name = 'tech-lead'`).
		Scan(&origin, &pluginName); err != nil {
		t.Fatalf("read migrated agent: %v", err)
	}
	if origin != "local" || pluginName.Valid {
		t.Errorf("agent backfill: want (local, NULL), got (%s, %v)", origin, pluginName)
	}
	if err := db.QueryRow(`SELECT origin, plugin_name FROM skills WHERE name = 'deployment'`).
		Scan(&origin, &pluginName); err != nil {
		t.Fatalf("read migrated skill: %v", err)
	}
	if origin != "local" || pluginName.Valid {
		t.Errorf("skill backfill: want (local, NULL), got (%s, %v)", origin, pluginName)
	}

	// New tables are usable, including the projects FK.
	if _, err := db.Exec(`INSERT INTO hooks (scope, project_id, event, matcher, command, timeout, source_file, seq, content_hash)
		VALUES ('project', 1, 'PermissionRequest', '*', 'swarmery hook permission-request', 130, '/tmp/p/.claude/settings.local.json', 0, 'h1')`); err != nil {
		t.Errorf("insert hook after migration: %v", err)
	}
	// FK enforcement: unknown project must be rejected.
	if _, err := db.Exec(`INSERT INTO hooks (scope, project_id, event, command, source_file, seq, content_hash)
		VALUES ('project', 999, 'Stop', 'x', '/tmp/x.json', 0, 'h2')`); err == nil {
		t.Error("hook with dangling project_id accepted; want FK violation")
	}

	// Idempotency: a second Migrate run is a no-op.
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}

// TestMigrate0012FreshDB verifies turns_fts exists after a fresh migrate and
// that the triggers track the ingester's REAL write pattern: INSERT with NULL
// text (ingest.go upsertTurn) followed by UPDATE turns SET text (flushTurnTexts).
func TestMigrate0012FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	matchCount := func(match string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM turns_fts WHERE turns_fts MATCH ?`, match).Scan(&n); err != nil {
			t.Fatalf("match %q: %v", match, err)
		}
		return n
	}

	mustExec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-07-16T00:00:00Z')`)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, started_at) VALUES (1, 1, 'u1', '2026-07-16T00:00:00Z')`)
	mustExec(`INSERT INTO turns (id, session_id, seq, role, started_at) VALUES (7, 1, 0, 'user', '2026-07-16T00:00:00Z')`)

	// INSERT trigger indexed the row with NULL text → no tokens yet.
	if n := matchCount(`"refactor"`); n != 0 {
		t.Errorf("match before text set = %d, want 0", n)
	}

	// UPDATE OF text trigger: delete-old + insert-new.
	mustExec(`UPDATE turns SET text = 'please refactor the reactor loop' WHERE id = 7`)
	if n := matchCount(`"refactor"`); n != 1 {
		t.Errorf("match after UPDATE OF text = %d, want 1", n)
	}

	// External-content contract: rowid == turns.id.
	var rowid int64
	if err := db.QueryRow(
		`SELECT rowid FROM turns_fts WHERE turns_fts MATCH '"refactor"'`).Scan(&rowid); err != nil {
		t.Fatalf("read rowid: %v", err)
	}
	if rowid != 7 {
		t.Errorf("rowid = %d, want 7 (turns.id)", rowid)
	}

	// A second text update replaces tokens (no stale matches).
	mustExec(`UPDATE turns SET text = 'now about databases instead' WHERE id = 7`)
	if n := matchCount(`"refactor"`); n != 0 {
		t.Errorf("stale token after re-update = %d, want 0", n)
	}
	if n := matchCount(`"databases"`); n != 1 {
		t.Errorf("match after re-update = %d, want 1", n)
	}

	// DELETE trigger removes the row from the index.
	mustExec(`DELETE FROM turns WHERE id = 7`)
	if n := matchCount(`"databases"`); n != 0 {
		t.Errorf("match after delete = %d, want 0", n)
	}
}

// TestMigrate0012Backfill verifies the one-time backfill: rows written under
// the 0011 schema (turns.text already set by the ingester) become searchable
// when 0012 applies, and the external-content index passes integrity-check.
func TestMigrate0012Backfill(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 11)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-07-16T00:00:00Z')`)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, started_at) VALUES (1, 1, 'u1', '2026-07-16T00:00:00Z')`)
	mustExec(`INSERT INTO turns (id, session_id, seq, role, started_at, text) VALUES
		(1, 1, 0, 'user',      '2026-07-16T00:00:00Z', 'stabilize the plasma containment'),
		(2, 1, 1, 'assistant', '2026-07-16T00:00:01Z', NULL)`)

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate populated db: %v", err)
	}

	var rowid int64
	if err := db.QueryRow(
		`SELECT rowid FROM turns_fts WHERE turns_fts MATCH '"plasma"'`).Scan(&rowid); err != nil {
		t.Fatalf("backfilled row not searchable: %v", err)
	}
	if rowid != 1 {
		t.Errorf("rowid = %d, want 1", rowid)
	}

	// The NULL-text row must be registered too (index/content consistency):
	// fts5 integrity-check errors if the shadow tables disagree with `turns`.
	if _, err := db.Exec(`INSERT INTO turns_fts(turns_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("fts5 integrity-check failed: %v", err)
	}

	// Idempotency: a second Migrate run is a no-op (no double-backfill).
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}

// TestMigrate0015ProjectMeta verifies 0015 applies on a database populated
// under the 0014 schema: projects gains pinned/tags with safe defaults, and
// the runner stays idempotent.
func TestMigrate0015ProjectMeta(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 14)

	// Pre-0015 sanity: the meta columns must not exist yet.
	if cols := columnSet(t, db, "projects"); cols["pinned"] || cols["tags"] {
		t.Fatal("projects.pinned/tags exist before 0015 — migrateUpTo applied too much")
	}
	if _, err := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/tmp/p', 'p', '2026-07-16T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate populated db: %v", err)
	}
	mustHaveColumns(t, db, "projects", "pinned", "tags")

	// Existing rows are backfilled with the defaults (unpinned, empty array).
	var pinned int
	var tags string
	if err := db.QueryRow(`SELECT pinned, tags FROM projects WHERE slug = 'p'`).Scan(&pinned, &tags); err != nil {
		t.Fatalf("read migrated project: %v", err)
	}
	if pinned != 0 || tags != "[]" {
		t.Errorf("backfill: want (0, []), got (%d, %s)", pinned, tags)
	}

	// Idempotency: a second Migrate run is a no-op.
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}

// TestMigration0013ApprovalRules: the auto-approve rules table exists with
// its defaults and the action CHECK.
func TestMigration0013ApprovalRules(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "rules.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO approval_rules (tool_pattern, created_at)
		 VALUES ('Read', '2026-07-16T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert minimal rule: %v", err)
	}
	var action string
	var enabled int
	if err := db.QueryRow(
		`SELECT action, enabled FROM approval_rules WHERE id = 1`).Scan(&action, &enabled); err != nil {
		t.Fatal(err)
	}
	if action != "approve" || enabled != 1 {
		t.Errorf("defaults = (%q, %d), want ('approve', 1)", action, enabled)
	}
	if _, err := db.Exec(
		`INSERT INTO approval_rules (tool_pattern, action, created_at)
		 VALUES ('Read', 'deny', '2026-07-16T00:00:00.000Z')`); err == nil {
		t.Error("action CHECK must reject 'deny'")
	}
}
