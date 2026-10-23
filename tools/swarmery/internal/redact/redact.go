// Package redact owns the daemon's secret-shape rules and the redactors built
// on them.
//
// The rules started as the response-layer redaction of /api/system/* (hook
// commands, agent/skill bodies, version content — internal/api keeps that use
// through a thin wrapper, and the system registry keeps ground truth so Stage 2
// rollback restores originals). The transcript ingest now runs them too,
// before anything reaches a table, and the rescrub worker (rescrub.go) applies
// them to rows stored before that. Pattern classes come verbatim from
// docs/system-config-format.md §8 (Redaction survey): 11 classes. Only the
// VALUE side is masked (key names stay visible so the UI stays diagnosable).
package redact

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

// literalRules is the subset of redactRules that matches a token by its own
// shape (classes 1–8 and 11). The two contextual classes, bearer and
// generic_key_value, key off a WORD next to a value; inside source code that
// word is an ordinary identifier (an assignment to a variable named token), so
// running them over file contents and diffs would mangle the stored code.
var literalRules = selectRules(func(name string) bool {
	return name != "bearer" && name != "generic_key_value"
})

func selectRules(keep func(name string) bool) []redactRule {
	var out []redactRule
	for _, r := range redactRules {
		if keep(r.name) {
			out = append(out, r)
		}
	}
	return out
}

// privateKeyRule masks the body of a PEM private-key block, keeping the BEGIN
// and END lines. A block cut short by truncation (no END line) is masked to the
// end of the text. It is NOT one of the 11 response-layer classes — those are
// pinned to guard-pack's secrets.json by redact_parity_test.go, and the API's
// response redaction keeps exactly them (Response) — so it is added only to
// the storage redactors, All and Literal.
var privateKeyRule = redactRule{
	"private_key",
	regexp.MustCompile(`(-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----)(?:[\s\S]*?(-----END [A-Z0-9 ]*PRIVATE KEY-----)|[\s\S]*$)`),
	"${1}" + redactedMark + "${2}",
}

// Storage-only token shapes, matched by their own prefix: they join Literal.
// Like privateKeyRule they are not response-layer classes (guard-pack's port
// keeps exactly those 11).
var storageLiteralExtra = []redactRule{
	// OpenAI-shaped keys (sk-proj-…, sk-…). The word boundary keeps words
	// such as risk-… and task-… out; an Anthropic key is masked earlier.
	{"openai_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), redactedMark},
	// Stripe live secret and restricted keys.
	{"stripe_key", regexp.MustCompile(`\b[rs]k_live_[A-Za-z0-9]{10,}`), redactedMark},
}

// Storage-only contextual shapes: a scheme word or a flag next to a value.
// Like bearer and generic_key_value they stay out of Literal (code).
var storageContextExtra = []redactRule{
	// An Authorization header with the Basic scheme, plain or JSON-quoted —
	// keep the header, the scheme word and the separator.
	{"basic_auth", regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?basic\s+)[A-Za-z0-9+/]+={0,2}`), "${1}" + redactedMark},
	// A password passed on a command line (--password <v>, --password=<v>,
	// --passwd …). A value that starts with "-" is the next flag, and
	// --password-file names a file.
	{"cli_password", regexp.MustCompile(`(?i)(--pass(?:word|wd)(?:=|\s+))[^\s-]\S*`), "${1}" + redactedMark},
}

// storageGenericKeyValue is the storage form of generic_key_value: the same
// keys, but a token-named key followed by a number is a count, not a
// credential — tokens: 1234, max_tokens=4096, input_tokens: 1,234 stay as
// they are. A token-named key masks a value only when the value holds a
// character other than digits and . , ; : ) ] } " '. The other keys
// (password, secret, …) mask any value, digits included.
var storageGenericKeyValue = redactRule{
	"generic_key_value",
	regexp.MustCompile(`(?i)((?:secret|passwd|password|api[_-]?key|credential)[A-Za-z0-9_-]*\s*[=:]\s*)\S+` +
		`|(token[A-Za-z0-9_-]*\s*[=:]\s*)\S*[^\s0-9.,;:)\]}"']\S*`),
	"${1}${2}" + redactedMark,
}

// storageRules / storageLiteralRules are what All and Literal apply: the
// response-layer classes (with storageGenericKeyValue in generic_key_value's
// place), PEM blocks, then the storage-only shapes.
var (
	storageRules = concatRules(swapGeneric(redactRules), []redactRule{privateKeyRule},
		storageLiteralExtra, storageContextExtra)
	storageLiteralRules = concatRules(literalRules, []redactRule{privateKeyRule}, storageLiteralExtra)
)

func concatRules(lists ...[]redactRule) []redactRule {
	var out []redactRule
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// swapGeneric returns rules with generic_key_value replaced by its storage
// form.
func swapGeneric(rules []redactRule) []redactRule {
	out := append([]redactRule(nil), rules...)
	for i, r := range out {
		if r.name == "generic_key_value" {
			out[i] = storageGenericKeyValue
		}
	}
	return out
}

func apply(rules []redactRule, s string) string {
	for _, r := range rules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// All masks every secret-shaped value in s before it is stored: the 11 classes
// (counts after a token-named key excepted), PEM private-key blocks and the
// storage-only shapes (value side only). It is idempotent: All(All(s)) == All(s).
func All(s string) string { return apply(storageRules, s) }

// Literal masks only the literal token shapes (classes 1–8 and 11, PEM
// private-key blocks, and the storage-only OpenAI and Stripe shapes). It is
// the redactor for code: Write/Edit content and file-change diffs.
func Literal(s string) string { return apply(storageLiteralRules, s) }

// Response is the API's response-layer redaction: exactly the 11 classes.
func Response(s string) string { return apply(redactRules, s) }

// Mark is the replacement every masked value becomes.
const Mark = redactedMark

// RuleNames lists the 11 response-layer rule classes in application order.
func RuleNames() []string {
	out := make([]string, len(redactRules))
	for i, r := range redactRules {
		out[i] = r.name
	}
	return out
}

// CountClasses lists the classes Count reports, in All's order.
func CountClasses() []string {
	out := make([]string, len(storageRules))
	for i, r := range storageRules {
		out[i] = r.name
	}
	return out
}

// Count reports, per rule class, how many values All would mask in s — never
// the values themselves. Rules are counted in All's order on the text as All
// leaves it after the earlier rules, and a match whose replacement equals the
// match (an already masked value) is not counted, so Count(All(s)) is empty.
func Count(s string) map[string]int { return countWith(storageRules, s) }
