package store

import (
	"testing"
)

// TestMigrate0021FreshDB verifies 0021 applies on a brand-new database: the
// agent_change_proposals table exists with its columns/index, the status
// CHECK enforces the closed lifecycle set, and the recommendations FK is
// honored (SET NULL semantics belong to the FK definition, insert-time
// enforcement to the pragma).
func TestMigrate0021FreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 21`).Scan(&name); err != nil {
		t.Fatalf("migration 21 not recorded: %v", err)
	}
	if name != "0021_agent_proposals.sql" {
		t.Errorf("migration 21 name: want 0021_agent_proposals.sql, got %s", name)
	}

	mustHaveColumns(t, db, "agent_change_proposals",
		"id", "recommendation_id", "agent", "agent_path", "base_sha256",
		"diff", "rationale", "status", "error", "pr_url", "created_at", "decided_at")
	mustHaveIndex(t, db, "idx_agent_proposals_agent")

	// Default status is 'proposed'.
	if _, err := db.Exec(`INSERT INTO agent_change_proposals
		(agent, agent_path, base_sha256, diff, rationale, created_at)
		VALUES ('tech-lead', '/tmp/a.md', 'abc', '--- a\n+++ b\n', 'why', '2026-07-23T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert minimal proposal: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM agent_change_proposals WHERE id = 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "proposed" {
		t.Errorf("default status = %q, want proposed", status)
	}

	// The CHECK rejects anything outside proposed|approved|applied|rejected|failed.
	for _, s := range []string{"approved", "applied", "rejected", "failed"} {
		if _, err := db.Exec(`UPDATE agent_change_proposals SET status = ? WHERE id = 1`, s); err != nil {
			t.Errorf("legal status %q rejected: %v", s, err)
		}
	}
	if _, err := db.Exec(`UPDATE agent_change_proposals SET status = 'adopted' WHERE id = 1`); err == nil {
		t.Error("status CHECK must reject 'adopted'")
	}
	if _, err := db.Exec(`INSERT INTO agent_change_proposals
		(agent, agent_path, base_sha256, diff, rationale, status, created_at)
		VALUES ('x', '/tmp/x.md', 'h', '', '', 'draft', '2026-07-23T00:00:00.000Z')`); err == nil {
		t.Error("status CHECK must reject 'draft'")
	}

	// FK enforcement: a dangling recommendation_id must be rejected.
	if _, err := db.Exec(`INSERT INTO agent_change_proposals
		(recommendation_id, agent, agent_path, base_sha256, diff, rationale, created_at)
		VALUES (999, 'y', '/tmp/y.md', 'h', '', '', '2026-07-23T00:00:00.000Z')`); err == nil {
		t.Error("proposal with dangling recommendation_id accepted; want FK violation")
	}
}

// TestMigrate0021OnPopulatedDB verifies 0021 applies over a database populated
// under the 0020 schema, the recommendations ON DELETE SET NULL contract
// works, and the runner stays idempotent.
func TestMigrate0021OnPopulatedDB(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 20)

	// Pre-0021 sanity: the proposals table must not exist yet.
	if cols := columnSet(t, db, "agent_change_proposals"); len(cols) != 0 {
		t.Fatal("agent_change_proposals exists before 0021 — migrateUpTo applied too much")
	}
	if _, err := db.Exec(`INSERT INTO recommendations
		(id, rule, target_kind, target, title, detail, evidence, status, dedup_key, created_at, updated_at)
		VALUES (1, 'R2', 'agent', 'tech-lead', 't', 'd', '{}', 'accepted', 'R2:tech-lead',
		        '2026-07-23T00:00:00.000Z', '2026-07-23T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert recommendation under 0020 schema: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate populated db: %v", err)
	}
	mustHaveColumns(t, db, "agent_change_proposals", "id", "recommendation_id", "agent", "status")

	if _, err := db.Exec(`INSERT INTO agent_change_proposals
		(recommendation_id, agent, agent_path, base_sha256, diff, rationale, created_at)
		VALUES (1, 'tech-lead', '/tmp/a.md', 'abc', 'diff', 'why', '2026-07-23T00:00:00.000Z')`); err != nil {
		t.Fatalf("insert proposal referencing existing recommendation: %v", err)
	}

	// ON DELETE SET NULL: deleting the recommendation orphans, not deletes.
	if _, err := db.Exec(`DELETE FROM recommendations WHERE id = 1`); err != nil {
		t.Fatalf("delete recommendation: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_change_proposals WHERE recommendation_id IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("proposals with NULL recommendation_id after delete = %d, want 1", n)
	}

	// Idempotency: a second Migrate run is a no-op.
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
}
