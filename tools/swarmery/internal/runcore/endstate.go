package runcore

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
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

// MinContinuationWindow is how much of the run's wall clock must still be left
// before a continuation is worth spawning.
//
// The guard it replaces was `elapsed >= timeout`, which is true only once the
// budget is already gone — so a run with two seconds left still spawned a fresh
// billed turn that the deadline killed mid-sentence. The spawn costs a full
// prompt (a session at its largest context, re-sent) and can produce nothing: a
// continuation that cannot finish a single tool call is pure spend. Five minutes
// is the smallest window in which an executor can plausibly read the unticked
// list, make one change and tick it; below that the honest answer is `partial`.
const MinContinuationWindow = 5 * time.Minute

// blockedLineRe matches the blocked sentinel of every engine that has one:
// `BLOCKED: x` (dispatch), `PHASE BLOCKED: x` (phaserun) and
// `PLAN BLOCKED at phase 3: x` (planrun). The qualifier before BLOCKED is a
// single uppercase word; the text between BLOCKED and the colon is free-form so
// planrun's `at phase <n>` survives. Leading markdown decoration (list bullets,
// emphasis, blockquote, heading) is tolerated because models wrap final lines in
// it constantly.
//
// The search that uses it SKIPS FENCED CODE BLOCKS (mdfence), for the same reason
// wsingest's checkbox readers do: a completion report that quotes its own contract
// ("end with `PHASE BLOCKED: <reason>`") inside a ``` block is showing the
// template, not invoking it — and blocked wins over a full tick count by design,
// so reading a quote as a sentinel stamps a finished phase blocked with a nonsense
// reason and no continuation can ever clear it.
var blockedLineRe = regexp.MustCompile(`^[\s>*_#+-]*(?:[A-Z][A-Z]+\s+)?BLOCKED\b[^:\n]*:\s*(.*)$`)

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
	if reason, ok := BlockedReason(text); ok {
		return EndBlocked, reason
	}
	if total <= 0 || done >= total {
		return EndDone, ""
	}
	return EndContinue, ""
}

// BlockedReason reports whether text ends with a blocked sentinel, and its
// reason (the whole line when the reason after the colon is empty).
//
// Exported because the blocked ending is evidence ON ITS OWN, independent of the
// document: when the phase doc cannot be read at settle time there is no tick
// count to classify with, but "the executor said it is blocked, and why" is still
// the truest thing known about the run — so the engines ask this directly rather
// than stamping something green over it.
func BlockedReason(text string) (reason string, ok bool) {
	line, reason, ok := blockedLine(text)
	if ok && reason == "" {
		reason = line
	}
	return reason, ok
}

// blockedLine returns the LAST blocked sentinel in text OUTSIDE ANY CODE FENCE
// (closest to the end — every contract says "end with", and a model that quotes
// the contract earlier in its reply must not shadow the ending it actually
// chose; a model that quotes it inside a fence has not chosen it at all).
func blockedLine(text string) (line, reason string, ok bool) {
	scan := func(raw string) {
		m := blockedLineRe.FindStringSubmatch(raw)
		if m == nil {
			return
		}
		line, reason, ok = strings.TrimSpace(raw), strings.TrimSpace(m[1]), true
	}
	mdfence.ForEachLine(text, func(_ int, raw string) { scan(raw) })
	if ok || !mdfence.EndsOpen(text) {
		return line, reason, ok
	}
	// The reply ends with a fence still open — almost always a pasted log or
	// traceback the executor never closed. Everything after it was skipped,
	// which would hide a real `PHASE BLOCKED:` written underneath and stamp a
	// stuck run green. Skipping a genuine block is the cheaper mistake here
	// (one wasted continuation) than missing a real one, so fall back to a raw
	// scan rather than trusting a fence the author clearly lost track of.
	for _, raw := range strings.Split(text, "\n") {
		scan(raw)
	}
	return line, reason, ok
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
