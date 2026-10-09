package wsingest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var feedbackClock = time.Date(2026, 10, 9, 11, 40, 0, 0, time.UTC)

func writeFeedbackDoc(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "phase-4-return.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFeedbackDoc(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAppendOperatorFeedbackBeforeReport: the section lands right above the
// report heading, the note quoted line by line (a blank line as a bare `>`),
// with the untick instruction outside the quote, and the report itself is
// unchanged.
func TestAppendOperatorFeedbackBeforeReport(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 4\n\n## Acceptance Criteria\n- [x] a\n- [ ] b\n\n## Completion Report\n\nShipped a.\n")

	if err := AppendOperatorFeedback(p, "  The retry loop swallows the 409.\r\n\r\nFix it.  \n", feedbackClock); err != nil {
		t.Fatalf("AppendOperatorFeedback: %v", err)
	}
	want := "# Phase 4\n\n## Acceptance Criteria\n- [x] a\n- [ ] b\n\n" +
		"## Operator feedback (2026-10-09 11:40)\n\n" +
		"> The retry loop swallows the 409.\n>\n> Fix it.\n\n" +
		"Re-verify every ticked acceptance criterion this feedback touches; untick (`- [x]` → `- [ ]`) any that no longer holds before you start, and tick it again only when it is true.\n" +
		"\n## Completion Report\n\nShipped a.\n"
	if got := readFeedbackDoc(t, p); got != want {
		t.Errorf("doc =\n%s\nwant\n%s", got, want)
	}
	// The ticks are untouched: the doc still counts what the run left behind.
	if done, total := CountCheckboxes(readFeedbackDoc(t, p)); done != 1 || total != 2 {
		t.Errorf("checkboxes = %d/%d, want 1/2 — feedback must not edit a tick", done, total)
	}
	if got := ParseCompletionReport(readFeedbackDoc(t, p)); !strings.Contains(got, "Shipped a.") || strings.Contains(got, "Operator feedback") {
		t.Errorf("completion report = %q — the feedback must sit outside it", got)
	}
}

// TestAppendOperatorFeedbackAtEOF: no report heading ⇒ appended at the end.
func TestAppendOperatorFeedbackAtEOF(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 4\n\n- [x] a\n\n\n")
	if err := AppendOperatorFeedback(p, "Add the test.", feedbackClock); err != nil {
		t.Fatalf("AppendOperatorFeedback: %v", err)
	}
	got := readFeedbackDoc(t, p)
	if !strings.HasPrefix(got, "# Phase 4\n\n- [x] a\n\n## Operator feedback (2026-10-09 11:40)\n\n> Add the test.\n\n") {
		t.Errorf("doc =\n%s", got)
	}
	if !strings.HasSuffix(got, "tick it again only when it is true.\n") {
		t.Errorf("the section is not the end of the doc:\n%s", got)
	}
}

// TestAppendOperatorFeedbackIgnoresFencedHeading: a `## Completion Report`
// quoted inside a code block is not the doc's report.
func TestAppendOperatorFeedbackIgnoresFencedHeading(t *testing.T) {
	doc := "# Phase 4\n\nTemplate:\n```md\n## Completion Report\n```\n\n- [ ] a\n\n## Completion Report\n\nReal.\n"
	p := writeFeedbackDoc(t, doc)
	if err := AppendOperatorFeedback(p, "Note.", feedbackClock); err != nil {
		t.Fatalf("AppendOperatorFeedback: %v", err)
	}
	got := readFeedbackDoc(t, p)
	fb := strings.Index(got, "## Operator feedback")
	fence := strings.Index(got, "```md\n## Completion Report\n```")
	real := strings.LastIndex(got, "\n## Completion Report\n\nReal.")
	if fb < 0 || fence < 0 || real < 0 {
		t.Fatalf("doc lost a part:\n%s", got)
	}
	if !(fence < fb && fb < real) {
		t.Errorf("feedback at %d, want after the fenced heading (%d) and before the real one (%d):\n%s", fb, fence, real, got)
	}
}

// TestAppendOperatorFeedbackChronological: a second note lands BELOW the first
// and above the report — the sections read oldest first.
func TestAppendOperatorFeedbackChronological(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 4\n\n- [x] a\n\n## Completion Report\n\nDone.\n")
	if err := AppendOperatorFeedback(p, "First note.", feedbackClock); err != nil {
		t.Fatal(err)
	}
	if err := AppendOperatorFeedback(p, "Second note.", feedbackClock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := readFeedbackDoc(t, p)
	first := strings.Index(got, "## Operator feedback (2026-10-09 11:40)\n\n> First note.")
	second := strings.Index(got, "## Operator feedback (2026-10-09 12:40)\n\n> Second note.")
	report := strings.Index(got, "## Completion Report")
	if first < 0 || second < 0 || report < 0 {
		t.Fatalf("doc lost a section:\n%s", got)
	}
	if !(first < second && second < report) {
		t.Errorf("order first=%d second=%d report=%d, want first < second < report:\n%s", first, second, report, got)
	}
	if n := strings.Count(got, "## Completion Report"); n != 1 {
		t.Errorf("report heading appears %d times, want 1", n)
	}
}

// TestAppendOperatorFeedbackRefusals: empty and oversize notes are refused and
// the doc is not touched.
func TestAppendOperatorFeedbackRefusals(t *testing.T) {
	const body = "# Phase 4\n\n## Completion Report\n"
	p := writeFeedbackDoc(t, body)

	if err := AppendOperatorFeedback(p, " \n\t", feedbackClock); !errors.Is(err, ErrFeedbackEmpty) {
		t.Errorf("empty: err = %v, want ErrFeedbackEmpty", err)
	}
	if err := AppendOperatorFeedback(p, strings.Repeat("x", ReviewFeedbackMax+1), feedbackClock); !errors.Is(err, ErrFeedbackTooLarge) {
		t.Errorf(">20 KB: err = %v, want ErrFeedbackTooLarge", err)
	}
	if got := readFeedbackDoc(t, p); got != body {
		t.Errorf("a refused note changed the doc:\n%s", got)
	}
	if err := AppendOperatorFeedback(p, strings.Repeat("x", ReviewFeedbackMax), feedbackClock); err != nil {
		t.Errorf("exactly the limit: %v, want accepted", err)
	}
	if err := AppendOperatorFeedback(filepath.Join(t.TempDir(), "missing.md"), "x", feedbackClock); err == nil {
		t.Error("a missing doc: err = nil, want the read error")
	}
}

// TestAppendOperatorFeedbackKeepsMode: an existing doc keeps its permissions.
func TestAppendOperatorFeedbackKeepsMode(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 4\n")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendOperatorFeedback(p, "x", feedbackClock); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600 kept", st.Mode().Perm())
	}
	// Atomic: the write went through a temp file that is gone afterwards.
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir holds %v, want only the doc (no temp file left behind)", names)
	}
}

