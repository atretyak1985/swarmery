package claudeacct

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// bakedDir stands in for a CLAUDE_CONFIG_DIR the daemon inherited from its
// launchd plist (`swarmery install --claude-config-dir`) or the caller's shell.
const bakedDir = "/baked/by/launchd/.claude-work"

func configDirEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, configDirEnv+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// An unbound project inherits the daemon's environment byte for byte — a baked
// CLAUDE_CONFIG_DIR included. That is the documented meaning of the install flag.
func TestSpawnEnv_UnboundIsBytePassthrough(t *testing.T) {
	base := []string{"PATH=/usr/bin", configDirEnv + "=" + bakedDir, "TERM=xterm"}
	got := SpawnEnv(base, "")
	if !slices.Equal(got, base) {
		t.Fatalf("SpawnEnv(base, \"\") = %v, want base unchanged %v", got, base)
	}
	if len(got) > 0 && &got[0] != &base[0] {
		t.Errorf("unbound spawn copied the environment; want the very same slice (passthrough identity)")
	}
}

// THE explicit-default case: a project deliberately bound to the default account
// must run under ~/.claude even when the daemon itself carries a baked
// CLAUDE_CONFIG_DIR — the variable has to be REMOVED, not merely left alone.
func TestSpawnEnv_ExplicitDefaultDropsInheritedConfigDir(t *testing.T) {
	fakeHome(t)
	base := []string{"PATH=/usr/bin", configDirEnv + "=" + bakedDir, "TERM=xterm"}
	got := SpawnEnv(base, "default")
	if entries := configDirEntries(got); len(entries) != 0 {
		t.Fatalf("explicit default binding kept %v — the daemon's baked account would win over the operator's choice", entries)
	}
	if want := []string{"PATH=/usr/bin", "TERM=xterm"}; !slices.Equal(got, want) {
		t.Errorf("env = %v, want %v (only CLAUDE_CONFIG_DIR removed, order kept)", got, want)
	}
	// And without an inherited variable there is nothing to do: passthrough.
	clean := []string{"PATH=/usr/bin"}
	if got := SpawnEnv(clean, "default"); !slices.Equal(got, clean) {
		t.Errorf("SpawnEnv(clean, default) = %v, want %v", got, clean)
	}
}

// A bound account: exactly one CLAUDE_CONFIG_DIR entry (the binding's, not the
// stale inherited one), the secret store appended after it, unrelated variables
// carried through untouched.
func TestSpawnEnv_BoundAccountMergesConfigDirAndSecrets(t *testing.T) {
	fakeHome(t)
	seedStore(t, "work", "MCP_TOKEN=abc\n", 0o600)
	want, ok := ConfigDirForAccount("work")
	if !ok || want == "" {
		t.Fatalf("precondition: ConfigDirForAccount(work) = %q, %v", want, ok)
	}

	base := []string{"PATH=/usr/bin", configDirEnv + "=" + bakedDir, "MCP_TOKEN=stale", "TERM=xterm"}
	got := SpawnEnv(base, "work")

	if entries := configDirEntries(got); len(entries) != 1 || entries[0] != configDirEnv+"="+want {
		t.Fatalf("CLAUDE_CONFIG_DIR entries = %v, want exactly [%s=%s] — execve keeps duplicates and getenv takes the first",
			entries, configDirEnv, want)
	}
	var tokens []string
	for _, kv := range got {
		if strings.HasPrefix(kv, "MCP_TOKEN=") {
			tokens = append(tokens, kv)
		}
	}
	if !slices.Equal(tokens, []string{"MCP_TOKEN=abc"}) {
		t.Errorf("MCP_TOKEN entries = %v, want exactly [MCP_TOKEN=abc] (store wins, stale copy removed)", tokens)
	}
	for _, keep := range []string{"PATH=/usr/bin", "TERM=xterm"} {
		if !slices.Contains(got, keep) {
			t.Errorf("dropped %q — only keys the delta sets may be removed", keep)
		}
	}
	// Delta order: config dir before the store, both after everything inherited.
	n := len(got)
	if got[n-2] != configDirEnv+"="+want || got[n-1] != "MCP_TOKEN=abc" {
		t.Errorf("tail = %v, want [CLAUDE_CONFIG_DIR=…, MCP_TOKEN=abc] last", got[n-2:])
	}
}

// SpawnDelta is the same two pieces, delta-shaped, for the PTY seam.
func TestSpawnDelta_ConfigDirThenSecrets(t *testing.T) {
	fakeHome(t)
	seedStore(t, "work", "MCP_TOKEN=abc\n", 0o600)
	dir, _ := ConfigDirForAccount("work")
	if got, want := SpawnDelta("work"), []string{configDirEnv + "=" + dir, "MCP_TOKEN=abc"}; !slices.Equal(got, want) {
		t.Errorf("SpawnDelta(work) = %v, want %v", got, want)
	}
	for _, key := range []string{"", "default"} {
		if got := SpawnDelta(key); got != nil {
			t.Errorf("SpawnDelta(%q) = %v, want nil", key, got)
		}
	}
}

// The empty-path guard: SpawnEnvFor("") must not read the RELATIVE
// .claude/settings.local.json under the process cwd. Proven non-vacuously by
// making the cwd a bound project for the duration of the test.
func TestSpawnEnvFor_EmptyProjectPathIsPassthrough(t *testing.T) {
	fakeHome(t)
	bound := t.TempDir()
	if err := SetBinding(bound, "work"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(bound); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prevWd) })
	if Binding("") == "" {
		t.Fatalf("precondition: Binding(\"\") resolved nothing — the relative-path trap this test guards is gone")
	}

	base := []string{"PATH=/usr/bin"}
	if got := SpawnEnvFor(base, ""); !slices.Equal(got, base) {
		t.Errorf("SpawnEnvFor(base, \"\") = %v, want %v untouched", got, base)
	}
	// And a real bound path resolves through the same function.
	if entries := configDirEntries(SpawnEnvFor(base, bound)); len(entries) != 1 {
		t.Errorf("SpawnEnvFor(bound) CLAUDE_CONFIG_DIR entries = %v, want exactly one", entries)
	}
}
