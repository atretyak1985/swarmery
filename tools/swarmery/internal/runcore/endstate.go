package runcore

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// EndState is what a CLEANLY EXITED headless run actually achieved, decided from
// evidence rather than from the exit code.
//
// The exit code cannot answer this question and never could. `claude -p` exits 0
// whenever the model ends its turn — including when it ends the turn to report
// progress, to propose a next step, or to say it is waiting on subagents it just
// killed by replying. phaserun and planrun stamped `done` on exit 0 regardless,
// so a run that ticked nothing and a run that ticked everything were recorded
// identically; the only thing that told them apart was a checkbox delta nobody
// gated on. The endings the prompts demand — `PHASE DONE`, `PHASE BLOCKED:`,
// `PLAN DONE` — were written into the transcript and read by nothing.
type EndState string

const (
	// EndDone: the work is finished — every acceptance criterion is ticked (or
	// the doc declares none to count).
	EndDone EndState = "done"
	// EndBlocked: the run said so, with a reason. A blocked run is NOT continued:
	// it asked for a human, and spending two more turns telling it to carry on is
	// exactly the loop the operator would have to pay for and then interrupt.
	EndBlocked EndState = "blocked"
	// EndContinue: a clean exit with criteria still unticked and no blocked line —
	// the Opus-5.x "report and stop" shape. The engine resumes the SAME session
	// with the unticked list rather than stamping a green `done` over unfinished
	// work.
	EndContinue EndState = "continue"
)

// MaxContinuations caps how many times one run may be resumed before its state is
// stamped `partial`. This is a MONEY bound, not a style preference: each
// continuation is a fresh billed turn at the run's pinned effort inside a session
// whose context is already at its largest, and a model that reports progress once
// will happily report it again. Two is the ceiling Anthropic's own unattended-run
// guidance suggests (2–3) taken at its cheap end.
const MaxContinuations = 2

// blockedLineRe matches the blocked sentinel of every engine that has one:
// `BLOCKED: x` (dispatch), `PHASE BLOCKED: x` (phaserun) and
// `PLAN BLOCKED at phase 3: x` (planrun). The qualifier before BLOCKED is a
// single uppercase word; the text between BLOCKED and the colon is free-form so
// planrun's `at phase <n>` survives. Leading markdown decoration (list bullets,
// emphasis, blockquote, heading) is tolerated because models wrap final lines in
// it constantly.
var blockedLineRe = regexp.MustCompile(`^[\s>*_#+-]*(?:[A-Z][A-Z]+\s+)?BLOCKED\b[^:\n]*:\s*(.*)$`)

// doneLineRe matches `DONE`, `PHASE DONE` and `PLAN DONE` as a whole line
// (optional trailing period/emphasis). It is deliberately anchored: a sentence
// containing the word "done" is prose, not a sentinel.
var doneLineRe = regexp.MustCompile(`^[\s>*_#+-]*(?:[A-Z][A-Z]+\s+)?DONE[\s.!*_]*$`)

// ClassifyEnd decides a cleanly-exited run's end state from its final assistant
// text plus the criteria count measured from the doc at that instant.
//
// The precedence is the plan's, and the order is load bearing:
//
//  1. A blocked line wins over everything, including a full tick count. An
//     executor that ticked its criteria and then reported it cannot proceed is
//     telling the operator something a green chip would erase.
//  2. All criteria ticked ⇒ done. Completion is proven by TICKED CRITERIA, never
//     by the model's own claim (the same doctrine phasediag states for outcomes):
//     an unticked criterion under a `PHASE DONE` line is unfinished work with a
//     confident sentence over it.
//  3. total <= 0 ⇒ done. A doc with no checkboxes offers nothing to measure, so
//     there is no list to hand a continuation and no way for one to converge.
//     Continuing here would burn two turns and stamp `partial` on every phase
//     whose doc simply has no criteria — strictly worse than the exit-code
//     behaviour it replaces.
//  4. Otherwise ⇒ continue.
//
// The returned string is the blocked REASON (empty for every other state).
func ClassifyEnd(text string, done, total int) (EndState, string) {
	if line, reason, ok := blockedLine(text); ok {
		if reason == "" {
			reason = line
		}
		return EndBlocked, reason
	}
	if total <= 0 || done >= total {
		return EndDone, ""
	}
	return EndContinue, ""
}

// blockedLine returns the LAST blocked sentinel in text (closest to the end —
// every contract says "end with", and a model that quotes the contract earlier in
// its reply must not shadow the ending it actually chose).
func blockedLine(text string) (line, reason string, ok bool) {
	for _, raw := range strings.Split(text, "\n") {
		m := blockedLineRe.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		line, reason, ok = strings.TrimSpace(raw), strings.TrimSpace(m[1]), true
	}
	return line, reason, ok
}

// HasDoneLine reports whether text ends a turn with a DONE sentinel. It is NOT
// what decides completion — the ticks are — but it separates "claimed done and
// wasn't" from "stopped without claiming anything", which is worth recording on
// the continuation event the operator reads.
func HasDoneLine(text string) bool {
	for _, raw := range strings.Split(text, "\n") {
		if doneLineRe.MatchString(raw) {
			return true
		}
	}
	return false
}

// continuationCriteriaCap bounds how many unticked criteria a continuation
// message lists. A plan run can have a hundred across its phases, and pasting all
// of them turns the nudge into another wall of context at the exact moment the
// window is already full.
const continuationCriteriaCap = 12

// ContinuationMessage is the resume text sent to a run that stopped with work
// left. blockedSentinel is the engine's own blocked ending (`PHASE BLOCKED` /
// `PLAN BLOCKED at phase <n>`) so the continuation points at the vocabulary the
// original prompt established rather than inventing a second one.
//
// The elapsed/budget line is the time signal from Anthropic's unattended-run
// guidance: a model that cannot see the clock treats every turn as if it had the
// whole window left, and the observed effect of showing it is that runs converge
// sooner instead of re-deriving context they already have.
func ContinuationMessage(unticked []string, blockedSentinel string, elapsed, timeout time.Duration) string {
	var b strings.Builder
	shown := unticked
	if len(shown) > continuationCriteriaCap {
		shown = shown[:continuationCriteriaCap]
	}
	fmt.Fprintf(&b, "%d acceptance criteria are still unticked:\n", len(unticked))
	for _, c := range shown {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	if len(unticked) > len(shown) {
		fmt.Fprintf(&b, "- … and %d more in the document.\n", len(unticked)-len(shown))
	}
	b.WriteString("\nContinue with them. Tick each checkbox in the document as you satisfy it. ")
	fmt.Fprintf(&b, "If one is blocked, end with %s: <reason>.\n", blockedSentinel)
	b.WriteString(ElapsedLine(elapsed, timeout))
	return b.String()
}

// ElapsedLine renders the time signal: `elapsed 240s / 14400s`. A zero timeout
// (no bound known) omits the right-hand side rather than printing `/ 0s`, which
// reads as "no time left".
func ElapsedLine(elapsed, timeout time.Duration) string {
	if timeout <= 0 {
		return fmt.Sprintf("elapsed %ds\n", int(elapsed.Seconds()))
	}
	return fmt.Sprintf("elapsed %ds / %ds\n", int(elapsed.Seconds()), int(timeout.Seconds()))
}
