package phaserun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// round1 is the round-1 review the round-2 tests start from: two findings and
// one area the reviewer did not read.
var round1 = mdfence.Review{
	Round: "1", Verdict: mdfence.ReviewFail, Complete: true, ScopeLeft: []string{"web/src/export"},
	Findings: []mdfence.Finding{
		{ID: "R1-1", Type: "silent-failure", File: "src/lib/export/writer.go", Line: 84, Evidence: "catch {} swallows the putObject error"},
		{ID: "R1-2", Type: "plan-conformance", File: "src/lib/export/csv.go", Line: 12, Criterion: "the CSV starts with its header",
			Evidence: "rows are written before the header"},
	},
}

// writeRound1 appends round1 to the phase doc, as the daemon's first review did.
func writeRound1(t *testing.T, docPath string) {
	t.Helper()
	r := round1
	if err := wsingest.AppendReviewFindings(docPath, wsingest.ReviewBlock{Review: &r, Verdict: "FAIL"}, 1,
		time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The round-2 brief carries the round-1 findings, its scope_left and the diff
// since the tree round 1 reviewed — not the phase doc, not the run's own change.
func TestRound2BriefIsFindingsScopeAndNewDiff(t *testing.T) {
	s, db, p1, info := reviewFixture(t)
	writeRound1(t, info.DocPath)
	dir, base := gitRepo(t)
	r1Tree := gitIn(t, dir, "rev-parse", "HEAD^{tree}")
	mustExec(t, db, `INSERT INTO phase_reviews (scope, phase_id, workspace_task_id, tree_before, tree_after, verdict, started_at)
		VALUES ('phase', ?, '1', ?, ?, 'fail', '2026-10-22T12:00:00Z')`, p1, r1Tree, r1Tree)
	if err := os.WriteFile(filepath.Join(dir, "fix.go"), []byte("package main\n\n// the review fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "review fix")
	now := gitIn(t, dir, "rev-parse", "HEAD^{tree}")

	brief := s.buildRound2Brief(p1, info, worktree.Acquired{Path: dir, StartPoint: base}, 1, now)
	if brief.fullDiff {
		t.Fatal("the brief fell back to the whole diff although round 1's tree is known")
	}
	prompt := round2Prompt("", info.Name, brief, 0, 2)
	for _, want := range []string{"REVIEW ROUND: 2\n", "id: R1-1", "id: R1-2", "rows are written before the header",
		"- web/src/export", "fix.go", "// the review fix", "write round: 2"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the round-2 prompt lacks %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"PHASE DOCUMENT:", "func main() {}", "main.go"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("the round-2 prompt carries %q — it must hold only the findings, scope_left and the new diff:\n%s", unwanted, prompt)
		}
	}

	// No tree_after for round 1 ⇒ the run's whole diff, flagged round2-full-diff.
	mustExec(t, db, `DELETE FROM phase_reviews`)
	brief = s.buildRound2Brief(p1, info, worktree.Acquired{Path: dir, StartPoint: base}, 1, now)
	if !brief.fullDiff || !strings.Contains(brief.diff, "func main() {}") {
		t.Errorf("without round 1's tree the brief = %+v, want the run's whole diff", brief)
	}
}

// The projection is read first: a round the scan already projected gives its
// open findings from review_findings (a resolved one is left out).
func TestRound2BriefReadsReviewFindings(t *testing.T) {
	s, db, _, info := reviewFixture(t)
	mustExec(t, db, `INSERT INTO review_blocks (workspace_task_id, doc_path, round, verdict, scope_left_json, written_at)
		VALUES (1, ?, '1', 'FAIL', '["api/"]', '2026-10-22T12:00:00Z')`, info.DocPath)
	for _, f := range []struct{ id, resolved string }{{"R1-1", ""}, {"R1-2", "2"}} {
		mustExec(t, db, `INSERT INTO review_findings (workspace_task_id, doc_path, round, finding_id, type, file, line,
			evidence, resolved_by, created_at) VALUES (1, ?, '1', ?, 'correctness', 'a.go', 3, 'broken', NULLIF(?, ''), 'now')`,
			info.DocPath, f.id, f.resolved)
	}
	findings, scope := s.previousRound(info.DocPath, 1)
	if len(findings) != 1 || findings[0].ID != "R1-1" || findings[0].Line != 3 || len(scope) != 1 || scope[0] != "api/" {
		t.Errorf("previousRound = %+v %v, want the one open finding R1-1 and scope [api/]", findings, scope)
	}
}

// round2Cycle runs the second review of a phase whose doc carries round1, with
// the reviewer answering output.
func round2Cycle(t *testing.T, output string) (verdict string, fix bool, doc string, dec *fakeDecider, fixRound int) {
	t.Helper()
	s, db, p1, info := reviewFixture(t)
	writeRound1(t, info.DocPath)
	s.Review = &fakeReviewer{outputs: []string{output}}
	s.treeFingerprint = func(string) (string, error) { return "tree-1", nil }
	dec = &fakeDecider{}
	s.ReviewDecisions = dec
	verdict, fix = s.reviewCycle(p1, info, worktree.Acquired{Path: t.TempDir()}, 1, "", false, GuardResult{})
	doc, _, _, _ = phaseDocAndState(t, db, p1)
	return verdict, fix, doc, dec, reviewFixRound(t, db, p1)
}

// partial-fix.md resolves R1-1 and says nothing about R1-2: the daemon adds
// «unaddressed R1-2», the round FAILs and — being round 2 — is handed over.
func TestRound2PartialFixFailsOnAnUnaddressedFinding(t *testing.T) {
	verdict, fix, doc, dec, fixRound := round2Cycle(t, reviewRoundsFixture(t, "partial-fix.md"))
	if verdict != "fail" || fix {
		t.Fatalf("round 2 = (%q, %v), want (fail, false) — no third automatic round", verdict, fix)
	}
	rs, errs := mdfence.ParseReviews(doc)
	if len(errs) != 0 || len(rs) != 2 {
		t.Fatalf("doc blocks = %+v %+v", rs, errs)
	}
	r2 := rs[1]
	if r2.Round != "2" || r2.Verdict != mdfence.ReviewFail || len(r2.Resolves) != 1 || r2.Resolves[0] != "R1-1" || len(r2.Findings) != 1 {
		t.Fatalf("round 2 block = %+v", r2)
	}
	if f := r2.Findings[0]; f.ID != "R2-U1" || f.Type != "plan-conformance" || f.File != "src/lib/export/csv.go" ||
		!strings.HasPrefix(f.Evidence, "unaddressed R1-2") || !strings.Contains(f.Note, "added by the daemon") {
		t.Errorf("daemon finding = %+v, want «unaddressed R1-2» as plan-conformance", f)
	}
	if got := dec.all(); len(got) != 1 || got[0].Round != 2 {
		t.Errorf("decisions = %+v, want one after round 2", got)
	}
	if fixRound != maxAutoReviewRounds {
		t.Errorf("review_fix_round = %d, want %d", fixRound, maxAutoReviewRounds)
	}
}

// full-fix.md resolves both: PASS, the cycle closes, nothing is handed over.
func TestRound2FullFixPasses(t *testing.T) {
	verdict, fix, doc, dec, fixRound := round2Cycle(t, reviewRoundsFixture(t, "full-fix.md"))
	if verdict != "pass" || fix {
		t.Fatalf("round 2 = (%q, %v), want (pass, false)", verdict, fix)
	}
	rs, _ := mdfence.ParseReviews(doc)
	if len(rs) != 2 || rs[1].Verdict != mdfence.ReviewPass || len(rs[1].Findings) != 0 || !rs[1].Complete {
		t.Fatalf("doc blocks = %+v, want round 2 PASS with no findings", rs)
	}
	if n := len(dec.all()); n != 0 {
		t.Errorf("decisions = %d, want none after a PASS", n)
	}
	if fixRound != 0 {
		t.Errorf("review_fix_round = %d, want 0 after a PASS", fixRound)
	}
}

// A round-2 reviewer that timed out is incomplete: recorded as an incomplete
// INCONCLUSIVE round in the doc (the phasegate hold), handed over.
func TestRound2TimeoutIsIncomplete(t *testing.T) {
	s, db, p1, info := reviewFixture(t)
	writeRound1(t, info.DocPath)
	s.Review = &timedOutReviewer{output: reviewRoundsFixture(t, "timeout.md")}
	s.treeFingerprint = func(string) (string, error) { return "tree-1", nil }
	dec := &fakeDecider{}
	s.ReviewDecisions = dec
	verdict, fix := s.reviewCycle(p1, info, worktree.Acquired{Path: t.TempDir()}, 1, "", false, GuardResult{})
	if verdict != "inconclusive" || fix {
		t.Fatalf("round 2 = (%q, %v), want (inconclusive, false)", verdict, fix)
	}
	doc, _, _, _ := phaseDocAndState(t, db, p1)
	rs, _ := mdfence.ParseReviews(doc)
	if len(rs) != 2 || rs[1].Verdict != mdfence.ReviewInconclusive || rs[1].Complete || rs[1].Unparsable ||
		!strings.Contains(strings.Join(rs[1].ScopeLeft, " "), "review-incomplete: reviewer-timed-out") {
		t.Fatalf("doc blocks = %+v, want an incomplete INCONCLUSIVE round 2 naming the timeout", rs)
	}
	if got := dec.all(); len(got) != 1 || !strings.Contains(got[0].Reason, IncompleteTimedOut) {
		t.Errorf("decisions = %+v", got)
	}
}

func TestUnaddressedFindings(t *testing.T) {
	prior := round1.Findings
	// Re-reported by id, by citing the id, or at the same file:line — all addressed.
	for _, now := range [][]mdfence.Finding{
		{{ID: "R1-2"}},
		{{ID: "R2-1", Evidence: "R1-2 still stands"}},
		{{ID: "R2-1", File: "src/lib/export/csv.go", Line: 12}},
	} {
		got := unaddressedFindings(prior, &mdfence.Review{Resolves: []string{"R1-1"}, Findings: now}, 2)
		if len(got) != 0 {
			t.Errorf("findings %+v: unaddressed = %+v, want none", now, got)
		}
	}
	if got := unaddressedFindings(prior, &mdfence.Review{}, 2); len(got) != 2 || got[0].ID != "R2-U1" || got[1].ID != "R2-U2" {
		t.Errorf("an empty round 2 = %+v, want both findings unaddressed", got)
	}
	if got := unaddressedFindings(prior, nil, 2); got != nil {
		t.Errorf("no block = %+v, want nil", got)
	}
}

// timedOutReviewer is a reviewer the hard timeout killed mid-sentence.
type timedOutReviewer struct{ output string }

func (r *timedOutReviewer) Run(_ context.Context, _ verify.RunSpec) (*verify.Run, error) {
	return &verify.Run{Output: r.output, TimedOut: true}, nil
}
