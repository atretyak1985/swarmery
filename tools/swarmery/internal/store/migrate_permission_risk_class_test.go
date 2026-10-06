package store

import "testing"

// permissionRiskClassVersion finds the permission_risk_class migration by
// NAME, so a renumber never breaks this test.
func permissionRiskClassVersion(t *testing.T) int {
	t.Helper()
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE name LIKE '%_permission_risk_class.sql'`).
		Scan(&v); err != nil {
		t.Fatalf("the permission_risk_class migration is not recorded: %v", err)
	}
	return v
}

func TestMigratePermissionRiskClassFreshDB(t *testing.T) {
	db := openRaw(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	mustHaveColumns(t, db, "permission_requests", "risk_class")
}

// Column absent before, present after; a pre-existing row reads the empty (ordinary) class
// and a re-run is a no-op.
func TestMigratePermissionRiskClassOnPopulatedDB(t *testing.T) {
	v := permissionRiskClassVersion(t)
	db := openRaw(t)
	migrateUpTo(t, db, v-1)
	if columnSet(t, db, "permission_requests")["risk_class"] {
		t.Fatal("risk_class exists before its migration")
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-10-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (1, 1, 'u-pre', 'active', '2026-10-06T00:00:00Z', 'hook')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO permission_requests (session_id, tool_name, request_json, status, requested_at)
		VALUES (1, 'Bash', '{}', 'approved', '2026-10-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT risk_class FROM permission_requests WHERE session_id = 1`).Scan(&got); err != nil || got != "" {
		t.Errorf("pre-existing row risk_class = %q (%v), want ''", got, err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}
