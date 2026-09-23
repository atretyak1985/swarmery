package planning

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// unsetMarker is what the fake claude prints when CLAUDE_CONFIG_DIR is not in
// its environment AT ALL — `${VAR-default}` (no colon) separates "unset" from
// "set to empty".
const unsetMarker = "__UNSET__"

// unsetConfigDir removes CLAUDE_CONFIG_DIR from the TEST process's environment
// so a developer shell cannot leak into os.Environ() and skew the result.
func unsetConfigDir(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv("CLAUDE_CONFIG_DIR")
	if !had {
		return
	}
	if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
		t.Fatalf("unset CLAUDE_CONFIG_DIR: %v", err)
	}
	t.Cleanup(func() { os.Setenv("CLAUDE_CONFIG_DIR", prev) })
}

// childConfigDir runs the REAL ClaudeRunner against a fake claude (via
// SWARMERY_CLAUDE_BIN, the resolution this package already uses) that reports its
// own CLAUDE_CONFIG_DIR into the run cwd, and returns what the CHILD saw.
func childConfigDir(t *testing.T, cwd string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fakeclaude.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"${CLAUDE_CONFIG_DIR-" + unsetMarker + "}\" > \"$PWD/acct.txt\"\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", script)

	if _, err := (ClaudeRunner{Timeout: 30 * time.Second}).Start(context.Background(),
		RunSpec{Prompt: "plan it", SessionUUID: "acct", Cwd: cwd}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(cwd, "acct.txt"))
	if err != nil {
		t.Fatalf("read acct.txt: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// A planner's Cwd IS the project path, so resolving the account from it is
// correct — a bound project's planner run lands on that account's config dir.
func TestPlannerResolvesAccountFromCwd(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	got := childConfigDir(t, project)
	if want := filepath.Join(home, ".claude-nabu-org"); got != want {
		t.Errorf("child CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// The same resolve on an UNBOUND project must add nothing — no CLAUDE_CONFIG_DIR
// in the child at all.
func TestPlannerUnboundProjectLeavesChildEnvUntouched(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())

	got := childConfigDir(t, t.TempDir())
	if got != unsetMarker {
		t.Errorf("child saw CLAUDE_CONFIG_DIR=%q, want it absent", got)
	}
}

// The byte-for-byte half: for an unbound cwd the planner's spawn expression is
// an exact copy of os.Environ().
func TestPlannerUnboundSpawnEnvIsByteIdenticalToOsEnviron(t *testing.T) {
	unsetConfigDir(t)
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()

	base := os.Environ()
	got := claudeacct.SpawnEnvResolved(os.Environ(), claudeacct.Resolve(cwd)) // the spawn line, verbatim
	if len(got) != len(base) {
		t.Fatalf("env length %d, want %d (an unbound spawn must add nothing)", len(got), len(base))
	}
	for i := range base {
		if got[i] != base[i] {
			t.Errorf("env[%d] = %s=…, want %s=… (values withheld: this is the real environment)", i, strings.SplitN(got[i], "=", 2)[0], strings.SplitN(base[i], "=", 2)[0])
		}
	}
}
