package store

import "testing"

// TestMigrate0071AcceptsMemoryKind: after 0071 the CHECK admits 'memory', which
// is the whole point of the migration — R10 cannot upsert without it.
func TestMigrate0071AcceptsMemoryKind(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 71`).Scan(&name); err != nil {
		t.Fatalf("migration 71 not recorded: %v", err)
	}
	if name != "0071_recommendation_kind_memory.sql" {
		t.Errorf("migration 71 name: want 0071_recommendation_kind_memory.sql, got %s", name)
	}

	if _, err := db.Exec(`
		INSERT INTO recommendations
		  (rule, target_kind, target, title, detail, evidence, dedup_key, created_at, updated_at)
		VALUES ('R10', 'memory', '/tmp/project', 'index over budget', 'consolidate', '{}',
		        'R10:memory:/tmp/project', '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert target_kind='memory' after 0071: %v", err)
	}

	// The widening must not have loosened the constraint into a no-op.
	if _, err := db.Exec(`
		INSERT INTO recommendations
		  (rule, target_kind, target, title, detail, evidence, dedup_key, created_at, updated_at)
		VALUES ('R10', 'not_a_kind', 'x', 't', 'd', '{}', 'dk2',
		        '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`,
	); err == nil {
		t.Fatal("an unknown target_kind was accepted — the CHECK constraint was dropped, not widened")
	}

	mustHaveIndex(t, db, "idx_recommendations_status")
}

// TestMigrate0071PreservesProposalRecommendationID is the data-safety case.
//
// The rebuild DROPs recommendations while foreign keys are enforced (the DSN
// sets foreign_keys(1) and migrate.go wraps each migration in a transaction, in
// which PRAGMA foreign_keys is a documented no-op). agent_change_proposals
// declares recommendation_id ON DELETE SET NULL, so without the capture/restore
// in 0071 this column is silently NULLed on every live proposal row.
func TestMigrate0071PreservesProposalRecommendationID(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 70)

	// A pre-0071 recommendation and a proposal pointing at it.
	if _, err := db.Exec(`
		INSERT INTO recommendations
		  (id, rule, target_kind, target, title, detail, evidence, dedup_key, created_at, updated_at)
		VALUES (7, 'R7', 'project', '/tmp/p', 'tidy the repo', 'detail', '{}', 'R7:project:/tmp/p',
		        '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agent_change_proposals
		  (id, recommendation_id, agent, agent_path, base_sha256, diff, rationale, created_at)
		VALUES (1, 7, 'core:tech-lead', '/tmp/a.md', 'abc', 'diff', 'why', '2026-09-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert proposal: %v", err)
	}
	// A second proposal with no recommendation must stay NULL, not be invented.
	if _, err := db.Exec(`
		INSERT INTO agent_change_proposals
		  (id, recommendation_id, agent, agent_path, base_sha256, diff, rationale, created_at)
		VALUES (2, NULL, 'core:planner', '/tmp/b.md', 'def', 'diff', 'why', '2026-09-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert unlinked proposal: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	var linked *int64
	if err := db.QueryRow(
		`SELECT recommendation_id FROM agent_change_proposals WHERE id = 1`).Scan(&linked); err != nil {
		t.Fatalf("read proposal 1: %v", err)
	}
	if linked == nil {
		t.Fatal("agent_change_proposals.recommendation_id was NULLed by the 0071 rebuild — " +
			"DROP TABLE recommendations fired ON DELETE SET NULL (PRAGMA foreign_keys is a no-op inside the migration transaction)")
	}
	if *linked != 7 {
		t.Errorf("recommendation_id = %d, want 7", *linked)
	}

	var unlinked *int64
	if err := db.QueryRow(
		`SELECT recommendation_id FROM agent_change_proposals WHERE id = 2`).Scan(&unlinked); err != nil {
		t.Fatalf("read proposal 2: %v", err)
	}
	if unlinked != nil {
		t.Errorf("proposal 2 recommendation_id = %d, want NULL", *unlinked)
	}

	// The parent row itself survived the rebuild verbatim.
	var title string
	if err := db.QueryRow(`SELECT title FROM recommendations WHERE id = 7`).Scan(&title); err != nil {
		t.Fatalf("read recommendation 7: %v", err)
	}
	if title != "tidy the repo" {
		t.Errorf("recommendation title = %q, want %q", title, "tidy the repo")
	}

	// And the scratch table the migration used is not left behind.
	var leftovers int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM temp.sqlite_master WHERE name = '_m0071_proposal_fk'`).Scan(&leftovers); err != nil {
		t.Fatalf("query temp schema: %v", err)
	}
	if leftovers != 0 {
		t.Errorf("the migration left its temp table behind (%d)", leftovers)
	}
}
