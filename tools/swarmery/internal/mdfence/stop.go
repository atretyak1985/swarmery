package mdfence

import (
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
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
	// LintStopOptionInvalid: an option has no id, an id that is not a bare token
	// (ValidOptionID), repeats an id, or uses the reserved id `other`; it was
	// renamed so every option stays answerable.
	LintStopOptionInvalid = "stop_option_invalid"
	// LintStopPathsRejected: one or more `paths:` entries were dropped — not a
	// literal repo-relative file path (LiteralRepoPath), or, once the daemon
	// checks it against the guard, under no protected glob. The executor is
	// untrusted: only what survives may reach the guard's `allow:` list.
	LintStopPathsRejected = "stop_paths_rejected"
)

// stopLintOrder is the stable order Stop.Flags are reported in.
var stopLintOrder = []string{
	LintStopTriggerUnknown, LintStopOptionsCount, LintStopReasonMissing, LintStopOptionInvalid, LintStopPathsRejected,
}

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
	// allow answer may add to the guard's `allow:` list. Only literal
	// repo-relative file paths are kept (LiteralRepoPath); the rest are dropped
	// and flagged stop_paths_rejected.
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
// kept, flattened), a count of options other than two (stop_options_count), a
// missing reason (stop_reason_missing), an option without an id, with an id that
// is not a bare token, with a repeated id or with the reserved id `other`
// (stop_option_invalid — renamed option-<n>), a `paths:` entry that is not a
// literal repo-relative file path as written (stop_paths_rejected — dropped).
//
// The block is written by the executor, which is untrusted, and its scalars end
// up in the phase doc and in the next run's prompt: every scalar returned is
// flattened onto one line (flatScalar), and the trigger and option ids — the
// tokens printed bare — lose their backticks too (token).
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
	st.Trigger = strings.ToLower(token(st.Trigger))
	if !KnownStopTrigger(st.Trigger) {
		flags[LintStopTriggerUnknown] = true
	}
	st.Reason, _ = scalar(keys["reason"])
	st.Reason = flatScalar(st.Reason)
	if st.Reason == "" {
		flags[LintStopReasonMissing] = true
	}
	paths, perr := stringList(b, "paths", keys["paths"])
	if perr != nil {
		return nil, perr
	}
	for _, p := range paths {
		// Vetted as written, never flattened first: a path the agent wrote with a
		// newline in it is not the file the flattened spelling names.
		if LiteralRepoPath(p) {
			st.Paths = append(st.Paths, p)
		} else {
			flags[LintStopPathsRejected] = true
		}
	}

	items, perr := mapList(b, "options", keys["options"])
	if perr != nil {
		return nil, perr
	}
	seen := map[string]bool{}
	for i, it := range items {
		o := StopOption{
			ID:          strings.ToLower(token(it.str("id"))),
			Label:       flatScalar(it.str("label")),
			Consequence: flatScalar(it.str("consequence")),
		}
		if !ValidOptionID(o.ID) || o.ID == StopOptionOther || seen[o.ID] {
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
	for _, f := range stopLintOrder {
		if flags[f] {
			st.Flags = append(st.Flags, f)
		}
	}
	return st, nil
}

// maxOptionIDLen bounds an option id; ids are short tokens, not sentences.
const maxOptionIDLen = 64

// ValidOptionID reports whether id is a bare option token: a lower-case letter
// or digit, then letters, digits, `.`, `_` or `-`. The daemon reads the chosen
// id back out of the recorded answer (`<id>` or `<id> — <note>`), so an id that
// could carry a space or a dash sequence would let an option that is not
// allow-add-to-scope read back as one.
func ValidOptionID(id string) bool {
	if id == "" || len(id) > maxOptionIDLen {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// flatScalar puts s on one line: every run of whitespace — newlines, carriage
// returns, tabs, the Unicode line and paragraph separators — becomes one space.
func flatScalar(s string) string { return strings.Join(strings.Fields(s), " ") }

// token is a bare token (a trigger, an option id) flattened and without
// backticks, so it can neither break a line nor open a code span or a fence.
func token(s string) string { return flatScalar(strings.ReplaceAll(s, "`", "")) }

// LiteralRepoPath reports whether p names one file literally, relative to the
// repository root — the only kind of agent-supplied path that may become an
// entry of a guard's `allow:` list, where entries act as globs. It rejects an
// empty path, glob metacharacters (`*` `?` `[` `]` `{` `}`, and bash's extglob
// openers `+(` `@(` `!(`), a leading `/`, a `..` segment, a backslash, a path
// not in clean form (`./x`, `a//b`, or a trailing `/`, which a guard reads as a
// whole directory), and anything under `.swarmery` — the guard's own inputs,
// never on offer (letter case ignored, as the hook does).
//
// It also rejects invalid UTF-8 and every rune that is not printable
// (unicode.IsPrint: control characters, non-ASCII spaces, the line and
// paragraph separators) or is a format rune (category Cf: zero-width spaces
// and joiners, the bidi embeddings and overrides U+202A–U+202E, the isolates
// U+2066–U+2069). Such a path would make the hook's guard parser fail closed
// on every call, and a bidi override could make the "releases" line the
// operator reads spell a different file than the one released.
//
// Whether the path names a FILE rather than a directory is not knowable from
// the string: the daemon checks that against the run's worktree and changed
// files before an entry is stored or offered (phaserun.vetAllowPaths).
func LiteralRepoPath(p string) bool {
	if p == "" || strings.TrimSpace(p) != p || strings.ContainsAny(p, "*?[]{}\\") || strings.HasPrefix(p, "/") ||
		!utf8.ValidString(p) {
		return false
	}
	for _, opener := range []string{"+(", "@(", "!("} {
		if strings.Contains(p, opener) {
			return false
		}
	}
	for _, r := range p {
		if !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	if p == "." || path.Clean(p) != p {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(p), ".swarmery")
}

// OptionIDs are the ids of the block's options, in order.
func (s *Stop) OptionIDs() []string {
	out := make([]string, 0, len(s.Options))
	for _, o := range s.Options {
		out = append(out, o.ID)
	}
	return out
}
