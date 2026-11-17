package store

import (
	"strings"
	"testing"
)

// TestMigrate0135ProposalOutcomeLens: the five columns land with empty
// defaults, and the status CHECK of 0074 is untouched (a kept_base row is
// stored as 'rejected'; an unknown status is still refused).
func TestMigrate0135ProposalOutcomeLens(t *testing.T) {
	db, err := Open(t.TempDir() + "/m135.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 135`).Scan(&name); err != nil {
		t.Fatalf("migration 135 not recorded: %v", err)
	}
	if name != "0135_proposal_outcome_lens.sql" {
		t.Errorf("migration 135 name = %s", name)
	}
	if _, err := db.Exec(`INSERT INTO agent_change_proposals
		(agent, agent_path, base_sha256, diff, rationale, created_at)
		VALUES ('a', 'plugins/core/agents/a.md', 'x', '', '', '2026-11-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var outcome, lens, form, followups, warnings string
	if err := db.QueryRow(`SELECT outcome, lens, form, suggested_followups_json, warnings_json
		FROM agent_change_proposals WHERE agent = 'a'`).
		Scan(&outcome, &lens, &form, &followups, &warnings); err != nil {
		t.Fatal(err)
	}
	if outcome != "" || lens != "" || form != "" || followups != "[]" || warnings != "[]" {
		t.Errorf("defaults: outcome=%q lens=%q form=%q followups=%q warnings=%q",
			outcome, lens, form, followups, warnings)
	}

	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'agent_change_proposals'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "CHECK (status IN ('proposed','approved','applied','rejected','failed','needs_target'))") {
		t.Errorf("status CHECK changed: %s", ddl)
	}
	if _, err := db.Exec(`UPDATE agent_change_proposals SET status = 'kept_base' WHERE agent = 'a'`); err == nil {
		t.Error("status 'kept_base' accepted — the CHECK must stay closed")
	}
}
