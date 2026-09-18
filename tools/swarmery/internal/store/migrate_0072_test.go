package store

import "testing"

// agent-memory phase 4 — migration 0072 (target_kind 'skill').
//
// 0072 is the twin of 0071: another CHECK widening that SQLite can only express
// as a table rebuild, with the same foreign-key hazard around the DROP. Until
// this file existed it was pinned only INCIDENTALLY — migrate_0071_test.go
// migrates to HEAD, so it runs 0072 too and a broken 0072 restore failed a test
// whose name and message blamed 0071. These cases name the right migration.

// TestMigrate0072AcceptsSkillKind: after 0072 the CHECK admits 'skill', which is
// the whole point of the migration — R11 cannot upsert without it.
func TestMigrate0072AcceptsSkillKind(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 72`).Scan(&name); err != nil {
		t.Fatalf("migration 72 not recorded: %v", err)
	}
	if name != "0072_recommendation_kind_skill.sql" {
		t.Errorf("migration 72 name: want 0072_recommendation_kind_skill.sql, got %s", name)
	}

	if _, err := db.Exec(`
		INSERT INTO recommendations
		  (rule, target_kind, target, title, detail, evidence, dedup_key, created_at, updated_at)
		VALUES ('R11', 'skill', 'sync cache before build', 'Recurring lesson', 'detail', '{}',
		        'R11:sync cache before build', '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert target_kind='skill' after 0072: %v", err)
	}

	// 0071's vocabulary must survive 0072's rebuild — the widening adds, never
	// replaces.
	if _, err := db.Exec(`
		INSERT INTO recommendations
		  (rule, target_kind, target, title, detail, evidence, dedup_key, created_at, updated_at)
		VALUES ('R10', 'memory', '/tmp/project', 'index over budget', 'consolidate', '{}',
		        'R10:memory:/tmp/project', '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert target_kind='memory' after 0072: %v", err)
	}

	// The widening must not have loosened the constraint into a no-op.
	if _, err := db.Exec(`
		INSERT INTO recommendations
		  (rule, target_kind, target, title, detail, evidence, dedup_key, created_at, updated_at)
		VALUES ('R11', 'not_a_kind', 'x', 't', 'd', '{}', 'dk2',
		        '2026-09-18T00:00:00Z', '2026-09-18T00:00:00Z')`,
	); err == nil {
		t.Fatal("an unknown target_kind was accepted — the CHECK constraint was dropped, not widened")
	}

	mustHaveIndex(t, db, "idx_recommendations_status")
}

// TestMigrate0072PreservesProposalRecommendationID is 0072's data-safety case,
// pinned at version 71 so a regression names 0072 rather than its predecessor.
//
// The rebuild DROPs recommendations while foreign keys are enforced (the DSN
// sets foreign_keys(1) and migrate.go wraps each migration in a transaction, in
// which PRAGMA foreign_keys is a documented no-op). agent_change_proposals
// declares recommendation_id ON DELETE SET NULL, so without 0072's own
// capture/restore the column is silently NULLed on every live proposal row.
func TestMigrate0072PreservesProposalRecommendationID(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 71)

	// A recommendation that predates 0072 and a proposal pointing at it.
	if _, err := db.Exec(`
		INSERT INTO recommendations
		  (id, rule, target_kind, target, title, detail, evidence, dedup_key, created_at, updated_at)
		VALUES (9, 'R10', 'memory', '/tmp/p', 'consolidate the index', 'detail', '{}', 'R10:memory:/tmp/p',
		        '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agent_change_proposals
		  (id, recommendation_id, agent, agent_path, base_sha256, diff, rationale, created_at)
		VALUES (1, 9, 'core:tech-lead', '/tmp/a.md', 'abc', 'diff', 'why', '2026-09-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert proposal: %v", err)
	}
	// A proposal with no recommendation must stay NULL, not be invented.
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
		t.Fatal("agent_change_proposals.recommendation_id was NULLed by the 0072 rebuild — " +
			"DROP TABLE recommendations fired ON DELETE SET NULL (PRAGMA foreign_keys is a no-op inside the migration transaction)")
	}
	if *linked != 9 {
		t.Errorf("recommendation_id = %d, want 9", *linked)
	}

	var unlinked *int64
	if err := db.QueryRow(
		`SELECT recommendation_id FROM agent_change_proposals WHERE id = 2`).Scan(&unlinked); err != nil {
		t.Fatalf("read proposal 2: %v", err)
	}
	if unlinked != nil {
		t.Errorf("proposal 2 recommendation_id = %d, want NULL", *unlinked)
	}

	// The parent row itself survived the rebuild verbatim, kind included.
	var title, kind string
	if err := db.QueryRow(
		`SELECT title, target_kind FROM recommendations WHERE id = 9`).Scan(&title, &kind); err != nil {
		t.Fatalf("read recommendation 9: %v", err)
	}
	if title != "consolidate the index" || kind != "memory" {
		t.Errorf("recommendation 9 = %q/%q, want %q/memory", title, kind, "consolidate the index")
	}

	// And the scratch table the migration used is not left behind. Temp tables
	// are per-connection and openRaw pins the pool to one, so a leak would be
	// visible to every later query on this handle.
	var leftovers int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM temp.sqlite_master WHERE name = '_m0072_proposal_fk'`).Scan(&leftovers); err != nil {
		t.Fatalf("query temp schema: %v", err)
	}
	if leftovers != 0 {
		t.Errorf("the migration left its temp table behind (%d)", leftovers)
	}
}
