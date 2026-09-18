package runcore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestArgs_PerEngineArgvPin is the neutrality pin for the runcore extraction.
//
// Every case below is the argv one of the five engines emitted BEFORE they shared
// a builder, transcribed from the runner code at main@83b6253 — a minimal spec
// (only what that engine always sets) and a fully-populated one. If a future edit
// to Args reorders, adds or drops a flag for any engine, exactly this test fails,
// which is the point: `claude` is order-insensitive, so nothing else would.
//
// The Spec in each case is the one that engine's adapter builds. Two mappings are
// worth naming because they are NOT visible in the argv:
//   - dispatch's "@agent: " mention is part of Prompt, not --agent (prompt
//     construction, which stays in dispatch); planrun's --agent is the flag.
//   - "project,local" is ONE argv element, not two.
func TestArgs_PerEngineArgvPin(t *testing.T) {
	const settings = "/proj/.claude/settings.json"

	cases := []struct {
		name string
		spec Spec
		want []string
	}{
		// ── planning: the planner run. No settings file, and --model is ALWAYS
		// emitted (the runner substitutes its own default before building the spec,
		// so the account default is never inherited). Permission mode comes from its
		// own knob, like phaserun's — a planner that cannot write produces nothing.
		{
			name: "planning/minimal",
			spec: Spec{Prompt: "plan this", SessionUUID: "u-plan", Model: "claude-opus-5", PermissionMode: "bypassPermissions"},
			want: []string{"-p", "plan this", "--session-id", "u-plan", "--permission-mode", "bypassPermissions", "--model", "claude-opus-5"},
		},
		{
			name: "planning/model-override",
			spec: Spec{Prompt: "plan this", SessionUUID: "u-plan", Model: "claude-sonnet-5", PermissionMode: "bypassPermissions"},
			want: []string{"-p", "plan this", "--session-id", "u-plan", "--permission-mode", "bypassPermissions", "--model", "claude-sonnet-5"},
		},

		// ── phaserun: one plan phase. Permission mode from its own knob, model from
		// its own knob (unset ⇒ omitted), settings file when the worktree cannot
		// discover the project's own.
		{
			name: "phaserun/minimal",
			spec: Spec{Prompt: "execute phase", SessionUUID: "u-phase", PermissionMode: "bypassPermissions"},
			want: []string{"-p", "execute phase", "--session-id", "u-phase", "--permission-mode", "bypassPermissions"},
		},
		{
			name: "phaserun/full",
			spec: Spec{
				Prompt: "execute phase", SessionUUID: "u-phase",
				PermissionMode: "acceptEdits", Model: "claude-opus-5", SettingsFile: settings,
			},
			want: []string{
				"-p", "execute phase", "--session-id", "u-phase",
				"--permission-mode", "acceptEdits", "--model", "claude-opus-5", "--settings", settings,
			},
		},

		// ── planrun: a whole plan. Same as phaserun plus --agent, which must land
		// BEFORE --settings (the settings file enables the plugin the agent ships
		// in — planrun emitted it last for that reason).
		{
			name: "planrun/minimal",
			spec: Spec{Prompt: "run plan", SessionUUID: "u-planrun", PermissionMode: "bypassPermissions"},
			want: []string{"-p", "run plan", "--session-id", "u-planrun", "--permission-mode", "bypassPermissions"},
		},
		{
			name: "planrun/full",
			spec: Spec{
				Prompt: "run plan", SessionUUID: "u-planrun",
				PermissionMode: "bypassPermissions", Agent: "tech-lead",
				Model: "claude-opus-5", SettingsFile: settings,
			},
			want: []string{
				"-p", "run plan", "--session-id", "u-planrun",
				"--permission-mode", "bypassPermissions", "--agent", "tech-lead",
				"--model", "claude-opus-5", "--settings", settings,
			},
		},

		// ── dispatch: a board card. The only engine pairing --setting-sources with
		// --permission-mode, and the one whose Prompt may already carry the agent
		// mention.
		{
			name: "dispatch/minimal",
			spec: Spec{
				Prompt: "do the task", SessionUUID: "u-task",
				SettingSources: "project,local", PermissionMode: "bypassPermissions",
			},
			want: []string{
				"-p", "do the task", "--session-id", "u-task",
				"--setting-sources", "project,local", "--permission-mode", "bypassPermissions",
			},
		},
		{
			name: "dispatch/agent-mention-and-model",
			spec: Spec{
				Prompt: "@tech-lead: do the task", SessionUUID: "u-task",
				SettingSources: "project,local", PermissionMode: "plan", Model: "claude-sonnet-5",
			},
			want: []string{
				"-p", "@tech-lead: do the task", "--session-id", "u-task",
				"--setting-sources", "project,local", "--permission-mode", "plan",
				"--model", "claude-sonnet-5",
			},
		},

		// ── verify: the read-only judge. --setting-sources like dispatch, but never
		// a permission mode (a read-only run has nothing to approve).
		{
			name: "verify/minimal",
			spec: Spec{Prompt: "grade this", SessionUUID: "u-verify", SettingSources: "project,local"},
			want: []string{"-p", "grade this", "--session-id", "u-verify", "--setting-sources", "project,local"},
		},
		{
			name: "verify/model",
			spec: Spec{
				Prompt: "grade this", SessionUUID: "u-verify",
				SettingSources: "project,local", Model: "claude-opus-5",
			},
			want: []string{
				"-p", "grade this", "--session-id", "u-verify",
				"--setting-sources", "project,local", "--model", "claude-opus-5",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Args(tc.spec); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Args() argv drifted for %s\n got: %q\nwant: %q", tc.name, got, tc.want)
			}
		})
	}
}

