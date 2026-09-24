package runcore

import (
	"fmt"
	"strings"
	"time"
)

// StandingInstruction is the "how a turn ends" block appended ONCE to every
// prompt this daemon spawns unattended (phaserun, planrun, dispatch).
//
// It exists because the default turn-ending behaviour of a modern Claude is
// tuned for a human on the other side: end the turn with a milestone report, an
// offer to continue, or a list of non-blocking decisions, and let the person
// say "go on". Under `claude -p` there is nobody to say it — the reply IS the
// process exit — so that helpful instinct becomes an early stop with a 0 exit
// code. Anthropic's own guidance for unattended runs is to state the stopping
// rule explicitly in the prompt AND to have the harness continue a text-only
// end of turn; this constant is the first half, and ClassifyEnd/EndContinue is
// the second.
//
// It deliberately does NOT relax any confirmation rule. Every engine's contract
// already forbids pushing, PRs, merges and package installs, and "keep going"
// must not be read as permission to do them — hence the last bullet, which
// restates the fence instead of leaving the two instructions to be reconciled by
// the model.
const StandingInstruction = `HOW YOUR TURN ENDS (this run is unattended — ending your reply terminates the process):
- Keep going while the next step is one you can take on your own. Do NOT end the turn to report progress, to announce a plan, or to ask whether to continue — a status note belongs in the SAME message as the next tool call, never instead of it.
- Decisions that do not need the operator are yours to make: choose the reasonable option, record the choice in your report, and carry on.
- End the turn only when every acceptance criterion is satisfied, when you are genuinely blocked, or immediately before an action that is destructive or irreversible.
- A turn that ends with criteria still unticked and no blocked line will be resumed automatically, at most twice. Spend those continuations on the remaining work, not on restating what is left.
- This does not loosen any rule above it: still no push, no PR, no merge, no package install, and still stop at the confirmations your contract names.`

// Budget carries the wall-clock bound of one run and the instant it started, so
// the prompt can state both. Both zero means "not known" and every renderer
// degrades to emitting nothing rather than a made-up number.
type Budget struct {
	Timeout time.Duration
	Started time.Time
}

// Line renders `Budget: 4h0m0s; started 2026-09-23T10:04:11Z` — the time signal
// the model otherwise has no way to see. A run that cannot tell how long it has
// behaves as though it has forever, which is how a 4-hour phase gets spent
// re-reading files it already read.
//
// Returns "" when neither field is set; a partially-known budget still prints
// the half it knows, because half a clock is better than none.
func (b Budget) Line() string {
	switch {
	case b.Timeout > 0 && !b.Started.IsZero():
		return fmt.Sprintf("Budget: %s; started %s", b.Timeout, b.Started.UTC().Format(time.RFC3339))
	case b.Timeout > 0:
		return fmt.Sprintf("Budget: %s", b.Timeout)
	case !b.Started.IsZero():
		return fmt.Sprintf("started %s", b.Started.UTC().Format(time.RFC3339))
	default:
		return ""
	}
}

// Elapsed reports how long the run has been going at now. Zero when the budget
// carries no start instant.
func (b Budget) Elapsed(now time.Time) time.Duration {
	if b.Started.IsZero() {
		return 0
	}
	return now.Sub(b.Started)
}

// TurnContract is the block every unattended prompt appends exactly once: the
// standing instruction, then the budget line when one is known.
//
// One function rather than two appends at each call site, because "exactly once"
// is an asserted property (prompt tests count both strings) and three engines
// concatenating two constants in their own order is how that property quietly
// becomes "twice in one prompt and never in another".
func TurnContract(b Budget) string {
	var sb strings.Builder
	sb.WriteString(StandingInstruction)
	if line := b.Line(); line != "" {
		sb.WriteString("\n")
		sb.WriteString(line)
	}
	return sb.String()
}
