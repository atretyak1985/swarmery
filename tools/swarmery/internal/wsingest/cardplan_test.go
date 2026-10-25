package wsingest

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskdir"
)

const cardPlanTestID = 70

// seedCardWithMicroPlan mints a REAL micro-plan (taskdir is the only producer of
// these docs in production) and a board card joined to it through
// tasks.workspace_dir. Returns the DB and the card's phase doc path.
func seedCardWithMicroPlan(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db := testDB(t)
	mustExec(t, db, `INSERT INTO projects (id, path, slug, first_seen)
		VALUES (1, '/repo/p', 'p', '2026-01-01T00:00:00Z')`)
	dir, err := taskdir.MintMicroPlan(t.TempDir(), "p", taskdir.Card{
		ExternalID: "T-cp", Title: "Fix the sweep", Prompt: "The sweep is wrong.",
	}, time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("MintMicroPlan: %v", err)
	}
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, status, created_at, source,
		external_id, board_column, workspace_dir)
		VALUES (?, 1, 'Fix the sweep', 'p', 'done', '2026-10-25T00:00:00Z', 'queue', 'T-cp', 'done', ?)`,
		cardPlanTestID, dir)
	return db, taskdir.PhaseDocPath(dir)
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCardMicroPlanDoc(t *testing.T) {
	db, doc := seedCardWithMicroPlan(t)
	if got, err := CardMicroPlanDoc(db, cardPlanTestID); err != nil || got != doc {
		t.Errorf("CardMicroPlanDoc = %q, %v; want %q", got, err, doc)
	}
	// Unknown card, card without a workspace_dir, and a dir whose doc is gone are
	// all "no micro-plan", never an error.
	if got, err := CardMicroPlanDoc(db, 9999); err != nil || got != "" {
		t.Errorf("unknown card = %q, %v; want \"\", nil", got, err)
	}
	mustExec(t, db, `INSERT INTO tasks (id, project_id, title, prompt, status, created_at, source, external_id)
		VALUES (71, 1, 'plain', 'p', 'done', '2026-10-25T00:00:00Z', 'queue', 'T-plain')`)
	if got, err := CardMicroPlanDoc(db, 71); err != nil || got != "" {
		t.Errorf("card without workspace_dir = %q, %v; want \"\", nil", got, err)
	}
	if err := os.Remove(doc); err != nil {
		t.Fatal(err)
	}
	if got, err := CardMicroPlanDoc(db, cardPlanTestID); err != nil || got != "" {
		t.Errorf("missing doc = %q, %v; want \"\", nil", got, err)
	}
}

// The regression for "a card cannot close without a commit": the micro-plan reads
// Done once its report carries evidence, and not a moment before.
func TestTickCardMicroPlan_TicksOnlyWithAReport(t *testing.T) {
	db, doc := seedCardWithMicroPlan(t)

	// Blank report: a drag to done proves nothing, so nothing is ticked.
	if n, err := TickCardMicroPlan(db, cardPlanTestID); err != nil || n != 0 {
		t.Fatalf("blank report tick = %d, %v; want 0, nil", n, err)
	}
	if done, total := CountCheckboxes(readDoc(t, doc)); done != 0 || total != 2 {
		t.Fatalf("after blank-report tick: %d/%d, want 0/2", done, total)
	}

	if err := AppendCompletionReport(doc, "**Outcome: closed without a commit — already_fixed.**\n\nHEAD has it."); err != nil {
		t.Fatalf("AppendCompletionReport: %v", err)
	}
	n, err := TickCardMicroPlan(db, cardPlanTestID)
	if err != nil || n != 2 {
		t.Fatalf("tick with report = %d, %v; want 2, nil", n, err)
	}
	if done, total := CountCheckboxes(readDoc(t, doc)); done != 2 || total != 2 {
		t.Errorf("after tick: %d/%d, want 2/2", done, total)
	}
	// Second call is a no-op.
	if n, err := TickCardMicroPlan(db, cardPlanTestID); err != nil || n != 0 {
		t.Errorf("second tick = %d, %v; want 0, nil", n, err)
	}
	// A card with no micro-plan is a no-op, not an error.
	if n, err := TickCardMicroPlan(db, 9999); err != nil || n != 0 {
		t.Errorf("unlinked card tick = %d, %v; want 0, nil", n, err)
	}
}

func TestCompletionReportBody(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"absent", "# P\n\n## Objective\n\nx\n", ""},
		{"blank at end", "# P\n\n## Completion Report\n", ""},
		{"blank before next section", "## Completion Report\n\n\n## Notes\n\nn\n", ""},
		{"content to EOF", "## Completion Report\n\nShipped.\n", "Shipped."},
		{"stops at next h2", "## Completion Report\n\nShipped.\n\n## Notes\n\nn\n", "Shipped."},
		{"keeps h3", "## Completion Report\n\nA\n\n### Sub\n\nB\n", "A\n\n### Sub\n\nB"},
		{
			// A doc quoting the heading inside a fenced prompt EARLIER is judged by
			// its real, last, unfenced section — and the fenced quote's "content"
			// must not make a blank report read as written.
			name: "fenced quote earlier is ignored",
			in: "# P\n\n## Agent prompt\n\n```markdown\n## Completion Report\n\nquoted template text\n```\n\n" +
				"## Acceptance criteria\n\n- [ ] a\n\n## Completion Report\n",
			want: "",
		},
		{
			// The objective quotes a card prompt verbatim, which can carry the heading
			// unfenced. LAST wins.
			name: "unfenced earlier mention loses to the last one",
			in:   "## Objective\n\n## Completion Report\n\nprompt text\n\n## Completion Report\n\nreal report\n",
			want: "real report",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := completionReportBody(c.in); got != c.want {
				t.Errorf("completionReportBody = %q, want %q", got, c.want)
			}
		})
	}
}

