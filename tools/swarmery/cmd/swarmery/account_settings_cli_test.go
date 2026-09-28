package main

// The terminal half of the --settings channel: `swarmery account exec` splices
// the composed project settings right after argv[0] — through a real execve,
// into a stub `claude` that prints its argv one argument per line — and
// `swarmery account env` still prints zero or one line.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct/accttest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runsettings"
)

const (
	// execSettingsHelperProject carries the project dir into the helper process
	// and tells it that it is the child; execSettingsHelperArgv the command.
	execSettingsHelperProject = "SWARMERY_TEST_ACCOUNT_EXEC_SETTINGS_PROJECT"
	execSettingsHelperArgv    = "SWARMERY_TEST_ACCOUNT_EXEC_SETTINGS_ARGV"
	runDirVar                 = "SWARMERY_RUN_DIR"
)

// settingsTree is an ADMITTED estate root (D5 Lock 2) with its own settings.json
// (a pluginConfigs entry AND a permissions block) and a project under it carrying
// its own. It returns the project dir and the composed-settings store dir.
func settingsTree(t *testing.T) (home, proj, runDir string) {
	t.Helper()
	home = fakeHome(t, "default")
	root := t.TempDir()
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatalf("SetEstate: %v", err)
	}
	accttest.AdmitEstate(t, "acme", root)
	writeTrusted(t, filepath.Join(root, ".claude", "settings.json"), map[string]any{
		"pluginConfigs": map[string]any{"estate-pack@mkt": map[string]any{"opt": "x"}},
		"permissions":   map[string]any{"deny": []any{"Bash(rm:*)"}},
	})
	proj = filepath.Join(root, "deployment", "repo")
	writeTrusted(t, filepath.Join(proj, ".claude", "settings.json"), map[string]any{
		"enabledPlugins": map[string]any{"estate-pack@mkt": true},
	})
	runDir = t.TempDir()
	return home, proj, runDir
}

