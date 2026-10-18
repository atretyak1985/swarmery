package store_test

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// TestMigrate0114RouteAppliedModelOnly: a phase-run row that was applied only
// through the router's effort (won_rung != route) is rewritten to applied = 0;
// a phase-run row whose model came from the router and every dispatch row keep
// their value.
func TestMigrate0114RouteAppliedModelOnly(t *testing.T) {
	db := openRaw0103(t)
	if err := store.MigrateUpTo(db, 113); err != nil {
		t.Fatalf("migrate to 113: %v", err)
	}
	const ins = `INSERT INTO route_decisions
		(id, surface, subject, mode, signals_json, score, tier, pick_model, pick_effort, applied, won_rung, created_at)
		VALUES (?, ?, ?, 'active', '{}', 30, 'M', 'sonnet', 'medium', 1, ?, '2026-10-14T07:15:00Z')`
	rows := []struct {
		id            int
		surface, rung string
		wantApplied   int
	}{
		{1, "phaserun", "request", 0}, // effort-only: no longer applied
		{2, "phaserun", "doc", 0},
		{3, "phaserun", "route", 1}, // the router's model ran
		{4, "dispatch", "card", 1},  // dispatch keeps its playbook/effort meaning
	}
	for _, r := range rows {
		if _, err := db.Exec(ins, r.id, r.surface, "phase:1", r.rung); err != nil {
			t.Fatalf("insert row %d: %v", r.id, err)
		}
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 114`).Scan(&name); err != nil || name != "0114_route_applied_model_only.sql" {
		t.Fatalf("migration 114 = %q, %v", name, err)
	}
	for _, r := range rows {
		var got int
		if err := db.QueryRow(`SELECT applied FROM route_decisions WHERE id = ?`, r.id).Scan(&got); err != nil {
			t.Fatalf("read row %d: %v", r.id, err)
		}
		if got != r.wantApplied {
			t.Errorf("row %d (%s, won_rung=%s) applied = %d, want %d", r.id, r.surface, r.rung, got, r.wantApplied)
		}
	}
}
