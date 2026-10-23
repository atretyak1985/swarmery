package phaserun

import (
	"os"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// goldenCheckFirst pins the review prompt from the "Look first" section to the
// diff: the workaround hits, then the «Check first» lessons, then the diff. A
// deliberate change deletes the file and reruns (the first run writes it).
const goldenCheckFirst = "testdata/review_checkfirst.golden"

// checkFirstText is lessons.ReviewerSection's output for two lessons (the lessons
// package pins that rendering itself; it imports phaserun, so this test cannot
// import it back).
const checkFirstText = "Check first — accepted lessons for the areas this phase works in (each names a mistake that recurred here; look for it in the diff first, and cite the id when a finding confirms it):\n" +
	"- [L-7] Single-flight every cold read of the /vcs cache in internal/api.\n" +
	"- [L-3] Redact a tool's output before truncating it in internal/repoprovider.\n\n"

func TestReviewPromptCheckFirstGolden(t *testing.T) {
	p := reviewPrompt("", "Phase 4 — X", "# doc", "DIFF STAT: one file", 7777, 1, lookFirstHits, checkFirstText)
	look := strings.Index(p, "Look first — the agent changed course here:")
	check := strings.Index(p, "Check first — accepted lessons")
	diff := strings.Index(p, "DIFF STAT: one file")
	if look < 0 || check < look || diff < check {
		t.Fatalf("order look=%d check=%d diff=%d — want look first < check first < diff", look, check, diff)
	}
	got := p[look:]
	want, err := os.ReadFile(goldenCheckFirst)
	if os.IsNotExist(err) {
		if err := os.WriteFile(goldenCheckFirst, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", goldenCheckFirst)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("review prompt drifted from %s:\n--- got\n%s\n--- want\n%s", goldenCheckFirst, got, want)
	}
}

// Without lessons the prompt is byte-identical to the one before the section existed.
func TestReviewPromptWithoutCheckFirstIsUnchanged(t *testing.T) {
	with := reviewPrompt("", "Phase 4 — X", "# doc", "DIFF", 7777, 1, lookFirstHits, "")
	if strings.Contains(with, "Check first") || !strings.Contains(with, "on the phase card.\n\nDIFF") {
		t.Errorf("an empty check-first list must add nothing:\n%s", with)
	}
}

// The review stage asks CheckFirst for the phase under review and puts its text
// after the run's workaround hits; every recorded review tells ReviewRecorded.
func TestReviewFeedsCheckFirstAndReportsTheRecord(t *testing.T) {
	s, db, p1, info := reviewFixture(t)
	mustExec(t, db, `UPDATE epic_phases SET run_session_uuid = 'run-1' WHERE id = ?`, p1)
	mustExec(t, db, `INSERT INTO workaround_hits (session_uuid, turn_id, phase_id, word, sentence, sentence_hash, created_at)
		VALUES ('run-1', 11, ?, 'instead', 'I re-cut the branch instead.', 'h1', '2026-10-22T00:00:00Z')`, p1)
	var asked []int64
	s.CheckFirst = func(phaseID int64) string {
		asked = append(asked, phaseID)
		return checkFirstText
	}
	var recorded []int64
	s.ReviewRecorded = func(phaseID int64) { recorded = append(recorded, phaseID) }
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
	prompt := calls[0].Prompt
	look, check := strings.Index(prompt, "Look first —"), strings.Index(prompt, "- [L-7] Single-flight")
	if look < 0 || check < look {
		t.Errorf("check-first lessons missing or before the workaround hits (look=%d check=%d)", look, check)
	}
	if len(asked) != 1 || asked[0] != p1 {
		t.Errorf("CheckFirst asked for %v, want [%d]", asked, p1)
	}
	if len(recorded) != 1 || recorded[0] != p1 {
		t.Errorf("ReviewRecorded told %v, want [%d]", recorded, p1)
	}
}

// A panicking hook costs the section, never the review.
func TestCheckFirstHookPanicIsContained(t *testing.T) {
	s := &Service{CheckFirst: func(int64) string { panic("boom") }, ReviewRecorded: func(int64) { panic("boom") }}
	if got := s.checkFirst(1); got != "" {
		t.Errorf("checkFirst after a panic = %q", got)
	}
	s.reviewRecorded(1) // must not panic
}
