package wsingest

import "testing"

// projects.slug is unique (migration projects_slug_unique), and the scanner is
// the second writer that mints project rows: a workspace with no telemetry
// project gets one under the workspace dir's name. When another project
// already holds that name, a bare INSERT fails on the slug index, upsertTask
// fails with it, and Scan only warns — so every card of the workspace silently
// vanished from the board on every scan. The row must mint under a free slug
// instead.
func TestScanMintsAProjectWhoseSlugIsTaken(t *testing.T) {
	// The fixture warns on its own (a card without README, one without a
	// date); the clash must add nothing to that baseline.
	baselineDB := testDB(t)
	seed(t, baselineDB)
	pinMtime(t)
	baseline := scan(t, baselineDB).Warnings

	db := testDB(t)
	seed(t, db)
	// Another project, at another path, already answers to the slug the
	// projgamma workspace would mint its row under.
	mustExec(t, db, `INSERT INTO projects (id, path, slug, name, first_seen)
		VALUES (9, '/somewhere/else', 'projgamma', 'Else', '2026-06-01T00:00:00Z')`)

	stats := scan(t, db)

	if stats.Warnings != baseline {
		t.Errorf("warnings = %d, want the fixture's %d — the slug clash must not fail the workspace", stats.Warnings, baseline)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM tasks WHERE external_id LIKE '%gamma-task'`); got != 1 {
		t.Errorf("gamma-task indexed %d times, want 1 — the workspace's cards were dropped", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE path='/work/gamma-app' AND slug='projgamma-2'`); got != 1 {
		t.Errorf("projgamma's row was not minted under the free suffixed slug projgamma-2")
	}
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE id=9 AND slug='projgamma'`); got != 1 {
		t.Errorf("the project that already held the slug was renamed")
	}
}
