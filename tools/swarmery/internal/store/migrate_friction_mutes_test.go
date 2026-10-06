package store

import "testing"

// frictionMutesVersion finds the friction_mutes migration by NAME, so a
// renumber never breaks these tests.
func frictionMutesVersion(t *testing.T) int {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE name LIKE '%_friction_mutes.sql'`).
		Scan(&v); err != nil {
		t.Fatalf("the friction_mutes migration is not recorded: %v", err)
	}
	return v
}

func TestMigrateFrictionMutesFreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	mustHaveColumns(t, db, "friction_mutes", "key", "reason", "verdict_id", "muted_at", "muted_until")
}

// On a populated DB: absent before, present after; key is the primary key,
// reason is NOT NULL, verdict_id may be NULL; a re-run is a no-op.
func TestMigrateFrictionMutesOnPopulatedDB(t *testing.T) {
	v := frictionMutesVersion(t)
	db := openRaw(t)
	migrateUpTo(t, db, v-1)
	if len(columnSet(t, db, "friction_mutes")) != 0 {
		t.Fatal("friction_mutes exists before its migration")
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ins := func(key string, reason any) error {
		_, err := db.Exec(`INSERT INTO friction_mutes (key, reason, verdict_id, muted_at, muted_until)
			VALUES (?, ?, NULL, '2026-10-06T00:00:00.000Z', '2026-11-05T00:00:00.000Z')`, key, reason)
		return err
	}
	if err := ins("k1", "noise"); err != nil {
		t.Fatalf("insert operator mute (NULL verdict_id): %v", err)
	}
	if err := ins("k1", "again"); err == nil {
		t.Error("a second row for the same key was accepted — PRIMARY KEY missing")
	}
	if err := ins("k2", nil); err == nil {
		t.Error("a NULL reason was accepted — NOT NULL missing")
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}
