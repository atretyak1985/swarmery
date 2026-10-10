package wsingest

import (
	"errors"
	"strings"
	"testing"
)

func TestParseDocReview(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want bool
	}{
		{"on", "# P\n**Review:** on\n", true},
		{"yes", "# P\n**Review:** yes\n", true},
		{"true, mixed case and padding", "# P\n  **review:**   TRUE  \n", true},
		{"off", "# P\n**Review:** off\n", false},
		{"no", "# P\n**Review:** no\n", false},
		{"none", "# P\n**Review:** none\n", false},
		{"unrecognized is off", "# P\n**Review:** maybe\n", false},
		{"absent is off", "# P\n**Verify:** strict\n", false},
		{"below the first section is not the header", "# P\n\n## Agent prompt\n**Review:** on\n", false},
		{"beside Verify", "# P\nStatus: Pending\n**Verify:** normal\n**Review:** on\n\n## Goal\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseDocReview(c.doc); got != c.want {
				t.Errorf("ParseDocReview = %v, want %v", got, c.want)
			}
		})
	}
	// Past the header bound a line is a quote, not a declaration.
	deep := "# P\n" + strings.Repeat("text\n", docStatusHeaderLines) + "**Review:** on\n"
	if ParseDocReview(deep) {
		t.Error("a **Review:** line past the header bound was read as the doc's own")
	}
}

// TestAppendReviewFindingsBeforeReport: same place and shape as operator feedback —
// above the report, findings quoted (so their checkboxes and headings stay inert),
// the instruction outside the quote, the report untouched.
func TestAppendReviewFindingsBeforeReport(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 6\n\n## Acceptance Criteria\n- [x] a\n\n## Completion Report\n\nShipped a.\n")
	findings := "P0 internal/x.go:12 — the error is swallowed.\n- [ ] not a criterion\n## Not a heading\n\nVERDICT: FAIL"

	if err := AppendReviewFindings(p, findings, feedbackClock); err != nil {
		t.Fatalf("AppendReviewFindings: %v", err)
	}
	got := readFeedbackDoc(t, p)
	want := "# Phase 6\n\n## Acceptance Criteria\n- [x] a\n\n" +
		"## Review findings (2026-10-09 11:40)\n\n" +
		"> P0 internal/x.go:12 — the error is swallowed.\n> - [ ] not a criterion\n> ## Not a heading\n>\n> VERDICT: FAIL\n\n" +
		reviewFindingsInstruction + "\n\n" +
		"## Completion Report\n\nShipped a.\n"
	if got != want {
		t.Fatalf("doc =\n%s\nwant\n%s", got, want)
	}
	if done, total := CountCheckboxes(got); done != 1 || total != 1 {
		t.Errorf("checkboxes = %d/%d, want 1/1 — a quoted finding must not become a criterion", done, total)
	}
}

func TestAppendReviewFindingsAtEOFAndRefusal(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 6\n\n- [ ] a\n")
	if err := AppendReviewFindings(p, "  \n ", feedbackClock); !errors.Is(err, ErrFindingsEmpty) {
		t.Fatalf("empty findings: err = %v, want ErrFindingsEmpty", err)
	}
	if got := readFeedbackDoc(t, p); got != "# Phase 6\n\n- [ ] a\n" {
		t.Fatalf("a refused append changed the doc:\n%s", got)
	}
	if err := AppendReviewFindings(p, "P1 a.go:1 — wrong.", feedbackClock); err != nil {
		t.Fatal(err)
	}
	if got := readFeedbackDoc(t, p); !strings.HasSuffix(got, "## Review findings (2026-10-09 11:40)\n\n> P1 a.go:1 — wrong.\n\n"+reviewFindingsInstruction+"\n") {
		t.Fatalf("doc without a report: section not appended at the end:\n%s", got)
	}
}

// review_mode is DOC-owned (re-derived on every scan, back to off when the line
// goes), and review_fix_round — the daemon's counter beside it — survives a rescan.
func TestApplyEpics_ReviewModeIsDocOwnedFixRoundIsNot(t *testing.T) {
	db := carryFixture(t)
	p := phase(1, "Phase 1", "/plan/p1.md")
	p.reviewMode = ReviewOn
	applyPhases(t, db, []epicPhase{p})
	mustExec(t, db, `UPDATE epic_phases SET review_fix_round=1 WHERE doc_path='/plan/p1.md'`)

	read := func() (mode string, round int) {
		t.Helper()
		if err := db.QueryRow(`SELECT review_mode, review_fix_round FROM epic_phases WHERE doc_path='/plan/p1.md'`).
			Scan(&mode, &round); err != nil {
			t.Fatal(err)
		}
		return mode, round
	}
	if mode, _ := read(); mode != ReviewOn {
		t.Fatalf("review_mode = %q, want on", mode)
	}
	applyPhases(t, db, []epicPhase{phase(1, "Phase 1", "/plan/p1.md")})
	mode, round := read()
	if mode != ReviewOff {
		t.Errorf("review_mode = %q, want off after the doc stopped asking", mode)
	}
	if round != 1 {
		t.Errorf("review_fix_round = %d after a rescan, want 1 — it is daemon-owned", round)
	}
}

func TestAppendReviewFindingsTruncates(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 6\n")
	if err := AppendReviewFindings(p, strings.Repeat("x", ReviewFindingsMax+500), feedbackClock); err != nil {
		t.Fatal(err)
	}
	got := readFeedbackDoc(t, p)
	if !strings.Contains(got, "findings truncated") {
		t.Error("an over-long findings block was not marked as truncated")
	}
	if len(got) > ReviewFindingsMax+1024 {
		t.Errorf("doc grew to %d bytes, want the findings capped near %d", len(got), ReviewFindingsMax)
	}
}
