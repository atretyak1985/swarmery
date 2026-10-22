package wsingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// The projection of `kind: review` blocks (review_blocks.go, migration 0122),
// end to end: parsePlan over a real plan dir, applyEpics into a real schema.

const reviewFixtures = "../../testdata/fixtures/review-blocks"

func reviewFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(reviewFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const reviewReadme = "# Epic\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n| 1 | One | `phase-1.md` | — |\n"

// reviewPlan writes a one-phase plan whose phase doc is body.
func reviewPlan(t *testing.T, body string) (planDir, docPath string) {
	t.Helper()
	planDir = writePlan(t, map[string]string{"README.md": reviewReadme, "phase-1.md": body})
	return planDir, filepath.Join(planDir, "phase-1.md")
}

// scanReviewPlan parses planDir and applies it the way scanEpics does.
func scanReviewPlan(t *testing.T, db *sql.DB, planDir string) {
	t.Helper()
	warn, _ := collectWarn(t)
	applyPhases(t, db, parsePlan(planDir, warn))
}

type findingRow struct {
	id                                                      int64
	typ, file, criterion, evidence, note, resolvedBy, source string
	line, testCaught                                        int
}

// readFindings keys a doc's review_findings rows by "<round>/<finding_id>".
func readFindings(t *testing.T, db *sql.DB, docPath string) map[string]findingRow {
	t.Helper()
	rows, err := db.Query(`SELECT id, round, finding_id, COALESCE(type, ''), file, COALESCE(line, 0), criterion,
		test_caught, evidence, note, COALESCE(resolved_by, ''), source
		FROM review_findings WHERE doc_path = ?`, docPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]findingRow{}
	for rows.Next() {
		var (
			f         findingRow
			round, id string
		)
		if err := rows.Scan(&f.id, &round, &id, &f.typ, &f.file, &f.line, &f.criterion, &f.testCaught,
			&f.evidence, &f.note, &f.resolvedBy, &f.source); err != nil {
			t.Fatal(err)
		}
		out[round+"/"+id] = f
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// blockRows lists a doc's review_blocks as "<round>:<verdict>:<complete>", by round.
func blockRows(t *testing.T, db *sql.DB, docPath string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT round || ':' || verdict || ':' || complete FROM review_blocks
		WHERE doc_path = ? ORDER BY round`, docPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func keys(m map[string]findingRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestReviewBlocksTwoRoundsIdempotent(t *testing.T) {
	db := carryFixture(t)
	planDir, doc := reviewPlan(t, reviewFixture(t, "round2-resolves.md"))
	scanReviewPlan(t, db, planDir)

	if got, want := blockRows(t, db, doc), []string{"1:FAIL:1", "2:PASS:1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("blocks = %v, want %v", got, want)
	}
	fs := readFindings(t, db, doc)
	if got, want := keys(fs), []string{"1/R1-1", "1/R1-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
	f := fs["1/R1-1"]
	if f.typ != "silent-failure" || f.file != "src/lib/export/writer.ts" || f.line != 84 || f.resolvedBy != "2" ||
		f.source != ReviewSourceInteractive {
		t.Errorf("R1-1 = %+v", f)
	}
	if fs["1/R1-2"].resolvedBy != "2" {
		t.Errorf("R1-2 resolved_by = %q, want 2 (round 2 resolves it)", fs["1/R1-2"].resolvedBy)
	}

	// A rescan of the unchanged doc — and a forced re-apply — touches the same rows.
	scanReviewPlan(t, db, planDir)
	scanReviewPlan(t, db, planDir)
	again := readFindings(t, db, doc)
	if !reflect.DeepEqual(again, fs) {
		t.Errorf("rescan changed the rows:\nbefore %+v\nafter  %+v", fs, again)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM review_blocks`); n != 2 {
		t.Errorf("review_blocks = %d after rescans, want 2", n)
	}
}

func TestReviewBlocksPlanRound(t *testing.T) {
	db := carryFixture(t)
	planDir, doc := reviewPlan(t, reviewFixture(t, "plan-round.md"))
	scanReviewPlan(t, db, planDir)

	if got, want := blockRows(t, db, doc), []string{"1:FAIL:1", "2:PASS:1", "plan:PASS:1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("blocks = %v, want %v", got, want)
	}
	fs := readFindings(t, db, doc)
	if len(fs) != 1 || fs["1/R1-1"].typ != "correctness" || fs["1/R1-1"].resolvedBy != "2" {
		t.Errorf("findings = %+v, want R1-1 correctness resolved by round 2", fs)
	}
}

func TestReviewBlocksRemovedFromDocRemoveRows(t *testing.T) {
	db := carryFixture(t)
	planDir, doc := reviewPlan(t, reviewFixture(t, "round2-resolves.md"))
	scanReviewPlan(t, db, planDir)

	// Drop the round-2 block: its block row goes, and the findings it resolved
	// are open again.
	body := reviewFixture(t, "round2-resolves.md")
	start := strings.Index(body, "```yaml\nkind: review\nround: 2")
	end := start + strings.Index(body[start+3:], "```") + 6
	if err := os.WriteFile(doc, []byte(body[:start]+body[end:]), 0o644); err != nil {
		t.Fatal(err)
	}
	scanReviewPlan(t, db, planDir)
	if got, want := blockRows(t, db, doc), []string{"1:FAIL:1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("blocks after removing round 2 = %v, want %v", got, want)
	}
	if fs := readFindings(t, db, doc); fs["1/R1-1"].resolvedBy != "" || len(fs) != 2 {
		t.Errorf("findings after removing round 2 = %+v, want both open", fs)
	}

	// Drop one finding: its row goes, the other keeps its id.
	before := readFindings(t, db, doc)["1/R1-1"].id
	body2 := strings.Replace(body[:start]+body[end:],
		"  - id: R1-2\n    type: contract\n    file: src/app/api/export/route.ts\n    line: 31\n", "", 1)
	if err := os.WriteFile(doc, []byte(body2), 0o644); err != nil {
		t.Fatal(err)
	}
	scanReviewPlan(t, db, planDir)
	fs := readFindings(t, db, doc)
	if got := keys(fs); !reflect.DeepEqual(got, []string{"1/R1-1"}) {
		t.Errorf("findings = %v, want only 1/R1-1", got)
	}
	if fs["1/R1-1"].id != before {
		t.Errorf("R1-1 row id %d → %d; an upsert must keep it", before, fs["1/R1-1"].id)
	}

	// Drop the whole section.
	if err := os.WriteFile(doc, []byte("# Phase 2 — Orders export\nStatus: In progress\n\n## Completion Report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanReviewPlan(t, db, planDir)
	if n := count(t, db, `SELECT (SELECT COUNT(*) FROM review_blocks) + (SELECT COUNT(*) FROM review_findings)`); n != 0 {
		t.Errorf("rows left after the section was removed = %d, want 0", n)
	}
}

func TestReviewBlocksGeneratedIDsAndUnknownType(t *testing.T) {
	db := carryFixture(t)
	body := "# Phase 1 — X\nStatus: In progress\n\n## Review\n\n```yaml\nkind: review\nround: 1\nverdict: FAIL\nsource: backfill\n" +
		"findings:\n  - type: style\n    file: a.go\n    note: tabs\n  - type: quality\n    file: b.go\n    evidence: \"commit abc1234\"\n```\n\n## Completion Report\n"
	planDir, doc := reviewPlan(t, body)
	scanReviewPlan(t, db, planDir)
	fs := readFindings(t, db, doc)
	if got := keys(fs); !reflect.DeepEqual(got, []string{"1/r1-1", "1/r1-2"}) {
		t.Fatalf("finding ids = %v, want r1-1, r1-2", got)
	}
	if f := fs["1/r1-1"]; f.typ != "" || !strings.Contains(f.note, "type: style") || f.source != ReviewSourceBackfill {
		t.Errorf("unknown type kept? %+v", f)
	}
	if f := fs["1/r1-2"]; f.typ != "quality" || f.evidence != "commit abc1234" {
		t.Errorf("r1-2 = %+v", f)
	}
}

// A doc whose only review block cannot be read stores no rows for it, and is not
// legacy: it HAS a review, just a broken one (the scan warns with its line).
func TestReviewBlocksUnparsableIsNotLegacy(t *testing.T) {
	db := carryFixture(t)
	planDir, _ := reviewPlan(t, reviewFixture(t, "unparsable.md"))
	scanReviewPlan(t, db, planDir)
	if n := count(t, db, `SELECT legacy_review FROM epic_phases`); n != 0 {
		t.Errorf("legacy_review = %d for a doc with an unparsable block, want 0", n)
	}
}

// legacyPlan writes the legacy fixture with its mtime set to at.
func legacyPlan(t *testing.T, at time.Time) (planDir, docPath string) {
	t.Helper()
	planDir, docPath = reviewPlan(t, reviewFixture(t, "legacy-done.md"))
	if err := os.Chtimes(docPath, at, at); err != nil {
		t.Fatal(err)
	}
	return planDir, docPath
}

func legacyOf(t *testing.T, db *sql.DB, docPath string) (legacy int, status string) {
	t.Helper()
	if err := db.QueryRow(`SELECT legacy_review, COALESCE(doc_status, '') FROM epic_phases WHERE doc_path = ?`, docPath).
		Scan(&legacy, &status); err != nil {
		t.Fatal(err)
	}
	return legacy, status
}

func TestLegacyReviewDoneBeforeCutoff(t *testing.T) {
	db := carryFixture(t)
	planDir, doc := legacyPlan(t, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	scanReviewPlan(t, db, planDir)
	if legacy, status := legacyOf(t, db, doc); legacy != 1 || status != "done" {
		t.Fatalf("legacy_review=%d status=%q, want 1 and done (status unchanged)", legacy, status)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM review_findings`); n != 0 {
		t.Errorf("a legacy phase grew %d finding rows", n)
	}

	// Sticky: a later edit moves the mtime past the cutoff; the phase is still legacy.
	now := time.Now().Add(time.Hour)
	if err := os.Chtimes(doc, now, now); err != nil {
		t.Fatal(err)
	}
	scanReviewPlan(t, db, planDir)
	if legacy, _ := legacyOf(t, db, doc); legacy != 1 {
		t.Errorf("legacy_review = %d after a later edit, want 1 (sticky)", legacy)
	}

	// A review block written later ends "legacy": the phase now has a review.
	body := reviewFixture(t, "legacy-done.md")
	body = strings.Replace(body, "## Completion Report", "## Review\n\n```yaml\nkind: review\nround: 1\nverdict: PASS\nsource: backfill\nfindings: []\n```\n\n## Completion Report", 1)
	if err := os.WriteFile(doc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	scanReviewPlan(t, db, planDir)
	if legacy, status := legacyOf(t, db, doc); legacy != 0 || status != "done" {
		t.Errorf("with a block: legacy_review=%d status=%q, want 0 and done", legacy, status)
	}
}

func TestLegacyReviewNotForPhasesAfterCutoffOrUnfinished(t *testing.T) {
	t.Run("done after the cutoff", func(t *testing.T) {
		db := carryFixture(t)
		planDir, doc := legacyPlan(t, time.Now().Add(time.Hour))
		scanReviewPlan(t, db, planDir)
		if legacy, _ := legacyOf(t, db, doc); legacy != 0 {
			t.Errorf("legacy_review = %d, want 0 — finished after the cutoff, it owes a review", legacy)
		}
	})
	t.Run("report written_at wins over mtime", func(t *testing.T) {
		db := carryFixture(t)
		body := strings.Replace(reviewFixture(t, "legacy-done.md"), "Shipped the export",
			"written_at: 2026-10-09T10:00:00Z\n\nShipped the export", 1)
		planDir, doc := reviewPlan(t, body) // mtime = now, after the cutoff
		scanReviewPlan(t, db, planDir)
		if legacy, _ := legacyOf(t, db, doc); legacy != 1 {
			t.Errorf("legacy_review = %d, want 1 — the report says it was written before the cutoff", legacy)
		}
	})
	t.Run("not done", func(t *testing.T) {
		db := carryFixture(t)
		body := strings.Replace(strings.Replace(reviewFixture(t, "legacy-done.md"), "Status: Done", "Status: In progress", 1),
			"- [x] export works", "- [ ] export works", 1)
		planDir, doc := reviewPlan(t, body)
		old := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
		if err := os.Chtimes(doc, old, old); err != nil {
			t.Fatal(err)
		}
		scanReviewPlan(t, db, planDir)
		if legacy, _ := legacyOf(t, db, doc); legacy != 0 {
			t.Errorf("legacy_review = %d for an unfinished phase, want 0", legacy)
		}
	})
	t.Run("empty report", func(t *testing.T) {
		db := carryFixture(t)
		body := reviewFixture(t, "legacy-done.md")
		body = body[:strings.Index(body, "## Completion Report")] + "## Completion Report\n"
		planDir, doc := reviewPlan(t, body)
		old := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
		if err := os.Chtimes(doc, old, old); err != nil {
			t.Fatal(err)
		}
		scanReviewPlan(t, db, planDir)
		if legacy, _ := legacyOf(t, db, doc); legacy != 0 {
			t.Errorf("legacy_review = %d with an empty Completion Report, want 0", legacy)
		}
	})
}

// A phase doc that leaves the plan takes its review rows with it.
func TestReviewRowsSweptWithTheirPhase(t *testing.T) {
	db := carryFixture(t)
	planDir, _ := reviewPlan(t, reviewFixture(t, "round2-resolves.md"))
	scanReviewPlan(t, db, planDir)
	if err := os.WriteFile(filepath.Join(planDir, "README.md"),
		[]byte("# Epic\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n| 1 | Two | `phase-2.md` | — |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "phase-2.md"), []byte("# Phase 2\n\n- [ ] a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanReviewPlan(t, db, planDir)
	if n := count(t, db, `SELECT (SELECT COUNT(*) FROM review_blocks) + (SELECT COUNT(*) FROM review_findings)`); n != 0 {
		t.Errorf("review rows of a removed phase = %d, want 0", n)
	}
}
