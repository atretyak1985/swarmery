package store

import "testing"

// TestMigrate0113AddsTurnForecasts: the turn-forecast table lands keyed one row
// per (session, turn), with the started_at index, and a half row (forecast
// only) keeps surprise/vector NULL — "not scored" is never "scored zero".
func TestMigrate0113AddsTurnForecasts(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 113`).Scan(&name); err != nil {
		t.Fatalf("migration 113 not recorded: %v", err)
	}
	if name != "0113_turn_forecasts.sql" {
		t.Errorf("migration 113 name: want 0113_turn_forecasts.sql, got %s", name)
	}
	mustHaveColumns(t, db, "turn_forecasts",
		"id", "session_id", "turn_seq", "started_at", "forecast", "actual",
		"surprise", "vector", "model", "forecast_tokens")

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_turn_forecasts_started'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("idx_turn_forecasts_started missing")
	}

	ins := `INSERT INTO turn_forecasts (session_id, turn_seq, started_at, forecast)
		VALUES (1, 1, '2026-10-14T00:00:00.000Z', '{}')`
	if _, err := db.Exec(ins); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := db.Exec(ins); err == nil {
		t.Fatal("second insert for the same (session, turn) succeeded; want a UNIQUE conflict")
	}
	var surprise, vector any
	if err := db.QueryRow(`SELECT surprise, vector FROM turn_forecasts WHERE session_id = 1`).Scan(&surprise, &vector); err != nil {
		t.Fatal(err)
	}
	if surprise != nil || vector != nil {
		t.Errorf("surprise/vector = %v/%v, want NULL on a half row", surprise, vector)
	}
}
