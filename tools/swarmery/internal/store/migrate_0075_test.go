package store

import "testing"

// TestMigrate0075AddsCacheWriteTTLColumns: the TTL split and the speed mode
// land on turns, and the legacy flat total stays — it is what still prices
// every row ingested before this migration.
func TestMigrate0075AddsCacheWriteTTLColumns(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 75`).Scan(&name); err != nil {
		t.Fatalf("migration 75 not recorded: %v", err)
	}
	if name != "0075_turns_cache_write_ttl.sql" {
		t.Errorf("migration 75 name: want 0075_turns_cache_write_ttl.sql, got %s", name)
	}

	mustHaveColumns(t, db, "turns",
		"cache_write_5m_tokens", "cache_write_1h_tokens", "speed", "tokens_cache_write")
}

// TestMigrate0075PreservesLegacyTurns is the data-safety case: a row written
// before the split existed keeps its flat total and comes out with NULL — not
// 0 — in the new buckets. NULL is what makes cost.EnrichTurn fall back to
// pricing the whole total at the 5m rate; a 0 would zero the turn's cache cost.
func TestMigrate0075PreservesLegacyTurns(t *testing.T) {
	db := openRaw(t)
	migrateUpTo(t, db, 74)

	if _, err := db.Exec(`
		INSERT INTO projects (id, path, slug, first_seen)
		VALUES (1, '/p', '-p', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at)
		VALUES (1, 1, 'u1', 'claude-opus-5-5', 'completed', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO turns (id, session_id, seq, role, model, started_at, tokens_cache_write, cost_usd)
		VALUES (1, 1, 0, 'assistant', 'claude-opus-5-5', '2026-09-23T00:00:01Z', 6935, 0.0346750)`); err != nil {
		t.Fatalf("insert legacy turn: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	var total int64
	var cost float64
	var w5m, w1h *int64
	var speed *string
	if err := db.QueryRow(`
		SELECT tokens_cache_write, cost_usd, cache_write_5m_tokens, cache_write_1h_tokens, speed
		  FROM turns WHERE id = 1`).Scan(&total, &cost, &w5m, &w1h, &speed); err != nil {
		t.Fatalf("read migrated turn: %v", err)
	}
	if total != 6935 {
		t.Errorf("tokens_cache_write = %d, want 6935 (the legacy total must survive)", total)
	}
	if cost != 0.0346750 {
		t.Errorf("cost_usd = %v, want 0.034675 (the migration must not re-price)", cost)
	}
	if w5m != nil || w1h != nil || speed != nil {
		t.Errorf("new columns = %v/%v/%v, want all NULL on a pre-0075 row", w5m, w1h, speed)
	}
}
