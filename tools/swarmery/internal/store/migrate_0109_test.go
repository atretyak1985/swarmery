package store

import "testing"

// TestMigrate0109AddsPermissionChannel: a permission request written before
// 0109 reads channel 'shim' and a NULL tool_use_id afterwards, both indexes
// land, and a new mod row keeps the values it is written with.
func TestMigrate0109AddsPermissionChannel(t *testing.T) {
	db := openRaw(t)
	if err := MigrateUpTo(db, 108); err != nil {
		t.Fatalf("migrate to 108: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (1, '/p', '-p', 'p', '2026-10-14T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, project_id, session_uuid, cwd, status, started_at, source)
		VALUES (1, 1, 'uuid-0109', '/p', 'active', '2026-10-14T00:00:00.000Z', 'jsonl')`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO permission_requests (id, session_id, tool_name, request_json, requested_at)
		VALUES (1, 1, 'Bash', '{}', '2026-10-14T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed request: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 109`).Scan(&name); err != nil {
		t.Fatalf("migration 109 not recorded: %v", err)
	}
	if name != "0109_permission_channel.sql" {
		t.Errorf("migration 109 name: want 0109_permission_channel.sql, got %s", name)
	}
	mustHaveColumns(t, db, "permission_requests", "channel", "tool_use_id")

	var channel string
	var toolUse any
	if err := db.QueryRow(`SELECT channel, tool_use_id FROM permission_requests WHERE id = 1`).Scan(&channel, &toolUse); err != nil {
		t.Fatal(err)
	}
	if channel != "shim" || toolUse != nil {
		t.Errorf("pre-0109 row: channel=%q tool_use_id=%v, want 'shim' and NULL", channel, toolUse)
	}

	for _, idx := range []string{"idx_pr_tool_use", "idx_pr_mod_hash"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s missing", idx)
		}
	}

	if _, err := db.Exec(`INSERT INTO permission_requests
		(id, session_id, tool_name, request_json, requested_at, channel, tool_use_id)
		VALUES (2, 1, 'Bash', '{}', '2026-10-14T00:00:01.000Z', 'mod', 'toolu_1')`); err != nil {
		t.Fatalf("insert mod row: %v", err)
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM permission_requests WHERE session_id = 1 AND tool_use_id = 'toolu_1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 2 {
		t.Errorf("lookup by (session, tool_use_id) = %d, want 2", id)
	}
}
