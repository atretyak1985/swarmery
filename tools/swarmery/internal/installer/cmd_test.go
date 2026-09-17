package installer

import (
	"reflect"
	"testing"
)

// noEnv is a getenv that reports every var as unset.
func noEnv(string) (string, bool) { return "", false }

func envFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestMergeInstallEnv_PreservesWhenNotResupplied(t *testing.T) {
	// The regression: a bare reinstall (no flags, no shell env) must carry the
	// previously-baked onboarding vars over instead of wiping them.
	prev := map[string]string{
		"SWARMERY_ONBOARD_ROOTS":  "/home/dev/projects",
		"SWARMERY_WORKSPACE_ROOT": "/home/dev/swarmery-workspace",
	}
	env, preserved := mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, noEnv)

	want := []EnvVar{
		{Key: "SWARMERY_ONBOARD_ROOTS", Value: "/home/dev/projects"},
		{Key: "SWARMERY_WORKSPACE_ROOT", Value: "/home/dev/swarmery-workspace"},
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env = %+v, want %+v", env, want)
	}
	if len(preserved) != 2 {
		t.Errorf("preserved = %v, want both keys", preserved)
	}
}

func TestMergeInstallEnv_ProjectsRootsBakedAndPreserved(t *testing.T) {
	// The multi-account enabler: --projects-roots (or the shell env) must land
	// in the plist, and a bare reinstall must carry it over like the others.
	env, _ := mergeInstallEnv(nil,
		map[string]bool{"projects-roots": true},
		map[string]string{"projects-roots": "auto"},
		noEnv)
	if len(env) != 1 || env[0].Key != "SWARMERY_PROJECTS_ROOTS" || env[0].Value != "auto" {
		t.Errorf("flag not baked: %+v", env)
	}

	prev := map[string]string{"SWARMERY_PROJECTS_ROOTS": "auto"}
	env, preserved := mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, noEnv)
	if len(env) != 1 || env[0].Value != "auto" {
		t.Errorf("bare reinstall wiped SWARMERY_PROJECTS_ROOTS: %+v", env)
	}
	if len(preserved) != 1 || preserved[0] != "SWARMERY_PROJECTS_ROOTS" {
		t.Errorf("preserved = %v, want [SWARMERY_PROJECTS_ROOTS]", preserved)
	}
}

func TestMergeInstallEnv_ExplicitFlagWins(t *testing.T) {
	prev := map[string]string{"SWARMERY_ONBOARD_ROOTS": "/old"}
	env, _ := mergeInstallEnv(prev,
		map[string]bool{"onboard-roots": true},
		map[string]string{"onboard-roots": "/new,/other"},
		noEnv)
	if len(env) != 1 || env[0].Value != "/new,/other" {
		t.Errorf("explicit flag ignored: %+v", env)
	}
}

func TestMergeInstallEnv_ExplicitEmptyClears(t *testing.T) {
	prev := map[string]string{"SWARMERY_ONBOARD_ROOTS": "/old"}
	env, preserved := mergeInstallEnv(prev,
		map[string]bool{"onboard-roots": true},
		map[string]string{"onboard-roots": ""},
		noEnv)
	if len(env) != 0 {
		t.Errorf("explicit empty should clear, got %+v", env)
	}
	if len(preserved) != 0 {
		t.Errorf("nothing should be preserved when explicitly cleared, got %v", preserved)
	}
}

func TestMergeInstallEnv_ShellEnvBeatsPrev(t *testing.T) {
	prev := map[string]string{"SWARMERY_ONBOARD_ROOTS": "/old"}
	env, preserved := mergeInstallEnv(prev, map[string]bool{}, map[string]string{},
		envFrom(map[string]string{"SWARMERY_ONBOARD_ROOTS": "/from-shell"}))
	if len(env) != 1 || env[0].Value != "/from-shell" {
		t.Errorf("shell env should win over prev: %+v", env)
	}
	if len(preserved) != 0 {
		t.Errorf("shell env is not a preservation: %v", preserved)
	}
}

func TestResolveInstallPort(t *testing.T) {
	if got := resolveInstallPort(nil, true, 9000); got != 9000 {
		t.Errorf("explicit port = %d, want 9000", got)
	}
	if got := resolveInstallPort(map[string]string{"SWARMERY_PORT": "8123"}, false, -1); got != 8123 {
		t.Errorf("preserved port = %d, want 8123", got)
	}
	if got := resolveInstallPort(nil, false, -1); got != 0 {
		t.Errorf("default port = %d, want 0", got)
	}
}

// ── CLAUDE_CONFIG_DIR ────────────────────────────────────────────────────────
//
// Claude Code namespaces its keychain credential per config dir, so a daemon
// spawned WITHOUT CLAUDE_CONFIG_DIR reads a different item than an operator
// whose sessions always export one — on a machine where every session goes
// through a launcher that sets it, the daemon's item is empty and every
// background run dies with "OAuth session expired and could not be refreshed"
// (diagnosed 2026-09-17). Baking the var into the plist is the fix, so it has
// to survive a bare reinstall like the SWARMERY_* vars.