// An empty flag value means OMIT, not "pass an empty string": a spawn carrying
// `--model ""` dies before the run starts, which is the difference between "no
// override" and "no run".
func TestArgs_OmitsBlankFlags(t *testing.T) {
	got := Args(Spec{
		Prompt: "p", SessionUUID: "u",
		Model: "  ", Agent: "\t", PermissionMode: " ", SettingsFile: "", SettingSources: " ",
	})
	want := []string{"-p", "p", "--session-id", "u"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args() = %q, want %q", got, want)
	}
}

// Prompt and SessionUUID are values, not flags: they reach argv verbatim even
// when blank, because the caller owns what a run is asked to do.
func TestArgs_PassesPromptVerbatim(t *testing.T) {
	got := Args(Spec{Prompt: "  leading space matters\n", SessionUUID: "u"})
	if got[1] != "  leading space matters\n" {
		t.Errorf("prompt was rewritten: %q", got[1])
	}
}

func TestArgs_ExtraArgsAppendLast(t *testing.T) {
	got := Args(Spec{Prompt: "p", SessionUUID: "u", Model: "m", ExtraArgs: []string{"--verbose", "--foo=bar"}})
	want := []string{"-p", "p", "--session-id", "u", "--model", "m", "--verbose", "--foo=bar"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args() = %q, want %q", got, want)
	}
}

// ── the exit ladder ──────────────────────────────────────────────────────────
//
// Driven through real short-lived processes rather than a mocked exec: the
// ladder's whole job is to classify what a REAL child did, and the surrounding
// procgroup isolate/drain is part of the behaviour being pinned.

// fakeBin writes an executable script and returns a Spec.Bin resolving to it.
func fakeBin(t *testing.T, body string) func() (string, error) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() (string, error) { return script, nil }
}

func TestStart_CleanExit(t *testing.T) {
	r := ClaudeRunner{Engine: "test"}
	res, err := r.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-clean", Cwd: t.TempDir(),
		Bin: fakeBin(t, "exit 0\n"), Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.ExitCode != 0 || res.TimedOut {
		t.Errorf("res = %+v, want a clean exit", res)
	}
	if res.SessionUUID != "u-clean" {
		t.Errorf("SessionUUID = %q, want u-clean", res.SessionUUID)
	}
	if res.Duration <= 0 {
		t.Error("Duration was not measured")
	}
}

