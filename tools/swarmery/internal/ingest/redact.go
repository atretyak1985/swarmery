package ingest

// Ingest-time redaction: secret-shaped values in a transcript are masked
// before any row is written. The rules and the payload walk live in
// internal/redact (Literal for the file-editing tools' code fields, All for
// everything else).
//
// Redaction runs before truncation wherever a value is cut (a token clipped at
// the cut would no longer match its own shape), and once more over the whole
// payload at every events write — rows recovered from the database by an
// incremental tail may predate this code.

import "github.com/atretyak1985/swarmery/tools/swarmery/internal/redact"

// redactPayload returns m redacted for a payload of toolName; m itself is
// never modified.
func redactPayload(toolName string, m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := redact.Value(toolName, m)
	return out.(map[string]any)
}

// redactResult is redactPayload for a decoded tool result of any shape.
func redactResult(toolName string, v any) any {
	out, _ := redact.Value(toolName, v)
	return out
}
