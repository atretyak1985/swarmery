package wsingest

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// legacyLedger mirrors the committed full-card fixture: 4-cell rows, a
// Ukrainian header, one malformed row, one row without an agent.
const legacyLedger = `| Агент | Фаза | Вердикт | Артефакт |
|---|---|---|---|
| @core:context-gatherer | 2 | OK | phases/02-context.md |
| @core:implementation-agent | 5 | RE-DISPATCH | — |
| implementation-agent | 5 | OK | src changes |
| broken row |
|  | 6 | OK | orphan row without an agent |
`

// TestParseLedgerLegacy4Cell pins the pre-0020 contract: 4-cell docs must
// parse byte-identically before and after the 7-cell extension.
func TestParseLedgerLegacy4Cell(t *testing.T) {
	got := parseLedger(legacyLedger)
	type row struct {
		seq                             int
		agent, phase, verdict, artifact string
	}
	want := []row{
		{1, "context-gatherer", "2", "OK", "phases/02-context.md"},
		{2, "implementation-agent", "5", "RE-DISPATCH", "—"},
		{3, "implementation-agent", "5", "OK", "src changes"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseLedger = %+v, want %d rows (malformed + empty-agent skipped)", got, len(want))
	}
	for i, w := range want {
		g := row{got[i].seq, got[i].agent, got[i].phase, got[i].verdict, got[i].artifact}
		if g != w {
			t.Errorf("row[%d] = %+v, want %+v", i, g, w)
		}
		if got[i].loops != nil || got[i].quality != nil || got[i].mistakes != "" {
			t.Errorf("row[%d] legacy assessment = loops %v quality %v mistakes %q, want nil/nil/empty",
				i, got[i].loops, got[i].quality, got[i].mistakes)
		}
	}
}

// assessmentLedger: 7-cell rows — agent | phase | verdict | loops | quality |
// mistakes | artifact (tech-lead ≥ core 2.2.0).
const assessmentLedger = `| Agent | Phase | Verdict | Loops | Quality | Mistakes | Artifact |
|---|---|---|---|---|---|---|
| @core:implementation-agent | 4 | OK | 0 | 5 | - | phases/04-impl.md |
| code-auditor | 5 | RE-DISPATCH | 2 | 2 | ignored AC#3, 3× write-before-read | — |
`

func intPtrEq(p *int, want int) bool { return p != nil && *p == want }

// TestParseLedger7Cell: the new layout fills loops/quality/mistakes and takes
// the artifact from cell 7; mistakes "-" folds to empty.
func TestParseLedger7Cell(t *testing.T) {
	got := parseLedger(assessmentLedger)
	if len(got) != 2 {
		t.Fatalf("parseLedger = %+v, want 2 rows", got)
	}
	r := got[0]
	if r.agent != "implementation-agent" || r.phase != "4" || r.verdict != "OK" ||
		r.artifact != "phases/04-impl.md" {
		t.Errorf("row[0] = %+v, want agent/phase/verdict/artifact from cells 1-3 and 7", r)
	}
	if !intPtrEq(r.loops, 0) || !intPtrEq(r.quality, 5) || r.mistakes != "" {
		t.Errorf("row[0] assessment = loops %v quality %v mistakes %q, want 0/5/empty ('-' folds)",
			r.loops, r.quality, r.mistakes)
	}
	r = got[1]
	if !intPtrEq(r.loops, 2) || !intPtrEq(r.quality, 2) ||
		r.mistakes != "ignored AC#3, 3× write-before-read" {
		t.Errorf("row[1] assessment = loops %v quality %v mistakes %q, want 2/2/kept",
			r.loops, r.quality, r.mistakes)
	}
	if r.artifact != "—" {
		t.Errorf("row[1] artifact = %q, want cell 7 (%q)", r.artifact, "—")
	}
}

// TestParseLedgerMalformedAssessmentCells: non-numeric or out-of-range
// loops/quality degrade to nil; the row is still ingested.
func TestParseLedgerMalformedAssessmentCells(t *testing.T) {
	const doc = `| Agent | Phase | Verdict | Loops | Quality | Mistakes | Artifact |
|---|---|---|---|---|---|---|
| @core:debugger | 3 | OK | - | high | - | phases/03-debug.md |
| tester | 5 | OK | 1 | 7 | flaky suite | logs/test.md |
| reviewer | 6 | OK | 0 | 0 |  | — |
`
	got := parseLedger(doc)
	if len(got) != 3 {
		t.Fatalf("parseLedger = %+v, want 3 rows (malformed cells tolerated, rows kept)", got)
	}
	if got[0].loops != nil || got[0].quality != nil {
		t.Errorf("row[0] = loops %v quality %v, want nil/nil (loops '-', quality 'high')",
			got[0].loops, got[0].quality)
	}
	if got[0].agent != "debugger" || got[0].artifact != "phases/03-debug.md" {
		t.Errorf("row[0] = %+v, want the row otherwise intact", got[0])
	}
	if !intPtrEq(got[1].loops, 1) || got[1].quality != nil {
		t.Errorf("row[1] = loops %v quality %v, want 1/nil (quality 7 out of 1..5)",
			got[1].loops, got[1].quality)
	}
	if got[2].quality != nil {
		t.Errorf("row[2] quality = %v, want nil (0 out of 1..5)", got[2].quality)
	}
	if got[2].mistakes != "" {
		t.Errorf("row[2] mistakes = %q, want empty for an empty cell", got[2].mistakes)
	}
}

// TestParseLedgerMixed4And7Cell: one doc mixing legacy and assessment rows —
// both parse; legacy rows keep nil assessment fields.
func TestParseLedgerMixed4And7Cell(t *testing.T) {
	const doc = `| Agent | Phase | Verdict | Loops | Quality | Mistakes | Artifact |
|---|---|---|---|---|---|---|
| @core:context-gatherer | 2 | OK | phases/02-context.md |
| @core:implementation-agent | 4 | OK | 1 | 4 | missed an edge case | phases/04-impl.md |
`
	got := parseLedger(doc)
	if len(got) != 2 {
		t.Fatalf("parseLedger = %+v, want 2 rows", got)
	}
	if got[0].agent != "context-gatherer" || got[0].artifact != "phases/02-context.md" ||
		got[0].loops != nil || got[0].quality != nil || got[0].mistakes != "" {
		t.Errorf("legacy row = %+v, want 4-cell layout with nil assessment", got[0])
	}
	if got[1].artifact != "phases/04-impl.md" || !intPtrEq(got[1].loops, 1) ||
		!intPtrEq(got[1].quality, 4) || got[1].mistakes != "missed an edge case" {
		t.Errorf("7-cell row = %+v, want loops 1, quality 4, mistakes kept", got[1])
	}
}

// TestLedgerAssessmentIngest: end-to-end — a scanned workspace ledger lands in
// task_delegations with the 0020 columns filled (NULL for legacy/malformed).
func TestLedgerAssessmentIngest(t *testing.T) {
	db := testDB(t)
	root, taskDir := tempTaskWorkspace(t)
	writeFile(t, filepath.Join(taskDir, "logs", "agents.md"),
		"| Agent | Phase | Verdict | Loops | Quality | Mistakes | Artifact |\n"+
			"|---|---|---|---|---|---|---|\n"+
			"| @core:context-gatherer | 2 | OK | phases/02-context.md |\n"+
			"| @core:implementation-agent | 4 | OK | 0 | 5 | - | phases/04-impl.md |\n"+
			"| code-auditor | 5 | RE-DISPATCH | - | high | ignored AC#3 | — |\n")

	stats, err := New(db, Config{WorkspaceRoot: root}).Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if stats.Delegations != 3 {
		t.Fatalf("delegations = %d, want 3", stats.Delegations)
	}

	rows, err := db.Query(`
		SELECT seq, agent, loops, quality, mistakes, artifact
		  FROM task_delegations ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type del struct {
		seq                int
		agent              string
		loops, quality     sql.NullInt64
		mistakes, artifact sql.NullString
	}
	var got []del
	for rows.Next() {
		var d del
		if err := rows.Scan(&d.seq, &d.agent, &d.loops, &d.quality, &d.mistakes, &d.artifact); err != nil {
			t.Fatal(err)
		}
		got = append(got, d)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %+v, want 3", got)
	}
	if got[0].loops.Valid || got[0].quality.Valid || got[0].mistakes.Valid {
		t.Errorf("legacy row = %+v, want NULL loops/quality/mistakes", got[0])
	}
	if got[1].loops.Int64 != 0 || !got[1].loops.Valid || got[1].quality.Int64 != 5 || !got[1].quality.Valid {
		t.Errorf("assessed row = %+v, want loops 0, quality 5", got[1])
	}
	if got[1].mistakes.Valid {
		t.Errorf("assessed row mistakes = %+v, want NULL ('-' folds to empty → NULL)", got[1].mistakes)
	}
	if got[1].artifact.String != "phases/04-impl.md" {
		t.Errorf("assessed row artifact = %q, want cell 7", got[1].artifact.String)
	}
	if got[2].loops.Valid || got[2].quality.Valid {
		t.Errorf("malformed row = %+v, want NULL ints, row kept", got[2])
	}
	if got[2].mistakes.String != "ignored AC#3" {
		t.Errorf("malformed row mistakes = %q, want kept", got[2].mistakes.String)
	}
}
