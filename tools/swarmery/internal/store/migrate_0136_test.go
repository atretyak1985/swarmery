package store

import (
	"strings"
	"testing"
)

// TestMigrate0136ProposalProbes: the table lands, both CHECKs are closed, and
// there is no foreign key onto agent_change_proposals (a probe row for a
// proposal that does not exist is accepted — notes 0082/0084).
func TestMigrate0136ProposalProbes(t *testing.T) {
	db, err := Open(t.TempDir() + "/m136.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 136`).Scan(&name); err != nil {
		t.Fatalf("migration 136 not recorded: %v", err)
	}
	if name != "0136_proposal_probes.sql" {
		t.Errorf("migration 136 name = %s", name)
	}

	if _, err := db.Exec(`INSERT INTO proposal_probes
		(proposal_id, job_id, source, task_id, base_pass, variant_pass, blind_choice, reason, usd, created_at)
		VALUES (999999, 'j1', 'promptfoo', 'case-1', 1, 1, 'tie', 'same', 0.12, '2026-11-03T00:00:00Z')`); err != nil {
		t.Fatalf("insert (no FK expected): %v", err)
	}
	if _, err := db.Exec(`INSERT INTO proposal_probes (proposal_id, blind_choice, created_at)
		VALUES (1, 'no_suite', '2026-11-03T00:00:00Z')`); err != nil {
		t.Fatalf("insert no_suite with defaults: %v", err)
	}
	var source string
	var basePass any
	if err := db.QueryRow(`SELECT source, base_pass FROM proposal_probes WHERE blind_choice = 'no_suite'`).
		Scan(&source, &basePass); err != nil {
		t.Fatal(err)
	}
	if source != "promptfoo" || basePass != nil {
		t.Errorf("defaults: source=%q base_pass=%v", source, basePass)
	}

	for _, bad := range []string{
		`INSERT INTO proposal_probes (proposal_id, blind_choice, created_at) VALUES (1, 'maybe', 'x')`,
		`INSERT INTO proposal_probes (proposal_id, source, blind_choice, created_at) VALUES (1, 'other', 'tie', 'x')`,
	} {
		if _, err := db.Exec(bad); err == nil {
			t.Errorf("CHECK accepted: %s", bad)
		}
	}

	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'proposal_probes'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(ddl), "REFERENCES") {
		t.Errorf("proposal_probes must carry no FK: %s", ddl)
	}
}
