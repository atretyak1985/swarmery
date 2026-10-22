package store_test

import (
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// TestMigrate0122FreshDB: the tables land, their CHECKs and unique keys hold, and
// the migration's own applied_at is readable as D21's cutoff.
func TestMigrate0122FreshDB(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	db, err := store.Open(t.TempDir() + "/m122.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at, ok, err := store.MigrationAppliedAt(db, store.ReviewFindingsMigration)
	if err != nil || !ok {
		t.Fatalf("MigrationAppliedAt = %v, %t, %v; want the 0122 row", at, ok, err)
	}
	if at.Before(before) || at.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("applied_at = %v, want ≈ now", at)
	}
	if _, ok, err := store.MigrationAppliedAt(db, "9999_nope.sql"); ok || err != nil {
		t.Errorf("an unrecorded migration: ok=%t err=%v, want false, nil", ok, err)
	}

	ins := func(typ, source string) error {
		_, err := db.Exec(`INSERT INTO review_findings
			(workspace_task_id, doc_path, round, finding_id, type, source, created_at)
			VALUES (1, '/p/phase-1.md', '1', 'R1-1', ?, ?, '2026-10-22T00:00:00Z')`, typ, source)
		return err
	}
	if err := ins("correctness", "daemon"); err != nil {
		t.Fatalf("insert a finding: %v", err)
	}
	if err := ins("correctness", "daemon"); err == nil {
		t.Error("a second (doc_path, round, finding_id) was accepted; want the unique key to refuse it")
	}
	if _, err := db.Exec(`DELETE FROM review_findings`); err != nil {
		t.Fatal(err)
	}
	if err := ins("style", "daemon"); err == nil {
		t.Error("type 'style' was accepted; the CHECK allows only the five lenses")
	}
	if err := ins("quality", "model"); err == nil {
		t.Error("source 'model' was accepted; the CHECK allows daemon|interactive|backfill")
	}
	if _, err := db.Exec(`INSERT INTO review_blocks (workspace_task_id, doc_path, round, verdict, written_at)
		VALUES (1, '/p/phase-1.md', 'plan', 'PASS', '2026-10-22T00:00:00Z')`); err != nil {
		t.Fatalf("insert a block: %v", err)
	}
	var legacy int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('epic_phases') WHERE name = 'legacy_review'`).Scan(&legacy); err != nil || legacy != 1 {
		t.Errorf("epic_phases.legacy_review present = %d, %v", legacy, err)
	}
}

// TestMigrate0122OnAnExistingDB: a database that already holds phases (built up to
// the previous migration, as a copied live DB is) migrates; its rows survive with
// legacy_review = 0 until wsingest computes it.
func TestMigrate0122OnAnExistingDB(t *testing.T) {
	db := openRaw0103(t)
	if err := store.MigrateUpTo(db, 119); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(wsingest.PhaseUpsertSQL,
		7, 1, "Phase one", "/plan/phase-1.md", "[]", 2, 2, "done", nil,
		"shipped", nil, "[]", "off", nil, 0, 0, "off"); err != nil {
		t.Fatalf("seed a phase: %v", err)
	}
	if _, ok, _ := store.MigrationAppliedAt(db, store.ReviewFindingsMigration); ok {
		t.Fatal("0122 recorded before it ran")
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var legacy int
	var status string
	if err := db.QueryRow(`SELECT legacy_review, doc_status FROM epic_phases WHERE doc_path = '/plan/phase-1.md'`).
		Scan(&legacy, &status); err != nil {
		t.Fatal(err)
	}
	if legacy != 0 || status != "done" {
		t.Errorf("after 0122: legacy_review=%d status=%q, want 0 and done", legacy, status)
	}
	if _, ok, err := store.MigrationAppliedAt(db, store.ReviewFindingsMigration); !ok || err != nil {
		t.Errorf("applied_at unreadable after migrating an existing db: %t, %v", ok, err)
	}
}