func writeTrusted(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// stubBin writes an executable named name that prints its argv one per line.
func stubBin(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runExec runs the real accountExec in a child test process, which execs argv
// with the stub dir first on PATH, and returns the stub's argv lines.
func runExec(t *testing.T, home, proj, runDir, stubDir string, argv ...string) []string {
	t.Helper()
	lines, _ := runExecFull(t, home, proj, runDir, stubDir, argv...)
	return lines
}

// runExecFull is runExec that also returns the child's stderr.
func runExecFull(t *testing.T, home, proj, runDir, stubDir string, argv ...string) ([]string, string) {
	t.Helper()
	raw, err := json.Marshal(argv)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAccountExecSettingsHelper")
	cmd.Env = []string{
		execSettingsHelperProject + "=" + proj,
		execSettingsHelperArgv + "=" + string(raw),
		"HOME=" + home,
		runDirVar + "=" + runDir,
		"PATH=" + stubDir + ":/usr/bin:/bin",
		// The estate store accttest wrote: the child must see the same one.
		"SWARMERY_SECRETS_DIR=" + os.Getenv("SWARMERY_SECRETS_DIR"),
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v (stderr %q)", err, stderr.String())
	}
	if s := stderr.String(); strings.Contains(s, "runsettings:") {
		t.Fatalf("the terminal splice must log nothing, stderr carried a runsettings line")
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n"), stderr.String()
}

func TestAccountExecSplicesSettingsAfterArgv0(t *testing.T) {
	home, proj, runDir := settingsTree(t)
	lines := runExec(t, home, proj, runDir, stubBin(t, "claude"), "claude", "-p", "hi")
	if len(lines) != 4 || lines[0] != "--settings" || lines[2] != "-p" || lines[3] != "hi" {
		t.Fatalf("argv = %q, want [--settings <file> -p hi]", lines)
	}
	want := regexp.MustCompile("^" + regexp.QuoteMeta(filepath.Join(runDir, "settings")) + "/[0-9a-f]{64}\\.json$")
	if !want.MatchString(lines[1]) {
		t.Fatalf("settings path %q is not a content-addressed file under the run dir", lines[1])
	}
	b, err := os.ReadFile(lines[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["permissions"]; ok {
		t.Fatal("the estate's permissions reached the terminal's composed file")
	}
	if _, ok := m["pluginConfigs"].(map[string]any)["estate-pack@mkt"]; !ok {
		t.Fatal("the estate's pluginConfigs is missing")
	}
	// D6: one source, three keys. The project's own enabledPlugins is NOT copied —
	// Claude Code loads the project's settings natively, behind its trust gate.
	if _, ok := m["enabledPlugins"]; ok {
		t.Fatal("the project's own enabledPlugins reached the composed file")
	}
	for k := range m {
		if k != "pluginConfigs" && k != "enabledPlugins" && k != "extraKnownMarketplaces" {
			t.Fatalf("composed file carries %q, outside runsettings.EstateKeys", k)
		}
	}
	fi, err := os.Stat(lines[1])
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("composed file mode: %v %v, want 0600", fi.Mode().Perm(), err)
	}
}

func TestAccountExecLeavesCallerSettingsAlone(t *testing.T) {
	home, proj, runDir := settingsTree(t)
	stub := stubBin(t, "claude")
	for _, argv := range [][]string{
		{"claude", "--settings", "/tmp/mine.json", "-p", "hi"},
		{"claude", "-p", "hi", "--settings=/tmp/mine.json"},
	} {
		lines := runExec(t, home, proj, runDir, stub, argv...)
		if !equalStrings(lines, argv[1:]) {
			t.Fatalf("argv = %q, want the caller's own %q untouched", lines, argv[1:])
		}
	}
}

func TestAccountExecSkipsNonClaudeArgv0(t *testing.T) {
	// In-process: the composer is never even called for another command.
	calls := 0
	prev := composeQuiet
	composeQuiet = func(res claudeacct.Resolution, in runsettings.Inputs) (string, string) {
		calls++
		return "/composed.json", ""
	}
	t.Cleanup(func() { composeQuiet = prev })
	var stderr bytes.Buffer
	if got := spliceSettings([]string{"sh", "-c", "x"}, claudeacct.Resolution{EstateAdmitted: true}, &stderr); !equalStrings(got, []string{"sh", "-c", "x"}) || calls != 0 {
		t.Fatalf("non-claude argv0: argv=%q composer calls=%d, want untouched and 0", got, calls)
	}
	if got := spliceSettings([]string{"/usr/local/bin/claude", "-p"}, claudeacct.Resolution{}, &stderr); calls != 1 || !equalStrings(got, []string{"/usr/local/bin/claude", "--settings", "/composed.json", "-p"}) {
		t.Fatalf("claude argv0: argv=%q calls=%d", got, calls)
	}
	composeQuiet = prev

	home, proj, runDir := settingsTree(t)
	lines := runExec(t, home, proj, runDir, stubBin(t, "sh-stub"), "sh-stub", "a", "b")
	if !equalStrings(lines, []string{"a", "b"}) {
		t.Fatalf("argv = %q, want [a b] — a non-claude command must never get --settings", lines)
	}
	// And a project with no estate gets no flag even for claude: no flag day.
	unbound := project(t, "")
	lines = runExec(t, home, unbound, runDir, stubBin(t, "claude"), "claude", "-p", "hi")
	if !equalStrings(lines, []string{"-p", "hi"}) {
		t.Fatalf("no estate: argv = %q, want [-p hi] byte-identical", lines)
	}
}

func TestAccountEnvStillPrintsAtMostOneLine(t *testing.T) {
	_, proj, runDir := settingsTree(t)
	t.Setenv(runDirVar, runDir)
	var out bytes.Buffer
	if err := accountEnv([]string{"--path", proj}, &out); err != nil {
		t.Fatalf("accountEnv: %v", err)
	}
	got := out.String()
	if n := strings.Count(got, "\n"); n > 1 {
		t.Fatalf("stdout has %d lines, want zero or one", n)
	}
	if strings.Contains(strings.ToLower(got), "settings") {
		t.Fatal("account env mentioned settings — it prints the config-dir line only")
	}
	// Bound to a named account: exactly the one CLAUDE_CONFIG_DIR= line.
	fakeHome(t, "default", "work")
	if err := claudeacct.SetBinding(proj, "work"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := accountEnv([]string{"--path", proj}, &out); err != nil {
		t.Fatalf("accountEnv: %v", err)
	}
	if got := out.String(); strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("bound: stdout = %q, want exactly one CLAUDE_CONFIG_DIR= line", got)
	}
}

// TestAccountExecSettingsHelper is the child half: it calls the real
// accountExec, which replaces this process with the stub. A no-op unless the
// parent selected it.
func TestAccountExecSettingsHelper(t *testing.T) {
	dir := os.Getenv(execSettingsHelperProject)
	if dir == "" {
		t.Skip("helper process for the TestAccountExec* settings tests")
	}
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(execSettingsHelperArgv)), &argv); err != nil {
		t.Fatalf("argv: %v", err)
	}
	if err := accountExec(append([]string{"--path", dir, "--"}, argv...)); err != nil {
		t.Fatalf("accountExec: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// D9 on the terminal: an admitted estate whose settings.json is unusable never
// blocks the command. Exactly one line goes to stderr, argv carries no
// --settings, and stdout is the command's alone.
func TestAccountExecComposeFailureWarnsOnStderr(t *testing.T) {
	home := fakeHome(t, "default")
	root := t.TempDir()
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatalf("SetEstate: %v", err)
	}
	accttest.AdmitEstate(t, "acme", root)
	bad := filepath.Join(root, ".claude", "settings.json")
	if err := os.WriteFile(bad, []byte(`{"pluginConfigs":`), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, stderr := runExecFull(t, home, root, t.TempDir(), stubBin(t, "claude"), "claude", "-p", "hi")
	if !equalStrings(lines, []string{"-p", "hi"}) {
		t.Fatalf("argv = %q, want [-p hi] with no --settings", lines)
	}
	want := "swarmery: project settings not composed (malformed); running without them\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want exactly %q", stderr, want)
	}
}
