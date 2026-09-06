package planning

import (
	"encoding/json"
	"strings"
)

// PlanningOption is one selectable answer of a wizard question.
type PlanningOption struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Pros        []string `json:"pros,omitempty"`
	Cons        []string `json:"cons,omitempty"`
	IsOther     bool     `json:"isOther,omitempty"`
	// Recommended marks the one option the planner would pick itself; the
	// dashboard highlights it so the operator sees the model's lean at a glance.
	Recommended bool `json:"recommended,omitempty"`
}

// PlanningSummary is the running plan rebuilt after every answer.
type PlanningSummary struct {
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	ProposedChanges    []string `json:"proposedChanges,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	SuggestedSize      string   `json:"suggestedSize,omitempty"` // S|M|L
}

// PlanningQuestion is one structured interview question.
type PlanningQuestion struct {
	ID          string           `json:"id"`
	Type        string           `json:"type"` // single_select|multi_select
	Question    string           `json:"question"`
	Description string           `json:"description,omitempty"`
	Options     []PlanningOption `json:"options"`
	RunningPlan *PlanningSummary `json:"runningPlan,omitempty"`
}

// protocolEnvelope is the wire shape the agent must emit.
type protocolEnvelope struct {
	Type string           `json:"type"` // "question"
	Data PlanningQuestion `json:"data"`
}

// ParsedTurn is the parse result for one assistant turn.
type ParsedTurn struct {
	Question  *PlanningQuestion // nil ⇒ contract not met (raw-text fallback)
	Reasoning string            // turn text minus the JSON block, trimmed
}

// ParseTurn parses one assistant reply turn.  It extracts the structured
// PlanningQuestion from the last fenced ```json block in text (falling back to
// a bare top-level {…} span), validates it against the protocol contract, and
// returns both the question (nil on any violation) and the remaining text as
// Reasoning.
//
// One repair pass is attempted before validation: truncated JSON is closed and
// trailing commas are stripped, because truncation can occur when the model
// hits its output limit mid-block.
func ParseTurn(text string) ParsedTurn {
	block, remaining := extractJSONBlock(text)
	if block == "" {
		return ParsedTurn{Reasoning: strings.TrimSpace(text)}
	}

	env, err := parseEnvelope(block)
	if err != nil {
		repaired := repairJSON(block)
		env, err = parseEnvelope(repaired)
		if err != nil {
			return ParsedTurn{Reasoning: strings.TrimSpace(remaining)}
		}
	}

	if !validateEnvelope(env) {
		return ParsedTurn{Reasoning: strings.TrimSpace(remaining)}
	}

	q := env.Data
	return ParsedTurn{
		Question:  &q,
		Reasoning: strings.TrimSpace(remaining),
	}
}

// extractJSONBlock finds the candidate JSON span and returns it together with
// the surrounding text (remaining) with the block and its fence removed.
//
// Priority:
//  1. Last ```json ... ``` fenced block.
//  2. Last top-level {…} span found by brace-matching from the last {"type"
//     occurrence (falls back to last lone `{`).
func extractJSONBlock(text string) (block, remaining string) {
	// 1. Try last ```json fence.
	const fenceOpen = "```json"
	const fenceClose = "```"

	last := strings.LastIndex(text, fenceOpen)
	if last >= 0 {
		afterOpen := text[last+len(fenceOpen):]
		// skip optional newline right after the fence marker
		if len(afterOpen) > 0 && afterOpen[0] == '\n' {
			afterOpen = afterOpen[1:]
		}
		closeIdx := strings.Index(afterOpen, fenceClose)
		if closeIdx >= 0 {
			block = strings.TrimSpace(afterOpen[:closeIdx])
			// remaining = everything before the opening fence + everything after
			// the closing fence+backticks
			afterClose := afterOpen[closeIdx+len(fenceClose):]
			remaining = text[:last] + afterClose
			return block, remaining
		}
		// Unclosed fence — take everything after the opening marker as the block
		// (truncated case); remaining is just what precedes the fence.
		block = strings.TrimSpace(afterOpen)
		remaining = text[:last]
		return block, remaining
	}

	// 2. Brace-match from last {"type" occurrence (or last bare `{`).
	startIdx := strings.LastIndex(text, `{"type"`)
	if startIdx < 0 {
		startIdx = strings.LastIndex(text, "{")
	}
	if startIdx < 0 {
		return "", text
	}

	end := braceEnd(text, startIdx)
	if end < 0 {
		// No closing brace found — treat remainder as the (truncated) block.
		block = text[startIdx:]
		remaining = text[:startIdx]
		return block, remaining
	}

	block = text[startIdx : end+1]
	remaining = text[:startIdx] + text[end+1:]
	return block, remaining
}

// braceEnd finds the index of the closing `}` that matches the `{` at pos,
// using a string-aware scanner so braces inside string literals are ignored.
// Returns -1 if the JSON is truncated.
func braceEnd(text string, pos int) int {
	depth := 0
	inString := false
	escaped := false
	for i := pos; i < len(text); i++ {
		ch := text[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' || ch == '[' {
			depth++
		} else if ch == '}' || ch == ']' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseEnvelope unmarshals raw JSON into a protocolEnvelope.
func parseEnvelope(raw string) (protocolEnvelope, error) {
	var env protocolEnvelope
	err := json.Unmarshal([]byte(raw), &env)
	return env, err
}

// repairJSON applies three lightweight repairs so a single retry can succeed
// on the most common model truncation / formatting mistakes:
//
//  1. Trim trailing non-JSON text after the last balanced closing brace/bracket.
//  2. Close unclosed `{` and `[` (string-aware counter).
//  3. Strip trailing commas before `}` or `]`.
func repairJSON(s string) string {
	s = strings.TrimSpace(s)

	// Step 1: trim trailing garbage after the last balanced top-level closing brace.
	s = trimTrailingGarbage(s)

	// Step 3 before step 2 (strip trailing commas) so the unmatched-bracket count
	// in step 2 is cleaner.
	s = stripTrailingCommas(s)

	// Step 2: close unclosed brackets/braces.
	s = closeUnclosed(s)

	// Step 3 again after we may have introduced new closing chars.
	s = stripTrailingCommas(s)

	return s
}

// trimTrailingGarbage truncates s after the position where the outermost
// brace/bracket first reaches depth 0, discarding any trailing prose that
// follows a fully balanced JSON object or array.  If no balanced span is found
// (truncated JSON) the original string is returned unchanged so that step 2
// (closeUnclosed) can still repair it.
func trimTrailingGarbage(s string) string {
	inString := false
	escaped := false
	depth := 0
	started := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' || ch == '[' {
			depth++
			started = true
		} else if ch == '}' || ch == ']' {
			depth--
			if started && depth == 0 {
				// First position where the top-level object/array is fully closed.
				return s[:i+1]
			}
		}
	}
	// No balanced span found — return unchanged for step 2 to handle.
	return s
}

// closeUnclosed counts unmatched `{` and `[` (string-aware) and appends the
// corresponding closing characters in LIFO order.
func closeUnclosed(s string) string {
	var stack []byte
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			stack = append(stack, byte('}'))
		case '[':
			stack = append(stack, byte(']'))
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	// Close an unclosed string first.
	if inString {
		s += `"`
	}
	// Append closing brackets in reverse order.
	var sb strings.Builder
	sb.WriteString(s)
	for i := len(stack) - 1; i >= 0; i-- {
		sb.WriteByte(stack[i])
	}
	return sb.String()
}

// stripTrailingCommas removes commas that immediately precede a `}` or `]`
// (possibly with whitespace between them), which are invalid in JSON but
// emitted by some models.
//
// Uses the same inString/escaped scanning idiom as braceEnd, trimTrailingGarbage,
// and closeUnclosed so that commas inside string literals are never touched.
func stripTrailingCommas(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			b.WriteByte(ch)
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			b.WriteByte(ch)
			continue
		}
		if ch == '"' {
			inString = !inString
			b.WriteByte(ch)
			continue
		}
		if inString {
			b.WriteByte(ch)
			continue
		}
		// Outside a string literal: check if this is a trailing comma.
		if ch == ',' {
			// Look ahead (skipping whitespace) for } or ].
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue // drop this trailing comma
			}
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// validateEnvelope checks the parsed envelope against the protocol contract.
func validateEnvelope(env protocolEnvelope) bool {
	if env.Type != "question" {
		return false
	}
	q := env.Data
	if q.ID == "" || q.Question == "" {
		return false
	}
	if q.Type != "single_select" && q.Type != "multi_select" {
		return false
	}
	// At least 2 options, each with non-empty id and label.
	valid := 0
	for _, o := range q.Options {
		if o.ID != "" && o.Label != "" {
			valid++
		}
	}
	return valid >= 2
}
