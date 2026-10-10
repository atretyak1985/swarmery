// The independent code review of a phase run (migration 0106). A phase doc opts in
// with a `**Review:** on` header line; after its run settles, internal/phaserun
// spawns a read-only reviewer, and a FAIL verdict is written back INTO the doc —
// md = truth — as a dated `## Review findings` section the fix re-run reads first
// (phaserun.ReviewFixNote).

package wsingest

import (
	"errors"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
)

// ReviewOn / ReviewOff are the values epic_phases.review_mode stores.
const (
	ReviewOn  = "on"
	ReviewOff = "off"
)

// docReviewRe matches the header line `**Review:** on`. It sits beside docVerifyRe
// (epics.go) and is scanned under the same header bound.
var docReviewRe = regexp.MustCompile(`(?i)^\s*\*\*Review:\*\*\s*(.+?)\s*$`)

// ParseDocReview reports whether the phase doc's header block opts into the review
// stage. Accepted spellings for on: on, yes, true; for off: off, no, false, none.
//
// Bounded by docStatusHeaderLines and stopping at the first `## ` section, exactly
// like ParseDocVerify: a `**Review:**` line quoted inside an agent prompt further
// down describes someone else's phase. Absent and unrecognized both mean off — a
// reviewer nobody asked for costs a session on every run.
func ParseDocReview(text string) (on bool) {
	lines := strings.Split(text, "\n")
	if len(lines) > docStatusHeaderLines {
		lines = lines[:docStatusHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		m := docReviewRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch v := strings.ToLower(strings.TrimSpace(m[1])); v {
		case ReviewOn, "yes", "true":
			return true
		case ReviewOff, "no", "false", "none":
			return false
		default:
			log.Printf("warn: wsingest: unrecognized **Review:** %q in a phase doc header — review stays off", v)
			return false
		}
	}
	return false
}

// ReviewFindingsMax bounds the findings block written into the doc. The doc is
// handed whole to the fix run's prompt (an argv), so it cannot grow without bound;
// the full findings stay in phase_reviews.findings.
const ReviewFindingsMax = 32 << 10 // 32 KB

// ErrFindingsEmpty is AppendReviewFindings' refusal of an empty findings block; the
// doc is left untouched.
var ErrFindingsEmpty = errors.New("review findings are empty")

// reviewFindingsHeading is the section heading's fixed lead; the timestamp follows
// in parentheses.
const reviewFindingsHeading = "## Review findings"

// reviewFindingsInstruction is the daemon's line under the quoted findings, outside
// the quote. Like operatorFeedbackUntick it edits no checkbox itself.
const reviewFindingsInstruction = "An independent code review of the previous run returned FAIL. " +
	"Fix every blocking finding quoted above before anything else; re-verify each ticked acceptance criterion " +
	"a finding touches and untick (`- [x]` → `- [ ]`) any that no longer holds, ticking it again only when it is true."

// AppendReviewFindings writes the reviewer's findings into the phase doc at docPath
// as
//
//	## Review findings (2006-01-02 15:04)
//
//	> <findings, every line quoted>
//
//	<reviewFindingsInstruction>
//
// with the same writer and at the same place as AppendOperatorFeedback: immediately
// before the first `## Completion Report` heading outside a fence, or at the end of
// the doc, written atomically and keeping the file's mode. The findings are quoted so
// a `- [ ]` or a heading inside them is never parsed as a criterion or a section. A
// block longer than ReviewFindingsMax is cut there with a marker.
func AppendReviewFindings(docPath, findings string, now time.Time) error {
	findings = strings.TrimSpace(findings)
	if findings == "" {
		return ErrFindingsEmpty
	}
	if len(findings) > ReviewFindingsMax {
		findings = strings.TrimSpace(findings[:ReviewFindingsMax]) + "\n\n[… findings truncated; the full text is in the review record]"
	}
	st, err := os.Stat(docPath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	section := reviewFindingsHeading + " (" + now.Format("2006-01-02 15:04") + ")\n\n" +
		quoteFeedback(findings) + "\n\n" + reviewFindingsInstruction + "\n"
	out := insertBeforeCompletionReport(string(body), section)
	return writeFileAtomic(docPath, []byte(out), st.Mode().Perm())
}
