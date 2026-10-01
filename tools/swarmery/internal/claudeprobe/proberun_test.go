package claudeprobe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// shapeLine builds a line that IS one of FailureKind's recorded shapes: the
// prefix, plus the `also` fragment for a shape that needs one. It is derived
// from failureShapes itself, so a shape added to the table is exercised here
// without anyone remembering to copy it.
func shapeLine(s failureShape) string {
	line := s.prefix
	if s.also != "" {
		line += " Opus limit. " + s.also + " to continue."
	}
	return line
}

// fakePing points the probe at a stub `claude` that prints line on the given
// stream and exits with code. The line travels through a file, not the script
// text: the recorded shapes carry apostrophes and a middle dot.
func fakePing(t *testing.T, line, stream string, code int) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(out, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	redirect := ""
	if stream == "stderr" {
		redirect = " >&2"
	}
	fakeClaude(t, "cat "+out+redirect+"\nexit "+strconv.Itoa(code))
}

// wantForKind is the verdict each failure kind must produce from the ping.
func wantForKind(t *testing.T, kind, line string) Result {
	t.Helper()
	switch kind {
	case FailureAuth:
		// A login demand keeps the login wording; any other auth shape is access
		// the account no longer has.
		if ClassifyExit(1, line).Status == StatusNoLogin {
			return Result{Status: StatusNoLogin, Reason: ReasonNoLogin}
		}
		return Result{Status: StatusNoLogin, Reason: ReasonAccessRefused}
	case FailureQuota:
		return Result{Status: StatusLimited, Reason: ReasonRateLimited}
	case FailureAPIError:
		return Result{Status: StatusUnknown, Reason: ReasonAPIError}
	}
	t.Fatalf("unknown failure kind %q", kind)
	return Result{}
}

// TestProbeRun: every shape in FailureKind's table, printed by a stub `claude`,
// is classified by its kind — for a failing exit AND for a zero one, because the
// ping's expected output is fixed and the CLI has printed such a line and still
// exited 0. Auth and quota are verdicts about the account; an API error is not.
func TestProbeRun(t *testing.T) {
	var auth, quota int
	for _, shape := range failureShapes {
		line := shapeLine(shape)
		if kind, ok := FailureKind(line); !ok || kind != shape.kind {
			t.Fatalf("fixture line %q does not match its own shape (%q, %v)", line, kind, ok)
		}
		switch shape.kind {
		case FailureAuth:
			auth++
		case FailureQuota:
			quota++
		}
		want := wantForKind(t, shape.kind, line)
		for _, code := range []int{1, 0} {
			for _, stream := range []string{"stdout", "stderr"} {
				name := shape.kind + "/" + shape.prefix + "/exit" + strconv.Itoa(code) + "/" + stream
				t.Run(name, func(t *testing.T) {
					fakePing(t, line, stream, code)
					if got := ProbeRun(context.Background(), nil, ""); got != want {
						t.Errorf("ProbeRun = %+v, want %+v", got, want)
					}
				})
			}
		}
	}
	// The table is the fixture: a test that iterated nothing would prove nothing.
	if auth < 4 || quota < 6 {
		t.Fatalf("failureShapes lists %d auth and %d quota shapes — the table shrank under this test", auth, quota)
	}
}

// TestProbeRunOrgDisabled names the one state no other probe can see: `auth
// status` reports such an account as logged in, and only a real model call is
// refused. The stub is the only evidence available for it — the state cannot be
// produced on demand.
func TestProbeRunOrgDisabled(t *testing.T) {
	fakePing(t, "Your organization has disabled Claude subscription access for Claude Code.", "stdout", 1)
	want := Result{Status: StatusNoLogin, Reason: ReasonAccessRefused}
	if got := ProbeRun(context.Background(), nil, ""); got != want {
		t.Errorf("ProbeRun = %+v, want %+v", got, want)
	}
}

