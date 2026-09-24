package decide

import (
	"context"
	"fmt"
	"strings"
)

// QD3Cause is D3's one question (learning-loop step 14.3): WHY did a scored
// phase run diverge from its forecast? Analytics only — the answer never feeds
// back into a run, a lesson's status or a retirement; it is a label on the
// phase_surprise row that phase 17 slices surprise by.
const QD3Cause = "d3.divergence_cause"

// DivergenceCauses is D3's closed taxonomy (the step's six causes).
var DivergenceCauses = []string{"spec-wrong", "code-differs", "scope-grew", "tooling-env", "model-fallback", "other"}

// d3InputBytes caps the divergence paragraph handed to the classifier.
const d3InputBytes = 1500

// D3Input is the evidence for one divergence classification. Every field is
// already on the control plane's side of the run — no transcript is read.
type D3Input struct {
	SessionUUID  string
	PhaseName    string
	Summary      string // phase_surprise.summary, one operator sentence
	TopComponent string // phase_surprise.top_component
	Divergence   string // the Completion Report's "Where reality diverged" paragraph
	// ModelFallback is the actuals' fallback flag. It is stored as the RULE's
	// view (decisions.rule_value) for comparison, never forced as the answer: a
	// safeguard fallback during a run does not by itself explain a divergence.
	ModelFallback bool
}

// D3Outcome is one D3 call.
type D3Outcome struct {
	Asked bool
	// Label is what an ACTIVE question stores on the surprise row: the answer at
	// or above the threshold, LabelUnknown below it. "" when the question is off,
	// in shadow, or the call failed — the caller then stores nothing.
	Label      string
	Confidence float64
	DecisionID int64
	Err        error
}

// D3 asks the divergence-cause question. A no-op (Asked=false) when the engine
// has no reachable backend or the question is off.
func (e *Engine) D3(ctx context.Context, in D3Input) D3Outcome {
	if !e.Configured() {
		return D3Outcome{}
	}
	mode := e.Mode(QD3Cause)
	if mode == ModeOff {
		return D3Outcome{}
	}
	rule := ""
	if in.ModelFallback {
		rule = "model-fallback"
	}
	input := fmt.Sprintf("phase: %s\nsurprise: %s\nlargest component: %s\nmodel fallback during the run: %t\nwhere reality diverged (the executor's own words):\n%s",
		in.PhaseName, in.Summary, in.TopComponent, in.ModelFallback,
		truncate(strings.TrimSpace(in.Divergence), d3InputBytes))
	a, err := e.Decide(ctx, Question{
		ID:   QD3Cause,
		Kind: KindChoice,
		Opts: DivergenceCauses,
		Prompt: "A coding agent's phase run landed far from its own forecast. From its explanation, what was the main cause? " +
			"spec-wrong = the plan/spec asked for the wrong thing; code-differs = the code was not what the plan assumed; " +
			"scope-grew = the work legitimately grew; tooling-env = tools, CI or environment got in the way; " +
			"model-fallback = the model was switched mid-run; other = none of these.",
		Input:       input,
		Subject:     in.SessionUUID,
		SessionUUID: in.SessionUUID,
		RuleValue:   rule,
	})
	out := D3Outcome{Asked: true, Confidence: a.Confidence, DecisionID: a.DecisionID, Err: err}
	if err != nil || mode != ModeActive {
		return out
	}
	out.Label = LabelUnknown
	if a.Calibrated && a.Confidence >= e.Threshold(QD3Cause) {
		out.Label = a.Value
	}
	return out
}
