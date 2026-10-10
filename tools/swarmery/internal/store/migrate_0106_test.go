package store_test

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// TestMigrate0106ReviewColumnsOwnership: review_mode is doc-owned (the upsert
// rewrites it) and review_fix_round is daemon-owned (a re-run of the EXACT upsert
// leaves it alone), and phase_reviews accepts a phase-scoped row.
func TestMigrate0106ReviewColumnsOwnership(t *testing.T) {
	db := openRaw0103(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 106`).Scan(&name); err != nil || name != "0106_phase_reviews.sql" {
		t.Fatalf("migration 106 = %q, %v; want 0106_phase_reviews.sql", name, err)
	}

	const doc = "/plan/phase-6-review.md"
	upsert := func(reviewMode string) {
		t.Helper()
		if _, err := db.Exec(wsingest.PhaseUpsertSQL,
			1, 6, "Phase six", doc, "[]", 3, 0, "in_progress", nil,
			nil, nil, "[]", "off", nil, reviewMode); err != nil {
			t.Fatalf("PhaseUpsertSQL: %v", err)
		}
	}
	upsert("on")
	if _, err := db.Exec(`UPDATE epic_phases SET review_fix_round = 1 WHERE doc_path = ?`, doc); err != nil {
		t.Fatal(err)
	}
	upsert("off")

	var mode string
	var round int
	if err := db.QueryRow(`SELECT review_mode, review_fix_round FROM epic_phases WHERE doc_path = ?`, doc).
		Scan(&mode, &round); err != nil {
		t.Fatal(err)
	}
	if mode != "off" {
		t.Errorf("review_mode = %q after a rescan said off, want off (doc-owned)", mode)
	}
	if round != 1 {
		t.Errorf("review_fix_round = %d after a rescan, want 1 (daemon-owned, never in DO UPDATE SET)", round)
	}

	if _, err := db.Exec(`INSERT INTO phase_reviews (scope, phase_id, workspace_task_id, verdict, started_at)
		VALUES ('phase', 1, '1', 'pass', '2026-10-10T00:00:00Z')`); err != nil {
		t.Fatalf("insert phase_reviews row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO phase_reviews (scope, workspace_task_id, verdict, started_at)
		VALUES ('branch', '1', 'pass', '2026-10-10T00:00:00Z')`); err == nil {
		t.Error("phase_reviews accepted scope='branch'; the CHECK allows only phase|plan")
	}
}
