package phaserun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// An executor keeps bypassPermissions (it edits, runs checks and commits in its
// own worktree), and with it the one kind of rule bypass still honours: deny
// rules. No push, no PR, no call into the daemon's own API.
func TestClaudeRunner_Start_ExecutorKeepsBypassWithDenyList(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")
	t.Setenv(claudeflags.AgentDenyEnv, "")

	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, argFile, "", 0)
	if _, err := r0().Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u-deny", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, _ := os.ReadFile(argFile)
	got := string(raw)
	if !strings.Contains(got, "--permission-mode\nbypassPermissions\n") {
		t.Errorf("executor argv lost bypassPermissions:\n%s", got)
	}
	if !strings.Contains(got, "--disallowedTools\n") || !strings.Contains(got, "Bash(git push*)") || !strings.Contains(got, "Bash(gh pr create*)") {
		t.Errorf("executor argv lacks the push/PR deny list:\n%s", got)
	}
}
