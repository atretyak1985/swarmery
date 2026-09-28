package claudeacct

// The ONE place that composes the environment a swarmery-launched `claude` runs
// under. Every spawn site — the five runcore engines, routines, provision, the
// seven System-project runners behind internal/systemspawn (decide, extract,
// handoff, improve, lessons, retroanalysis, trajjudge), the dashboard's resume
// and terminal dock, and `swarmery account exec` — hands its base environment and
// a Resolution (resolve.go) here and uses the result verbatim.
//
// The delta, in this order (resolvedDelta):
//
//  1. the config dir for the resolved ACCOUNT (Resolution.EnvLines);
//  2. the ACCOUNT's store <account>.env — the back-compat layer — when the
//     release table admits it (storeroot.go): a rootless store keeps today's
//     behaviour, a rooted one reaches only the trees its root lines name;
//  3. the ESTATE's store <estate>.env, only when it is ROOTED and admits the
//     estate root. An unanchored estate contributes nothing.
//
// Admission is decided HERE again, from the resolution's rungs and the stores
// on disk — never read back from the Resolution's admission fields — so a
// hand-built Resolution cannot claim a store its rungs do not earn.
//
// Two WARNs are logged here and nowhere else, each once per path per process:
// the binding a rung's provenance ignored (Lock 1), and an account store
// released only because it carries no root line (store-rootless).
//
// then collapsed BY NAME keeping the LAST writer, so the delta holds at most one
// entry per variable name. THE ESTATE WINS A NAME COLLISION, because it is
// appended after the account store. The collapse is load-bearing: `swarmery
// account exec` hands the array to a raw execve(2), where a libc getenv()
// returns the FIRST match, while os/exec normalises to last-wins — a duplicated
// name would make the terminal and the daemon resolve one variable differently.
//
// Three account states, three answers:
//
//   - UNBOUND (Account ""): whatever the parent carries — including a
//     CLAUDE_CONFIG_DIR baked into the daemon's plist by `swarmery install
//     --claude-config-dir` — is what the child sees. That is the documented
//     meaning of the flag: "the account every daemon-spawned run uses when a
//     project has NO binding". With no estate store either, base is returned
//     as-is (same backing array).
//   - EXPLICITLY DEFAULT (Account "default"): the base WITHOUT
//     CLAUDE_CONFIG_DIR. The CLI selects ~/.claude by the variable's ABSENCE,
//     so an explicit binding to the default account must remove an inherited
//     one — otherwise a project the operator deliberately pinned to the default
//     account would silently run under the daemon's baked account while
//     `swarmery account which` says "default". No delta expresses "unset",
//     which is why this is a whole-env function and not a delta function. The
//     estate's secrets are still appended: the payer does not decide them.
//   - BOUND (any other key): the base minus every name the delta sets, then the
//     delta. Exactly one entry per name, because execve copies the array
//     verbatim and a stale CLAUDE_CONFIG_DIR exported by the caller's shell
//     would otherwise win over the binding.

import (
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// resolvedDelta is the env DELTA for one resolution — see the file header for
// the order and the collapse. Unexported on purpose: a delta cannot express
// "unset", so every seam goes through SpawnEnvResolved.
func resolvedDelta(r Resolution) []string {
	for _, ig := range r.IgnoredRungs() {
		logDistrusted(ig.Path, ig.Reason)
	}
	a := admit(r)
	delta := r.EnvLines()
	if a.accountReleased {
		if a.accountRootlessWarn != "" {
			warnRootlessOnce(a.accountRootlessWarn, r.Account)
		}
		delta = append(delta, SecretEnvForAccount(r.Account)...)
	}
	if a.estateReleased {
		delta = append(delta, SecretEnvForStore(r.Estate)...)
	}
	return collapseLastWins(delta)
}

// rootlessWarned is the store-rootless warn-once ledger, keyed by store path.
// A pointer so a test can swap in a fresh one.
var rootlessWarned = &sync.Map{}

// warnRootlessOnce logs, once per store per process, that an account store was
// released with no root line to bound it. It names the path and the fix,
// never a variable.
func warnRootlessOnce(path, key string) {
	if _, seen := rootlessWarned.LoadOrStore(path, struct{}{}); seen {
		return
	}
	log.Printf("claudeacct: store-rootless: %s carries no \"%s\" line, so it is released to every directory "+
		"bound to %s; to confine it, add one line per tree it serves: %s <abs path>",
		path, "# "+storeRootMarker, strings.TrimSpace(key), "# "+storeRootMarker)
}

// collapseLastWins keeps, for every name, only its LAST entry, in the order those
// last entries appear. A delta with no repeated name is returned unchanged.
func collapseLastWins(delta []string) []string {
	last := make(map[string]int, len(delta))
	for i, kv := range delta {
		last[envKey(kv)] = i
	}
	if len(last) == len(delta) {
		return delta
	}
	out := make([]string, 0, len(last))
	for i, kv := range delta {
		if last[envKey(kv)] == i {
			out = append(out, kv)
		}
	}
	return out
}

// SpawnEnvResolved returns the environment a child launched under r runs with,
// derived from base (normally os.Environ()). See the file header for the three
// account cases. base is returned as-is — same backing array — when there is
// nothing to add and nothing to remove, so a spawn that resolves nothing stays a
// byte-identical passthrough.
func SpawnEnvResolved(base []string, r Resolution) []string {
	delta := resolvedDelta(r)
	drop := make(map[string]struct{}, len(delta)+1)
	for _, kv := range delta {
		drop[envKey(kv)] = struct{}{}
	}
	if strings.TrimSpace(r.Account) == ingest.DefaultAccount {
		drop[configDirEnv] = struct{}{}
	}
	kept := withoutKeys(base, drop)
	if len(delta) == 0 {
		return kept
	}
	// Clip so the append can never write into base's spare capacity.
	return append(slices.Clip(kept), delta...)
}

// SpawnEnv is SpawnEnvResolved for a caller that holds only an ACCOUNT key and
// no project path — no estate is resolved, so only the account layer applies.
func SpawnEnv(base []string, key string) []string {
	key = strings.TrimSpace(key)
	return SpawnEnvResolved(base, Resolution{
		Account:        key,
		DefaultProfile: key == "" || key == ingest.DefaultAccount,
	})
}

// SpawnEnvFor resolves projectPath on both axes (Resolve) and delegates to
// SpawnEnvResolved.
//
// The empty-path guard is load-bearing, not defensive style: a binding path is
// joined onto its argument unconditionally, so "" would read the RELATIVE
// .claude/settings.local.json against the daemon's own working directory and
// silently bind the child to whatever unrelated settings file sits there. ""
// means "no project" and must return base untouched.
//
// A path under the daemon's worktree root resolves through its source checkout
// (worktreesrc.go), so a worktree cwd is no longer a silent "default account";
// the key-carrying engines still resolve once from the PROJECT path and pass the
// Resolution down, which is the contract runcore.Spec enforces.
func SpawnEnvFor(base []string, projectPath string) []string {
	if strings.TrimSpace(projectPath) == "" {
		return base
	}
	return SpawnEnvResolved(base, Resolve(projectPath))
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