func TestMergeInstallEnv_ClaudeConfigDirBakedAndPreserved(t *testing.T) {
	env, _ := mergeInstallEnv(nil,
		map[string]bool{"claude-config-dir": true},
		map[string]string{"claude-config-dir": "/home/dev/.claude"},
		noEnv)
	if len(env) != 1 || env[0].Key != "CLAUDE_CONFIG_DIR" || env[0].Value != "/home/dev/.claude" {
		t.Fatalf("flag not baked: %+v", env)
	}

	prev := map[string]string{"CLAUDE_CONFIG_DIR": "/home/dev/.claude"}
	env, preserved := mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, noEnv)
	if len(env) != 1 || env[0].Value != "/home/dev/.claude" {
		t.Errorf("bare reinstall dropped it: %+v", env)
	}
	if len(preserved) != 1 || preserved[0] != "CLAUDE_CONFIG_DIR" {
		t.Errorf("preserved = %v, want CLAUDE_CONFIG_DIR", preserved)
	}
}

// Unlike every SWARMERY_* var, this one must NOT be read from the installing
// shell. It is ambient in any session started with an explicit config dir, so a
// `swarmery install` typed inside a second-account session would silently
// re-home every unbound background run onto that account.
func TestMergeInstallEnv_ClaudeConfigDirIgnoresShellEnv(t *testing.T) {
	shell := func(k string) (string, bool) {
		if k == "CLAUDE_CONFIG_DIR" {
			return "/home/dev/.claude-work", true
		}
		return "", false
	}

	env, _ := mergeInstallEnv(nil, map[string]bool{}, map[string]string{}, shell)
	if len(env) != 0 {
		t.Errorf("shell CLAUDE_CONFIG_DIR leaked into the plist: %+v", env)
	}

	// …and it must not beat a value already baked in, either.
	prev := map[string]string{"CLAUDE_CONFIG_DIR": "/home/dev/.claude"}
	env, _ = mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, shell)
	if len(env) != 1 || env[0].Value != "/home/dev/.claude" {
		t.Errorf("shell env overrode the existing plist: %+v", env)
	}

	// The flag still wins, so the operator can change it deliberately.
	env, _ = mergeInstallEnv(prev,
		map[string]bool{"claude-config-dir": true},
		map[string]string{"claude-config-dir": "/home/dev/.claude-other"},
		shell)
	if len(env) != 1 || env[0].Value != "/home/dev/.claude-other" {
		t.Errorf("explicit flag did not win: %+v", env)
	}
}

// A var with no flag — SWARMERY_EXCLUDE is the live example — must survive a
// bare reinstall. Before this, mergeInstallEnv only looked at installEnvKeys,
// so any of the ~60 knobs the daemon reads that an operator had baked in by
// hand was silently wiped by the next `swarmery install`.
func TestMergeInstallEnv_PreservesVarsWithNoFlag(t *testing.T) {
	prev := map[string]string{
		"SWARMERY_ONBOARD_ROOTS": "/home/dev/projects",
		"SWARMERY_EXCLUDE":       "/tmp,/tmp/*",
		"SWARMERY_AUTOVERIFY":    "1",
		"SWARMERY_PORT":          "7777", // owned by resolveInstallPort — must NOT be duplicated
	}
	env, preserved := mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, noEnv)

	got := map[string]string{}
	for _, e := range env {
		if _, dup := got[e.Key]; dup {
			t.Errorf("duplicate key %q in plist env", e.Key)
		}
		got[e.Key] = e.Value
	}
	for k, want := range map[string]string{
		"SWARMERY_ONBOARD_ROOTS": "/home/dev/projects",
		"SWARMERY_EXCLUDE":       "/tmp,/tmp/*",
		"SWARMERY_AUTOVERIFY":    "1",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if _, ok := got["SWARMERY_PORT"]; ok {
		t.Error("SWARMERY_PORT passed through mergeInstallEnv; resolveInstallPort owns it")
	}
	if len(preserved) != 3 {
		t.Errorf("preserved = %v, want all three carried vars", preserved)
	}
}

// Deterministic ordering: the flagged vars keep installEnvKeys order, the
// carried-over ones are sorted, so two reinstalls produce an identical plist.
func TestMergeInstallEnv_PassthroughOrderIsStable(t *testing.T) {
	prev := map[string]string{"SWARMERY_ZEBRA": "z", "SWARMERY_ALPHA": "a", "SWARMERY_MID": "m"}
	first, _ := mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, noEnv)
	second, _ := mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, noEnv)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("unstable order:\n %+v\n %+v", first, second)
	}
	want := []string{"SWARMERY_ALPHA", "SWARMERY_MID", "SWARMERY_ZEBRA"}
	for i, w := range want {
		if first[i].Key != w {
			t.Errorf("env[%d] = %s, want %s", i, first[i].Key, w)
		}
	}
}
