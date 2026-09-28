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
// Order matters only for overlap: the spend-limit line is checked before the
// model line, which keys on the capitalised "Run /usage-credits".
// A CLI wording change degrades a limit to StatusUnknown — the pre-existing
// behaviour — never to a wrong verdict.
var limitMarkers = []limitMarker{
	{all: []string{"You've hit your session limit"}, scope: "session"},
	{all: []string{"You've hit your individual spend limit"}, scope: "weekly"},
	{all: []string{"You've reached your", "Run /usage-credits"}, scope: "model"},
	{all: []string{"Usage limit reached"}, scope: ""},
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
