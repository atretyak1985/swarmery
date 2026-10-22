package dispatch

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// See phaserun's twin: bypassPermissions stays, the deny list rides along.
func TestClaudeRunner_Start_ExecutorKeepsBypassWithDenyList(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")
	t.Setenv(claudeflags.AgentDenyEnv, "")

	got := spawnArgs(t, RunSpec{Prompt: "p", SessionUUID: "u-deny"})
	if !strings.Contains(got, "--permission-mode bypassPermissions") {
		t.Errorf("executor argv lost bypassPermissions: %s", got)
	}
	if !strings.Contains(got, "--disallowedTools ") || !strings.Contains(got, "Bash(git push*)") {
		t.Errorf("executor argv lacks the push/PR deny list: %s", got)
	}
}