func TestStart_NonzeroExitIsAnOutcome(t *testing.T) {
	r := ClaudeRunner{Engine: "test"}
	res, err := r.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-fail", Cwd: t.TempDir(),
		Bin: fakeBin(t, "echo boom 1>&2\nexit 7\n"), Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("a nonzero exit must be an outcome, not an error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if res.Stderr != "boom" {
		t.Errorf("Stderr = %q, want boom", res.Stderr)
	}
}

func TestStart_TimeoutIsAnOutcome(t *testing.T) {
	r := ClaudeRunner{Engine: "test"}
	res, err := r.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-slow", Cwd: t.TempDir(),
		Bin: fakeBin(t, "sleep 10\n"), Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("a timeout must be an outcome, not an error: %v", err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Errorf("res = %+v, want TimedOut with ExitCode -1", res)
	}
}

// Timeout: 0 is dispatch's shape — the caller's ctx owns the deadline, and it
// must still classify as a timeout.
func TestStart_CallerOwnedDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res, err := ClaudeRunner{Engine: "test"}.Start(ctx, Spec{
		Prompt: "p", SessionUUID: "u-ctx", Cwd: t.TempDir(),
		Bin: fakeBin(t, "sleep 10\n"), // Timeout unset
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Errorf("res = %+v, want TimedOut with ExitCode -1", res)
	}
}

func TestStart_BinResolutionFailureIsAnError(t *testing.T) {
	wantErr := os.ErrNotExist
	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-nobin", Cwd: t.TempDir(),
		Bin: func() (string, error) { return "", wantErr },
	})
	if err != wantErr {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if res == nil || res.ExitCode != -1 || res.SessionUUID != "u-nobin" {
		t.Errorf("res = %+v, want ExitCode -1 with the uuid echoed", res)
	}
}

func TestStart_UnstartableProcessIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-miss", Cwd: t.TempDir(),
		Bin: func() (string, error) { return missing, nil }, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected a start error for a binary that cannot be executed")
	}
	if res == nil || res.ExitCode != -1 {
		t.Errorf("res = %+v, want ExitCode -1", res)
	}
}

// ── stdout ───────────────────────────────────────────────────────────────────

func TestStart_CaptureStdoutFull(t *testing.T) {
	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-out", Cwd: t.TempDir(),
		Bin: fakeBin(t, "echo VERDICT: PASS\n"), Timeout: 30 * time.Second,
		CaptureStdout: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !strings.Contains(res.Output, "VERDICT: PASS") {
		t.Errorf("Output = %q, want the verdict line", res.Output)
	}
}

// The bounded tail is dispatch's: enough to classify an exit, never a transcript.
func TestStart_StdoutTailIsBounded(t *testing.T) {
	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-tail", Cwd: t.TempDir(),
		Bin: fakeBin(t, "printf 'aaaaaaaaaa'\nprintf 'TAIL'\n"), Timeout: 30 * time.Second,
		StdoutTailBytes: 4,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.StdoutTail != "TAIL" {
		t.Errorf("StdoutTail = %q, want TAIL", res.StdoutTail)
	}
	if res.Output != "" {
		t.Errorf("Output = %q, want empty (no full capture was asked for)", res.Output)
	}
}

// A run that asked for neither leaves the child's stdout discarded at the OS
// level. This is load-bearing: it is why four of five engines put no pipe between
// the daemon and output nobody reads.
func TestStart_StdoutDiscardedByDefault(t *testing.T) {
	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-quiet", Cwd: t.TempDir(),
		Bin: fakeBin(t, "echo noise\n"), Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.Output != "" || res.StdoutTail != "" {
		t.Errorf("res captured stdout unasked: Output=%q StdoutTail=%q", res.Output, res.StdoutTail)
	}
}

