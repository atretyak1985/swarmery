package store

import "testing"

const insEvidence132 = `INSERT INTO decision_evidence
	(workspace_task_id, ref_id, doc_path, kind, status, created_at)
	VALUES (1, ?, '/p/phase-1.md', ?, ?, '2026-10-23T00:00:00Z')`

// TestMigrate0132DecisionEvidence: decision_evidence lands with its kind and
// status CHECKs and one row per (task, doc, kind, id).
func TestMigrate0132DecisionEvidence(t *testing.T) {
	db, err := Open(t.TempDir() + "/m132.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 132`).Scan(&name); err != nil {
		t.Fatalf("migration 132 not recorded: %v", err)
	}
	if name != "0132_decision_evidence.sql" {
		t.Errorf("migration 132 name = %s", name)
	}
	for _, kind := range []string{"report", "pr", "review"} {
		if _, err := db.Exec(insEvidence132, "D-"+kind, kind, "evidence"); err != nil {
			t.Errorf("kind %q refused: %v", kind, err)
		}
	}
	if _, err := db.Exec(insEvidence132, "D-9", "comment", "evidence"); err == nil {
		t.Error("kind 'comment' accepted")
	}
	for _, st := range []string{"not-implemented", "missing"} {
		if _, err := db.Exec(insEvidence132, "SC-"+st, "report", st); err != nil {
			t.Errorf("status %q refused: %v", st, err)
		}
	}
	if _, err := db.Exec(insEvidence132, "SC-9", "report", "remaining"); err == nil {
		t.Error("status 'remaining' accepted — remaining is derived, never stored")
	}
	if _, err := db.Exec(insEvidence132, "D-report", "report", "evidence"); err == nil {
		t.Error("a second row for the same (task, doc, kind, id) accepted")
	}
	var excerpt, reason string
	if err := db.QueryRow(`SELECT excerpt, reason FROM decision_evidence WHERE ref_id = 'D-report'`).Scan(&excerpt, &reason); err != nil {
		t.Fatal(err)
	}
	if excerpt != "" || reason != "" {
		t.Errorf("defaults: excerpt=%q reason=%q", excerpt, reason)
	}
}
