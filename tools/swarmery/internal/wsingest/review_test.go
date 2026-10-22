package wsingest

import (
	"errors"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
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

// failBlock is a reviewer's FAIL block, as mdfence.ParseReviews returns it.
func failBlock(t *testing.T) *mdfence.Review {
	t.Helper()
	rs, errs := mdfence.ParseReviews("```yaml\nkind: review\nround: 7\nverdict: FAIL\nfindings:\n" +
		"  - id: R1-1\n    type: silent-failure\n    file: internal/x.go\n    line: 12\n" +
		"    criterion: \"errors: are reported\"\n    evidence: \"catch {} # swallows it\"\n    note: \"- [ ] not a criterion\\n## not a heading\"\n" +
		"  - type: quality\n    file: internal/y.go\nchecked_callers: [internal/z.go:3]\n```\n")
	if len(errs) != 0 || len(rs) != 1 {
		t.Fatalf("fixture block: %+v %+v", rs, errs)
	}
	return &rs[0]
}

// TestAppendReviewFindingsWritesParsableBlock: a FAIL round lands as one
// parsable kind: review block under a NEW `## Review` section placed before
// `## Forecast`; the daemon's round, verdict and source win; inert text stays inert.
func TestAppendReviewFindingsWritesParsableBlock(t *testing.T) {
	doc := "# Phase 6\n\n## Acceptance Criteria\n- [x] a\n\n## Forecast\n\n```yaml\nkind: prior\n```\n\n## Completion Report\n\nShipped a.\n"
	p := writeFeedbackDoc(t, doc)
	if err := AppendReviewFindings(p, ReviewBlock{Review: failBlock(t), Verdict: "FAIL", Prose: "ignored"}, 1, feedbackClock); err != nil {
		t.Fatalf("AppendReviewFindings: %v", err)
	}
	got := readFeedbackDoc(t, p)
	if !strings.HasPrefix(got, "# Phase 6\n\n## Acceptance Criteria\n- [x] a\n\n## Review\n\n```yaml\nkind: review\nround: 1\nverdict: FAIL\n") {
		t.Fatalf("section not created before ## Forecast:\n%s", got)
	}
	if !strings.HasSuffix(got, "```\n\n## Forecast\n\n```yaml\nkind: prior\n```\n\n## Completion Report\n\nShipped a.\n") {
		t.Fatalf("forecast / report disturbed:\n%s", got)
	}
	if strings.Contains(got, "ignored") {
		t.Error("the prose was written although the reviewer produced a block")
	}
	rs, errs := mdfence.ParseReviews(got)
	if len(errs) != 0 || len(rs) != 1 {
		t.Fatalf("written block does not parse: %+v %+v\n%s", rs, errs, got)
	}
	r := rs[0]
	if r.Round != "1" || r.Verdict != mdfence.ReviewFail || !r.Complete || r.Unparsable || r.Source != ReviewSourceDaemon ||
		r.WrittenAt != "2026-10-09T11:40:00Z" || len(r.CheckedCallers) != 1 {
		t.Errorf("block = %+v", r)
	}
	if len(r.Findings) != 2 || r.Findings[0].ID != "R1-1" || r.Findings[0].Criterion != "errors: are reported" ||
		r.Findings[0].Evidence != "catch {} # swallows it" || r.Findings[0].Line != 12 || r.Findings[1].ID != "R1-2" {
		t.Errorf("findings = %+v", r.Findings)
	}
	if done, total := CountCheckboxes(got); done != 1 || total != 1 {
		t.Errorf("checkboxes = %d/%d, want 1/1 — a finding's text must not become a criterion", done, total)
	}
}

// TestAppendReviewFindingsPassAndSecondRound: a PASS round is written too (zero
// findings, complete), after the earlier round inside the existing section, which
// is never rewritten.
func TestAppendReviewFindingsPassAndSecondRound(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 6\n\n- [x] a\n\n## Completion Report\n")
	if err := AppendReviewFindings(p, ReviewBlock{Review: failBlock(t), Verdict: "FAIL"}, 1, feedbackClock); err != nil {
		t.Fatal(err)
	}
	first := readFeedbackDoc(t, p)
	pass := &mdfence.Review{Round: "2", Verdict: mdfence.ReviewPass, Complete: true, Resolves: []string{"R1-1", "R1-2"}}
	if err := AppendReviewFindings(p, ReviewBlock{Review: pass, Verdict: "PASS"}, 2, feedbackClock); err != nil {
		t.Fatal(err)
	}
	got := readFeedbackDoc(t, p)
	head := first[:strings.Index(first, "## Completion Report")]
	if !strings.HasPrefix(got, strings.TrimRight(head, "\n")) {
		t.Errorf("round 1 was rewritten:\n%s", got)
	}
	if c := strings.Count(got, "## Review\n"); c != 1 {
		t.Errorf("%d ## Review sections, want 1:\n%s", c, got)
	}
	if !strings.HasSuffix(got, "```\n\n## Completion Report\n") {
		t.Errorf("report not kept last:\n%s", got)
	}
	rs, errs := mdfence.ParseReviews(got)
	if len(errs) != 0 || len(rs) != 2 {
		t.Fatalf("blocks = %+v %+v", rs, errs)
	}
	if l := mdfence.Latest(rs); l.Round != "2" || l.Verdict != mdfence.ReviewPass || len(l.Findings) != 0 || !l.Complete ||
		len(l.Resolves) != 2 {
		t.Errorf("latest = %+v", l)
	}
	if !strings.Contains(got, "findings: []") {
		t.Error("a PASS block must keep `findings: []`")
	}
}

// TestAppendReviewFindingsProseFallback: no readable block ⇒ a minimal block
// marked unparsable / incomplete, then the prose quoted; at EOF without a report.
func TestAppendReviewFindingsProseFallback(t *testing.T) {
	p := writeFeedbackDoc(t, "# Phase 6\n\n- [ ] a\n")
	if err := AppendReviewFindings(p, ReviewBlock{Verdict: "FAIL", Prose: "  \n "}, 1, feedbackClock); !errors.Is(err, ErrFindingsEmpty) {
		t.Fatalf("empty: err = %v, want ErrFindingsEmpty", err)
	}
	if got := readFeedbackDoc(t, p); got != "# Phase 6\n\n- [ ] a\n" {
		t.Fatalf("a refused append changed the doc:\n%s", got)
	}
	if err := AppendReviewFindings(p, ReviewBlock{Verdict: "FAIL", Prose: "P1 a.go:1 — wrong.\n- [ ] not a criterion\n\nVERDICT: FAIL"}, 1, feedbackClock); err != nil {
		t.Fatal(err)
	}
	got := readFeedbackDoc(t, p)
	if !strings.HasSuffix(got, "```\n\n> P1 a.go:1 — wrong.\n> - [ ] not a criterion\n>\n> VERDICT: FAIL\n") {
		t.Fatalf("prose not quoted after the block at the end:\n%s", got)
	}
	rs, errs := mdfence.ParseReviews(got)
	if len(errs) != 0 || len(rs) != 1 {
		t.Fatalf("fallback block = %+v %+v", rs, errs)
	}
	if r := rs[0]; !r.Unparsable || r.Complete || r.Verdict != mdfence.ReviewFail || len(r.ScopeLeft) != 1 || len(r.Findings) != 0 {
		t.Errorf("fallback = %+v, want unparsable, incomplete, FAIL", r)
	}
	if done, total := CountCheckboxes(got); done != 0 || total != 1 {
		t.Errorf("checkboxes = %d/%d, want 0/1", done, total)
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
	if err := AppendReviewFindings(p, ReviewBlock{Verdict: "FAIL", Prose: strings.Repeat("x", ReviewFindingsMax+500)}, 1, feedbackClock); err != nil {
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