// End to end on a doc whose earlier fenced block quotes the heading with content:
// the tick must refuse while the real report is blank.
func TestTickCardMicroPlan_FencedQuoteDoesNotCountAsAReport(t *testing.T) {
	db, doc := seedCardWithMicroPlan(t)
	quoted := strings.Replace(readDoc(t, doc), "## Acceptance criteria",
		"## Agent prompt\n\n```markdown\n## Completion Report\n\nwrite your report here\n```\n\n## Acceptance criteria", 1)
	if err := os.WriteFile(doc, []byte(quoted), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := TickCardMicroPlan(db, cardPlanTestID); err != nil || n != 0 {
		t.Fatalf("tick over a fenced quote = %d, %v; want 0, nil", n, err)
	}
	if err := AppendCompletionReport(doc, "real evidence"); err != nil {
		t.Fatal(err)
	}
	got := readDoc(t, doc)
	if !strings.Contains(got, "```markdown\n## Completion Report\n\nwrite your report here\n```") {
		t.Errorf("the fenced quote was rewritten:\n%s", got)
	}
	if !strings.HasSuffix(got, "## Completion Report\n\nreal evidence\n") {
		t.Errorf("evidence did not land in the real (last) section:\n%s", got)
	}
	if n, err := TickCardMicroPlan(db, cardPlanTestID); err != nil || n != 2 {
		t.Errorf("tick after evidence = %d, %v; want 2, nil", n, err)
	}
}

func TestAppendCompletionReport(t *testing.T) {
	_, doc := seedCardWithMicroPlan(t)

	// Blank report: the body becomes the report.
	if err := AppendCompletionReport(doc, "first evidence"); err != nil {
		t.Fatal(err)
	}
	if got := completionReportBody(readDoc(t, doc)); got != "first evidence" {
		t.Fatalf("report = %q, want the body", got)
	}

	// Idempotent: the same body twice leaves the file byte-identical.
	before := readDoc(t, doc)
	if err := AppendCompletionReport(doc, "first evidence"); err != nil {
		t.Fatal(err)
	}
	if after := readDoc(t, doc); after != before {
		t.Errorf("a repeated body changed the doc:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// Existing content is kept; a new body goes under the sub-heading.
	if err := AppendCompletionReport(doc, "second evidence"); err != nil {
		t.Fatal(err)
	}
	want := "first evidence\n\n### Closed without a commit\n\nsecond evidence"
	if got := completionReportBody(readDoc(t, doc)); got != want {
		t.Errorf("report = %q, want %q", got, want)
	}
	// And that append is idempotent too.
	before = readDoc(t, doc)
	if err := AppendCompletionReport(doc, "second evidence"); err != nil {
		t.Fatal(err)
	}
	if after := readDoc(t, doc); after != before {
		t.Errorf("a repeated appended body changed the doc:\n%s", after)
	}

	// Blank body is a no-op.
	if err := AppendCompletionReport(doc, "  \n"); err != nil {
		t.Fatal(err)
	}
	if after := readDoc(t, doc); after != before {
		t.Errorf("a blank body changed the doc:\n%s", after)
	}
	// The criteria above the report survive untouched.
	if done, total := CountCheckboxes(readDoc(t, doc)); done != 0 || total != 2 {
		t.Errorf("checkboxes = %d/%d, want 0/2 (append must not tick)", done, total)
	}
}

func TestAppendCompletionReport_KeepsSectionsAfterTheReport(t *testing.T) {
	in := "# P\n\n- [ ] a\n\n## Completion Report\n\n## Notes\n\nkeep me\n"
	got, changed := appendCompletionReport(in, "ev")
	if !changed {
		t.Fatal("want changed")
	}
	want := "# P\n\n- [ ] a\n\n## Completion Report\n\nev\n\n## Notes\n\nkeep me\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	// A doc with no report section gains one at the end.
	got, changed = appendCompletionReport("# P\n\n- [ ] a\n", "ev")
	if !changed || got != "# P\n\n- [ ] a\n\n## Completion Report\n\nev\n" {
		t.Errorf("no-section append = %q, %v", got, changed)
	}
}

func TestCompletionReportBlank(t *testing.T) {
	_, doc := seedCardWithMicroPlan(t)
	if blank, err := CompletionReportBlank(doc); err != nil || !blank {
		t.Errorf("fresh micro-plan blank = %v, %v; want true, nil", blank, err)
	}
	if err := AppendCompletionReport(doc, "x"); err != nil {
		t.Fatal(err)
	}
	if blank, err := CompletionReportBlank(doc); err != nil || blank {
		t.Errorf("after append blank = %v, %v; want false, nil", blank, err)
	}
	if _, err := CompletionReportBlank(doc + ".missing"); err == nil {
		t.Error("missing doc: want an error")
	}
}
