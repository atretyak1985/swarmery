package store

import (
	"testing"
)

// TestMigrate0024FreshDB verifies 0024 applies on a brand-new database: the
// tasks table gains all 13 board/dispatch/verdict columns with their declared
// defaults, and idx_tasks_board exists.
func TestMigrate0024FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 24`).Scan(&name); err != nil {
		t.Fatalf("migration 24 not recorded: %v", err)
	}
	if name != "0024_task_board.sql" {
		t.Errorf("migration 24 name: want 0024_task_board.sql, got %s", name)
	}

	mustHaveColumns(t, db, "tasks",
		"board_column", "paused", "user_paused", "dependencies", "model",
		"file_scope", "branch", "worktree_path", "dispatch_error", "retry_count",
		"verify_verdict", "verify_detail", "column_moved_at")
	mustHaveIndex(t, db, "idx_tasks_board")

	// A minimal insert lands with the declared defaults.
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, '/tmp/p', 'p', 'P', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, prompt, created_at)
		VALUES (1, 1, 't', 'p', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert minimal task: %v", err)
	}
	var col, deps, scope string
	var paused, userPaused, retry int
	if err := db.QueryRow(`SELECT board_column, paused, user_paused, dependencies, file_scope, retry_count
		FROM tasks WHERE id = 1`).Scan(&col, &paused, &userPaused, &deps, &scope, &retry); err != nil {
		t.Fatal(err)
	}
	if col != "triage" {
		t.Errorf("default board_column = %q, want triage", col)
	}
	if deps != "[]" || scope != "[]" {
		t.Errorf("default dependencies/file_scope = %q/%q, want []/[]", deps, scope)
	}
	if paused != 0 || userPaused != 0 || retry != 0 {
		t.Errorf("default paused/user_paused/retry = %d/%d/%d, want 0/0/0", paused, userPaused, retry)
	}
}

// TestMigrate0024OnPopulatedDB verifies 0024 applies over a database populated
// under the 0023 schema without data loss: pre-existing task rows survive and
// adopt board_column='triage'. Guards the "existing rows land in triage" claim
// from the phase doc's acceptance criteria.
func TestMigrate0024OnPopulatedDB(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 23)

	// Pre-0024: the board columns must not exist yet.
	if cols := columnSet(t, db, "tasks"); cols["board_column"] {
		t.Fatal("board_column exists before 0024 — migrateUpTo applied too much")
	}

	// Populate a task under the 0023 schema.
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, '/tmp/p', 'p', 'P', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id, project_id, title, prompt, created_at)
		VALUES (7, 1, 'legacy', 'goal', '2026-07-24T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert legacy task under 0023 schema: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate populated db: %v", err)
	}
	mustHaveColumns(t, db, "tasks", "board_column", "verify_verdict")

	// No data loss + the new default applied to the pre-existing row.
	var title, col string
	if err := db.QueryRow(`SELECT title, board_column FROM tasks WHERE id = 7`).Scan(&title, &col); err != nil {
		t.Fatalf("legacy task vanished after migrate: %v", err)
	}
	if title != "legacy" {
		t.Errorf("legacy task title = %q, want legacy (data loss)", title)
	}
	if col != "triage" {
		t.Errorf("legacy task board_column = %q, want triage", col)
	}

	// Idempotency: a second Migrate run is a no-op.
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}
