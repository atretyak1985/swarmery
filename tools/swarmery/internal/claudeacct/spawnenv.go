package claudeacct

// The ONE place that composes the environment a swarmery-launched `claude` runs
// under. Every spawn site — the five runcore engines, routines, improve,
// retroanalysis, provision, the dashboard's resume, and `swarmery account exec`
// — hands its base environment and the account key here and uses the result
// verbatim. Before this file each site appended its own delta, and the secret
// store (secrets.go) reached only two of them while the README promised "same
// store, same key" everywhere.
//
// Three account states, three answers:
//
//   - UNBOUND (key ""): the base is returned unchanged. Whatever the parent
//     carries — including a CLAUDE_CONFIG_DIR baked into the daemon's plist by
//     `swarmery install --claude-config-dir` — is what the child sees. That is
//     the documented meaning of the flag: "the account every daemon-spawned run
//     uses when a project has NO binding".
//   - EXPLICITLY DEFAULT (key "default"): the base WITHOUT CLAUDE_CONFIG_DIR. The
//     CLI selects ~/.claude by the variable's ABSENCE, so an explicit binding to
//     the default account must remove an inherited one — otherwise a project the
//     operator deliberately pinned to the default account would silently run
//     under the daemon's baked account while `swarmery account which` says
//     "default". No delta expresses "unset", which is why this is a whole-env
//     function and not a delta function.
//   - BOUND (any other key): the base minus every key the delta sets, then the
//     delta — the config dir first, the account's secret store after it. Exactly
//     one entry per key, because execve(2) copies the array verbatim and a libc
//     getenv() returns the FIRST match, so a stale CLAUDE_CONFIG_DIR exported by
//     the caller's shell would otherwise win over the binding (os/exec normalises
//     to last-wins; raw execve does not — the merge makes both agree).

import (
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// SpawnDelta is the env DELTA for one account key — EnvForAccount followed by
// SecretEnvForAccount — for the one seam that must stay delta-shaped: the
// terminal dock's PTY (internal/term.Manager.Start appends it after the daemon's
// own environment). nil for "" and for the default account. Everything that
// sets a child's whole environment uses SpawnEnv instead.
func SpawnDelta(key string) []string {
	return append(EnvForAccount(key), SecretEnvForAccount(key)...)
}

// SpawnEnv returns the environment a child launched for account key runs under,
// derived from base (normally os.Environ()). See the file header for the three
// cases. base is returned as-is — same backing array — for an unbound key, so a
// spawn that resolves no account stays a byte-identical passthrough.
func SpawnEnv(base []string, key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return base
	}
	if key == ingest.DefaultAccount {
		return withoutKeys(base, map[string]struct{}{configDirEnv: {}})
	}
	delta := SpawnDelta(key)
	if len(delta) == 0 {
		return base
	}
	drop := make(map[string]struct{}, len(delta))
	for _, kv := range delta {
		drop[envKey(kv)] = struct{}{}
	}
	return append(withoutKeys(base, drop), delta...)
}

// SpawnEnvFor resolves projectPath's binding and delegates to SpawnEnv.
//
// The empty-path guard is load-bearing, not defensive style: Binding joins its
// argument with ".claude/settings.local.json" unconditionally, so Binding("")
// would read that RELATIVE path against the daemon's own working directory and
// silently bind the child to whatever unrelated settings file sits there. ""
// means "no project" and must return base untouched.
//
// Daemon sites whose cwd is a WORKTREE (dispatch, verify, planrun, phaserun)
// must not call this with the worktree path — a worktree carries no binding
// file — they hold the resolved key already and call SpawnEnv.
func SpawnEnvFor(base []string, projectPath string) []string {
	if strings.TrimSpace(projectPath) == "" {
		return base
	}
	return SpawnEnv(base, Binding(projectPath))
}

// withoutKeys copies base minus every entry whose name is in drop. base itself
// is returned when nothing would be dropped, preserving passthrough identity.
func withoutKeys(base []string, drop map[string]struct{}) []string {
	hit := false
	for _, kv := range base {
		if _, ok := drop[envKey(kv)]; ok {
			hit = true
			break
		}
	}
	if !hit {
		return base
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		if _, ok := drop[envKey(kv)]; ok {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// envKey is the NAME half of a "NAME=value" entry. An entry with no "=" is not a
// valid assignment, so it is keyed whole — that way it can never be mistaken for
// a name a delta is trying to replace.
func envKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}
