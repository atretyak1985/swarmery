package store

import "testing"

// TestMigrate0130AddsWorktreeCleanup: the proposals table lands with its
// columns, and the partial unique index allows one OPEN proposal per checkout
// while decided history rows for the same path stay unconstrained.
func TestMigrate0130AddsWorktreeCleanup(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 130`).Scan(&name); err != nil {
		t.Fatalf("migration 130 not recorded: %v", err)
	}
	if name != "0130_worktree_cleanup.sql" {
		t.Errorf("migration 130 name: want 0130_worktree_cleanup.sql, got %s", name)
	}
	mustHaveColumns(t, db, "worktree_cleanup_proposals",
		"id", "repo_root", "project_id", "path", "branch", "head_sha", "phase_id", "task_id",
		"proof_kind", "proof_detail", "proposed_at", "decided", "decided_at", "salvage_branch", "error")

	ins := `INSERT INTO worktree_cleanup_proposals (repo_root, path, proof_kind, proposed_at, decided)
	        VALUES ('/r', '/r/wt', 'landing', '2026-10-22T00:00:00Z', ?)`
	if _, err := db.Exec(ins, nil); err != nil {
		t.Fatalf("first open proposal: %v", err)
	}
	if _, err := db.Exec(ins, nil); err == nil {
		t.Fatal("second OPEN proposal for the same checkout succeeded; want a unique conflict")
	}
	for _, d := range []string{"removed", "removed", "gone"} {
		if _, err := db.Exec(ins, d); err != nil {
			t.Fatalf("decided history row %q: %v", d, err)
		}
	}
}
