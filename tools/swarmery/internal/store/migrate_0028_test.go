package store

import "testing"

// TestMigrate0028FreshDB verifies 0028 creates epic_phases with its defaults and
// the (workspace_task_id, doc_path) uniqueness — on a brand-new database.
func TestMigrate0028FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 28`).Scan(&name); err != nil {
		t.Fatalf("migration 28 not recorded: %v", err)
	}
	if name != "0028_epic_phases.sql" {
		t.Errorf("migration 28 name: want 0028_epic_phases.sql, got %s", name)
	}

	mustHaveColumns(t, db, "epic_phases",
		"workspace_task_id", "seq", "name", "doc_path", "depends_on",
		"checkboxes_total", "checkboxes_done", "activated_at", "activated_board_task_id")
	mustHaveIndex(t, db, "idx_epic_phases_task")

	// Defaults: depends_on '[]', checkbox counts 0, activated_* NULL.
	if _, err := db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path)
		VALUES (1, 1, 'Phase 1', '/ws/plan/phase-1.md')`); err != nil {
		t.Fatalf("insert minimal phase: %v", err)
	}
	var (
		deps                       string
		total, done                int
		activatedAt, activatedTask any
	)
	if err := db.QueryRow(`SELECT depends_on, checkboxes_total, checkboxes_done,
		activated_at, activated_board_task_id FROM epic_phases WHERE workspace_task_id = 1`).
		Scan(&deps, &total, &done, &activatedAt, &activatedTask); err != nil {
		t.Fatal(err)
	}
	if deps != "[]" {
		t.Errorf("default depends_on = %q, want []", deps)
	}
	if total != 0 || done != 0 {
		t.Errorf("default checkbox counts = %d/%d, want 0/0", done, total)
	}
	if activatedAt != nil || activatedTask != nil {
		t.Errorf("default activated_* = %v/%v, want nil/nil", activatedAt, activatedTask)
	}

	// UNIQUE(workspace_task_id, doc_path): a duplicate doc_path for the same task
	// is rejected (the upsert relies on this to be idempotent).
	if _, err := db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path)
		VALUES (1, 2, 'Phase 1 dup', '/ws/plan/phase-1.md')`); err == nil {
		t.Error("expected UNIQUE violation for duplicate (workspace_task_id, doc_path), got nil")
	}

	// The same doc_path under a DIFFERENT task is fine (each epic owns its phases).
	if _, err := db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path)
		VALUES (2, 1, 'Other epic phase 1', '/ws/plan/phase-1.md')`); err != nil {
		t.Fatalf("same doc_path under a different task should be allowed: %v", err)
	}

	// Idempotency: a second Migrate run is a no-op.
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}
