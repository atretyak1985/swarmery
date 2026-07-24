package wsingest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCountCheckboxes(t *testing.T) {
	cases := []struct {
		name              string
		in                string
		wantDone, wantTot int
	}{
		{"mixed", "- [x] a\n- [ ] b\n- [x] c\n", 2, 3},
		{"none", "## Acceptance\n\nsome prose, no boxes\n", 0, 0},
		{"upper X", "- [X] done\n- [ ] not\n", 1, 2},
		{"star bullet", "* [x] a\n* [ ] b\n", 1, 2},
		{"indented", "  - [x] nested\n    - [ ] deeper\n", 1, 2},
		{"not a checkbox", "- [] a\n- [y] b\n- regular item\n", 0, 0},
		{"all done", "- [x] a\n- [x] b\n", 2, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done, tot := countCheckboxes(c.in)
			if done != c.wantDone || tot != c.wantTot {
				t.Errorf("countCheckboxes = %d/%d, want %d/%d", done, tot, c.wantDone, c.wantTot)
			}
		})
	}
}

func TestParseLeadingInts(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"—", nil},
		{"-", nil},
		{"none", nil},
		{"", nil},
		{"1, 2", []int{1, 2}},
		{"1 (API), 3 (live states)", []int{1, 3}},
		{"1", []int{1}},
		{"10—15", []int{10, 15}},
	}
	for _, c := range cases {
		if got := parseLeadingInts(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseLeadingInts(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDocFromCell(t *testing.T) {
	cases := []struct{ in, want string }{
		{"`phase-1-task-queue.md`", "phase-1-task-queue.md"},
		{"`step-03-wire.md`", "step-03-wire.md"},
		{"phase-2-parser.md", "phase-2-parser.md"},
		{"see phase-4-board-ui.md here", "phase-4-board-ui.md"},
		{"no doc here", ""},
		{"", ""},
		{"`some/path/phase-9.md`", "phase-9.md"}, // basename only
	}
	for _, c := range cases {
		if got := docFromCell(c.in); got != c.want {
			t.Errorf("docFromCell(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPhaseTableCols(t *testing.T) {
	t.Run("full header", func(t *testing.T) {
		cells := []string{"#", "Phase", "Doc", "Repo area", "Depends on", "Parallel?", "Est."}
		seq, name, doc, dep, ok := phaseTableCols(cells)
		if !ok || seq != 0 || name != 1 || doc != 2 || dep != 4 {
			t.Errorf("cols = seq %d name %d doc %d dep %d ok %v", seq, name, doc, dep, ok)
		}
	})
	t.Run("no doc column → not ok", func(t *testing.T) {
		cells := []string{"#", "Phase", "Repo area"}
		if _, _, _, _, ok := phaseTableCols(cells); ok {
			t.Error("expected ok=false without a Doc column")
		}
	})
	t.Run("synonyms seq/name/file", func(t *testing.T) {
		cells := []string{"Seq", "Name", "File", "Depends"}
		seq, name, doc, dep, ok := phaseTableCols(cells)
		if !ok || seq != 0 || name != 1 || doc != 2 || dep != 3 {
			t.Errorf("cols = seq %d name %d doc %d dep %d ok %v", seq, name, doc, dep, ok)
		}
	})
}

func TestParsePlanTable(t *testing.T) {
	readme := `# Epic

## Phase sequencing

| # | Phase | Doc | Repo area | Depends on | Parallel? | Est. |
|---|---|---|---|---|---|---|
| 1 | Schema | ` + "`phase-1-schema.md`" + ` | daemon | — | with 2 | 1 d |
| 2 | Parser | ` + "`phase-2-parser.md`" + ` | daemon | 1 | — | 1 d |
| 3 | UI | ` + "`phase-3-ui.md`" + ` | web | 1, 2 | — | 2 d |

**Critical path:** 1 → 2 → 3.
`
	phases := parsePlanTable(readme)
	if len(phases) != 3 {
		t.Fatalf("phases = %d, want 3", len(phases))
	}
	if phases[0].seq != 1 || phases[0].name != "Schema" || phases[0].docPath != "phase-1-schema.md" {
		t.Errorf("phase[0] = %+v", phases[0])
	}
	if phases[0].dependsOn != nil {
		t.Errorf("phase[0].dependsOn = %v, want nil (em-dash)", phases[0].dependsOn)
	}
	if !reflect.DeepEqual(phases[2].dependsOn, []int{1, 2}) {
		t.Errorf("phase[2].dependsOn = %v, want [1 2]", phases[2].dependsOn)
	}
}

func TestParsePlanTableNoTable(t *testing.T) {
	readme := "# Epic\n\nJust prose, no phase-sequencing table at all.\n"
	if got := parsePlanTable(readme); got != nil {
		t.Errorf("parsePlanTable(no table) = %v, want nil", got)
	}
}

// writePlan is a tiny fixture helper: writes files (name→content) into a fresh
// plan dir and returns its path.
func writePlan(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "plan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParsePlanWithTable(t *testing.T) {
	dir := writePlan(t, map[string]string{
		"README.md": "# Epic\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n" +
			"| 1 | Schema | `phase-1.md` | — |\n| 2 | UI | `phase-2.md` | 1 |\n",
		"phase-1.md": "# Phase 1 — Schema\n\n## Acceptance criteria\n- [x] a\n- [ ] b\n",
		"phase-2.md": "# Phase 2 — UI\n\nno checkboxes here\n",
	})
	phases := parsePlan(dir)
	if len(phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(phases))
	}
	// H1 title overrides the terse table label.
	if phases[0].name != "Phase 1 — Schema" {
		t.Errorf("phase[0].name = %q, want the doc H1", phases[0].name)
	}
	if phases[0].checkboxesDone != 1 || phases[0].checkboxesTotal != 2 {
		t.Errorf("phase[0] checkboxes = %d/%d, want 1/2", phases[0].checkboxesDone, phases[0].checkboxesTotal)
	}
	if phases[1].checkboxesTotal != 0 {
		t.Errorf("phase[1] checkboxes total = %d, want 0", phases[1].checkboxesTotal)
	}
	if !filepath.IsAbs(phases[0].docPath) {
		t.Errorf("phase[0].docPath = %q, want absolute", phases[0].docPath)
	}
}

func TestParsePlanFallbackNoTable(t *testing.T) {
	// No table in README → one phase per phase-*/step-* doc, seq by filename sort.
	dir := writePlan(t, map[string]string{
		"README.md":  "# Epic\n\nprose only, no table\n",
		"phase-2-b.md": "# Second\n- [x] done\n",
		"phase-1-a.md": "# First\n- [ ] todo\n- [x] done\n",
		"step-03-c.md": "# Third\nno boxes\n",
		"notes.md":     "# Not a phase doc — ignored\n- [x] x\n",
	})
	phases := parsePlan(dir)
	if len(phases) != 3 {
		t.Fatalf("phases = %d, want 3 (notes.md excluded)", len(phases))
	}
	// Sorted: phase-1-a, phase-2-b, step-03-c.
	if phases[0].name != "First" || phases[1].name != "Second" || phases[2].name != "Third" {
		t.Errorf("fallback order = %q/%q/%q", phases[0].name, phases[1].name, phases[2].name)
	}
	if phases[0].seq != 1 || phases[1].seq != 2 || phases[2].seq != 3 {
		t.Errorf("fallback seqs = %d/%d/%d", phases[0].seq, phases[1].seq, phases[2].seq)
	}
	if phases[0].checkboxesDone != 1 || phases[0].checkboxesTotal != 2 {
		t.Errorf("phase[0] checkboxes = %d/%d, want 1/2", phases[0].checkboxesDone, phases[0].checkboxesTotal)
	}
}

func TestParsePlanMissingDocFile(t *testing.T) {
	// A table row naming a doc that doesn't exist keeps its metadata, no counts,
	// docPath cleared to "".
	dir := writePlan(t, map[string]string{
		"README.md": "# Epic\n\n| # | Phase | Doc | Depends on |\n|---|---|---|---|\n" +
			"| 1 | Ghost | `phase-missing.md` | — |\n",
	})
	phases := parsePlan(dir)
	if len(phases) != 1 {
		t.Fatalf("phases = %d, want 1", len(phases))
	}
	if phases[0].name != "Ghost" {
		t.Errorf("phase[0].name = %q, want Ghost (table label kept)", phases[0].name)
	}
	if phases[0].docPath != "" {
		t.Errorf("phase[0].docPath = %q, want empty (unresolved)", phases[0].docPath)
	}
	if phases[0].checkboxesTotal != 0 {
		t.Errorf("phase[0] total = %d, want 0", phases[0].checkboxesTotal)
	}
}

func TestParsePlanEmptyDir(t *testing.T) {
	dir := writePlan(t, map[string]string{}) // no files at all
	if got := parsePlan(dir); len(got) != 0 {
		t.Errorf("parsePlan(empty) = %v, want none", got)
	}
}

// TestScanEpicsGammaFixture drives the full hash-gated scan over the committed
// gamma-task plan fixture (README table + 3 phase docs, one with 4 checkboxes
// half done, one fully done, one with zero checkboxes).
func TestScanEpicsGammaFixture(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	pinMtime(t)
	stats := scan(t, db)

	if stats.EpicPhases != 3 {
		t.Errorf("epic_phases = %d, want 3 (gamma plan)", stats.EpicPhases)
	}

	var taskID int64
	if err := db.QueryRow(`SELECT id FROM tasks WHERE external_id='2026-07-08-gamma-task'`).Scan(&taskID); err != nil {
		t.Fatalf("gamma-task row: %v", err)
	}

	rows, err := db.Query(`SELECT seq, name, depends_on, checkboxes_done, checkboxes_total
		FROM epic_phases WHERE workspace_task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type ph struct {
		seq         int
		name, deps  string
		done, total int
	}
	var got []ph
	for rows.Next() {
		var p ph
		if err := rows.Scan(&p.seq, &p.name, &p.deps, &p.done, &p.total); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	if len(got) != 3 {
		t.Fatalf("epic_phases rows = %d, want 3", len(got))
	}
	// Phase 1: H1 "Phase 1 — Schema + write API", 2/4 checkboxes, no deps.
	if got[0].seq != 1 || got[0].done != 2 || got[0].total != 4 || got[0].deps != "[]" {
		t.Errorf("phase 1 = %+v, want seq1 2/4 deps[]", got[0])
	}
	// Phase 2: fully done 3/3, deps [1].
	if got[1].seq != 2 || got[1].done != 3 || got[1].total != 3 || got[1].deps != "[1]" {
		t.Errorf("phase 2 = %+v, want seq2 3/3 deps[1]", got[1])
	}
	// Phase 3: zero checkboxes, deps [1,2].
	if got[2].seq != 3 || got[2].done != 0 || got[2].total != 0 || got[2].deps != "[1,2]" {
		t.Errorf("phase 3 = %+v, want seq3 0/0 deps[1,2]", got[2])
	}
}

// TestScanEpicsPreservesActivation checks that a rescan after an activation
// (activated_at + board task stamped) does NOT clear the activation state, even
// though applyEpics deletes+reinserts the phase rows.
func TestScanEpicsPreservesActivation(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	pinMtime(t)
	scan(t, db)

	var taskID int64
	if err := db.QueryRow(`SELECT id FROM tasks WHERE external_id='2026-07-08-gamma-task'`).Scan(&taskID); err != nil {
		t.Fatalf("gamma-task row: %v", err)
	}
	// Simulate an activation of phase seq=1.
	if _, err := db.Exec(`UPDATE epic_phases SET activated_at='2026-07-24T00:00:00Z',
		activated_board_task_id=999 WHERE workspace_task_id=? AND seq=1`, taskID); err != nil {
		t.Fatal(err)
	}
	// Force the gate to re-parse by clearing the stored plan hash, then rescan.
	if _, err := db.Exec(`DELETE FROM task_artifacts WHERE task_id=? AND kind='plan'`, taskID); err != nil {
		t.Fatal(err)
	}
	scan(t, db)

	var at string
	var boardID int64
	if err := db.QueryRow(`SELECT activated_at, activated_board_task_id
		FROM epic_phases WHERE workspace_task_id=? AND seq=1`, taskID).Scan(&at, &boardID); err != nil {
		t.Fatalf("phase 1 after rescan: %v", err)
	}
	if at != "2026-07-24T00:00:00Z" || boardID != 999 {
		t.Errorf("activation lost after rescan: at=%q board=%d", at, boardID)
	}
}
