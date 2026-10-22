package api

import (
	"slices"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// A composer message is data. The claude CLI's -p is a boolean (--print) and
// the prompt is positional, so a message beginning with a dash that sits among
// the flags is parsed as an option. resumeArgs puts it last, after the
// end-of-options marker, with every flag before it.
func TestResumeArgs_DashLedMessageStaysData(t *testing.T) {
	t.Setenv(resumePermEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")

	for _, text := range []string{"--add-dir=/", "-x", "--mcp-config={}"} {
		t.Run(text, func(t *testing.T) {
			// An executor origin, so the resume carries a permission mode and a
			// deny list; both must sit before the marker. (A foreign session gets
			// no mode at all — see resume_perm_test.go.)
			args := resumeArgs("u-1", text, resumeOrigin{
				Model: "m", Agent: "a", SettingsFile: "/s.json",
				Site: claudeflags.SiteExecutor, DenyArgs: claudeflags.AgentDenyArgs(0),
			})
			n := len(args)
			if n < 2 || args[n-2] != "--" || args[n-1] != text {
				t.Fatalf("argv must end with `-- <message>`: %q", args)
			}
			if slices.Index(args, "--") != n-2 {
				t.Errorf("exactly one end-of-options marker, right before the message: %q", args)
			}
			if slices.Contains(args[:n-2], text) {
				t.Errorf("message %q appears among the flags: %q", text, args)
			}
			if !slices.Contains(args[:n-2], "--permission-mode") {
				t.Errorf("--permission-mode must stay before the marker: %q", args)
			}
		})
	}
}
