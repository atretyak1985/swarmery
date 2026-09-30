package claudeprobe

import "strings"

// noLoginMarkers are the CLI's recorded no-login output shapes, one per
// invocation family: the `auth status` JSON field, the plain-run line
// (`Not logged in · Please run /login`, printed by `claude -p` under a
// credential-less config dir — docs/claude-cli-credential-behaviour.md §1),
// and the two auth-failure lines measured on isApiErrorMessage transcript
// records 2026-09-21 (`Failed to authenticate: OAuth session expired and could
// not be refreshed` ×44, `Login expired · Please run /login` ×2) that used to
// fall through to unknown and be discarded.
// Fixed matchers — classification never feeds output anywhere else.
var noLoginMarkers = []string{
	`"loggedIn": false`,
	"Not logged in",
	"Login expired",
	"Failed to authenticate: OAuth session expired",
}

// limitMarker is one recorded usage-limit shape. all must ALL be present
// (substring, case-sensitive, ASCII apostrophe 0x27), and scope is the fixed
// vocabulary value it maps to.
type limitMarker struct {
	all   []string
	scope string
}

// limitMarkers are the usage-limit shapes measured on isApiErrorMessage
// transcript records under both config dirs on 2026-09-21, verbatim:
//
//	You've hit your session limit · resets 1:30am (<tz>)                       → session
//	You've hit your individual spend limit · run /usage-credits … resets …     → weekly
//	You've reached your <model> limit. Run /usage-credits to continue …        → model
//	Usage limit reached                                  (the TUI line)        → ""
//
// and the three shapes measured in `turns` rows with model='<synthetic>' on
// 2026-09-30 that the list above lacked:
//
//	You've hit your weekly limit · resets Sep 19 at 5pm (<tz>)                 → weekly
//	You've hit your org's monthly spend limit · run /usage-credits …           → ""
//	You've hit your monthly spend limit. /model to switch models.              → ""
//
// The two monthly shapes map to "": the scope vocabulary (migration 0089) has
// no monthly value, and "" is its "a limit, scope not in the vocabulary" slot.
//
// Order matters only for overlap: the spend-limit line is checked before the
// model line, which keys on the capitalised "Run /usage-credits".
// A CLI wording change degrades a limit to StatusUnknown — the pre-existing
// behaviour — never to a wrong verdict.
var limitMarkers = []limitMarker{
	{all: []string{"You've hit your session limit"}, scope: "session"},
	{all: []string{"You've hit your individual spend limit"}, scope: "weekly"},
	{all: []string{"You've hit your weekly limit"}, scope: "weekly"},
	{all: []string{"You've hit your org's monthly spend limit"}, scope: ""},
	{all: []string{"You've hit your monthly spend limit"}, scope: ""},
	{all: []string{"You've reached your", "Run /usage-credits"}, scope: "model"},
	{all: []string{"Usage limit reached"}, scope: ""},
}

// AutoModeNoVerdictMarker is the fixed part of the denial a tool call gets when
// the CLI's server-side auto-mode permission check fails to answer ("The
// server-side auto mode classifier gave no verdict (error)", or "… (the
// response ended before its verdict arrived)"): a transient failure of the
// check, not a judgement about the call. A substring, never a prefix — the
// sentence opens differently per variant. Exported so every reader keys on one
// spelling.
const AutoModeNoVerdictMarker = "auto mode classifier gave no verdict"

// Failure kinds FailureKind reports. They are the d2.failure_cause values of
// the same name (internal/decide), spelled here so this package stays a leaf.
const (
	FailureAuth     = "auth"
	FailureQuota    = "quota"
	FailureAPIError = "api-error"
)

// failureShape is one recorded account/API failure line: the text must START
// with prefix and, when also is set, carry it somewhere after.
type failureShape struct {
	prefix string
	also   string
	kind   string
}