// TestAppendOperatorFeedbackCannotInjectStructure: feedback that quotes a report
// heading, a criterion and a code fence stays quoted text — the doc's report, its
// checkbox count and its fence-aware parsing are exactly what they were, and a
// later note still lands above the real report.
func TestAppendOperatorFeedbackCannotInjectStructure(t *testing.T) {
	const doc = "# Phase 4\n\n## Acceptance Criteria\n- [x] a\n- [ ] b\n\n## Completion Report\n\nShipped a.\n"
	p := writeFeedbackDoc(t, doc)
	wantReport := ParseCompletionReport(doc)
	wantDone, wantTotal := CountCheckboxes(doc)

	note := "Wrong.\n## Completion Report\n- [ ] x\n- [x] y\n```\nunclosed fence"
	if err := AppendOperatorFeedback(p, note, feedbackClock); err != nil {
		t.Fatalf("AppendOperatorFeedback: %v", err)
	}
	got := readFeedbackDoc(t, p)
	if !strings.Contains(got, "> ## Completion Report\n> - [ ] x\n> - [x] y\n> ```\n> unclosed fence\n") {
		t.Errorf("the note is not quoted line by line:\n%s", got)
	}
	if r := ParseCompletionReport(got); r != wantReport {
		t.Errorf("completion report = %q, want %q unchanged", r, wantReport)
	}
	if done, total := CountCheckboxes(got); done != wantDone || total != wantTotal {
		t.Errorf("checkboxes = %d/%d, want %d/%d unchanged", done, total, wantDone, wantTotal)
	}

	if err := AppendOperatorFeedback(p, "Second.", feedbackClock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = readFeedbackDoc(t, p)
	second := strings.Index(got, "> Second.")
	report := strings.Index(got, "\n## Completion Report\n\nShipped a.")
	if second < 0 || report < 0 || second > report {
		t.Errorf("second note at %d, real report at %d — want the note above the real report:\n%s", second, report, got)
	}
	if r := ParseCompletionReport(got); r != wantReport {
		t.Errorf("completion report after a second note = %q, want %q unchanged", r, wantReport)
	}
}
