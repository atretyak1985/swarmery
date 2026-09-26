package decide

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// QD1 is D1's question id: "how did this run end?"
const QD1 = "d1.run_end"

// D1's options.
const (
	D1Report   = "report-with-next-step"
	D1Blocked  = "blocked"
	D1Question = "question-for-operator"
	D1Done     = "done"
)

// D1Options is D1's closed answer set.
var D1Options = []string{D1Report, D1Blocked, D1Question, D1Done}

// d1InputBytes caps the evidence: the tail of the last assistant message only.
const d1InputBytes = 2000

// Action is what D1 tells the engine's settle loop to do.
type Action int

const (
	// Keep: the rules stand (the loop continues the run exactly as before).
	Keep Action = iota
	// NotifyOperator: do not continue; settle `partial` and hand it to a human.
	NotifyOperator
	// StampBlocked: settle `blocked` with the classifier's reason.
	StampBlocked
)

// D1Input is the ambiguous branch's evidence, as the settle loop sees it.
type D1Input struct {
	Engine      string // "phaserun" | "planrun"
	SubjectID   int64
	SessionUUID string
	LastText    string
	StopReason  string
	Done, Total int
	Attempt     int
}

// D1Outcome is D1's verdict for the loop.
type D1Outcome struct {
	Action     Action
	Detail     string
	DecisionID int64
	Answer     string
}

// Ambiguous is the only branch D1 is ever asked about: the run exited cleanly,
// the API says the model simply ended its turn, criteria are still unticked and
// the rules found no blocked line (the caller only asks after ClassifyRunEnd
// returned continue). Everything else is decided by the rules alone.
func Ambiguous(in D1Input) bool {
	return strings.EqualFold(strings.TrimSpace(in.StopReason), "end_turn") && in.Total > 0 && in.Done < in.Total
}

// D1 asks "how did this run end?" for an ambiguous clean exit and says what
// the loop should do. It returns Keep — today's behaviour — whenever the
// engine is nil or unconfigured, the question is off or in shadow, the branch
// is not ambiguous, or the classifier failed. Only an ACTIVE answer changes
// anything, and below the threshold (or uncalibrated) it falls back to the safe
// default: no continuation, notify the operator.
func (e *Engine) D1(ctx context.Context, in D1Input) D1Outcome {
	if !e.Configured() || !Ambiguous(in) {
		return D1Outcome{}
	}
	mode := e.Mode(QD1)
	if mode == ModeOff {
		return D1Outcome{}
	}
	q := Question{
		ID:   QD1,
		Kind: KindChoice,
		Opts: D1Options,
		Prompt: "An unattended coding agent ended its turn with acceptance criteria still unticked. " +
			"How did it end? report-with-next-step = it reported progress or proposed the next step it could take itself; " +
			"blocked = it cannot proceed without something it lacks; question-for-operator = it asked the operator a question; " +
			"done = it claims the work is finished.",
		Input:       fmt.Sprintf("criteria ticked: %d of %d\nlast assistant message (tail):\n%s", in.Done, in.Total, tail(in.LastText, d1InputBytes)),
		Subject:     fmt.Sprintf("%s:%d", in.Engine, in.SubjectID),
		SessionUUID: in.SessionUUID,
		RuleValue:   "continue",
	}
	a, err := e.Decide(ctx, q)
	out := D1Outcome{DecisionID: a.DecisionID, Answer: a.Value}
	if err != nil {
		log.Printf("warning: decide: d1 %s: %v (rules stand)", q.Subject, err)
		return out
	}
	if mode != ModeActive {
		return out
	}
	thr := e.Threshold(QD1)
	switch {
	case !a.Calibrated || a.Confidence < thr:
		out.Action = NotifyOperator
		out.Detail = fmt.Sprintf("classifier unsure (%s %.2f < %.2f, %s): %d of %d criteria ticked — needs operator, no automatic continuation",
			a.Value, a.Confidence, thr, calibration(a), in.Done, in.Total)
	case a.Value == D1Question:
		out.Action = NotifyOperator
		out.Detail = fmt.Sprintf("the run asked the operator a question (classifier %.2f): %d of %d criteria ticked — needs operator", a.Confidence, in.Done, in.Total)
	case a.Value == D1Blocked:
		out.Action = StampBlocked
		out.Detail = fmt.Sprintf("classifier: the run reads as blocked (%.2f) with %d of %d criteria ticked", a.Confidence, in.Done, in.Total)
	default:
		// report-with-next-step ⇒ the rules' continuation; done ⇒ trust the
		// checkboxes anyway, which also means continuing.
		return out
	}
	e.MarkActed(out.DecisionID)
	if out.Action == NotifyOperator && e.OnNeedsOperator != nil {
		e.OnNeedsOperator(NeedsOperator{Engine: in.Engine, SubjectID: in.SubjectID, SessionUUID: in.SessionUUID, Detail: out.Detail})
	}
	return out
}

func calibration(a Answer) string {
	if a.Calibrated {
		return "calibrated"
	}
	return "uncalibrated"
}

// D1TruthAfterContinuation is step 9.3's ground truth for a decision that was
// followed by a continuation: blocked when the continued run ended blocked,
// report-with-next-step when it ticked more criteria (continuing was right),
// question-for-operator when it ticked nothing (it needed something it did not
// have).
func D1TruthAfterContinuation(end string, doneBefore, doneAfter int) string {
	switch {
	case end == "blocked":
		return D1Blocked
	case doneAfter > doneBefore:
		return D1Report
	default:
		return D1Question
	}
}

// D1Tracker carries one settle loop's pending decision to the next iteration,
// where what the continuation achieved becomes its ground truth.
type D1Tracker struct {
	e          *Engine
	pendingID  int64
	doneBefore int
}

// Tracker returns a tracker for one settle loop. Safe on a nil engine.
func (e *Engine) Tracker() *D1Tracker { return &D1Tracker{e: e} }

// Decide runs D1 and, when the rules stand (a continuation follows), keeps the
// decision pending for Observe.
func (t *D1Tracker) Decide(ctx context.Context, in D1Input) D1Outcome {
	o := t.e.D1(ctx, in)
	if o.DecisionID != 0 && o.Action == Keep {
		t.pendingID, t.doneBefore = o.DecisionID, in.Done
	}
	return o
}

// Observe records the pending decision's ground truth from the loop's next
// classification. No-op when nothing is pending.
func (t *D1Tracker) Observe(end string, done int) {
	if t == nil || t.pendingID == 0 || t.e == nil {
		return
	}
	truth := D1TruthAfterContinuation(end, t.doneBefore, done)
	if err := RecordGroundTruth(t.e.DB, t.pendingID, truth, t.e.now()); err != nil {
		log.Printf("warning: decide: ground truth for decision %d: %v", t.pendingID, err)
	}
	t.pendingID = 0
}
