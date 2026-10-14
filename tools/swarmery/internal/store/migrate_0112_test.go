package store_test

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// TestMigrate0112Mods: the mods table is created under version 112 with its
// name recorded, accepts a cache row, and refuses a second row for one dir.
func TestMigrate0112Mods(t *testing.T) {
	db := openRaw0103(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 112`).Scan(&name); err != nil || name != "0112_mods.sql" {
		t.Fatalf("migration 112 = %q, %v; want 0112_mods.sql", name, err)
	}

	const ins = `INSERT INTO mods (name, plugin_id, dir, source, version, module_sha, events, uses, env, state, risk, validated_at, errors, report)
		VALUES (?, ?, ?, ?, ?, ?, '[]', '[]', '{}', '[]', '[]', '2026-10-13T00:00:00Z', '[]', '{}')`
	if _, err := db.Exec(ins, "link", "link@mkt", "/cache/mkt/link/1.0.0", "cache", "1.0.0", "abc"); err != nil {
		t.Fatalf("insert mods row: %v", err)
	}
	if _, err := db.Exec(ins, "link-again", nil, "/cache/mkt/link/1.0.0", "plugin-dir", nil, nil); err == nil {
		t.Error("mods accepted a second row for the same dir; UNIQUE(dir) must refuse it")
	}
	if _, err := db.Exec(`INSERT INTO mods (name, source) VALUES ('x', 'cache')`); err == nil {
		t.Error("mods accepted a row without dir; dir is NOT NULL")
	}

	// Re-running Migrate on a migrated db is a no-op.
	if err := store.Migrate(db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}
