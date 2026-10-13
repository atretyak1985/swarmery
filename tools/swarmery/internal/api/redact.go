package api

// phase 4: system (step-05) — response-layer redaction.
//
// Applied to hook commands, agent/skill frontmatter+body, and version
// content IN RESPONSES ONLY — the DB keeps ground truth so Stage 2 rollback
// restores originals. Pattern classes come verbatim from
// docs/system-config-format.md §8 (Redaction survey): 11 classes. Only the
// VALUE side is masked (key names stay visible so the UI stays diagnosable).

import "regexp"

// redactedMark replaces every secret value.
const redactedMark = "•••"

// redactRule is one pattern class; repl may reference capture groups to keep
// the key/prefix side of an assignment.
type redactRule struct {
	// name is the rule's stable identifier. Nothing here reads it: it exists so
	// redact_parity_test.go can pin plugins/guard-pack/hooks/patterns/secrets.json
	// (whose marks are [REDACTED:<name>]) to this list, rule by rule.
	name string
	re   *regexp.Regexp
	repl string
}

// redactRules — order matters: literal token shapes first (so a token inside
// an assignment or header is masked even if the generic rules also match),
// then contextual rules (Bearer, generic assignments, URL userinfo).
var redactRules = []redactRule{
	// 1. Anthropic keys
	{"anthropic_key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]+`), redactedMark},
	// 2. GitHub tokens
	{"github_token", regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+)`), redactedMark},
	// 3. GitLab tokens
	{"gitlab_token", regexp.MustCompile(`glpat-[A-Za-z0-9_-]+`), redactedMark},
	// 4. AWS access keys
	{"aws_access_key", regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}`), redactedMark},
	// 5. Slack tokens
	{"slack_token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]+`), redactedMark},
	// 6. Google API keys
	{"google_api_key", regexp.MustCompile(`AIza[A-Za-z0-9_-]{35}`), redactedMark},
	// 7. npm tokens
	{"npm_token", regexp.MustCompile(`npm_[A-Za-z0-9]{36}`), redactedMark},
	// 8. JWTs
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`), redactedMark},
	// 9. Bearer headers — keep the scheme word
	{"bearer", regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._-]+`), "${1}" + redactedMark},
	// 10. Generic assignments — keep the key name and separator
	{"generic_key_value", regexp.MustCompile(`(?i)((?:token|secret|passwd|password|api[_-]?key|credential)[A-Za-z0-9_-]*\s*[=:]\s*)\S+`), "${1}" + redactedMark},
	// 11. URL userinfo creds — keep scheme and user, mask the password
	{"url_userinfo", regexp.MustCompile(`([a-z][a-z0-9+.-]*://[^/\s:@]+:)[^/\s@]+@`), "${1}" + redactedMark + "@"},
}

// redact masks every secret-shaped value in s (value side only).
func redact(s string) string {
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// redactPtr redacts through a nullable column value.
func redactPtr(s *string) *string {
	if s == nil {
		return nil
	}
	out := redact(*s)
	return &out
}
