// External test package on purpose: the point of this test is to re-run
// wsingest's EXACT upsert (wsingest.PhaseUpsertSQL), and wsingest transitively
// imports store — an in-package (package store) test importing it would be an
// import cycle. Only exported store API is used here.
package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// phaseLandingMigration is matched by NAME so a renumber on merge does not
// silently point the test at another file.
const phaseLandingMigration = "0103_phase_landing.sql"

const phaseLandingVersion = 103

var phaseLandingColumns = []string{
	"landing_state", "pr_url", "pr_provider", "pr_number",
	"pr_status", "pr_checked_at", "landed_at", "landing_error",
}

// openRaw0103 mirrors store's own openRaw test helper (same DSN pragmas as
// store.Open) without running migrations, so the test controls the schema version.
func openRaw0103(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func assertMigration0103Recorded(t *testing.T, db *sql.DB) {
	t.Helper()
	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = ?`,
		phaseLandingVersion).Scan(&name); err != nil {
		t.Fatalf("migration %d not recorded: %v", phaseLandingVersion, err)
	}
	if name != phaseLandingMigration {
		t.Errorf("migration %d name = %s, want %s", phaseLandingVersion, name, phaseLandingMigration)
	}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('epic_phases')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		have[c] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, c := range phaseLandingColumns {
		if !have[c] {
			t.Errorf("epic_phases: missing column %s", c)
		}
	}
}

// upsertPhase runs wsingest's real upsert with doc-owned values for one phase doc.
func upsertPhase(t *testing.T, db *sql.DB, taskID int64, seq int, name, docPath string, done int) {
	t.Helper()
	if _, err := db.Exec(wsingest.PhaseUpsertSQL,
		taskID, seq, name, docPath, "[]",
		3, done, "in_progress", nil,
		nil, nil, "[]", "off", nil, "off"); err != nil {
		t.Fatalf("PhaseUpsertSQL: %v", err)
	}
}

func readLanding(t *testing.T, db *sql.DB, docPath string) (state string, prURL *string) {
	t.Helper()
	if err := db.QueryRow(`SELECT landing_state, pr_url FROM epic_phases WHERE doc_path = ?`,
		docPath).Scan(&state, &prURL); err != nil {
		t.Fatalf("read landing: %v", err)
	}
	return state, prURL
}

// TestMigrate0103FreshDBLandingSurvivesUpsert: on a fresh DB the landing columns
// exist with landing_state defaulting to 'none', and a daemon-written landing
// (pr_open + pr_url) survives a re-run of wsingest's exact upsert, which must
// still refresh the doc-owned columns it does list.
func TestMigrate0103FreshDBLandingSurvivesUpsert(t *testing.T) {
	db := openRaw0103(t)
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}
	assertMigration0103Recorded(t, db)

	const doc = "/plan/phase-1-x.md"
	upsertPhase(t, db, 1, 1, "Phase one", doc, 0)
	if st, url := readLanding(t, db, doc); st != "none" || url != nil {
		t.Fatalf("fresh row landing = (%q, %v), want (none, NULL)", st, url)
	}

	const prURL = "https://example.invalid/acme/repo/pull/42"
	if _, err := db.Exec(`UPDATE epic_phases SET landing_state = 'pr_open', pr_url = ?
		WHERE doc_path = ?`, prURL, doc); err != nil {
		t.Fatalf("stamp landing: %v", err)
	}

	// The executor ticks a box: the plan hash changes and wsingest re-upserts.
	upsertPhase(t, db, 1, 1, "Phase one (renamed title)", doc, 2)

	st, url := readLanding(t, db, doc)
	if st != "pr_open" {
		t.Errorf("landing_state after re-upsert = %q, want pr_open", st)
	}
	if url == nil || *url != prURL {
		t.Errorf("pr_url after re-upsert = %v, want %q", url, prURL)
	}
	var name string
	var done int
	if err := db.QueryRow(`SELECT name, checkboxes_done FROM epic_phases WHERE doc_path = ?`,
		doc).Scan(&name, &done); err != nil {
		t.Fatal(err)
	}
	if name != "Phase one (renamed title)" || done != 2 {
		t.Errorf("doc-owned columns not refreshed: name=%q done=%d", name, done)
	}
}

// TestMigrate0103ExistingDB: a store already holding phases at 0102 migrates
// cleanly; existing rows read landing_state='none' with NULL pr_* columns, and a
// landing stamped afterwards survives the upsert too.
func TestMigrate0103ExistingDB(t *testing.T) {
	db := openRaw0103(t)
	if err := store.MigrateUpTo(db, phaseLandingVersion-1); err != nil {
		t.Fatalf("migrate to %d: %v", phaseLandingVersion-1, err)
	}
	if _, err := db.Exec(`INSERT INTO epic_phases
		(id, workspace_task_id, seq, name, doc_path, run_state, run_branch)
		VALUES (7, 1, 1, 'Ran', '/plan/phase-1.md', 'done', 'swarm/phase-7'),
		       (8, 1, 2, 'Never ran', '/plan/phase-2.md', 'idle', NULL)`); err != nil {
		t.Fatalf("seed phases: %v", err)
	}

	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate existing db: %v", err)
	}
	assertMigration0103Recorded(t, db)

	for _, doc := range []string{"/plan/phase-1.md", "/plan/phase-2.md"} {
		if st, url := readLanding(t, db, doc); st != "none" || url != nil {
			t.Errorf("%s landing = (%q, %v), want (none, NULL)", doc, st, url)
		}
	}

	if _, err := db.Exec(`UPDATE epic_phases SET landing_state = 'pr_open',
		pr_url = 'https://example.invalid/pull/7', pr_number = 7, pr_provider = 'github'
		WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	upsertPhase(t, db, 1, 1, "Ran", "/plan/phase-1.md", 3)

	var st, prov, branch string
	var num int64
	if err := db.QueryRow(`SELECT landing_state, pr_provider, pr_number, run_branch
		FROM epic_phases WHERE id = 7`).Scan(&st, &prov, &num, &branch); err != nil {
		t.Fatal(err)
	}
	if st != "pr_open" || prov != "github" || num != 7 || branch != "swarm/phase-7" {
		t.Errorf("after re-upsert: state=%q provider=%q number=%d branch=%q", st, prov, num, branch)
	}
}
