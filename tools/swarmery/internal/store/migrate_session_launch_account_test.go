package store

import "testing"

// launchAccountVersion finds the session_launch_account migration by NAME, so a
// renumber never breaks this test.
func launchAccountVersion(t *testing.T) int {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var v int
	var name string
	if err := db.QueryRow(`SELECT version, name FROM schema_migrations WHERE name LIKE '%_session_launch_account.sql'`).
		Scan(&v, &name); err != nil {
		t.Fatalf("the session_launch_account migration is not recorded: %v", err)
	}
	return v
}

func TestMigrateSessionLaunchAccountFreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name LIKE '%_session_launch_account.sql'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("recorded %d time(s) (%v), want 1", n, err)
	}
	mustHaveColumns(t, db, "sessions", "launch_account")
}

// Column absent before, present after, and a pre-existing row reads ''.
func TestMigrateSessionLaunchAccountOnPopulatedDB(t *testing.T) {
	v := launchAccountVersion(t)
	db := openRaw(t)
	migrateUpTo(t, db, v-1)
	if columnSet(t, db, "sessions")["launch_account"] {
		t.Fatal("launch_account exists before its migration")
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-09-29T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source, account)
		VALUES (1, 'u-pre', 'completed', '2026-09-29T00:00:00Z', 'jsonl', 'work')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT launch_account FROM sessions WHERE session_uuid = 'u-pre'`).Scan(&got); err != nil || got != "" {
		t.Errorf("pre-existing row launch_account = %q (%v), want ''", got, err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}

// AccountDrift returns exactly the rows where both columns are known and
// differ; SetSessionLaunchAccount never writes ''.
func TestAccountDrift(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-09-29T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]string{{"same", "work"}, {"drift", "work"}, {"unknown-launch", "work"}, {"unknown-land", ""}} {
		if _, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source, account)
			VALUES (1, ?, 'active', '2026-09-29T00:00:00Z', 'jsonl', ?)`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	for uuid, key := range map[string]string{"same": "work", "drift": "default", "unknown-land": "default"} {
		if ok, err := SetSessionLaunchAccount(db, uuid, key); err != nil || !ok {
			t.Fatalf("SetSessionLaunchAccount(%s) = %v %v", uuid, ok, err)
		}
	}
	if ok, _ := SetSessionLaunchAccount(db, "drift", ""); ok {
		t.Error("an empty key was written")
	}
	if ok, _ := SetSessionLaunchAccount(db, "no-such", "x"); ok {
		t.Error("a missing row reported updated")
	}
	rows, err := AccountDrift(db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionUUID != "drift" || rows[0].Account != "work" || rows[0].LaunchAccount != "default" {
		t.Errorf("AccountDrift = %+v, want only the drift row", rows)
	}
}
