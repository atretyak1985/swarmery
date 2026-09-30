package verify

import (
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
)

// Verification has no admission gate of its own, but its verdicts still open the
// account's breaker: a verifier whose `claude` is refused by the API reaches the
// hook as no-login with the access-refused reason, a usage limit as limited,
// and a verifier that exited zero stays ready whatever it printed.
func TestVerifyAccountVerdictHookAccessRefusedExit(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	spec := RunSpec{Prompt: "p", SessionUUID: "verdict-org", Resolution: claudeacct.Resolution{Account: "nabu-org"}}

	for _, tc := range []struct {
		name, script string
		want         claudeprobe.Result
	}{
		{"access refused",
			`echo 'grading…'; echo 'Your organization has disabled Claude subscription access'; exit 1`,
			claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonAccessRefused}},
		{"out of credits",
			`echo "You're out of usage credits. Add more to continue."; exit 1`,
			claudeprobe.Result{Status: claudeprobe.StatusLimited, Reason: claudeprobe.ReasonRateLimited}},
		{"zero exit over a failure line",
			`echo 'Your organization has disabled Claude subscription access'; exit 0`,
			claudeprobe.Result{Status: claudeprobe.StatusReady}},
		{"API error",
			`echo 'API Error: 529 Overloaded'; exit 1`,
			claudeprobe.Result{Status: claudeprobe.StatusUnknown, Reason: claudeprobe.ReasonUnrecognised}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeClaudeRunner(t, tc.script)
			_, calls, account, result := runWithVerdictHook(t, spec)
			if calls != 1 || account != "nabu-org" {
				t.Fatalf("hook calls = %d account = %q, want one call for nabu-org", calls, account)
			}
			if result != tc.want {
				t.Errorf("verdict = %+v, want %+v", result, tc.want)
			}
		})
	}
}
