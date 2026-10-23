package mdfence

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// StopKind is the `kind:` value of the block a phase run writes when one of its
// guard's stop triggers fires (verify-not-trust phase 10, D4/D28): instead of
// working around the obstacle, the executor stops and asks.
//
//	```yaml
//	kind: stop
//	trigger: protected-path
//	reason: the fix needs a change to scripts/tests/run.sh, which the guard protects
//	paths:
//	  - "scripts/tests/run.sh"
//	options:
//	  - id: allow
//	    label: Allow editing scripts/tests/run.sh
//	    consequence: the re-run edits the script and finishes the phase
//	  - id: skip
//	    label: Leave the script alone
//	    consequence: the criterion that needs it stays open
//	```
const StopKind = "stop"

// Stop triggers — the closed vocabulary (the idea boundary keeps it short: do not
// add one without a plan decision). A trigger outside it is kept verbatim and
// linted (LintStopTriggerUnknown); the decision is still raised.
const (
	StopOutOfScope          = "out-of-scope"
	StopProtectedPath       = "protected-path"
	StopAccessError         = "access-error"
	StopCriterionImpossible = "criterion-impossible"
)

// StopTriggers lists the closed vocabulary in the order the prompt states it.
var StopTriggers = []string{StopOutOfScope, StopProtectedPath, StopAccessError, StopCriterionImpossible}

// Stop block lints: the block is accepted, the contract slip is named.
const (
	// LintStopTriggerUnknown: `trigger` is not one of StopTriggers.
	LintStopTriggerUnknown = "stop_trigger_unknown"
	// LintStopOptionsCount: the block offers other than exactly two options.
	LintStopOptionsCount = "stop_options_count"
)

// StopOption is one answer the executor offers the operator.
type StopOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
}

// Stop is a parsed `kind: stop` block.
type Stop struct {
	Trigger string       `json:"trigger"`
	Reason  string       `json:"reason"`
	Options []StopOption `json:"options"`
	// Paths are the files a protected-path stop is about (optional): answering
	// with an allow option adds them to the guard block's `allow:` list.
	Paths []string `json:"paths,omitempty"`
	// Lints are the contract slips (LintStopTriggerUnknown, LintStopOptionsCount).
	Lints []string `json:"lints,omitempty"`
}

// KnownTrigger reports whether trigger is in the closed vocabulary.
func KnownTrigger(trigger string) bool {
	for _, t := range StopTriggers {
		if t == trigger {
			return true
		}
	}
	return false
}

// StopTriggerFor maps a guard block's `stop_if` entry — prose the planner wrote,
// "protected path", "access or login error" — to its trigger id, or "" when the
// entry names none of them. Matching is by keyword, so the planner's wording may
// drift without the prompt losing the id.
func StopTriggerFor(stopIf string) string {
	s := strings.ToLower(stopIf)
	switch {
	case strings.Contains(s, "scope"):
		return StopOutOfScope
	case strings.Contains(s, "protected"):
		return StopProtectedPath
	case strings.Contains(s, "access"), strings.Contains(s, "login"), strings.Contains(s, "auth"):
		return StopAccessError
	case strings.Contains(s, "impossible"), strings.Contains(s, "criterion"):
		return StopCriterionImpossible
	}
	return ""
}

// AllowsScope reports whether option id is the one that lets a protected-path
// stop's paths into scope: `allow`, or any id that starts with it
// (`allow-add-to-scope`, `allow-edit`).
func AllowsScope(optionID string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(optionID)), "allow")
}

// ParseStop reads the LAST `kind: stop` yaml block of output — an executor's
// final message, where the contract puts it right before `PHASE BLOCKED:`. It
// returns (nil, nil) when output carries no such block, and an unparsable
// ParseError when the block is there but cannot be read: not yaml, not a
// mapping, no trigger or reason, or options that are not a list of mappings
// with an id and a label.
//
// A block with an unknown trigger or other than two options is NOT an error: it
// parses, with the slip named in Lints, because the operator still has to
// answer it.
func ParseStop(output string) (*Stop, *ParseError) {
	blocks := kindBlocks(output, StopKind)
	if len(blocks) == 0 {
		return nil, nil
	}
	b := blocks[len(blocks)-1]
	m, perr := decodeMapping(b)
	if perr != nil {
		return nil, perr
	}
	keys := mappingKeys(m)
	st := &Stop{}
	for _, k := range []string{"trigger", "reason"} {
		v, ok := scalar(keys[k])
		if !ok || v == "" {
			return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, 1), Reason: "missing key " + k}
		}
		if k == "trigger" {
			st.Trigger = strings.ToLower(v)
		} else {
			st.Reason = v
		}
	}
	opts, ok := keys["options"]
	if !ok || isNull(opts) {
		return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, 1), Reason: "missing key options"}
	}
	if opts.Kind != yaml.SequenceNode {
		return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, opts.Line), Reason: "options: must be a list"}
	}
	for i, o := range opts.Content {
		if o.Kind != yaml.MappingNode {
			return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, o.Line),
				Reason: fmt.Sprintf("options[%d]: must be a mapping with id, label and consequence", i)}
		}
		ok := mappingKeys(o)
		opt := StopOption{}
		opt.ID, _ = scalar(ok["id"])
		opt.Label, _ = scalar(ok["label"])
		opt.Consequence, _ = scalar(ok["consequence"])
		if opt.ID == "" || opt.Label == "" {
			return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, o.Line),
				Reason: fmt.Sprintf("options[%d]: id and label are required", i)}
		}
		st.Options = append(st.Options, opt)
	}
	if n, ok := keys["paths"]; ok {
		if st.Paths, perr = stringList(b, "paths", n); perr != nil {
			return nil, perr
		}
	}
	if !KnownTrigger(st.Trigger) {
		st.Lints = append(st.Lints, LintStopTriggerUnknown)
	}
	if len(st.Options) != 2 {
		st.Lints = append(st.Lints, LintStopOptionsCount)
	}
	return st, nil
}
