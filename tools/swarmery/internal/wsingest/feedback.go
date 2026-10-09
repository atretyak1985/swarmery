// Operator feedback (phase landing, "return to agent"): the operator reviewed a
// finished phase run and sends it back with a note. The note is written INTO the
// phase doc — md = truth — as a dated `## Operator feedback` section, so the next
// run of the phase reads it as part of its contract and the operator's dashboard
// shows it in the doc like any other section. The phase-run prompt points the
// executor at the most recent such section (phaserun.StartOptions.Returned).

package wsingest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReviewFeedbackMax bounds a review note an operator sends back to an agent —
// the board card's re-run feedback and a phase's return feedback alike.
// Generous — it is a review note, not a document — but what it is appended to is
// handed to a headless agent, so it cannot be unbounded.
const ReviewFeedbackMax = 20 << 10 // 20 KB

// ErrFeedbackEmpty / ErrFeedbackTooLarge are AppendOperatorFeedback's refusals;
// the doc is left untouched by either.
var (
	ErrFeedbackEmpty    = errors.New("operator feedback is empty")
	ErrFeedbackTooLarge = errors.New("operator feedback is too large")
)

// operatorFeedbackHeading is the section heading's fixed lead; the timestamp
// follows in parentheses.
const operatorFeedbackHeading = "## Operator feedback"

// operatorFeedbackUntick is the instruction every feedback section carries. Ticks
// stay as the previous run left them (nothing here edits a checkbox); the
// executor that reads the feedback decides which ones it invalidates.
const operatorFeedbackUntick = "Re-verify every ticked acceptance criterion this feedback touches; " +
	"untick (`- [x]` → `- [ ]`) any that no longer holds before you start, and tick it again only when it is true."

// AppendOperatorFeedback writes feedback into the phase doc at docPath as
//
//	## Operator feedback (2006-01-02 15:04)
//
//	> <feedback, every line quoted>
//
//	<operatorFeedbackUntick>
//
// immediately BEFORE the doc's `## Completion Report` heading — the first one
// outside a fenced code block — or at the end of the doc when it has none. A
// second call therefore lands below the first and still above the report: the
// sections read in chronological order. now stamps the heading in its own
// location. The feedback is trimmed; empty or larger than ReviewFeedbackMax is
// refused before the doc is read.
//
// The operator's text is rendered as a Markdown blockquote (quoteFeedback): a
// line in it that reads `## Completion Report`, `- [ ] …` or a code fence is
// quoted text, never a heading, a criterion or a fence the doc's parsers act on.
// The untick instruction stays outside the quote — it is the daemon's line.
//
// The write is atomic (writeFileAtomic: a temp file in the doc's directory,
// renamed over it), so a crash mid-write never leaves a truncated phase doc, and
// it keeps the existing file's mode.
func AppendOperatorFeedback(docPath, feedback string, now time.Time) error {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return ErrFeedbackEmpty
	}
	if len(feedback) > ReviewFeedbackMax {
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrFeedbackTooLarge, len(feedback), ReviewFeedbackMax)
	}
	st, err := os.Stat(docPath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	out := insertBeforeCompletionReport(string(body), operatorFeedbackSection(feedback, now))
	return writeFileAtomic(docPath, []byte(out), st.Mode().Perm())
}

// writeFileAtomic replaces path with data through a temp file in the same
// directory (so the rename never crosses a filesystem), set to perm before the
// rename. On any failure the temp file is removed and path is left untouched.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	renamed = true
	return nil
}

// operatorFeedbackSection renders one section, ending in exactly one newline.
func operatorFeedbackSection(feedback string, now time.Time) string {
	return operatorFeedbackHeading + " (" + now.Format("2006-01-02 15:04") + ")\n\n" +
		quoteFeedback(feedback) + "\n\n" + operatorFeedbackUntick + "\n"
}

// quoteFeedback renders the operator's text as a Markdown blockquote: every line
// prefixed `> `, a blank line as a bare `>` (no trailing space). CRLF is
// normalised first so no line keeps a stray carriage return.
func quoteFeedback(feedback string) string {
	lines := strings.Split(strings.ReplaceAll(feedback, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = ">"
			continue
		}
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// insertBeforeCompletionReport places section right above the first
// `## Completion Report` heading outside a fence (completionHeadingRe, the
// heading ParseCompletionReport reads), separated from what precedes it and
// from the heading by one blank line each. No such heading ⇒ appended at the
// end. Pure; unit-tested.
func insertBeforeCompletionReport(text, section string) string {
	lines := strings.Split(text, "\n")
	at := -1
	forEachLineOutsideFences(text, func(i int, line string) {
		if at < 0 && completionHeadingRe.MatchString(line) {
			at = i
		}
	})
	if at < 0 {
		return joinBlock(text, section)
	}
	head := strings.Join(lines[:at], "\n")
	tail := strings.Join(lines[at:], "\n")
	return joinBlock(head, section) + "\n" + tail
}

// joinBlock appends block after text with one blank line between them (none
// when text is empty).
func joinBlock(text, block string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return block
	}
	return text + "\n\n" + block
}
