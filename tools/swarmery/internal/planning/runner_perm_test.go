package planning

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// fakeClaudeArgs points the runner at a script that dumps its argv (one per
// line) into argFile and exits 0.
func fakeClaudeArgs(t *testing.T, argFile string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fakeclaude.sh")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + argFile + "; done\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)
}

// A planner run's entire product is files: the plan dir it writes under the
// private workspace. Without --permission-mode every Write and every mkdir is
// auto-denied in a headless run and the process still exits 0, so the run is
// recorded as a clean success while the plan exists only in a reply nobody
// stores (internal/claudeflags).
//
// Its cwd is the operator's PRIMARY checkout, though, so the mode is dontAsk with
// an allow-list rather than bypassPermissions: read anything, write only under
// the dirs the service names, and no Bash beyond read-only git history.
func TestClaudeRunner_Start_PassesPermissionMode(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")

	argv := plannerArgv(t, RunSpec{Prompt: "p", SessionUUID: "u-pm1", Cwd: t.TempDir()})
	if !strings.Contains(strings.Join(argv, "\n"), "--permission-mode\ndontAsk") {
		t.Errorf("args missing --permission-mode dontAsk:\n%s", strings.Join(argv, "\n"))
	}
}

// The planner's default argv: no bypassPermissions anywhere, and an allow-list
// that carries no unrestricted Bash — only the read-only git history rules.
func TestClaudeRunner_Start_DefaultIsLeastPrivilege(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")

	argv := plannerArgv(t, RunSpec{Prompt: "p", SessionUUID: "u-pm3", Cwd: t.TempDir()})
	joined := strings.Join(argv, "\n")
	if strings.Contains(joined, "bypassPermissions") {
		t.Fatalf("planner argv carries bypassPermissions:\n%s", joined)
	}
	allowed := flagValue(argv, "--allowedTools")
	if allowed == "" {
		t.Fatalf("planner argv has no --allowedTools:\n%s", joined)
	}
	for _, tool := range strings.Split(allowed, ",") {
		if tool == "Bash" || strings.HasPrefix(tool, "Bash(") && !strings.HasPrefix(tool, "Bash(git log") && !strings.HasPrefix(tool, "Bash(git show") {
			t.Errorf("planner allow-list carries %q — no Bash beyond read-only git history in the primary checkout", tool)
		}
		if strings.HasPrefix(tool, "Write(") {
			t.Errorf("planner allow-list carries %q — Write(//abs/**) grants nothing on 2.1.308, the rule must be Edit(...)", tool)
		}
	}
	for _, want := range []string{"Read", "Grep", "Glob"} {
		if !strings.Contains(","+allowed+",", ","+want+",") {
			t.Errorf("planner allow-list %q lacks %s", allowed, want)
		}
	}
}

// The writable dirs the service names become path-confined Edit rules.
func TestClaudeRunner_Start_WritableDirsBecomeEditRules(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")
	ws := t.TempDir()

	argv := plannerArgv(t, RunSpec{Prompt: "p", SessionUUID: "u-pm4", Cwd: t.TempDir(), WritableDirs: []string{ws}})
	if allowed := flagValue(argv, "--allowedTools"); !strings.Contains(allowed, "Edit(/"+ws+"/**)") {
		t.Errorf("allow-list %q lacks the workspace Edit rule for %s", allowed, ws)
	}
}

func plannerArgv(t *testing.T, spec RunSpec) []string {
	t.Helper()
	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaudeArgs(t, argFile)
	r := ClaudeRunner{Timeout: 30 * time.Second}
	if _, err := r.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, _ := os.ReadFile(argFile)
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func flagValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func TestClaudeRunner_Start_PermissionModeOffOmitsFlag(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "off")

	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaudeArgs(t, argFile)
	r := ClaudeRunner{Timeout: 30 * time.Second}
	if _, err := r.Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u-pm2", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, _ := os.ReadFile(argFile)
	if got := strings.TrimSpace(string(raw)); strings.Contains(got, "--permission-mode") {
		t.Errorf("args carry --permission-mode although %s=off:\n%s", permEnv, got)
	}
}
