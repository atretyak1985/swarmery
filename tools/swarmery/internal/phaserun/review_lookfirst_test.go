package phaserun

import (
	"os"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/workaround"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// goldenLookFirst is the "Look first" section for lookFirstHits. Only the section
// is pinned — the preamble around it belongs to other contracts. A deliberate
// change deletes the file and reruns (the first run writes it).
const goldenLookFirst = "testdata/review_lookfirst.golden"

var lookFirstHits = workaround.PhaseHits{
	Total: 3,
	Hits: []workaround.Stored{
		{Word: "instead", Sentence: "That would have turned the continuation into a planned task instead of a mid-phase discovery.",
			Before: "A plan doc that knew the run teardown removes the worktree, so a returned branch always reaches the dirty-branch reclaim.",
			After:  "PHASE DONE"},
		{Word: "pre-existing", Sentence: "The gofmt hits are pre-existing."},
	},
}

func TestReviewPromptLookFirstGolden(t *testing.T) {
	got := lookFirstSection(lookFirstHits)
	want, err := os.ReadFile(goldenLookFirst)
	if os.IsNotExist(err) {
		if err := os.WriteFile(goldenLookFirst, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", goldenLookFirst)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("Look first section drifted from %s:\n--- got\n%s\n--- want\n%s", goldenLookFirst, got, want)
	}

	// In the prompt it sits after the phase document and before the diff.
	p := reviewPrompt("", "Phase 4 — X", "# doc", "DIFF STAT: one file", 7777, 1, lookFirstHits)
	doc := strings.Index(p, "# doc\n----------------------------------------")
	look := strings.Index(p, "Look first — the agent changed course here:")
	diff := strings.Index(p, "DIFF STAT: one file")
	if doc < 0 || look < doc || diff < look {
		t.Errorf("order doc=%d look=%d diff=%d — want doc < look first < diff", doc, look, diff)
	}
	// Without hits the prompt is the prompt it was before the section existed.
	if p0 := reviewPrompt("", "Phase 4 — X", "# doc", "DIFF", 7777, 1, workaround.PhaseHits{}); strings.Contains(p0, "Look first") ||
		!strings.Contains(p0, "----------------------------------------\n\nDIFF") {
		t.Errorf("a run without hits must not carry the section:\n%s", p0)
	}
}

func TestLookFirstSectionCapsTheList(t *testing.T) {
	var h workaround.PhaseHits
	for i := 0; i < lookFirstMax+5; i++ {
		h.Hits = append(h.Hits, workaround.Stored{Word: "skip", Sentence: "I skip it."})
	}
	h.Total = len(h.Hits) + 10
	s := lookFirstSection(h)
	if n := strings.Count(s, "\n- \"skip\""); n != lookFirstMax {
		t.Errorf("%d hits listed, want %d", n, lookFirstMax)
	}
	if !strings.Contains(s, "- … and 15 more on the phase card.") {
		t.Errorf("the overflow line is missing:\n%s", s)
	}
}

// The review stage reads the RUN session's hits from the store and hands them to
// the reviewer; another session's hits on the same phase stay out.
func TestReviewFeedsLookFirstHits(t *testing.T) {
	s, db, p1, info := reviewFixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_session_uuid = 'run-1' WHERE id = ?`, p1)
	ins := `INSERT INTO workaround_hits (session_uuid, turn_id, phase_id, word, sentence, sentence_hash, context_before, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '2026-10-22T00:00:00Z')`
	mustExec(t, db, ins, "run-1", 11, p1, "instead", "I checked the branch out instead of re-cutting it.", "h1", "The reclaim refused it.")
	mustExec(t, db, ins, "older-run", 12, p1, "flaky", "An older run called it flaky.", "h2", "")
	// A PASS with its kind: review block: without one it would be review-incomplete (D20).
	rev := &fakeReviewer{outputs: []string{reviewerOutput("PASS", "[]")}}
	s.Review = rev
	dir, base := gitRepo(t)

	if verdict, _ := s.reviewRun(p1, info, worktree.Acquired{Path: dir, Branch: "swarm/x", StartPoint: base}, "done", GuardResult{}); verdict != "pass" {
		t.Fatalf("verdict = %q", verdict)
	}
	calls := rev.calls()
	if len(calls) != 1 {
		t.Fatalf("%d reviewer calls", len(calls))
	}
	p := calls[0].Prompt
	for _, want := range []string{
		"Look first — the agent changed course here:",
		`- "instead" — I checked the branch out instead of re-cutting it.`,
		"    before: The reclaim refused it.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "older run called it flaky") {
		t.Error("another session's hit leaked into this run's review")
	}
}
