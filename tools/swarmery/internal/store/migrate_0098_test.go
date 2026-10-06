package store

import (
	"io/fs"
	"testing"
)

// truthSourceMigration is the migration under test, matched by NAME so a
// renumber on merge does not silently point the test at another file.
const truthSourceMigration = "0098_decision_truth_source.sql"

func truthSourceVersion(t *testing.T) int {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == truthSourceMigration {
			v, err := migrationVersion(e.Name())
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("migration %s not embedded", truthSourceMigration)
	return 0
}

// TestMigrateTruthSourceBackfill: on a store that already holds labels, the
// migration marks a d1.run_end label 'observed', any other label 'operator',
// and leaves an unlabelled row empty. ground_truth / ground_truth_at stay
// byte-identical.
func TestMigrateTruthSourceBackfill(t *testing.T) {
	v := truthSourceVersion(t)
	db := openRaw(t)
	if err := MigrateUpTo(db, v-1); err != nil {
		t.Fatalf("migrate to %d: %v", v-1, err)
	}
	if _, err := db.Exec(`INSERT INTO decisions
		(id, question_id, input_hash, answer, ground_truth, ground_truth_at, created_at) VALUES
		(1, 'd1.run_end', 'h1', 'done', 'done', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z'),
		(2, 'd2.outcome', 'h2', 'success', 'failure', '2026-10-02T00:00:00Z', '2026-10-02T00:00:00Z'),
		(3, 'd2.outcome', 'h3', 'success', NULL, NULL, '2026-10-03T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	type row struct{ truth, at any }
	read := func() map[int64]row {
		rs, err := db.Query(`SELECT id, ground_truth, ground_truth_at FROM decisions ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		out := map[int64]row{}
		for rs.Next() {
			var id int64
			var r row
			if err := rs.Scan(&id, &r.truth, &r.at); err != nil {
				t.Fatal(err)
			}
			out[id] = r
		}
		return out
	}
	before := read()

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = ?`, v).Scan(&name); err != nil {
		t.Fatalf("migration %d not recorded: %v", v, err)
	}
	if name != truthSourceMigration {
		t.Errorf("migration %d name = %s, want %s", v, name, truthSourceMigration)
	}

	after := read()
	for id, b := range before {
		if a := after[id]; a != b {
			t.Errorf("decision %d: ground_truth/at changed %v -> %v", id, b, a)
		}
	}
	want := map[int64]string{1: "observed", 2: "operator", 3: ""}
	for id, w := range want {
		var got string
		if err := db.QueryRow(`SELECT ground_truth_source FROM decisions WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("decision %d: ground_truth_source = %q, want %q", id, got, w)
		}
	}
}