// failureShapes are the synthetic assistant texts the CLI writes in place of a
// model reply when the account or the API failed, measured verbatim in `turns`
// rows with model='<synthetic>' on 2026-09-30 (U+00B7 middle dot, ASCII
// apostrophe 0x27). The two limit shapes that date's store no longer held
// (`You've reached your <model> limit. Run /usage-credits …`, the TUI's `Usage
// limit reached`) are carried over from limitMarkers' 2026-09-21 measurement.
//
// `API Error:` is matched on the prefix alone — every tail seen (ENOTFOUND,
// `529 Overloaded`, ECONNRESET, ConnectionRefused, a connection lost or stalled
// mid-response, no response) is the same kind.
var failureShapes = []failureShape{
	{prefix: "Not logged in · Please run /login", kind: FailureAuth},
	{prefix: "Login expired · Please run /login", kind: FailureAuth},
	{prefix: "Failed to authenticate: OAuth session expired", kind: FailureAuth},
	{prefix: "Your organization has disabled Claude subscription access", kind: FailureAuth},
	{prefix: "Please run /login · API Error: 401", kind: FailureAuth},

	{prefix: "You've hit your session limit", kind: FailureQuota},
	{prefix: "You've hit your weekly limit", kind: FailureQuota},
	{prefix: "You've hit your individual spend limit", kind: FailureQuota},
	{prefix: "You've hit your org's monthly spend limit", kind: FailureQuota},
	{prefix: "You've hit your monthly spend limit", kind: FailureQuota},
	{prefix: "You've reached your", also: "Run /usage-credits", kind: FailureQuota},
	{prefix: "You're out of usage credits", kind: FailureQuota},
	{prefix: "Usage limit reached", kind: FailureQuota},

	{prefix: "API Error:", kind: FailureAPIError},
	{prefix: "Request timed out", kind: FailureAPIError},
}

// FailureKind reports whether text IS one of the recorded account/API failure
// lines and, if so, which kind: FailureAuth, FailureQuota or FailureAPIError.
// The trimmed text must START with a shape — a sentence that merely quotes a
// marker (a plan, a review, a model explaining the error) does not match, which
// is what lets this read model prose without inventing failures. An empty text
// and the CLI's `No response requested.` filler match nothing.
// The text is matched, never returned or stored.
func FailureKind(text string) (kind string, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	for _, s := range failureShapes {
		if !strings.HasPrefix(text, s.prefix) {
			continue
		}
		if s.also != "" && !strings.Contains(text[len(s.prefix):], s.also) {
			continue
		}
		return s.kind, true
	}
	return "", false
}

// FailureKindOfTail applies FailureKind to the LAST non-empty line of output —
// a run's output tail ends with the failure line when the failure is what ended
// it; an earlier line that names one is history, not the ending.
func FailureKindOfTail(output string) (kind string, ok bool) {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return FailureKind(line)
		}
	}
	return "", false
}

// LimitScope reports whether output carries one of the recorded usage-limit
// shapes and, if so, which scope: "session", "weekly", "model", or "" (a limit
// whose scope the wording does not say). ok=false means no limit shape matched.
// The output is matched, never returned or stored.
func LimitScope(output string) (scope string, ok bool) {
	for _, m := range limitMarkers {
		hit := true
		for _, s := range m.all {
			if !strings.Contains(output, s) {
				hit = false
				break
			}
		}
		if hit {
			return m.scope, true
		}
	}
	return "", false
}

// ClassifyExit maps a finished `claude` invocation to a Status. It is the SAME
// rule the probe applies to its own child process, exported so real dispatch
// and verification runs can be read as probes rather than duplicating the
// matcher — two copies of this rule are how the daemon starts disagreeing with
// itself about whether an account works.
//
// Exit-status first, wording second: zero exit → ready unconditionally (even
// when the output mentions a limit — the durable record of that case comes from
// the transcript path, internal/ingest); a non-zero exit is limited when output
// carries a recorded usage-limit shape, else no-login when it carries a recorded
// no-login shape; everything else is unknown, never ready. output is the run's
// combined tail; it is matched, never stored and never echoed into a Reason —
// Reason is always one of the fixed constants.
func ClassifyExit(exitCode int, output string) Result {
	if exitCode == 0 {
		return Result{Status: StatusReady}
	}
	if _, ok := LimitScope(output); ok {
		return Result{Status: StatusLimited, Reason: ReasonRateLimited}
	}
	for _, marker := range noLoginMarkers {
		if strings.Contains(output, marker) {
			return Result{Status: StatusNoLogin, Reason: ReasonNoLogin}
		}
	}
	return Result{Status: StatusUnknown, Reason: ReasonUnrecognised}
}
