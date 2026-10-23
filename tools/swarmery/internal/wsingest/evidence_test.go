package wsingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
)

const evidenceFixtures = "../../testdata/fixtures/decision-evidence"

func evidenceFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(evidenceFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScanEvidence(t *testing.T) {
	cases := []struct {
		name, report string
		want         []EvidenceMention
	}{
		{"empty", "", nil},
		{"cites", "Closes SC-4 per D-2.\nAlso SC-04 again.", []EvidenceMention{
			{ID: "SC-4", Status: EvidenceHeld, Excerpt: "Closes SC-4 per D-2."},
			{ID: "D-2", Status: EvidenceHeld, Excerpt: "Closes SC-4 per D-2."},
		}},
		{"not implemented wins even after a mention", "D-5 came up.\n- not implemented: **D-5** — waits on the bucket", []EvidenceMention{
			{ID: "D-5", Status: EvidenceNotImplemented, Excerpt: "waits on the bucket"},
		}},
		{"colon separator", "Not implemented: SC-7: out of scope", []EvidenceMention{
			{ID: "SC-7", Status: EvidenceNotImplemented, Excerpt: "out of scope"},
		}},
		{"fenced and non-ids skipped", "```\nD-9\n```\nAD-3 and SC-0 and D-x", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ScanEvidence(c.report); !reflect.DeepEqual(got, c.want) {
				t.Errorf("ScanEvidence = %#v, want %#v", got, c.want)
			}
		})
	}
}

type evidenceRow struct{ id, kind, status, excerpt string }

