package phaserun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
)

// reviewRoundsFixture reads testdata/fixtures/review-rounds/<name>.
func reviewRoundsFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "review-rounds", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// classified runs one reviewer output through concludeReview — the same reading
// the review stage does — and then ClassifyReview.
func classified(output string, timedOut bool) (verdict, class string, incomplete bool) {
	o := reviewOutcome{}
	o.blocks, o.blockErrs = reviewerBlocks(output)
	o = concludeReview(o, &verify.Run{Output: output, TimedOut: timedOut}, nil)
	class, incomplete = ClassifyReview(o, o.blocks)
	return o.verdict, class, incomplete
}

// blockOutput is a reviewer answer whose block says blockVerdict / complete and
// whose VERDICT line says lineVerdict.
func blockOutput(blockVerdict, complete, lineVerdict string) string {
	return "Findings.\n\n```yaml\nkind: review\nround: 1\nverdict: " + blockVerdict + "\ncomplete: " + complete +
		"\nscope_left: [web/]\nfindings: []\nresolves: []\nids: []\ndrift: []\nchecked_callers: []\n```\nVERDICT: " + lineVerdict
}

func TestClassifyReview(t *testing.T) {
	cases := []struct {
		name       string
		output     string
		timedOut   bool
		wantClass  string
		incomplete bool
	}{
		{"timeout", reviewRoundsFixture(t, "timeout.md"), true, IncompleteTimedOut, true},
		{"no verdict", reviewRoundsFixture(t, "timeout.md"), false, IncompleteNoVerdict, true},
		{"missing block on a FAIL", "- P0 a.go:1 — broken.\nVERDICT: FAIL", false, IncompleteNoBlock, true},
		{"unparsable block", "```yaml\nkind: review\nround: first\nverdict: FAIL\n```\nVERDICT: FAIL", false, IncompleteUnparsable, true},
		{"complete false", blockOutput("FAIL", "false", "FAIL"), false, IncompleteCompleteFalse, true},
		{"PASS without block", reviewRoundsFixture(t, "pass-no-block.md"), false, IncompletePassNoBlock, true},
		{"verdict mismatch", blockOutput("PASS", "true", "FAIL"), false, IncompleteVerdictMismatch, true},
		{"whole PASS", reviewRoundsFixture(t, "full-fix.md"), false, "", false},
		{"whole FAIL", blockOutput("FAIL", "true", "FAIL"), false, "", false},
		{"reviewer says INCONCLUSIVE", "missing files\nVERDICT: INCONCLUSIVE", false, classReviewCouldNot, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, class, incomplete := classified(c.output, c.timedOut)
			if class != c.wantClass || incomplete != c.incomplete {
				t.Errorf("ClassifyReview = (%q, %v), want (%q, %v)", class, incomplete, c.wantClass, c.incomplete)
			}
		})
	}
}

// The inconclusive classes that are a broken contract or a failed start stay
// INCONCLUSIVE: reviewer-mutated-tree above all (the reviewer broke the contract,
// it did not run out of scope).
func TestClassifyReviewKeepsInconclusiveClasses(t *testing.T) {
	for _, class := range []string{ClassReviewerMutatedTree, classReviewerNotStarted, classReviewUnverifiable} {
		o := reviewOutcome{verdict: string(verify.VerdictInconclusive), detail: class + ": something"}
		if got, incomplete := ClassifyReview(o, nil); got != class || incomplete {
			t.Errorf("%s: ClassifyReview = (%q, %v), want (%q, false)", class, got, incomplete, class)
		}
	}
	// A mutated tree voids the verdict even when the output carried a whole block.
	o := reviewOutcome{verdict: string(verify.VerdictInconclusive), detail: ClassReviewerMutatedTree + ": a→b"}
	o.blocks, _ = reviewerBlocks(reviewRoundsFixture(t, "full-fix.md"))
	if got, incomplete := ClassifyReview(o, o.blocks); got != ClassReviewerMutatedTree || incomplete {
		t.Errorf("mutated tree with a block = (%q, %v), want (%q, false)", got, incomplete, ClassReviewerMutatedTree)
	}
}

func TestIncompleteDetailAndReason(t *testing.T) {
	if got, want := incompleteDetail(IncompleteTimedOut, "reviewer-timed-out: killed"),
		"review-incomplete: reviewer-timed-out: reviewer-timed-out: killed"; got != want {
		t.Errorf("incompleteDetail = %q, want %q", got, want)
	}
	if got := incompleteReason(IncompleteCompleteFalse); got != "review-incomplete: complete-false — the reviewer stopped before covering its scope" {
		t.Errorf("incompleteReason = %q", got)
	}
}
