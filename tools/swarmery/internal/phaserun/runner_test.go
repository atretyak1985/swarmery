package phaserun

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// fakeClaude writes a shell script that dumps its argv (one per line) to
// argFile, emits the given stderr, and exits with code. SWARMERY_CLAUDE_BIN
// points the runner at it (resolution reuses planning.ClaudeBin).
func fakeClaude(t *testing.T, argFile, stderr string, code int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fakeclaude.sh")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + argFile + "; done\n"
	if stderr != "" {
		body += "echo " + stderr + " 1>&2\n"
	}
	body += "exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)
}

func TestClaudeRunner_Start_DefaultNoModelFlag(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, argFile, "", 0)
	// Deliberately SET: the runner no longer reads the environment (the service
	// resolves the ladder and fills RunSpec.Model), so an env value with an empty
	// spec must still produce no --model flag.
	t.Setenv(modelEnv, "claude-opus-5")
	t.Setenv(permEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")

	r := ClaudeRunner{Timeout: 30 * time.Second}
	run, err := r.Start(context.Background(), RunSpec{
		Prompt: "execute phase", SessionUUID: "u1", Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.ExitCode != 0 || run.TimedOut {
		t.Errorf("run = %+v, want clean exit", run)
	}
	if run.SessionUUID != "u1" {
		t.Errorf("SessionUUID = %q, want u1", run.SessionUUID)
	}
	raw, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	// The permission mode is NOT optional decoration: without it every Write,
	// Edit and un-allowlisted Bash call in the run is auto-denied (no approver
	// exists in a headless run) and the process still exits 0 — a phase recorded
	// as a clean success that landed nothing (internal/claudeflags).
	want := []string{"-p", "execute phase", "--session-id", "u1", "--permission-mode", "bypassPermissions"}
	if len(args) != len(want) {
		t.Fatalf("args = %q, want %q", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

// A model on the spec — whatever rung of the ladder put it there — reaches
// --model. The ladder itself is pinned service-side in model_test.go.
func TestClaudeRunner_Start_ModelFromSpec(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, argFile, "", 0)
	t.Setenv(modelEnv, "") // proves the flag comes from the spec, not the env

	r := ClaudeRunner{Timeout: 30 * time.Second}
	if _, err := r.Start(context.Background(), RunSpec{
		Prompt: "p", SessionUUID: "u2", Cwd: t.TempDir(), Model: "claude-opus-5",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, _ := os.ReadFile(argFile)
	got := strings.TrimSpace(string(raw))
	if !strings.Contains(got, "--model\nclaude-opus-5") {
		t.Errorf("args missing --model claude-opus-5:\n%s", got)
	}
}

func TestClaudeRunner_Start_NonzeroExit(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, argFile, "boom", 7)

	r := ClaudeRunner{Timeout: 30 * time.Second}
	run, err := r.Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u3", Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start returned error for nonzero exit (should be an outcome): %v", err)
	}
	if run.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", run.ExitCode)
	}
	if run.Stderr != "boom" {
		t.Errorf("Stderr = %q, want boom", run.Stderr)
	}
}

func TestClaudeRunner_Start_Timeout(t *testing.T) {
	script := filepath.Join(t.TempDir(), "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)

	r := ClaudeRunner{Timeout: 100 * time.Millisecond}
	run, err := r.Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u4", Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: a timeout is an outcome, not an error: %v", err)
	}
	if !run.TimedOut || run.ExitCode != -1 {
		t.Errorf("run = %+v, want TimedOut with ExitCode -1", run)
	}
}

// TestClaudeRunner_Start_KillsDescendantTree is the OD-238 regression: when a
// run ends early (timeout or cancel — the same seam), its subprocess tree must
// be gone by the time Start returns, because the service deletes the worktree
// the moment it does. Driven by cancel-after-the-child-exists rather than a
// short timeout: spawning a fresh script costs ~300 ms on macOS, so a deadline
// tight enough to be quick would race the shell's first line.
func TestClaudeRunner_Start_KillsDescendantTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "spawner.sh")
	body := "#!/bin/sh\nsleep 30 & echo $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := ClaudeRunner{Timeout: 30 * time.Second}
		if _, err := r.Start(ctx, RunSpec{Prompt: "p", SessionUUID: "u6", Cwd: dir}); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()

	pid := waitForPid(t, pidFile)
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}

	// Start returns only once the group is drained, so no polling is needed here.
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL) // don't leak from a failing test
		t.Errorf("descendant %d survived the cancelled run", pid)
	}
}

