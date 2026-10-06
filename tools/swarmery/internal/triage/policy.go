package triage

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

// ValueSkip is always legal; it is stored as state 'skipped' and nothing is applied.
const ValueSkip = "skip"

// Policy decisions returned by decide.
const (
	DecisionAuto    = "auto"
	DecisionSuggest = "suggest"
	DecisionSkip    = "skip"
	DecisionReject  = "reject"
)

// deniedKinds can never be written by a triage run, whatever the judge says.
var deniedKinds = map[string]bool{"approval": true, "proposal": true, "alert": true}

// Rule is what a run may do for one kind (or kind/class). Auto "*" = any value
// in Part.Allowed; SampleMax > 0 holds that many items per run back as samples.
type Rule struct {
	Auto, Suggest []string
	SampleMax     int
}

// policy is the whole of what a run may do; key = Kind or Kind + "/" + Class.
var policy = map[string]Rule{
	"classifier":            {Auto: []string{"*"}, SampleMax: 5},
	"advisor/informational": {Auto: []string{"dismiss"}},
	"advisor/card":          {Suggest: []string{"fix-card", "dismiss"}},
	"advisor/improve":       {Suggest: []string{"improve", "dismiss"}},
	"advisor/plain":         {Suggest: []string{"track", "dismiss"}},
	"lesson":                {Suggest: []string{"accept", "not-useful"}},
	"retire":                {Suggest: []string{"stop", "keep"}},
	"friction":              {Auto: []string{"noise", "fixable"}},
	"agent":                 {Auto: []string{"file"}},
}

// kindOrder is the fixed order a run visits kinds in (the policy table's order).
var kindOrder = []string{"classifier", "advisor", "lesson", "retire", "friction", "agent"}

func ruleFor(kind, class string) (Rule, bool) {
	if class != "" {
		if r, ok := policy[kind+"/"+class]; ok {
			return r, true
		}
	}
	r, ok := policy[kind]
	return r, ok
}

// decide returns "auto" | "suggest" | "skip" | "reject" for one part's value.
// A denied or unknown kind always rejects; a value must be in p.Allowed (or
// be ValueSkip) and in the rule.
func decide(kind, class string, p Part, value string) string {
	if deniedKinds[kind] {
		return DecisionReject
	}
	rule, ok := ruleFor(kind, class)
	if !ok {
		return DecisionReject
	}
	if value == ValueSkip {
		return DecisionSkip
	}
	if value == "" || !slices.Contains(p.Allowed, value) {
		return DecisionReject
	}
	if slices.Contains(rule.Auto, "*") || slices.Contains(rule.Auto, value) {
		return DecisionAuto
	}
	if slices.Contains(rule.Suggest, value) {
		return DecisionSuggest
	}
	return DecisionReject
}

// sampleMax is how many items of kind a run holds back as samples.
// SWARMERY_TRIAGE_SAMPLE overrides the classifier's SampleMax.
func sampleMax(kind string) int {
	r, ok := policy[kind]
	if !ok {
		return 0
	}
	if kind == "classifier" {
		if v := strings.TrimSpace(os.Getenv("SWARMERY_TRIAGE_SAMPLE")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				return n
			}
		}
	}
	return r.SampleMax
}
