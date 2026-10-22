package store_test

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// TestMigrate0126BlastRadiusDocOwned: blast_radius lands empty by default and is
// doc-owned — the EXACT scan upsert rewrites it, including back to empty when the
// doc's `Blast radius:` line is deleted.
func TestMigrate0126BlastRadiusDocOwned(t *testing.T) {
	db := openRaw0103(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 126`).Scan(&name); err != nil || name != "0126_phase_blast_radius.sql" {
		t.Fatalf("migration 126 = %q, %v; want 0126_phase_blast_radius.sql", name, err)
	}

	const doc = "/plan/phase-2-totals.md"
	upsert := func(line string) {
		t.Helper()
		if _, err := db.Exec(wsingest.PhaseUpsertSQL,
			1, 2, "Phase two", doc, "[]", 3, 0, "in_progress", nil,
			nil, nil, "[]", "off", nil, 0, 0, "off", line); err != nil {
			t.Fatalf("PhaseUpsertSQL: %v", err)
		}
	}
	read := func() string {
		t.Helper()
		var v string
		if err := db.QueryRow(`SELECT blast_radius FROM epic_phases WHERE doc_path = ?`, doc).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if _, err := db.Exec(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path) VALUES (1, 9, 'Raw', '/plan/raw.md')`); err != nil {
		t.Fatal(err)
	}
	var def string
	if err := db.QueryRow(`SELECT blast_radius FROM epic_phases WHERE doc_path = '/plan/raw.md'`).Scan(&def); err != nil || def != "" {
		t.Fatalf("default blast_radius = %q, %v; want ''", def, err)
	}

	const line = "Blast radius: 2 callers in internal/orders — internal/orders/a.go:1, internal/orders/b.go:2 (sources: rg)"
	upsert(line)
	if got := read(); got != line {
		t.Errorf("blast_radius = %q, want the doc's line", got)
	}
	upsert("")
	if got := read(); got != "" {
		t.Errorf("blast_radius = %q after a rescan found no line, want '' (doc-owned)", got)
	}
}