// waitForPid reads the pid a spawned child wrote, once the file is non-empty.
func waitForPid(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(file)
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr != nil {
				t.Fatalf("pid file %q: %v", raw, convErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child never wrote its pid to %s", file)
	return 0
}

func TestTimeoutFromEnv(t *testing.T) {
	tests := []struct {
		name, env string
		want      time.Duration
	}{
		{"unset", "", phaseRunTimeout},
		{"override", "90m", 90 * time.Minute},
		{"unparseable falls back", "soon", phaseRunTimeout},
		{"non-positive falls back", "0s", phaseRunTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(timeoutEnv, tc.env)
			if got := timeoutFromEnv(); got != tc.want {
				t.Errorf("timeoutFromEnv() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestClaudeRunner_Start_BinMissing(t *testing.T) {
	t.Setenv("SWARMERY_CLAUDE_BIN", filepath.Join(t.TempDir(), "does-not-exist"))
	r := ClaudeRunner{Timeout: 5 * time.Second}
	run, err := r.Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u5", Cwd: t.TempDir()})
	if err == nil {
		t.Fatal("expected a start error for a missing binary")
	}
	if run == nil || run.ExitCode != -1 {
		t.Errorf("run = %+v, want ExitCode -1", run)
	}
}

// tail now lives in internal/runcore (one copy for all five engines); its test
// moved with it to internal/runcore/spawner_test.go.

// The site knob must reach the CLI, and its escape hatch must drop the flag
// entirely — an operator debugging a permission question needs both.
func TestClaudeRunner_Start_PermissionModeKnob(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "")

	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, argFile, "", 0)
	t.Setenv(permEnv, "acceptEdits")
	if _, err := r0().Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u-pm1", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, _ := os.ReadFile(argFile)
	if got := strings.TrimSpace(string(raw)); !strings.Contains(got, "--permission-mode\nacceptEdits") {
		t.Errorf("args missing --permission-mode acceptEdits:\n%s", got)
	}

	offFile := filepath.Join(t.TempDir(), "args-off")
	fakeClaude(t, offFile, "", 0)
	t.Setenv(permEnv, "off")
	if _, err := r0().Start(context.Background(), RunSpec{Prompt: "p", SessionUUID: "u-pm2", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, _ = os.ReadFile(offFile)
	if got := strings.TrimSpace(string(raw)); strings.Contains(got, "--permission-mode") {
		t.Errorf("args carry --permission-mode although the knob is off:\n%s", got)
	}
}

// r0 is the runner shape every test here uses (a short timeout so a hung fake
// cannot wedge the suite).
func r0() ClaudeRunner { return ClaudeRunner{Timeout: 30 * time.Second} }

// TestClaudeRunner_Start_FullArgvPin is the adapter-side half of the runcore argv
// pin (internal/runcore/spawner_test.go, case phaserun/full): every flag this
// engine can emit, in the order it emits them. The knob-by-knob tests above each
// cover one flag; this is the only assertion that pins --settings' position, and
// position is exactly what a shared builder can silently change.
func TestClaudeRunner_Start_FullArgvPin(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, argFile, "", 0)
	t.Setenv(claudeflags.ModeEnv, "")
	t.Setenv(permEnv, "acceptEdits")
	t.Setenv(modelEnv, "")
	settings := filepath.Join(t.TempDir(), "settings.json")

	if _, err := r0().Start(context.Background(), RunSpec{
		Prompt: "execute phase", SessionUUID: "u-full", Cwd: t.TempDir(), SettingsFile: settings,
		Model: "claude-opus-5",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	raw, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{
		"-p", "execute phase", "--session-id", "u-full",
		"--permission-mode", "acceptEdits", "--model", "claude-opus-5", "--settings", settings,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}
