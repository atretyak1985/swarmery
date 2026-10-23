package mdfence

import (
	"fmt"
	"strings"
)

// StopKind is the `kind:` value of the block an executor writes when one of its
// phase's stop triggers fires (verify-not-trust phase 10, D4/D28): instead of
// working around the obstacle, it stops and hands the operator two options.
//
//	```yaml
//	kind: stop
//	trigger: protected-path
//	reason: the fix needs pkg/export/writer_test.go, which the guard protects
//	paths:
//	  - pkg/export/writer_test.go
//	options:
//	  - id: allow-add-to-scope
//	    label: Let me edit the test
//	    consequence: the test changes with the code it covers
//	  - id: skip-test-change
//	    label: Leave the test alone
//	    consequence: the criterion about the writer stays open
//	```
//
// The block is read from the executor's FINAL output, not from the phase doc.
const StopKind = "stop"

// The closed trigger vocabulary. The list stays short by design: a trigger is a
// reason to stop and ask, and every new one is a new way to stall a run.
const (
	StopTriggerOutOfScope          = "out-of-scope"
	StopTriggerProtectedPath       = "protected-path"
	StopTriggerAccessError         = "access-error"
	StopTriggerCriterionImpossible = "criterion-impossible"
)

// StopTriggers is the closed vocabulary, in the order the prompt lists it.
var StopTriggers = []string{
	StopTriggerOutOfScope, StopTriggerProtectedPath, StopTriggerAccessError, StopTriggerCriterionImpossible,
}

// StopOptionOther is the option id a free-text answer is recorded under. An
// executor cannot offer it: it is the operator's "neither of these".
const StopOptionOther = "other"

// Stop-block lints. A flagged block is still a stop: the run did stop and the
// operator still has to answer, so a contract slip is shown, never swallowed.
const (
	// LintStopTriggerUnknown: the trigger is missing or outside StopTriggers.
	LintStopTriggerUnknown = "stop_trigger_unknown"
	// LintStopOptionsCount: the block offers other than exactly two options.
	LintStopOptionsCount = "stop_options_count"
	// LintStopReasonMissing: the block gives no reason.
	LintStopReasonMissing = "stop_reason_missing"
	// LintStopOptionInvalid: an option has no id, repeats an id, or uses the
	// reserved id `other`; it was renamed so every option stays answerable.
	LintStopOptionInvalid = "stop_option_invalid"
)

// StopOption is one answer a stop block offers the operator.
type StopOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
}

// Stop is an executor's `kind: stop` block.
type Stop struct {
	Trigger string
	Reason  string
	Options []StopOption
	// Paths are the files a protected-path stop is about (optional): what an
	// allow answer adds to the guard's `allow:` list.
	Paths []string
	// Flags are the lints the block raised (Lint* constants), in a stable order.
	Flags []string
	// Line is the 1-based line of the block's opening fence in the output.
	Line int
}

// KnownStopTrigger reports whether trigger is in the closed vocabulary.
func KnownStopTrigger(trigger string) bool {
	for _, t := range StopTriggers {
		if t == trigger {
			return true
		}
	}
	return false
}

// ParseStop reads the LAST `kind: stop` yaml block of an executor's output — the
// last one, because it is the executor's final word on why it stopped.
//
// No block returns (nil, nil). A block that is not valid yaml, is not a mapping,
// or holds `options` / `paths` of the wrong shape returns a *ParseError
// (ErrUnparsable, with the output line). Everything softer is a flag on a parsed
// block: an unknown or missing trigger (stop_trigger_unknown — the trigger is
// kept verbatim), a count of options other than two (stop_options_count), a
// missing reason (stop_reason_missing), an option without an id, with a repeated
// id or with the reserved id `other` (stop_option_invalid — renamed option-<n>).
func ParseStop(output string) (*Stop, error) {
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
	st := &Stop{Line: b.Start + 1}
	flags := map[string]bool{}

	st.Trigger, _ = scalar(keys["trigger"])
	st.Trigger = strings.ToLower(st.Trigger)
	if !KnownStopTrigger(st.Trigger) {
		flags[LintStopTriggerUnknown] = true
	}
	st.Reason, _ = scalar(keys["reason"])
	st.Reason = strings.Join(strings.Fields(st.Reason), " ")
	if st.Reason == "" {
		flags[LintStopReasonMissing] = true
	}
	if st.Paths, perr = stringList(b, "paths", keys["paths"]); perr != nil {
		return nil, perr
	}

	items, perr := mapList(b, "options", keys["options"])
	if perr != nil {
		return nil, perr
	}
	seen := map[string]bool{}
	for i, it := range items {
		o := StopOption{
			ID:          strings.ToLower(it.str("id")),
			Label:       strings.Join(strings.Fields(it.str("label")), " "),
			Consequence: strings.Join(strings.Fields(it.str("consequence")), " "),
		}
		if o.ID == "" || o.ID == StopOptionOther || seen[o.ID] {
			flags[LintStopOptionInvalid] = true
			o.ID = fmt.Sprintf("option-%d", i+1)
			for seen[o.ID] {
				o.ID += "x"
			}
		}
		seen[o.ID] = true
		if o.Label == "" {
			o.Label = o.ID
		}
		st.Options = append(st.Options, o)
	}
	if len(st.Options) != 2 {
		flags[LintStopOptionsCount] = true
	}
	for _, f := range []string{LintStopTriggerUnknown, LintStopOptionsCount, LintStopReasonMissing, LintStopOptionInvalid} {
		if flags[f] {
			st.Flags = append(st.Flags, f)
		}
	}
	return st, nil
}

// OptionIDs are the ids of the block's options, in order.
func (s *Stop) OptionIDs() []string {
	out := make([]string, 0, len(s.Options))
	for _, o := range s.Options {
		out = append(out, o.ID)
	}
	return out
}
