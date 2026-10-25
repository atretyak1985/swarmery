package taskdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var dispatchedAt = time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)

func testCard() Card {
	return Card{
		ExternalID: "T-42",
		Title:      "Fix the janitor sweep",
		Prompt:     "The janitor sweeps foreign dirs.\nMake it skip dirs it does not own.",
		RepoPath:   "/repo/p",
	}
}

func mint(t *testing.T, card Card) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	dir, err := MintMicroPlan(root, "swarmery", card, dispatchedAt)
	if err != nil {
		t.Fatalf("MintMicroPlan: %v", err)
	}
	return root, dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// The path shape is a contract, not an implementation detail: wsingest derives the
// workspace task's external_id from the date segments and the leaf dir name, so a
// change here renames rows.
func TestMintMicroPlan_PathShape(t *testing.T) {
	root, dir := mint(t, testCard())
	want := filepath.Join(root, "swarmery", "workspace", "working", "2026", "08", "17", "card-t-42")
	if dir != want {
		t.Errorf("dir = %q\nwant %q", dir, want)
	}
	for _, rel := range []string{"README.md", "plan/README.md", "plan/" + PhaseDocName} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
}

// Two generic checkboxes, and the Completion Report LAST — the dashboard's phase
// Summary tab renders that section and nothing else, so a doc without it shows "no
// summary of the work written" over work that shipped.
func TestMintMicroPlan_PhaseDocContract(t *testing.T) {
	_, dir := mint(t, testCard())
	doc := read(t, PhaseDocPath(dir))

	if n := strings.Count(doc, "\n- [ ] "); n != 2 {
		t.Errorf("unticked checkboxes = %d, want 2\n%s", n, doc)
	}
	if !strings.Contains(doc, "swarm/T-42") {
		t.Error("the doc does not name the branch the work must land on")
	}
	if !strings.Contains(doc, "Swarm-Task-Id: T-42") {
		t.Error("the doc does not name the commit trailer that makes the work attributable")
	}
	// Criterion 1 must admit the no-change outcome (already fixed, not a defect, a
	// duplicate) backed by written evidence — otherwise a card that honestly needed
	// no commit can never read Done.
	wantCriterion := "- [ ] Either the task described above is implemented and committed on `swarm/T-42` " +
		"with the `Swarm-Task-Id: T-42` trailer, or the card is closed as no change needed " +
		"(already fixed, not a defect, or a duplicate) with the evidence written in the " +
		"Completion Report below.\n"
	if !strings.Contains(doc, wantCriterion) {
		t.Errorf("criterion 1 does not carry the no-change arm:\n%s", doc)
	}
	if !strings.Contains(doc, "- [ ] Verification verdict is `pass`, or a reviewer explicitly accepted the card "+
		"from the Review lane.\n") {
		t.Errorf("criterion 2 changed:\n%s", doc)
	}
	// The card's whole prompt, verbatim — a truncated objective is a different task.
	if !strings.Contains(doc, "Make it skip dirs it does not own.") {
		t.Errorf("the prompt's second line was dropped:\n%s", doc)
	}
	if !strings.HasSuffix(strings.TrimSpace(doc), "## Completion Report") {
		t.Errorf("## Completion Report is not the last section:\n%s", doc)
	}
}

// Idempotent by directory: a re-dispatch — or verify's fix task, which carries the
// ROOT card's external id on purpose — must not regenerate a doc that already
// carries ticks and a report.
func TestMintMicroPlan_NeverRewritesAnExistingDir(t *testing.T) {
	root, dir := mint(t, testCard())
	doc := PhaseDocPath(dir)
	worked := read(t, doc) + "\nThe executor wrote this.\n"
	if err := os.WriteFile(doc, []byte(strings.Replace(worked, "- [ ] Either the task", "- [x] Either the task", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	again, err := MintMicroPlan(root, "swarmery", testCard(), dispatchedAt)
	if err != nil {
		t.Fatalf("second MintMicroPlan: %v", err)
	}
	if again != dir {
		t.Errorf("second mint returned %q, want the same dir %q", again, dir)
	}
	after := read(t, doc)
	if !strings.Contains(after, "- [x] Either the task") || !strings.Contains(after, "The executor wrote this.") {
		t.Errorf("a re-mint erased the run's record:\n%s", after)
	}
}

// External ids reach a path segment AND a derived external_id, so they are
// sanitized rather than trusted — including the traversal attempt, which must not
// be able to place a task dir outside the workspace.
func TestSlug_Sanitizes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"T-42", "card-t-42"},
		{"Verify-Fix-3", "card-verify-fix-3"},
		{"weird id/with spaces", "card-weird-id-with-spaces"},
		{"../../escape", "card-escape"},
		{"", "card-unnamed"},
	} {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMintMicroPlan_RejectsMissingInputs(t *testing.T) {
	if _, err := MintMicroPlan("", "p", testCard(), dispatchedAt); err == nil {
		t.Error("minted with no workspace root")
	}
	if _, err := MintMicroPlan(t.TempDir(), "", testCard(), dispatchedAt); err == nil {
		t.Error("minted with no project")
	}
	if _, err := MintMicroPlan(t.TempDir(), "p", Card{Title: "x"}, dispatchedAt); err == nil {
		t.Error("minted a card with no external id — nothing could ever join it back")
	}
}

// A card with no prompt and no title still has to produce a parseable tree: the
// board accepts sparse cards, and a mint that failed on one would take the run down
// with it (mint failure is non-fatal, but a broken tree would linger).
func TestMintMicroPlan_ToleratesASparseCard(t *testing.T) {
	_, dir := mint(t, Card{ExternalID: "T-9"})
	if got := read(t, filepath.Join(dir, "README.md")); !strings.Contains(got, "# T-9") {
		t.Errorf("card README has no usable title:\n%s", got)
	}
	if got := read(t, PhaseDocPath(dir)); !strings.Contains(got, "(the card carried no prompt)") {
		t.Errorf("phase doc does not say the prompt was empty:\n%s", got)
	}
}

// A pipe in a card title would split the sequencing table's cell and make the row
// unparseable — the phase would silently vanish from the plan.
func TestMintMicroPlan_EscapesTableCells(t *testing.T) {
	_, dir := mint(t, Card{ExternalID: "T-7", Title: "fix a|b routing", Prompt: "p"})
	readme := read(t, filepath.Join(dir, "plan", "README.md"))
	if !strings.Contains(readme, `| 1 | fix a\|b routing | `) {
		t.Errorf("table cell was not escaped:\n%s", readme)
	}
}

// A multi-line prompt must not break the single-line fields it feeds.
func TestMintMicroPlan_KeepsHeadingFieldsOnOneLine(t *testing.T) {
	_, dir := mint(t, Card{
		ExternalID: "T-8",
		Title:      "title\nwith a newline",
		Prompt:     "first line\nsecond line",
	})
	for _, path := range []string{
		filepath.Join(dir, "README.md"),
		filepath.Join(dir, "plan", "README.md"),
	} {
		body := read(t, path)
		if !strings.Contains(body, "# title with a newline") {
			t.Errorf("%s: heading was not collapsed to one line:\n%s", path, body)
		}
	}
	if body := read(t, filepath.Join(dir, "README.md")); !strings.Contains(body, "- **Goal**: first line\n") {
		t.Errorf("goal field is not one line:\n%s", body)
	}
}
