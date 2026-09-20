package store

import "testing"

// TestMigrate0074AddsTargetColumns: the rebuild lands the two routing columns and
// widens the status vocabulary with 'needs_target' — both are what phase 5's
// skill proposals are stored in.
func TestMigrate0074AddsTargetColumns(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 74`).Scan(&name); err != nil {
		t.Fatalf("migration 74 not recorded: %v", err)
	}
	if name != "0074_proposals_target.sql" {
		t.Errorf("migration 74 name: want 0074_proposals_target.sql, got %s", name)
	}

	mustHaveColumns(t, db, "agent_change_proposals", "target_kind", "target_path")
	mustHaveIndex(t, db, "idx_agent_proposals_agent")
	mustHaveIndex(t, db, "idx_agent_proposals_one_open")

	// A skill row with the new status is storable.
	if _, err := db.Exec(`
		INSERT INTO agent_change_proposals
		  (agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES ('sync the cache before building', '', 'skill', '', '', '', '',
		        'needs_target', '2026-09-20T00:00:00Z')`); err != nil {
		t.Fatalf("insert needs_target skill proposal: %v", err)
	}

	// The widening must not have turned either CHECK into a no-op.
	if _, err := db.Exec(`
		INSERT INTO agent_change_proposals
		  (agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES ('x', '', 'playbook', '', '', '', '', 'proposed', '2026-09-20T00:00:00Z')`,
	); err == nil {
		t.Error("target_kind='playbook' was accepted — the CHECK was dropped, not added")
	}
	if _, err := db.Exec(`
		INSERT INTO agent_change_proposals
		  (agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES ('y', '', 'agent', '', '', '', '', 'half_done', '2026-09-20T00:00:00Z')`,
	); err == nil {
		t.Error("status='half_done' was accepted — the status CHECK was dropped, not widened")
	}
}

// TestMigrate0074OneOpenPerTarget pins the invariant the index now carries:
// open-ness is per TARGET, not per agent name. An agent proposal and a skill
// proposal may share a name; two proposals for the same SKILL.md may not.
func TestMigrate0074OneOpenPerTarget(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	ins := func(agent, kind, path, status string) error {
		_, err := db.Exec(`
			INSERT INTO agent_change_proposals
			  (agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
			VALUES (?, '', ?, ?, '', '', '', ?, '2026-09-20T00:00:00Z')`,
			agent, kind, path, status)
		return err
	}

	if err := ins("tech-lead", "agent", "", "proposed"); err != nil {
		t.Fatalf("first agent proposal: %v", err)
	}
	if err := ins("tech-lead", "agent", "", "approved"); err == nil {
		t.Error("a second OPEN agent proposal for the same agent was accepted")
	}
	// Different KIND, same name — a distinct target, so it must be allowed.
	if err := ins("tech-lead", "skill", "", "needs_target"); err != nil {
		t.Errorf("a skill proposal must not collide with an agent of the same name: %v", err)
	}
	// Same SKILL.md twice — one target, so the second must collide.
	const skillPath = "plugins/core/skills/docker-build/SKILL.md"
	if err := ins("cache lesson", "skill", skillPath, "proposed"); err != nil {
		t.Fatalf("first skill proposal: %v", err)
	}
	if err := ins("another lesson", "skill", skillPath, "proposed"); err == nil {
		t.Error("a second OPEN proposal for the same SKILL.md was accepted")
	}
	// A CLOSED row frees the slot (the index is partial on the open statuses).
	if _, err := db.Exec(
		`UPDATE agent_change_proposals SET status = 'rejected' WHERE target_path = ?`, skillPath); err != nil {
		t.Fatal(err)
	}
	if err := ins("another lesson", "skill", skillPath, "proposed"); err != nil {
		t.Errorf("rejecting the open row must free the target slot: %v", err)
	}
}

// TestMigrate0074PreservesRowsAndFK is the data-safety case for the rebuild: the
// pre-0074 rows survive verbatim, default to target_kind='agent', and keep their
// recommendation_id (the table dropped here is the CHILD, so no ON DELETE action
// fires — this test is what proves that claim rather than assuming it).
func TestMigrate0074PreservesRowsAndFK(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 73)

	if _, err := db.Exec(`
		INSERT INTO recommendations
		  (id, rule, target_kind, target, title, detail, evidence, dedup_key, created_at, updated_at)
		VALUES (9, 'R2', 'agent', 'tech-lead', 'improve it', 'detail', '{}', 'R2:agent:tech-lead',
		        '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert recommendation: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO agent_change_proposals
		  (id, recommendation_id, agent, agent_path, base_sha256, diff, rationale, status, created_at)
		VALUES (1, 9, 'tech-lead', 'plugins/core/agents/tech-lead.md', 'abc', 'the diff', 'why',
		        'approved', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert proposal: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	var recID *int64
	var agent, agentPath, kind, path, diff, status string
	if err := db.QueryRow(`
		SELECT recommendation_id, agent, agent_path, target_kind, target_path, diff, status
		  FROM agent_change_proposals WHERE id = 1`).
		Scan(&recID, &agent, &agentPath, &kind, &path, &diff, &status); err != nil {
		t.Fatalf("read migrated proposal: %v", err)
	}
	if recID == nil || *recID != 9 {
		t.Errorf("recommendation_id = %v, want 9 (the rebuild dropped the link)", recID)
	}
	if agent != "tech-lead" || agentPath != "plugins/core/agents/tech-lead.md" {
		t.Errorf("agent/%s path/%s not carried across", agent, agentPath)
	}
	if kind != "agent" || path != "" {
		t.Errorf("legacy row = %s/%q, want agent/\"\" (the DEFAULT must preserve meaning)", kind, path)
	}
	if diff != "the diff" || status != "approved" {
		t.Errorf("diff/status = %q/%q, want %q/approved", diff, status, "the diff")
	}

	// The scratch table must not survive under its build name.
	var leftovers int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'agent_change_proposals_new'`).Scan(&leftovers); err != nil {
		t.Fatalf("query schema: %v", err)
	}
	if leftovers != 0 {
		t.Errorf("the rebuild left agent_change_proposals_new behind (%d)", leftovers)
	}
}
