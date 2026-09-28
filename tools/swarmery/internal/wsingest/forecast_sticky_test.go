package wsingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// The report-filled verdict on a PRIOR is sticky: it judges the doc as it was
// when the prior FIRST appeared, not as it is at the latest rescan. Otherwise a
// legitimate prior — stored non-post-hoc while the report was an empty stub —
// flips to post_hoc=1 the moment the phase run fills its Completion Report, and
// every executed phase drops out of the learning loop's sample.

const stickyReadme = "# Epic\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n" +
	"| 1 | Sticky | `phase-1.md` | — |\n"

func stickyPriorDoc(writtenAt, report string) string {
	return "# Phase 1 — Sticky\n\n## Acceptance criteria\n- [ ] a\n\n" +
		"## Forecast\n\n```yaml\n" +
		"kind: prior\nwritten_at: " + writtenAt + "\n" +
		"areas: [internal/ingest]\nsize_band: M\nduration_band: 30-90m\n" +
		"outcome: done\nconfidence: 0.7\n```\n" +
		"\n## Completion Report\n" + report
}

// scanStickyDoc writes (or rewrites) phase-1.md, re-parses the plan and applies
// it — one full rescan, exactly as the daemon does when the doc's bytes change —
// and returns the stored prior's (post_hoc, post_hoc_reason).
func scanStickyDoc(t *testing.T, db *sql.DB, planDir, doc string) (int, string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(planDir, "phase-1.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	warn, _ := collectWarn(t)
	applyPhases(t, db, parsePlan(planDir, warn))

	var postHoc int
	var reason string
	if err := db.QueryRow(`
		SELECT f.post_hoc, f.post_hoc_reason FROM phase_forecasts f
		  JOIN epic_phases e ON e.id = f.phase_id
		 WHERE e.doc_path LIKE '%phase-1.md' AND f.kind = 'prior'`).Scan(&postHoc, &reason); err != nil {
		t.Fatal(err)
	}
	return postHoc, reason
}

func TestPriorPostHocIsSticky(t *testing.T) {
	const at = "2026-09-23T10:12:00Z"
	const filled = "\nShipped the thing.\n"

	t.Run("prior stored before the report stays non-post-hoc after it fills", func(t *testing.T) {
		db := carryFixture(t)
		dir := writePlan(t, map[string]string{"README.md": stickyReadme})

		if ph, r := scanStickyDoc(t, db, dir, stickyPriorDoc(at, "")); ph != 0 || r != "" {
			t.Fatalf("before the report: (post_hoc %d, reason %q), want (0, \"\")", ph, r)
		}
		if ph, r := scanStickyDoc(t, db, dir, stickyPriorDoc(at, filled)); ph != 0 || r != "" {
			t.Errorf("after the report filled: (post_hoc %d, reason %q), want (0, \"\") — "+
				"the prior was a prediction when it was first seen", ph, r)
		}
		// And it stays that way across further rescans of the reported doc.
		if ph, r := scanStickyDoc(t, db, dir, stickyPriorDoc(at, filled+"More.\n")); ph != 0 || r != "" {
			t.Errorf("second rescan: (post_hoc %d, reason %q), want (0, \"\")", ph, r)
		}
	})

	t.Run("prior first seen with the report already filled is post hoc", func(t *testing.T) {
		db := carryFixture(t)
		dir := writePlan(t, map[string]string{"README.md": stickyReadme})

		if ph, r := scanStickyDoc(t, db, dir, stickyPriorDoc(at, filled)); ph != 1 || r != PostHocReportFilled {
			t.Errorf("backfilled prior: (post_hoc %d, reason %q), want (1, %q)", ph, r, PostHocReportFilled)
		}
	})

	t.Run("a prior re-dated after the report filled is a new prior", func(t *testing.T) {
		db := carryFixture(t)
		dir := writePlan(t, map[string]string{"README.md": stickyReadme})

		scanStickyDoc(t, db, dir, stickyPriorDoc(at, ""))
		if ph, r := scanStickyDoc(t, db, dir, stickyPriorDoc("2026-09-24T09:00:00Z", filled)); ph != 1 || r != PostHocReportFilled {
			t.Errorf("re-dated prior: (post_hoc %d, reason %q), want (1, %q)", ph, r, PostHocReportFilled)
		}
	})
}
