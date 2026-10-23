package store

import "testing"

// TestMigrate0131AddsWorktreeCleanupHeadSHA: proposals gain head_sha, and a row
// inserted without it reads the empty string (the value Apply refuses as head-changed).
func TestMigrate0131AddsWorktreeCleanupHeadSHA(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 131`).Scan(&name); err != nil {
		t.Fatalf("migration 131 not recorded: %v", err)
	}
	if name != "0131_worktree_cleanup_head_sha.sql" {
		t.Errorf("migration 131 name: want 0131_worktree_cleanup_head_sha.sql, got %s", name)
	}
	mustHaveColumns(t, db, "worktree_cleanup_proposals", "head_sha")

	if _, err := db.Exec(`INSERT INTO worktree_cleanup_proposals (repo_root, path, proof_kind, proposed_at)
	        VALUES ('/r', '/r/wt', 'landing', '2026-10-23T00:00:00Z')`); err != nil {
		t.Fatalf("insert without head_sha: %v", err)
	}
	var head string
	if err := db.QueryRow(`SELECT head_sha FROM worktree_cleanup_proposals WHERE path = '/r/wt'`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != "" {
		t.Errorf("head_sha default = %q, want ''", head)
	}
}
