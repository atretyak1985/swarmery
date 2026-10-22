package api

// phase 4: system (step-05) — response-layer redaction.
//
// Applied to hook commands, agent/skill frontmatter+body, and version
// content IN RESPONSES ONLY — the system registry keeps ground truth so Stage 2
// rollback restores originals. The rules live in internal/redact, shared with
// the transcript ingest; responses keep exactly the 11 classes.

import rules "github.com/atretyak1985/swarmery/tools/swarmery/internal/redact"

// redact masks every secret-shaped value in s (value side only).
func redact(s string) string { return rules.Response(s) }

// redactPtr redacts through a nullable column value.
func redactPtr(s *string) *string {
	if s == nil {
		return nil
	}
	out := rules.Response(*s)
	return &out
}
