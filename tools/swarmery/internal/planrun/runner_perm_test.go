package planrun

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + argFile + "; done\nexit " + strconv.Itoa(0) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)
}

// A plan run orchestrates real work — it must be able to write, run and commit.
// Without --permission-mode every such call is auto-denied in a headless run and
// the process still exits 0 (internal/claudeflags).
func TestClaudeRunner_Start_PassesPermissionMode(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")

	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaudeArgs(t, argFile)
	r := ClaudeRunner{Timeout: 30 * time.Second}
	if _, err := r.Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u-pm1", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, _ := os.ReadFile(argFile)
	if got := strings.TrimSpace(string(raw)); !strings.Contains(got, "--permission-mode\n"+claudeflags.DefaultMode) {
		t.Errorf("args missing --permission-mode %s:\n%s", claudeflags.DefaultMode, got)
	}
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

// TestClaudeRunner_Start_FullArgvPin is the adapter-side half of the runcore argv
// pin (internal/runcore/spawner_test.go, case planrun/full): every flag this
// engine can emit, in the order it emits them. --agent MUST land before
// --settings, because the settings file is what enables the plugin the agent
// ships in; nothing but this assertion would catch a reordering, since `claude`
// itself is order-insensitive.
func TestClaudeRunner_Start_FullArgvPin(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "")
	t.Setenv(modelEnv, "claude-opus-5")

	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaudeArgs(t, argFile)
	settings := filepath.Join(t.TempDir(), "settings.json")

	r := ClaudeRunner{Timeout: 30 * time.Second}
	if _, err := r.Start(context.Background(), RunSpec{
		Prompt: "run plan", SessionUUID: "u-full", Cwd: t.TempDir(),
		Agent: "tech-lead", SettingsFile: settings,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{
		"-p", "run plan", "--session-id", "u-full",
		"--permission-mode", claudeflags.DefaultMode, "--agent", "tech-lead",
		"--model", "claude-opus-5", "--effort", DefaultEffort, "--settings", settings,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}
