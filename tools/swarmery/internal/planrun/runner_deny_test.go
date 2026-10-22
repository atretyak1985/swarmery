package planrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// See phaserun's twin: bypassPermissions stays, the deny list rides along.
func TestClaudeRunner_Start_ExecutorKeepsBypassWithDenyList(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")
	t.Setenv(claudeflags.AgentDenyEnv, "")

	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaudeArgs(t, argFile)
	r := ClaudeRunner{Timeout: 30 * time.Second}
	if _, err := r.Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u-deny", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, _ := os.ReadFile(argFile)
	got := string(raw)
	if !strings.Contains(got, "--permission-mode\nbypassPermissions\n") {
		t.Errorf("executor argv lost bypassPermissions:\n%s", got)
	}
	if !strings.Contains(got, "--disallowedTools\n") || !strings.Contains(got, "Bash(git push*)") {
		t.Errorf("executor argv lacks the push/PR deny list:\n%s", got)
	}
}