// ── teardown ─────────────────────────────────────────────────────────────────

// The OD-238 regression, now owned in ONE place: when a run ends early, its whole
// subprocess tree must be gone by the time Start returns, because phaserun and
// planrun delete the worktree the moment it does.
func TestStart_KillsDescendantTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := fakeBin(t, "sleep 30 & echo $! > "+pidFile+"\nwait\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := (ClaudeRunner{Engine: "test"}).Start(ctx, Spec{
			Prompt: "p", SessionUUID: "u-tree", Cwd: dir, Bin: bin, Timeout: 30 * time.Second,
		}); err != nil {
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

// ── the shared helpers ───────────────────────────────────────────────────────

func TestTail(t *testing.T) {
	if got := Tail("  hello world  ", 5); got != "world" {
		t.Errorf("Tail = %q, want world", got)
	}
	if got := Tail("short", 100); got != "short" {
		t.Errorf("Tail = %q, want short", got)
	}
}

// uuidRE is the full RFC-4122 v4 shape, version nibble and variant bits included
// — the strictest of the five per-engine assertions this replaces.
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		u := NewUUID()
		if !uuidRE.MatchString(u) {
			t.Fatalf("NewUUID() = %q, not a v4 UUID", u)
		}
		if seen[u] {
			t.Fatalf("NewUUID() collision on %q", u)
		}
		seen[u] = true
	}
}

func TestAccountFor_EmptyProjectPathResolvesNothing(t *testing.T) {
	// The guard that matters: an empty path must never reach Binding, which would
	// join it with a RELATIVE settings path against the daemon's own cwd.
	if got := AccountFor(""); got != "" {
		t.Errorf("AccountFor(\"\") = %q, want the empty key", got)
	}
	if got := AccountFor(t.TempDir()); got != "" {
		t.Errorf("AccountFor(unbound project) = %q, want the empty key", got)
	}
}

// ── nil Spec.Bin: the production shape ───────────────────────────────────────
//
// dispatch and verify never set Bin, and for a long time nil meant "exec the
// literal string claude and let PATH find it". Under launchd the daemon's PATH
// is /usr/bin:/bin:/usr/sbin:/sbin, which contains no install dir, so both
// engines died with `exec: "claude": executable file not found in $PATH` before
// a single token was spent — reproduced live on 2026-09-17 with a dispatched
// board task and a manually-run routine. nil must therefore resolve.

func TestStart_NilBinGoesThroughTheResolver(t *testing.T) {
	script := fakeBin(t, "exit 0\n")
	called := false
	orig := resolveBin
	resolveBin = func() (string, error) { called = true; return script() }
	t.Cleanup(func() { resolveBin = orig })

	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-nil-bin", Cwd: t.TempDir(),
		Timeout: 30 * time.Second, // Bin deliberately nil — dispatch/verify's shape.
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !called {
		t.Error("a nil Spec.Bin did not go through resolveBin — it would exec a bare \"claude\"")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestStart_NilBinResolutionFailureIsAnError(t *testing.T) {
	wantErr := errors.New("no claude anywhere")
	orig := resolveBin
	resolveBin = func() (string, error) { return "", wantErr }
	t.Cleanup(func() { resolveBin = orig })

	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-nil-bin-fail", Cwd: t.TempDir(),
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if res == nil || res.ExitCode != -1 {
		t.Errorf("res = %+v, want ExitCode -1 (never started)", res)
	}
}

// The default must actually be wired to claudebin, not left nil — otherwise
// both tests above would pass against a resolveBin that production never uses.
func TestResolveBinDefaultIsWired(t *testing.T) {
	if resolveBin == nil {
		t.Fatal("resolveBin is nil; a nil Spec.Bin would panic or exec a bare \"claude\"")
	}
}
