package phaserun

import (
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/verify"
)

// D20: a review that stopped early is «incomplete», never «passed». An
// incomplete review starts no fix re-run and counts as no verdict: the phase is
// held (phasegate) until the operator repeats the review or accepts it as is.
//
// It is the union of the ways a reviewer can return without having covered its
// scope — it ran out of time, it never said a verdict, it gave no readable
// machine copy of what it read, it said itself that it stopped (`complete:
// false`), or its block and its VERDICT line disagree. A reviewer that BROKE its
// contract (reviewer-mutated-tree) or could not start is not incomplete: that is
// INCONCLUSIVE, as before.

// ClassReviewIncomplete is the class of an incomplete review; its detail is
// "review-incomplete: <sub-reason>: <what the reviewer did>".
const ClassReviewIncomplete = "review-incomplete"

// Sub-reasons of ClassReviewIncomplete, in ClassifyReview's order.
const (
	IncompleteTimedOut        = classReviewerTimedOut  // killed by the hard timeout
	IncompleteNoVerdict       = classReviewerNoVerdict // no VERDICT: line at all
	IncompleteUnparsable      = "unparsable-block"     // a kind: review block that does not parse
	IncompleteNoBlock         = "no-block"             // a FAIL with no kind: review block
	IncompleteCompleteFalse   = "complete-false"       // the reviewer said it stopped early
	IncompletePassNoBlock     = "pass-without-block"   // a PASS nobody can check the scope of
	IncompleteVerdictMismatch = "verdict-mismatch"     // block verdict ≠ VERDICT line
)

// ClassifyReview reads one finished review. blocks are the `kind: review`
// blocks parsed out of the reviewer's OUTPUT (the last one is the round's), and
// out.blockErrs counts the ones that did not parse.
//
// incomplete=true ⇒ class is the sub-reason (IncompleteTimedOut, …).
// incomplete=false ⇒ class is "" for a usable PASS/FAIL, or the inconclusive
// class of out.detail (reviewer-mutated-tree, reviewer-did-not-start, …) — those
// stay INCONCLUSIVE.
//
// Order: timed-out → no-verdict → unparsable / no block → complete:false →
// PASS-without-block → verdict mismatch.
func ClassifyReview(out reviewOutcome, blocks []mdfence.Review) (class string, incomplete bool) {
	if out.verdict == string(verify.VerdictInconclusive) {
		c := detailClass(out.detail)
		switch c {
		case classReviewerTimedOut, classReviewerNoVerdict:
			return c, true
		}
		return c, false
	}
	if out.verdict != string(verify.VerdictPass) && out.verdict != string(verify.VerdictFail) {
		return detailClass(out.detail), false
	}
	if len(blocks) == 0 {
		switch {
		case out.blockErrs > 0:
			return IncompleteUnparsable, true
		case out.verdict == string(verify.VerdictFail):
			return IncompleteNoBlock, true
		default:
			return IncompletePassNoBlock, true
		}
	}
	last := blocks[len(blocks)-1]
	switch {
	case last.Unparsable:
		return IncompleteUnparsable, true
	case !last.Complete:
		return IncompleteCompleteFalse, true
	case !strings.EqualFold(last.Verdict, out.verdict):
		return IncompleteVerdictMismatch, true
	}
	return "", false
}

// detailClass is the "<class>" of a "<class>: <detail>" verdict detail.
func detailClass(detail string) string {
	c, _, _ := strings.Cut(detail, ":")
	return strings.TrimSpace(c)
}

// incompleteDetail is the phase_reviews.detail of an incomplete review.
func incompleteDetail(sub, detail string) string {
	d := ClassReviewIncomplete + ": " + sub
	if detail = strings.TrimSpace(detail); detail != "" {
		d += ": " + detail
	}
	return d
}

// incompleteReason is the one line written into the round's `scope_left` — what
// the operator reads on the Review tab and in the phase's completion hold.
func incompleteReason(sub string) string {
	why := map[string]string{
		IncompleteTimedOut:        "the reviewer was killed by the hard timeout before it reported a verdict",
		IncompleteNoVerdict:       "the reviewer produced no VERDICT line",
		IncompleteUnparsable:      "the reviewer's kind: review block does not parse",
		IncompleteNoBlock:         "the reviewer wrote no kind: review block, so what it covered is unknown",
		IncompleteCompleteFalse:   "the reviewer stopped before covering its scope",
		IncompletePassNoBlock:     "a PASS without a kind: review block — what it covered is unknown",
		IncompleteVerdictMismatch: "the block's verdict disagrees with the VERDICT line",
	}[sub]
	if why == "" {
		why = sub
	}
	return ClassReviewIncomplete + ": " + sub + " — " + why
}
