// External test package for the same reason as migrate_0103_test.go: it re-runs
// wsingest's exact upsert, and wsingest imports store.
package store_test

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// TestMigrate0105CriteriaClassesAreDocOwned: the two class counters land with a
// 0 default, the upsert writes them, and a re-upsert OVERWRITES them — they are
// re-derived from the doc on every scan, unlike the daemon-owned landing_* set.
func TestMigrate0105CriteriaClassesAreDocOwned(t *testing.T) {
	db := openRaw0103(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 105`).Scan(&name); err != nil {
		t.Fatalf("migration 105 not recorded: %v", err)
	}
	if name != "0105_criteria_classes.sql" {
		t.Errorf("migration 105 name = %s", name)
	}

	const doc = "/plan/phase-3-x.md"
	upsert := func(land, manual int) {
		t.Helper()
		if _, err := db.Exec(wsingest.PhaseUpsertSQL,
			1, 3, "Phase three", doc, "[]",
			5, 2, "in_progress", nil,
			nil, nil, "[]", "off", nil, land, manual, "off"); err != nil {
			t.Fatalf("PhaseUpsertSQL: %v", err)
		}
	}
	read := func() (land, manual int) {
		t.Helper()
		if err := db.QueryRow(`SELECT criteria_land_open, criteria_manual_open FROM epic_phases
			WHERE doc_path = ?`, doc).Scan(&land, &manual); err != nil {
			t.Fatal(err)
		}
		return land, manual
	}

	upsert(2, 1)
	if l, m := read(); l != 2 || m != 1 {
		t.Fatalf("after insert = (%d, %d), want (2, 1)", l, m)
	}
	// The [LAND] criteria got ticked on merge: the rescan must bring the count down.
	upsert(0, 1)
	if l, m := read(); l != 0 || m != 1 {
		t.Errorf("after re-upsert = (%d, %d), want (0, 1)", l, m)
	}
}