func readEvidence(t *testing.T, db *sql.DB) []evidenceRow {
	t.Helper()
	rows, err := db.Query(`SELECT decision_id, kind, status, excerpt FROM decision_evidence
		WHERE workspace_task_id = ? ORDER BY kind, decision_id`, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []evidenceRow
	for rows.Next() {
		var r evidenceRow
		if err := rows.Scan(&r.id, &r.kind, &r.status, &r.excerpt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// scanEvidencePlan applies a parsed plan the way scanEpics does: phases (and
// their review blocks), then the report evidence.
func scanEvidencePlan(t *testing.T, db *sql.DB, planDir string) {
	t.Helper()
	warn, _ := collectWarn(t)
	phases := parsePlan(planDir, warn)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEpics(tx, carryTaskID, phases, true); err != nil {
		tx.Rollback()
		t.Fatalf("applyEpics: %v", err)
	}
	if err := applyEvidence(tx, carryTaskID, phases); err != nil {
		tx.Rollback()
		t.Fatalf("applyEvidence: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

const evidenceReadme = "# Epic\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n" +
	"| 1 | Export | `phase-1.md` | — |\n| 2 | Import | `phase-2.md` | 1 |\n"

func TestDecisionEvidenceFromReports(t *testing.T) {
	db := carryFixture(t)
	planDir := writePlan(t, map[string]string{
		"README.md":  evidenceReadme,
		"phase-1.md": evidenceFixture(t, "report-cites.md"),
		"phase-2.md": evidenceFixture(t, "plan-review.md"),
	})
	scanEvidencePlan(t, db, planDir)
	first := readEvidence(t, db)

	want := []evidenceRow{
		{"D-2", "report", "evidence", "Shipped the streaming writer as decided in D-2 (CSV, not XLSX); the endpoint"},
		{"D-5", "report", "not-implemented", "the S3 mirror waits on the bucket policy"},
		{"SC-4", "report", "evidence", "returns 413 past the row cap, which closes SC-4."},
		{"D-2", "review", "evidence", "phase 1 @ 1a2b3c4 internal/export/writer.go:40"},
		{"SC-3", "review", "missing", ""},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("evidence rows =\n%v\nwant\n%v", first, want)
	}

	// The drift became a plan-conformance finding on the doc that carries the block.
	fs := readFindings(t, db, filepath.Join(planDir, "phase-2.md"))
	d, ok := fs["plan/drift-1"]
	if !ok {
		t.Fatalf("drift finding missing; findings = %v", keys(fs))
	}
	if d.typ != "plan-conformance" || d.file != "internal/import/reader.go" || d.line != 88 ||
		d.criterion != "D-2" || d.note != "reads XLSX although D-2 chose CSV" {
		t.Errorf("drift finding = %+v", d)
	}

	// Idempotent: a second scan of the same docs leaves the same rows, same ids.
	var idsBefore, idsAfter []int64
	idsOf := func() []int64 {
		rows, err := db.Query(`SELECT id FROM decision_evidence ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	idsBefore = idsOf()
	scanEvidencePlan(t, db, planDir)
	idsAfter = idsOf()
	if got := readEvidence(t, db); !reflect.DeepEqual(got, first) {
		t.Errorf("second scan changed rows:\n%v\nwant\n%v", got, first)
	}
	if !reflect.DeepEqual(idsBefore, idsAfter) {
		t.Errorf("second scan churned row ids: %v → %v", idsBefore, idsAfter)
	}

	// A citation removed from the report retracts its row.
	if err := os.WriteFile(filepath.Join(planDir, "phase-1.md"),
		[]byte("# Phase 1\n\n## Completion Report\n\nOnly SC-4 now.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scanEvidencePlan(t, db, planDir)
	var reportIDs []string
	for _, r := range readEvidence(t, db) {
		if r.kind == "report" {
			reportIDs = append(reportIDs, r.id)
		}
	}
	if !reflect.DeepEqual(reportIDs, []string{"SC-4"}) {
		t.Errorf("report ids after edit = %v, want [SC-4]", reportIDs)
	}
}

// seedDeclared writes the plan's declared ids: decisions D-2, D-5, D-7 and spec
// criteria SC-3, SC-4.
func seedDeclared(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, d := range []struct {
		id  string
		seq int
	}{{"D-2", 2}, {"D-5", 5}, {"D-7", 7}} {
		mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, source, question, status, answer, created_at)
			VALUES (?, ?, ?, 'interview', 'Which format?', 'decided', 'CSV', '2026-10-01T00:00:00Z')`, carryTaskID, d.id, d.seq)
	}
	mustExec(t, db, `INSERT INTO phase_decisions (workspace_task_id, decision_id, seq, source, question, status, created_at)
		VALUES (?, 'D-8', 8, 'stop', 'guard', 'undecided', '2026-10-01T00:00:00Z')`, carryTaskID)
	mustExec(t, db, `INSERT INTO spec_criteria (workspace_task_id, pos, cid, text) VALUES (?, 0, 'SC-3', 'import works'), (?, 1, 'SC-4', 'row cap')`,
		carryTaskID, carryTaskID)
}

func tableStatuses(tb DecisionTable) map[string]string {
	out := map[string]string{}
	for _, r := range tb.Rows {
		out[r.ID] = r.Status
	}
	return out
}

func TestLoadDecisionTable(t *testing.T) {
	db := carryFixture(t)
	seedDeclared(t, db)
	planDir := writePlan(t, map[string]string{
		"README.md":  evidenceReadme,
		"phase-1.md": evidenceFixture(t, "report-cites.md"),
		"phase-2.md": evidenceFixture(t, "plan-review.md"),
	})
	scanEvidencePlan(t, db, planDir)
	mustExec(t, db, `UPDATE epic_phases SET covers = '["SC-3","SC-4"]', decision_deps = '["D-2","D-5"]' WHERE seq = 1`)
	mustExec(t, db, `UPDATE epic_phases SET covers = '["SC-3"]' WHERE seq = 2`)

	tb, err := LoadDecisionTable(db, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range tb.Rows {
		order = append(order, r.ID)
	}
	// Interview decisions first (the stop decision D-8 is not a plan decision), then criteria.
	if want := []string{"D-2", "D-5", "D-7", "SC-3", "SC-4"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("rows = %v, want %v", order, want)
	}
	want := map[string]string{
		"D-2":  RowEvidence,       // report + review both say so
		"D-5":  RowNotImplemented, // never remaining
		"D-7":  RowRemaining,      // nobody claims or cites it
		"SC-3": RowRemaining,      // the plan review looked and found it missing
		"SC-4": RowEvidence,
	}
	if got := tableStatuses(tb); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	if tb.Rows[1].Reason != "the S3 mirror waits on the bucket policy" {
		t.Errorf("D-5 reason = %q", tb.Rows[1].Reason)
	}
	if !reflect.DeepEqual(tb.Rows[3].ClaimedBy, []int{1, 2}) {
		t.Errorf("SC-3 claimedBy = %v, want [1 2]", tb.Rows[3].ClaimedBy)
	}
	if tb.Rows[0].Text != "Which format? → CSV" {
		t.Errorf("D-2 text = %q", tb.Rows[0].Text)
	}
	if tb.Counts[RowEvidence] != 2 || tb.Counts[RowRemaining] != 2 || tb.Counts[RowNotImplemented] != 1 {
		t.Errorf("counts = %v", tb.Counts)
	}
	// The interactive plan round is the latest plan review: FAIL ⇒ verified.
	if tb.PlanReview.Status != PlanReviewVerified || tb.PlanReview.Verdict != "FAIL" {
		t.Errorf("plan review = %+v, want verified FAIL", tb.PlanReview)
	}
}

func TestLoadDecisionTableInconclusiveIsNotVerified(t *testing.T) {
	db := carryFixture(t)
	seedDeclared(t, db)
	mustExec(t, db, `INSERT INTO phase_reviews (scope, phase_id, workspace_task_id, session_uuid, run_session_uuid,
		tree_before, tree_after, verdict, detail, findings, fix_round, started_at, finished_at)
		VALUES ('plan', NULL, ?, 's', 'branchset:x', '', '', 'inconclusive', 'review-unverifiable: 412 files', '', 0,
		'2026-10-02T00:00:00Z', '2026-10-02T00:01:00Z')`, strconv.FormatInt(carryTaskID, 10))

	tb, err := LoadDecisionTable(db, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if tb.PlanReview.Status != PlanReviewNotVerified || tb.PlanReview.ReasonClass != "review-unverifiable" {
		t.Fatalf("plan review = %+v, want not-verified / review-unverifiable", tb.PlanReview)
	}
	for _, r := range tb.Rows {
		if r.Status != RowNotVerified || r.Reason != "review-unverifiable" {
			t.Errorf("%s = %s (%s), want not-verified (review-unverifiable) — never an empty table", r.ID, r.Status, r.Reason)
		}
	}
}

func TestLoadDecisionTableEmptyForOldPlans(t *testing.T) {
	db := carryFixture(t)
	tb, err := LoadDecisionTable(db, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Rows) != 0 || tb.Rows == nil {
		t.Errorf("rows = %#v, want an empty non-nil list", tb.Rows)
	}
}

func TestApplyPlanReviewDaemonBlock(t *testing.T) {
	db := carryFixture(t)
	seedDeclared(t, db)
	planDir := writePlan(t, map[string]string{"README.md": evidenceReadme, "phase-1.md": "# P1\n", "phase-2.md": "# P2\n"})
	mustExec(t, db, `INSERT INTO task_artifacts (task_id, kind, path, content_hash, parsed_at) VALUES (?, 'plan', ?, 'h', 'now')`,
		carryTaskID, planDir)
	scanEvidencePlan(t, db, planDir)

	out := "Findings…\n\n```yaml\nkind: review\nround: plan\nverdict: PASS\nfindings: []\nids:\n" +
		"  - {id: SC-3, evidence: \"phase 2 @ abc internal/import/x.go:3\"}\n  - {id: D-7, evidence: missing}\n" +
		"drift:\n  - {id: D-2, where: internal/a.go, how: \"XLSX\"}\n```\nVERDICT: PASS\n"
	blk := PlanRoundBlock(out)
	if blk == nil {
		t.Fatal("PlanRoundBlock found no round: plan block")
	}
	if err := ApplyPlanReview(db, carryTaskID, *blk); err != nil {
		t.Fatal(err)
	}
	// Twice: a repeat replaces, never duplicates.
	if err := ApplyPlanReview(db, carryTaskID, *blk); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(planDir, "README.md")
	fs := readFindings(t, db, readme)
	if d, ok := fs["plan/drift-1"]; !ok || d.typ != "plan-conformance" || d.source != ReviewSourceDaemon {
		t.Errorf("daemon drift finding = %+v (ok=%v)", d, ok)
	}
	got := readEvidence(t, db)
	want := []evidenceRow{
		{"D-7", "review", "missing", ""},
		{"SC-3", "review", "evidence", "phase 2 @ abc internal/import/x.go:3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("evidence = %v, want %v", got, want)
	}

	// A plan rescan neither sweeps the README-keyed rows nor their evidence.
	scanEvidencePlan(t, db, planDir)
	if len(readFindings(t, db, readme)) != 1 || len(readEvidence(t, db)) != 2 {
		t.Errorf("rescan swept the daemon plan review rows")
	}
	if tableStatuses(mustTable(t, db))["SC-3"] != RowEvidence {
		t.Errorf("SC-3 should read evidence from the daemon plan review")
	}
	if blk.Round != mdfence.ReviewRoundPlan {
		t.Errorf("round = %q", blk.Round)
	}
}

func mustTable(t *testing.T, db *sql.DB) DecisionTable {
	t.Helper()
	tb, err := LoadDecisionTable(db, carryTaskID)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}
