package installer

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// validateClaudeBin gates what `swarmery install` bakes as SWARMERY_CLAUDE_BIN:
// only an absolute, existing, executable file outside the shim dir passes.
func TestValidateClaudeBin(t *testing.T) {
	shim := t.TempDir()
	t.Setenv("SWARMERY_BIN_DIR", shim)
	realDir := t.TempDir()
	write := func(dir, name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write(realDir, "claude", 0o755)
	noExec := write(realDir, "claude-noexec", 0o644)
	inShim := write(shim, "claude", 0o755)

	if err := validateClaudeBin(good); err != nil {
		t.Errorf("validateClaudeBin(%s) = %v, want nil", good, err)
	}
	for name, p := range map[string]string{
		"relative":       "bin/claude",
		"missing":        filepath.Join(realDir, "nope"),
		"not executable": noExec,
		"directory":      realDir,
		"the shim":       inShim,
	} {
		if err := validateClaudeBin(p); err == nil {
			t.Errorf("validateClaudeBin(%s: %s) = nil, want a refusal", name, p)
		}
	}
}

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

// --claude-bin follows the same precedence as the SWARMERY_* flags: flag >
// shell env > existing plist, and the other baked vars survive.
func TestMergeInstallEnv_ClaudeBin(t *testing.T) {
	prev := map[string]string{"SWARMERY_CLAUDE_BIN": "/old/claude", "SWARMERY_EXCLUDE": "x"}
	lookup := func(env []EnvVar, k string) string {
		for _, e := range env {
			if e.Key == k {
				return e.Value
			}
		}
		return ""
	}
	env, _ := mergeInstallEnv(prev, map[string]bool{"claude-bin": true},
		map[string]string{"claude-bin": " /real/claude "}, noEnv)
	if got := lookup(env, "SWARMERY_CLAUDE_BIN"); got != "/real/claude" {
		t.Errorf("flag: SWARMERY_CLAUDE_BIN = %q, want /real/claude", got)
	}
	if lookup(env, "SWARMERY_EXCLUDE") != "x" {
		t.Error("a previously baked var was dropped")
	}
	shell := func(k string) (string, bool) {
		if k == "SWARMERY_CLAUDE_BIN" {
			return "/shell/claude", true
		}
		return "", false
	}
	env, _ = mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, shell)
	if got := lookup(env, "SWARMERY_CLAUDE_BIN"); got != "/shell/claude" {
		t.Errorf("shell env: SWARMERY_CLAUDE_BIN = %q, want /shell/claude", got)
	}
	env, _ = mergeInstallEnv(prev, map[string]bool{}, map[string]string{}, noEnv)
	if got := lookup(env, "SWARMERY_CLAUDE_BIN"); got != "/old/claude" {
		t.Errorf("preserve: SWARMERY_CLAUDE_BIN = %q, want /old/claude", got)
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

// phaserunPolicy is the committed policy file, as the absolute path the
// installer insists on.
func phaserunPolicy(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "config", "route-policy.phaserun.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// envMap is the plist read back: what ExistingPlistEnv would hand the next
// install (SWARMERY_PORT included, as the real plist carries it).
func envMap(port int, env []EnvVar) map[string]string {
	m := map[string]string{}
	if port > 0 {
		m["SWARMERY_PORT"] = strconv.Itoa(port)
	}
	for _, e := range env {
		m[e.Key] = e.Value
	}
	return m
}

// plistHas reports whether the rendered plist carries key=value in its
// EnvironmentVariables dict, as launchd will read it.
func plistHas(plist, key, value string) bool {
	return strings.Contains(plist, "\t\t<key>"+key+"</key>\n\t\t<string>"+value+"</string>\n")
}

func plistHasKey(plist, key string) bool {
	return strings.Contains(plist, "<key>"+key+"</key>")
}

// The two route knobs reach the generated plist from either source and survive
// a flag-less reinstall the same way --port does: flag > shell env > existing
// plist, and an explicit empty flag clears. Each case renders the real Plist()
// from mergeInstallEnv's output, so the assertion is on what launchd reads.
func TestInstallRouteEnvBakedIntoPlistAndPreserved(t *testing.T) {
	policy := phaserunPolicy(t)
	baked := map[string]string{
		"SWARMERY_PORT":           "7777",
		"SWARMERY_ROUTE_PHASERUN": "active",
		"SWARMERY_ROUTE_POLICY":   policy,
		"SWARMERY_ONBOARD_ROOTS":  "/home/dev/projects",
	}
	for _, tc := range []struct {
		name       string
		prev       map[string]string
		set        map[string]bool
		flags      map[string]string
		getenv     func(string) (string, bool)
		wantMode   string // "" = absent from the plist
		wantPolicy string
	}{
		{
			name:   "flags bake both",
			set:    map[string]bool{"route-phaserun": true, "route-policy": true},
			flags:  map[string]string{"route-phaserun": "active", "route-policy": " " + policy + " "},
			getenv: noEnv, wantMode: "active", wantPolicy: policy,
		},
		{
			name: "shell env bakes both",
			getenv: envFrom(map[string]string{
				"SWARMERY_ROUTE_PHASERUN": "active",
				"SWARMERY_ROUTE_POLICY":   policy,
			}),
			wantMode: "active", wantPolicy: policy,
		},
		{
			name: "flag-less reinstall keeps the baked values",
			prev: baked, getenv: noEnv, wantMode: "active", wantPolicy: policy,
		},
		{
			name:   "rollback flag beats the plist, policy kept",
			prev:   baked,
			set:    map[string]bool{"route-phaserun": true},
			flags:  map[string]string{"route-phaserun": "shadow"},
			getenv: noEnv, wantMode: "shadow", wantPolicy: policy,
		},
		{
			name:   "explicit empty clears both",
			prev:   baked,
			set:    map[string]bool{"route-phaserun": true, "route-policy": true},
			flags:  map[string]string{"route-phaserun": "", "route-policy": ""},
			getenv: noEnv,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := tc.set
			if set == nil {
				set = map[string]bool{}
			}
			flags := tc.flags
			if flags == nil {
				flags = map[string]string{}
			}
			env, _ := mergeInstallEnv(tc.prev, set, flags, tc.getenv)
			if err := validateInstallEnv(env); err != nil {
				t.Fatalf("validateInstallEnv: %v", err)
			}
			plist := Plist("/home/dev/.swarmery/bin/swarmery", "/home/dev/.swarmery/logs", 7777, env...)
			for _, kv := range []struct{ key, want string }{
				{"SWARMERY_ROUTE_PHASERUN", tc.wantMode},
				{"SWARMERY_ROUTE_POLICY", tc.wantPolicy},
			} {
				if kv.want == "" {
					if plistHasKey(plist, kv.key) {
						t.Errorf("%s still baked into the plist:\n%s", kv.key, plist)
					}
					continue
				}
				if !plistHas(plist, kv.key, kv.want) {
					t.Errorf("plist lacks %s=%s:\n%s", kv.key, kv.want, plist)
				}
			}
			if tc.prev != nil && !plistHas(plist, "SWARMERY_ONBOARD_ROOTS", "/home/dev/projects") {
				t.Error("an unrelated baked var was dropped")
			}
			if strings.Count(plist, "<key>SWARMERY_PORT</key>") != 1 {
				t.Errorf("SWARMERY_PORT not emitted exactly once:\n%s", plist)
			}
		})
	}
}

// Two flag-less reinstalls in a row produce byte-identical plists: the route
// knobs ride the installEnvKeys order, not the sorted pass-through.
func TestInstallRouteEnvReinstallIsStable(t *testing.T) {
	policy := phaserunPolicy(t)
	env, _ := mergeInstallEnv(nil,
		map[string]bool{"route-phaserun": true, "route-policy": true},
		map[string]string{"route-phaserun": "active", "route-policy": policy}, noEnv)
	first := Plist("/b", "/l", 7777, env...)

	env2, preserved := mergeInstallEnv(envMap(7777, env), map[string]bool{}, map[string]string{}, noEnv)
	second := Plist("/b", "/l", 7777, env2...)
	if first != second {
		t.Fatalf("reinstall changed the plist:\n--- first\n%s\n--- second\n%s", first, second)
	}
	want := []string{"SWARMERY_ROUTE_PHASERUN", "SWARMERY_ROUTE_POLICY"}
	if !reflect.DeepEqual(preserved, want) {
		t.Errorf("preserved = %v, want %v", preserved, want)
	}
}

// validateInstallEnv refuses route values the daemon would silently misread:
// a mode outside off|shadow|active (read as shadow), a relative policy path
// (launchd's cwd is not the checkout) and a policy that does not load.
func TestValidateInstallEnvRoute(t *testing.T) {
	policy := phaserunPolicy(t)
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"tiers":{"M":{"model":"gpt"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ok := range [][]EnvVar{
		nil,
		{{Key: "SWARMERY_ROUTE_PHASERUN", Value: "off"}},
		{{Key: "SWARMERY_ROUTE_PHASERUN", Value: "shadow"}},
		{{Key: "SWARMERY_ROUTE_PHASERUN", Value: "Active"}},
		{{Key: "SWARMERY_ROUTE_POLICY", Value: policy}},
	} {
		if err := validateInstallEnv(ok); err != nil {
			t.Errorf("validateInstallEnv(%v) = %v, want nil", ok, err)
		}
	}
	for name, env := range map[string][]EnvVar{
		"typo mode":       {{Key: "SWARMERY_ROUTE_PHASERUN", Value: "actve"}},
		"relative policy": {{Key: "SWARMERY_ROUTE_POLICY", Value: "config/route-policy.phaserun.json"}},
		"missing policy":  {{Key: "SWARMERY_ROUTE_POLICY", Value: filepath.Join(t.TempDir(), "nope.json")}},
		"invalid policy":  {{Key: "SWARMERY_ROUTE_POLICY", Value: bad}},
	} {
		err := validateInstallEnv(env)
		if err == nil {
			t.Errorf("%s: validateInstallEnv = nil, want a refusal", name)
			continue
		}
		if !strings.Contains(err.Error(), `""`) {
			t.Errorf("%s: error %q does not say how to clear the value", name, err)
		}
	}
}