// TestProbeRunHealthy: the stub that answers "OK" is ready.
func TestProbeRunHealthy(t *testing.T) {
	fakePing(t, "OK", "stdout", 0)
	if got := ProbeRun(context.Background(), nil, ""); got != (Result{Status: StatusReady}) {
		t.Errorf("ProbeRun = %+v, want ready", got)
	}
}

// TestProbeRunProseCannotFakeAMarker: a reply that merely mentions a failure
// shape mid-line is not one — FailureKind matches a line's START.
func TestProbeRunProseCannotFakeAMarker(t *testing.T) {
	fakePing(t, "OK — earlier the CLI said: Not logged in · Please run /login", "stdout", 0)
	if got := ProbeRun(context.Background(), nil, ""); got != (Result{Status: StatusReady}) {
		t.Errorf("ProbeRun = %+v, want ready", got)
	}
}

// TestProbeRunUnrecognisedFailure: a non-zero exit with no recorded shape is
// unknown — never ready, never a verdict about the account.
func TestProbeRunUnrecognisedFailure(t *testing.T) {
	fakePing(t, "segmentation fault", "stderr", 3)
	want := Result{Status: StatusUnknown, Reason: ReasonUnrecognised}
	if got := ProbeRun(context.Background(), nil, ""); got != want {
		t.Errorf("ProbeRun = %+v, want %+v", got, want)
	}
}

// TestProbeRunMissingBinary: no CLI on the machine is unknown, not a crash.
func TestProbeRunMissingBinary(t *testing.T) {
	prev := resolveBin
	resolveBin = func() (string, error) { return "", errors.New("claude not found") }
	t.Cleanup(func() { resolveBin = prev })

	want := Result{Status: StatusUnknown, Reason: ReasonNoBinary}
	if got := ProbeRun(context.Background(), nil, ""); got != want {
		t.Errorf("ProbeRun = %+v, want %+v", got, want)
	}
}

// TestProbeRunStartFailed: a binary that cannot be executed is unknown.
func TestProbeRunStartFailed(t *testing.T) {
	prev := resolveBin
	resolveBin = func() (string, error) { return filepath.Join(t.TempDir(), "absent-claude"), nil }
	t.Cleanup(func() { resolveBin = prev })

	want := Result{Status: StatusUnknown, Reason: ReasonStartFailed}
	if got := ProbeRun(context.Background(), nil, ""); got != want {
		t.Errorf("ProbeRun = %+v, want %+v", got, want)
	}
}

