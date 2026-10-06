package approvals

import (
	"encoding/json"
	"strings"
)

// toolArgFields maps a tool to the tool_input field carrying its "argument
// string" — what auto-approve rule patterns match against and what webhook
// notification bodies show. Deliberately a CLOSED allow-list
// (deny-by-default): a tool not listed here has no argument form, so
// Tool(argGlob) patterns can never match it.
var toolArgFields = map[string]string{
	"Bash":     "command",
	"Read":     "file_path",
	"Write":    "file_path",
	"Edit":     "file_path",
	"WebFetch": "url",
	"Glob":     "pattern",
	"Grep":     "pattern",
}

// argOf extracts the argument string of one tool call from its tool_input.
// ok=false when the tool has no mapping, tool_input is malformed, or the
// field is absent/empty.
func argOf(toolName string, toolInput json.RawMessage) (string, bool) {
	field, known := toolArgFields[toolName]
	if !known || len(toolInput) == 0 {
		return "", false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(toolInput, &m); err != nil {
		return "", false
	}
	var v string
	if err := json.Unmarshal(m[field], &v); err != nil {
		return "", false
	}
	return v, v != ""
}

// ArgOf is argOf for callers outside the package (the needs-you queue's
// previews), so the tool → argument-field allow-list stays in one place.
func ArgOf(toolName string, toolInput json.RawMessage) (string, bool) {
	return argOf(toolName, toolInput)
}

// truncate trims and caps s at max runes with an ellipsis (webhook bodies).
func truncate(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}
