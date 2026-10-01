package claudeprobe

import "testing"

// The shared classification table: one rule for the probe AND the real
// dispatch/verify runners. Exit-status first, wording second.
func TestClassifyExit(t *testing.T) {
	cases := []struct {
		name       string
		exitCode   int
		output     string
		wantStatus Status
		wantReason string
	}{
		{"clean exit is ready", 0, `{"loggedIn": true}`, StatusReady, ""},
		{"clean exit with empty output is ready", 0, "", StatusReady, ""},
		// Exit-status first: a zero exit is ready even if the output happens
		// to contain a marker-looking string (e.g. the model quoting it).
		{"clean exit outranks marker text", 0, "Not logged in", StatusReady, ""},
		{"auth status JSON no-login", 1, `{"loggedIn": false, "authMethod": "none"}`, StatusNoLogin, ReasonNoLogin},
		// The recorded plain-run line (docs/claude-cli-credential-behaviour.md §1).
		{"plain-run no-login line", 1, "Not logged in · Please run /login", StatusNoLogin, ReasonNoLogin},
		{"marker buried in surrounding output", 1, "some banner\nNot logged in · Please run /login\n", StatusNoLogin, ReasonNoLogin},
		{"exit 1 with unrelated output", 1, "Error: something else entirely", StatusUnknown, ReasonUnrecognised},
		{"exit 1 with empty output", 1, "", StatusUnknown, ReasonUnrecognised},
		{"other nonzero exit", 3, "explosion", StatusUnknown, ReasonUnrecognised},
		// The two auth-failure shapes measured on transcript error records.
		{"login expired line", 1, "Login expired · Please run /login", StatusNoLogin, ReasonNoLogin},
		{"oauth refresh failure", 1, "Failed to authenticate: OAuth session expired and could not be refreshed", StatusNoLogin, ReasonNoLogin},
		// The four measured usage-limit shapes, at a non-zero exit.
		{"session limit", 1, limitSession, StatusLimited, ReasonRateLimited},
		{"spend limit", 1, limitSpend, StatusLimited, ReasonRateLimited},
		{"model limit", 1, limitModel, StatusLimited, ReasonRateLimited},
		{"TUI usage limit line", 1, "Usage limit reached", StatusLimited, ReasonRateLimited},
		// Contract regression guard: exit 0 stays ready even with a limit marker.
		{"clean exit outranks a limit marker", 0, limitSession, StatusReady, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyExit(tc.exitCode, tc.output)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

const (
	limitSession = "You've hit your session limit · resets 1:30am (Europe/Kiev)"
	limitSpend   = "You've hit your individual spend limit · run /usage-credits to ask your admin for a higher limit · your weekly limit resets Sep 14 at 5pm (Europe/Kiev)"
	limitModel   = "You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model."
)

func TestLimitScope(t *testing.T) {
	cases := []struct {
		output    string
		wantScope string
		wantOK    bool
	}{
		{limitSession, "session", true},
		{limitSpend, "weekly", true},
		{limitModel, "model", true},
		{"banner\nUsage limit reached\n", "", true},
		{"You've reached your destination", "", false},
		{"Not logged in", "", false},
		{"", "", false},
		// Typographic apostrophe is NOT the measured shape.
		{"You’ve hit your session limit", "", false},
		// The three shapes measured 2026-09-30 that the list lacked.
		{limitWeekly, "weekly", true},
		{limitOrgMonthly, "", true},
		{limitMonthly, "", true},
	}
	for _, tc := range cases {
		scope, ok := LimitScope(tc.output)
		if scope != tc.wantScope || ok != tc.wantOK {
			t.Errorf("LimitScope(%q) = (%q, %v), want (%q, %v)", tc.output, scope, ok, tc.wantScope, tc.wantOK)
		}
	}
	// ClassifyExit keeps its rule — exit status first, wording second — and
	// simply recognises the new shapes as limits at a non-zero exit.
	for _, out := range []string{limitWeekly, limitOrgMonthly, limitMonthly} {
		if got := ClassifyExit(1, out); got.Status != StatusLimited || got.Reason != ReasonRateLimited {
			t.Errorf("ClassifyExit(1, %q) = %+v, want limited", out, got)
		}
		if got := ClassifyExit(0, out); got.Status != StatusReady {
			t.Errorf("ClassifyExit(0, %q) = %+v, want ready", out, got)
		}
	}
}

// The synthetic failure texts measured verbatim in `turns` rows with
// model='<synthetic>' on 2026-09-30 (U+00B7 middle dot, ASCII apostrophe).
const (
	limitWeekly     = "You've hit your weekly limit · resets Sep 19 at 5pm (Europe/Kiev)"
	limitOrgMonthly = "You've hit your org's monthly spend limit · run /usage-credits to ask your admin for a higher limit · your session limit resets 1:40pm (Europe/Kiev)"
	limitMonthly    = "You've hit your monthly spend limit. /model to switch models."
)

// One case per row of the shape table, then the texts that must NOT match.
func TestFailureKind(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantKind string
		wantOK   bool
	}{
		// auth
		{"not logged in", "Not logged in · Please run /login", FailureAuth, true},
		{"login expired", "Login expired · Please run /login", FailureAuth, true},
		{"oauth session expired", "Failed to authenticate: OAuth session expired and could not be refreshed", FailureAuth, true},
		{"subscription access disabled", "Your organization has disabled Claude subscription access for Claude Code · Use an Anthropic API key instead, or ask your admin to enable access", FailureAuth, true},
		{"expired token behind a login prompt", "Please run /login · API Error: 401 OAuth access token has expired. Re-authenticate to continue.", FailureAuth, true},
		// quota
		{"session limit", limitSession, FailureQuota, true},
		{"weekly limit", limitWeekly, FailureQuota, true},
		{"individual spend limit", limitSpend, FailureQuota, true},
		{"org monthly spend limit", limitOrgMonthly, FailureQuota, true},
		{"org monthly spend limit, short form", "You've hit your org's monthly spend limit · run /usage-credits to ask your admin for a higher limit", FailureQuota, true},
		{"monthly spend limit", limitMonthly, FailureQuota, true},
		{"model limit", limitModel, FailureQuota, true},
		{"out of usage credits", "You're out of usage credits. Run /usage-credits to keep using Fable 5 or /model to switch models.", FailureQuota, true},
		{"TUI usage limit line", "Usage limit reached", FailureQuota, true},
		// api-error: the prefix alone, whatever the tail
		{"api error ENOTFOUND", "API Error: Can't reach the API server — check your internet or DNS (ENOTFOUND)", FailureAPIError, true},
		{"api error 529", "API Error: 529 Overloaded. This is a server-side issue, usually temporary — try again in a moment.", FailureAPIError, true},
		{"api error ECONNRESET", "API Error: Connection dropped (ECONNRESET)", FailureAPIError, true},
		{"api error ConnectionRefused", "API Error: Connection refused — a firewall or proxy may be blocking it (ConnectionRefused)", FailureAPIError, true},
		{"api error connection lost", "API Error: Connection lost mid-response. The response above may be incomplete.", FailureAPIError, true},
		{"api error no response", "API Error: No response from API", FailureAPIError, true},
		{"request timed out", "Request timed out", FailureAPIError, true},
		// surrounding whitespace is trimmed before matching
		{"leading and trailing whitespace", "\n  Not logged in · Please run /login \n", FailureAuth, true},

		// NOT failures
		{"empty synthetic text", "", "", false},
		{"whitespace only", " \n\t", "", false},
		{"the CLI's filler line", "No response requested.", "", false},
		{"prose quoting an auth marker mid-sentence", "The run printed Not logged in · Please run /login and stopped.", "", false},
		{"prose quoting a quota marker mid-sentence", "It said \"You've hit your session limit\" twice.", "", false},
		{"prose quoting an api error mid-sentence", "We then saw API Error: 529 Overloaded in the log.", "", false},
		{"a marker on a later line is not the start", "Summary of the run:\nAPI Error: Connection dropped (ECONNRESET)", "", false},
		{"model line without the credits hint", "You've reached your destination", "", false},
		{"the credits hint must follow the prefix", "Run /usage-credits. You've reached your limit", "", false},
		{"typographic apostrophe is not the measured shape", "You’ve hit your session limit", "", false},
		{"lower-case api error is not the measured shape", "api error: something", "", false},
		{"auto-mode denial is not an account failure", "The server-side " + AutoModeNoVerdictMarker + " (error)", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := FailureKind(tc.text)
			if kind != tc.wantKind || ok != tc.wantOK {
				t.Errorf("FailureKind(%q) = (%q, %v), want (%q, %v)", tc.text, kind, ok, tc.wantKind, tc.wantOK)
			}
		})
	}
	// Every limit shape LimitScope knows is a quota failure: the two matchers
	// must not disagree about what a limit is.
	for _, out := range []string{limitSession, limitSpend, limitModel, limitWeekly, limitOrgMonthly, limitMonthly, "Usage limit reached"} {
		if _, ok := LimitScope(out); !ok {
			t.Errorf("LimitScope does not know %q", out)
		}
		if kind, ok := FailureKind(out); !ok || kind != FailureQuota {
			t.Errorf("FailureKind(%q) = (%q, %v), want quota", out, kind, ok)
		}
	}
	if AutoModeNoVerdictMarker != "auto mode classifier gave no verdict" {
		t.Errorf("AutoModeNoVerdictMarker = %q", AutoModeNoVerdictMarker)
	}
}

// Only the LAST non-empty line of a run's output tail decides.
func TestFailureKindOfTail(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		wantKind string
		wantOK   bool
	}{
		{"failure is the last line", "working…\ndone step 1\nAPI Error: Connection dropped (ECONNRESET)", FailureAPIError, true},
		{"trailing blank lines are skipped", "banner\nNot logged in · Please run /login\n\n  \n", FailureAuth, true},
		{"a single line", limitSession, FailureQuota, true},
		{"CRLF line endings", "banner\r\nRequest timed out\r\n", FailureAPIError, true},
		{"an earlier failure line is history", "API Error: 529 Overloaded\nretried\nAll 7 criteria ticked.", "", false},
		{"last line only quotes a marker", "banner\nthe log said Not logged in · Please run /login", "", false},
		{"empty output", "", "", false},
		{"blank output", "\n \n", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := FailureKindOfTail(tc.output)
			if kind != tc.wantKind || ok != tc.wantOK {
				t.Errorf("FailureKindOfTail(%q) = (%q, %v), want (%q, %v)", tc.output, kind, ok, tc.wantKind, tc.wantOK)
			}
		})
	}
}
