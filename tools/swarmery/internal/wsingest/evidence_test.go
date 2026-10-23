package wsingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
)

func TestScanEvidence(t *testing.T) {
	text := "Shipped per D-2 and SC-4; D-02 again.\n" +
		"```\nD-9 inside a fence\n```\n" +
		"not implemented: D-5 — superseded by D-7\n" +
		"Also mentions D-5 in passing.\n" +
		"Not implemented: **SC-3**: no fixture data\n" +
		"BUILD-2 and xD-3 are not ids; d-4 is not either.\n"
	got := ScanEvidence(text)
	want := []EvidenceMention{
		{ID: "D-2", Status: EvidenceStatusEvidence, Excerpt: "Shipped per D-2 and SC-4; D-02 again."},
		{ID: "SC-4", Status: EvidenceStatusEvidence, Excerpt: "Shipped per D-2 and SC-4; D-02 again."},
		{ID: "D-5", Status: EvidenceStatusNotImplemented, Reason: "superseded by D-7", Excerpt: "not implemented: D-5 — superseded by D-7"},
		{ID: "SC-3", Status: EvidenceStatusNotImplemented, Reason: "no fixture data", Excerpt: "Not implemented: **SC-3**: no fixture data"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScanEvidence =\n%+v\nwant\n%+v", got, want)
	}
	// A not-implemented line after a plain mention still wins.
	got = ScanEvidence("D-5 was discussed.\nnot implemented: D-5 — out of scope\n")
	if len(got) != 1 || got[0].Status != EvidenceStatusNotImplemented || got[0].Reason != "out of scope" {
		t.Errorf("not-implemented after a mention = %+v", got)
	}
	if ScanEvidence("") != nil {
		t.Error("empty text yields mentions")
	}
}

func TestPlanReviewIDs(t *testing.T) {
	r := mdfence.Review{Round: mdfence.ReviewRoundPlan, IDs: []mdfence.IDEvidence{
		{ID: "D-2", Evidence: "phase 3 @ abc1234"},
		{ID: "sc-04", Evidence: "missing"},
		{ID: "ExportJob", Evidence: "src/x.ts:1"},
		{ID: "D-2", Evidence: "phase 9"},
		{ID: "D-6"},
	}}
	got := PlanReviewIDs(r)
	want := []ReviewIDEvidence{
		{ID: "D-2", Status: EvidenceStatusEvidence, Excerpt: "phase 3 @ abc1234", PhaseSeq: 3},
		{ID: "SC-4", Status: EvidenceStatusMissing, Excerpt: "missing"},
		{ID: "D-6", Status: EvidenceStatusMissing},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PlanReviewIDs =\n%+v\nwant\n%+v", got, want)
	}
	r.Round = "1"
	if PlanReviewIDs(r) != nil {
		t.Error("a phase round's ids[] were read as decision evidence")
	}
}

type evidenceDBRow struct {
	refID, kind, status, reason, excerpt, createdAt string
	phaseID                                         int64
}

func readEvidence(t *testing.T, db *sql.DB, docPath string) map[string]evidenceDBRow {
	t.Helper()
	rows, err := db.Query(`SELECT ref_id, kind, status, reason, excerpt, created_at, COALESCE(phase_id, 0)
		FROM decision_evidence WHERE doc_path = ?`, docPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]evidenceDBRow{}
	for rows.Next() {
		var r evidenceDBRow
		if err := rows.Scan(&r.refID, &r.kind, &r.status, &r.reason, &r.excerpt, &r.createdAt, &r.phaseID); err != nil {
			t.Fatal(err)
		}
		out[r.kind+"/"+r.refID] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestApplyEvidenceFromReport: the fixture report cites D-2 and SC-4 (evidence)
// and marks D-5 `not implemented:` (with its reason, never remaining); the quoted
// prompt's D-9/SC-9 count for nothing; a second scan changes nothing; an edited
// report prunes what it no longer says.
func TestApplyEvidenceFromReport(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "evidence", "phase-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	db := carryFixture(t)
	planDir, docPath := reviewPlan(t, string(body))
	scanReviewPlan(t, db, planDir)

	got := readEvidence(t, db, docPath)
	if len(got) != 3 {
		t.Fatalf("rows = %+v, want D-2, SC-4, D-5", got)
	}
	var phaseID int64
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE doc_path = ?`, docPath).Scan(&phaseID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"D-2", "SC-4"} {
		r := got["report/"+id]
		if r.status != EvidenceStatusEvidence || r.phaseID != phaseID || r.excerpt == "" {
			t.Errorf("%s = %+v, want evidence on phase %d", id, r, phaseID)
		}
	}
	d5 := got["report/D-5"]
	if d5.status != EvidenceStatusNotImplemented || d5.reason != "the discount engine moved to the pricing plan" {
		t.Errorf("D-5 = %+v, want not-implemented with its reason", d5)
	}

	// Idempotent: the same doc again is the same rows, created_at untouched.
	scanReviewPlan(t, db, planDir)
	if again := readEvidence(t, db, docPath); !reflect.DeepEqual(again, got) {
		t.Errorf("second scan changed rows:\n%+v\nwas\n%+v", again, got)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM decision_evidence`).Scan(&n); err != nil || n != 3 {
		t.Errorf("total rows after rescan = %d, %v; want 3", n, err)
	}

	// The report stops citing SC-4: its row goes.
	edited := strings.Replace(string(body), "and SC-4\n", "\n", 1)
	if err := os.WriteFile(docPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	scanReviewPlan(t, db, planDir)
	if _, ok := readEvidence(t, db, docPath)["report/SC-4"]; ok {
		t.Error("SC-4 kept after the report stopped citing it")
	}
}

// TestApplyEvidenceFromPlanRoundBlock: a `round: plan` block in a phase doc maps
// ids[] to kind-review evidence and drift[] to plan-conformance findings.
func TestApplyEvidenceFromPlanRoundBlock(t *testing.T) {
	doc := "# Phase 1 — One\n\n## Acceptance Criteria\n- [x] a\n\n## Review\n\n```yaml\nkind: review\nround: plan\nverdict: FAIL\n" +
		"ids:\n  - {id: D-2, evidence: \"phase 1 @ abc1234\"}\n  - {id: SC-4, evidence: missing}\n" +
		"drift:\n  - {id: D-4, where: \"internal/x/y.go:42\", how: \"a second table instead of the column\"}\n```\n"
	db := carryFixture(t)
	planDir, docPath := reviewPlan(t, doc)
	scanReviewPlan(t, db, planDir)

	ev := readEvidence(t, db, docPath)
	if ev["review/D-2"].status != EvidenceStatusEvidence || ev["review/D-2"].phaseID == 0 {
		t.Errorf("D-2 = %+v, want evidence naming phase 1", ev["review/D-2"])
	}
	if ev["review/SC-4"].status != EvidenceStatusMissing {
		t.Errorf("SC-4 = %+v, want missing", ev["review/SC-4"])
	}
	f := readFindings(t, db, docPath)
	d := f["plan/drift-1"]
	if d.typ != "plan-conformance" || d.criterion != "D-4" || d.file != "internal/x/y.go:42" ||
		d.note != "a second table instead of the column" {
		t.Errorf("drift finding = %+v (all: %+v)", d, f)
	}
	scanReviewPlan(t, db, planDir)
	if again := readFindings(t, db, docPath); again["plan/drift-1"].id != d.id {
		t.Errorf("rescan re-minted the drift finding: %+v", again)
	}
}

// TestApplyPlanReview: the daemon's plan review writes under the synthetic doc;
// the next review replaces it; nil clears it; the orphan sweep leaves it alone.
func TestApplyPlanReview(t *testing.T) {
	db := carryFixture(t)
	planDir, _ := reviewPlan(t, "# Phase 1 — One\n\n## Acceptance Criteria\n- [x] a\n")
	scanReviewPlan(t, db, planDir)
	rs, errs := mdfence.ParseReviews("```yaml\nkind: review\nround: plan\nverdict: FAIL\n" +
		"ids:\n  - {id: D-1, evidence: \"phase 1 @ abc\"}\n  - {id: SC-2, evidence: missing}\n" +
		"drift:\n  - {id: D-1, where: a.go:1, how: off}\n  - {id: D-3, where: b.go, how: gone}\n```\n")
	if len(errs) != 0 || len(rs) != 1 {
		t.Fatalf("block: %+v %+v", rs, errs)
	}
	if err := ApplyPlanReview(db, carryTaskID, &rs[0]); err != nil {
		t.Fatal(err)
	}
	doc := PlanReviewDocPath(carryTaskID)
	ev := readEvidence(t, db, doc)
	if len(ev) != 2 || ev["review/D-1"].status != EvidenceStatusEvidence || ev["review/SC-2"].status != EvidenceStatusMissing {
		t.Errorf("evidence = %+v", ev)
	}
	f := readFindings(t, db, doc)
	if len(f) != 2 || f["plan/drift-2"].criterion != "D-3" || f["plan/drift-1"].source != ReviewSourceDaemon {
		t.Errorf("findings = %+v", f)
	}
	first := f["plan/drift-1"].id

	// A rescan of the plan (with a prune) must not sweep the synthetic doc.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := sweepOrphanReviewRows(tx, carryTaskID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(readEvidence(t, db, doc)) != 2 || len(readFindings(t, db, doc)) != 2 {
		t.Error("the orphan sweep removed the daemon plan review's rows")
	}

	// Same block again: same finding rows (ids kept).
	rs[0].Drift = rs[0].Drift[:1]
	if err := ApplyPlanReview(db, carryTaskID, &rs[0]); err != nil {
		t.Fatal(err)
	}
	f = readFindings(t, db, doc)
	if len(f) != 1 || f["plan/drift-1"].id != first {
		t.Errorf("after replace: %+v (first id %d)", f, first)
	}
	if err := ApplyPlanReview(db, carryTaskID, nil); err != nil {
		t.Fatal(err)
	}
	if len(readEvidence(t, db, doc)) != 0 || len(readFindings(t, db, doc)) != 0 {
		t.Error("a nil review left rows behind")
	}
}
