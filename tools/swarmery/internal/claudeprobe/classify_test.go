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
	}
	for _, tc := range cases {
		scope, ok := LimitScope(tc.output)
		if scope != tc.wantScope || ok != tc.wantOK {
			t.Errorf("LimitScope(%q) = (%q, %v), want (%q, %v)", tc.output, scope, ok, tc.wantScope, tc.wantOK)
		}
	}
}