// TestProbeRunTimeout: a hung ping is unknown/timeout.
func TestProbeRunTimeout(t *testing.T) {
	fakeClaude(t, "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	want := Result{Status: StatusUnknown, Reason: ReasonTimeout}
	if got := ProbeRun(ctx, nil, ""); got != want {
		t.Errorf("ProbeRun = %+v, want %+v", got, want)
	}
}

// envWith is the test process's environment with CLAUDE_CONFIG_DIR replaced by
// dir ("" = removed) and extra appended — a caller-built environment.
func envWith(dir string, extra ...string) []string {
	env := withoutConfigDir(os.Environ())
	if dir != "" {
		env = append(env, configDirEnv+"="+dir)
	}
	return append(env, extra...)
}

// TestProbeRunArgvAndEnv: the ping is the fixed cheap invocation with nothing
// spliced into it, it runs in the directory it was given, and its environment
// is EXACTLY the one the caller built — nothing stripped, nothing added. That
// is what lets the caller hand it the environment of the run it vouches for.
func TestProbeRunArgvAndEnv(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen")
	fakeClaude(t, `{ printf '%s\n' "$@"; printf 'dir=%s\n' "$(pwd -P)"; printf 'cfg=%s\n' "${CLAUDE_CONFIG_DIR-__UNSET__}"; printf 'store=%s\n' "${SWARMERY_TEST_PROBE_VAR-__UNSET__}"; } > `+seen+`
echo OK`)
	// The hostile precondition: the probing process itself carries a config dir.
	t.Setenv("CLAUDE_CONFIG_DIR", "/somewhere/inherited")
	cwd := t.TempDir()

	run := func(env []string, dir string) []string {
		t.Helper()
		if got := ProbeRun(context.Background(), env, dir); got.Status != StatusReady {
			t.Fatalf("ProbeRun = %+v, want ready", got)
		}
		raw, err := os.ReadFile(seen)
		if err != nil {
			t.Fatalf("read marker: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	tail := func(lines []string) string { return strings.Join(lines[len(lines)-2:], " ") }

	// A caller-built environment that names a config dir and carries a variable.
	named := filepath.Join(t.TempDir(), ".claude-work")
	lines := run(envWith(named, "SWARMERY_TEST_PROBE_VAR=delivered"), cwd)
	// --no-session-persistence: a ping must leave no transcript, or every
	// pre-flight becomes an ingested "Reply with exactly: OK" session that the
	// session lists, cost analytics and the D2 labeller all pick up.
	wantArgv := []string{"-p", PingPrompt, "--model", PingModel, "--effort", PingEffort, "--max-turns", "1",
		"--no-session-persistence"}
	if got := lines[:len(lines)-3]; strings.Join(got, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Errorf("argv = %q, want exactly %q — nothing may be spliced into the ping", got, wantArgv)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimPrefix(lines[len(lines)-3], "dir="); got != resolved {
		t.Errorf("ping ran in %q, want the given dir %q", got, resolved)
	}
	if got, want := tail(lines), "cfg="+named+" store=delivered"; got != want {
		t.Errorf("ping saw %q, want %q", got, want)
	}

	// An environment with NO config dir: absent in the child, even though the
	// probing process has one.
	if got, want := tail(run(envWith(""), "")), "cfg=__UNSET__ store=__UNSET__"; got != want {
		t.Errorf("ping saw %q, want %q", got, want)
	}

	// A nil environment is the process's own — an inherited config dir is KEPT,
	// not stripped: that is an unbound project's run.
	if got, want := tail(run(nil, "")), "cfg=/somewhere/inherited store=__UNSET__"; got != want {
		t.Errorf("ping saw %q, want %q", got, want)
	}
}

// TestProbeEnv: stage one under a caller-built environment. Unlike Probe, which
// strips whatever config dir the daemon inherited, ProbeEnv hands the child
// exactly what it was given.
func TestProbeEnv(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen")
	fakeClaude(t, `printf '%s %s' "$*" "${CLAUDE_CONFIG_DIR-__UNSET__}" > `+seen+`
printf '{"loggedIn": true}\n'`)
	t.Setenv("CLAUDE_CONFIG_DIR", "/somewhere/inherited")
	read := func() string {
		t.Helper()
		raw, err := os.ReadFile(seen)
		if err != nil {
			t.Fatalf("read marker: %v", err)
		}
		return string(raw)
	}

	if got := ProbeEnv(context.Background(), nil); got != (Result{Status: StatusReady}) {
		t.Fatalf("ProbeEnv = %+v, want ready", got)
	}
	if got := read(); got != "auth status /somewhere/inherited" {
		t.Errorf("nil env: child saw %q, want `auth status` under the inherited dir", got)
	}
	if got := ProbeEnv(context.Background(), envWith("/cfg/work")); got.Status != StatusReady {
		t.Fatalf("ProbeEnv = %+v, want ready", got)
	}
	if got := read(); got != "auth status /cfg/work" {
		t.Errorf("built env: child saw %q", got)
	}
	// Probe keeps its own rule: the inherited dir is stripped.
	if got := Probe(context.Background(), ""); got.Status != StatusReady {
		t.Fatalf("Probe = %+v, want ready", got)
	}
	if got := read(); got != "auth status __UNSET__" {
		t.Errorf("Probe: child saw %q, want the inherited dir stripped", got)
	}

	fakeClaude(t, `printf '{"loggedIn": false, "authMethod": "none"}\n'; exit 1`)
	if got := ProbeEnv(context.Background(), nil); got != (Result{Status: StatusNoLogin, Reason: ReasonNoLogin}) {
		t.Errorf("ProbeEnv on a dir with no login = %+v", got)
	}
	prev := resolveBin
	resolveBin = func() (string, error) { return "", errors.New("claude not found") }
	t.Cleanup(func() { resolveBin = prev })
	if got := ProbeEnv(context.Background(), nil); got != (Result{Status: StatusUnknown, Reason: ReasonNoBinary}) {
		t.Errorf("ProbeEnv with no binary = %+v", got)
	}
}

// TestClassifyRun: a finished run's exit read as an account verdict. Everything
// ClassifyExit already names is unchanged; the additions are the failure shapes
// it does not know, on a non-zero exit only.
func TestClassifyRun(t *testing.T) {
	for _, tc := range []struct {
		name           string
		exit           int
		stdout, stderr string
		want           Result
	}{
		{"zero exit is ready even over a failure line", 0,
			"Your organization has disabled Claude subscription access", "", Result{Status: StatusReady}},
		{"login demand keeps its wording", 1,
			"Not logged in · Please run /login", "", Result{Status: StatusNoLogin, Reason: ReasonNoLogin}},
		{"usage limit is limited", 1,
			"You've hit your session limit · resets 1:30am (UTC)", "", Result{Status: StatusLimited, Reason: ReasonRateLimited}},
		{"org-disabled is an auth verdict", 1,
			"working…\nYour organization has disabled Claude subscription access", "",
			Result{Status: StatusNoLogin, Reason: ReasonAccessRefused}},
		{"a 401 is an auth verdict", 1,
			"Please run /login · API Error: 401 Invalid bearer token", "",
			Result{Status: StatusNoLogin, Reason: ReasonAccessRefused}},
		{"out of credits is limited", 1,
			"You're out of usage credits. Add more to continue.", "", Result{Status: StatusLimited, Reason: ReasonRateLimited}},
		{"a failure line on stderr counts", 1,
			"", "Your organization has disabled Claude subscription access",
			Result{Status: StatusNoLogin, Reason: ReasonAccessRefused}},
		{"an API error never becomes a verdict", 1,
			"API Error: 529 Overloaded", "", Result{Status: StatusUnknown, Reason: ReasonUnrecognised}},
		{"an earlier failure line is history, not the ending", 1,
			"Your organization has disabled Claude subscription access\nthe build failed", "",
			Result{Status: StatusUnknown, Reason: ReasonUnrecognised}},
		{"an ordinary failure is unknown", 2,
			"tests failed", "exit status 2", Result{Status: StatusUnknown, Reason: ReasonUnrecognised}},
		// A verdict opens the account's breaker, so a marker merely QUOTED in a
		// tail must not become one — the older markers included.
		{"a hook quoting auth status JSON is not a verdict", 1,
			"", "hook: other dir → {\"loggedIn\": false, \"authMethod\": \"none\"}\nhook exited 1",
			Result{Status: StatusUnknown, Reason: ReasonUnrecognised}},
		{"a final message that mentions a limit is not a verdict", 1,
			"I added a matcher for the TUI's \"Usage limit reached\" line.", "",
			Result{Status: StatusUnknown, Reason: ReasonUnrecognised}},
		{"an earlier limit line is history, not the ending", 1,
			"You've hit your session limit · resets 1:30am (UTC)\nthe build failed", "",
			Result{Status: StatusUnknown, Reason: ReasonUnrecognised}},
		{"a login demand mid-sentence is not a verdict", 1,
			"Fixed the case where the CLI said Not logged in · Please run /login", "",
			Result{Status: StatusUnknown, Reason: ReasonUnrecognised}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyRun(tc.exit, tc.stdout, tc.stderr); got != tc.want {
				t.Errorf("ClassifyRun = %+v, want %+v", got, tc.want)
			}
		})
	}
}
