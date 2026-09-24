package store

import "testing"

// TestMigrate0082AddsPhaseSurprise: the surprise table lands keyed one row per
// run, with the phase and retro-window lookup indexes, and the revision columns
// NULL by default (no prior/posterior pair ⇒ no delta, never a zero delta).
func TestMigrate0082AddsPhaseSurprise(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 82`).Scan(&name); err != nil {
		t.Fatalf("migration 82 not recorded: %v", err)
	}
	if name != "0082_phase_surprise.sql" {
		t.Errorf("migration 82 name: want 0082_phase_surprise.sql, got %s", name)
	}
	mustHaveColumns(t, db, "phase_surprise",
		"phase_id", "session_uuid", "forecast_kind", "forecast_post_hoc", "forecast_doc_hash",
		"surprise_index", "top_component", "components_json", "weights_json", "detail_json",
		"revision_index", "revision_json", "summary", "actuals_source",
		"notified_at", "auto_verify_at", "computed_at")

	for _, idx := range []string{"idx_phase_surprise_phase", "idx_phase_surprise_computed"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s missing", idx)
		}
	}

	ins := `INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, computed_at)
		VALUES (1, 'u-1', 0.4, '2026-09-23T00:00:00Z')`
	if _, err := db.Exec(ins); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("second insert for the same session_uuid succeeded; want a UNIQUE conflict")
	}
	var rev, notified any
	if err := db.QueryRow(`SELECT revision_index, notified_at FROM phase_surprise WHERE session_uuid = 'u-1'`).
		Scan(&rev, &notified); err != nil {
		t.Fatal(err)
	}
	if rev != nil || notified != nil {
		t.Errorf("revision_index/notified_at = %v/%v, want NULL", rev, notified)
	}
}
