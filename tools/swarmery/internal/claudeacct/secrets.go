package claudeacct

// Per-account SECRETS, delivered through the one channel that survives every
// route a swarmery-spawned `claude` can take: the PARENT PROCESS ENVIRONMENT.
//
// # Why the environment and nothing else
//
// The MCP servers a plugin ships reference their credentials as ${VAR}, and the
// CLI expands those. A measurement across every route and every
// --setting-sources value established three rules:
//
//  1. the parent process environment expands ${VAR} EVERYWHERE — every route,
//     every setting-source combination;
//  2. a settings `env` block expands only for a plugin-shipped .mcp.json AND
//     only under the `user` setting source. Every daemon spawn passes
//     `project,local`, so a settings `env` block is silently inert there;
//  3. inside a daemon-cut worktree the SOURCE checkout's settings.local.json
//     shadows the worktree's own copy, so a per-worktree override is not even
//     expressible.
//
// Rules 2 and 3 are why credentials must never be written into a
// settings.local.json. Rule 1 is what this file builds on.
//
// # Why a per-account store and not the launchd plist
//
// Baking the variables into the daemon's LaunchAgent plist also satisfies rule
// 1, and was rejected for two reasons: it is MACHINE-WIDE, so every daemon run
// of every project would inherit one account's credentials regardless of its
// binding; and a LaunchAgent plist does not reach a login shell, so it covers
// neither half of the terminal channel. Keying the store by account fixes both,
// and the bindings needed to key it are already on disk (binding.go).
//
// # The contract
//
// These functions are the SECRET-carrying siblings of EnvFor/EnvForAccount, and
// they are deliberately separate rather than folded into them: `swarmery account
// env` prints EnvFor's result to STDOUT (cmd/swarmery/account.go), i.e. into the
// operator's terminal and scrollback, and the shell function installed by
// plugins/accounts-pack pattern-matches that whole output against
// `CLAUDE_CONFIG_DIR=?*`. Secrets must never reach that stdout, and a second
// line would silently drop the account binding. Only the two exec/spawn seams —
// which pass an env array to a child and never print it — call these.

import (
	"bufio"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

const (
	// secretsDirEnv overrides the store's base directory. The same escape hatch
	// SWARMERY_CREDENTIALS_DIR gives the credential store (internal/usage), and
	// for the same reason: no test may read or write the operator's real
	// ~/.swarmery.
	secretsDirEnv = "SWARMERY_SECRETS_DIR"
	// secretsDirMode/secretsFileMode are the hygiene contract, identical to the
	// credential store's: only the owner may traverse the directory or read a
	// file. secretsFileMode is also the CEILING enforced by the loader.
	secretsDirMode  = 0o700
	secretsFileMode = 0o600
	// secretsGroupOther is every permission bit outside the owner's. A store
	// file with any of them set is refused.
	secretsGroupOther = 0o077
)

// SecretsDir is the directory holding the per-account secret stores:
// $SWARMERY_SECRETS_DIR when set, else ~/.swarmery/secrets. "" when neither can
// be resolved, which every caller reads as "there is no store" — a machine with
// no resolvable home simply gets no secrets, never an error.
func SecretsDir() string {
	if dir := strings.TrimSpace(os.Getenv(secretsDirEnv)); dir != "" {
		return dir
	}
	home, err := userHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".swarmery", "secrets")
}

// SecretsPath is one account's store file, or "" when there is no store dir or
// the key is not usable as a bare file name.
//
// The key ultimately comes from a directory name the OPERATOR controls, so it is
// validated rather than trusted (ValidKey): a "/" or a ".." in it would
// otherwise escape the store directory.
func SecretsPath(account string) string {
	account = strings.TrimSpace(account)
	if account == "" || account == ingest.DefaultAccount || !ValidKey(account) {
		return ""
	}
	base := SecretsDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, account+".env")
}

// SecretEnvForAccount is the env DELTA of secrets for one account key, to be
// appended to os.Environ() by a spawner. nil for the default account, for an
// unresolvable key, for a missing store file, and for a store file whose mode is
// too permissive.
//
// nil is the answer for "there is nothing here", never an error: 12 of the 13
// indexed projects on a typical machine have no store at all, and a spawn must
// not fail because an optional file is absent.
func SecretEnvForAccount(key string) []string {
	path := SecretsPath(key)
	if path == "" {
		return nil
	}
	return secretEnvFromFile(path)
}

// SecretEnvFor resolves the project's binding and delegates. Terminal callers
// use this one; the daemon's spawn sites hold the resolved key already and must
// use SecretEnvForAccount — they run in a worktree that carries no binding file
// of its own, so resolving from cwd there would silently yield the default
// account and no secrets.
func SecretEnvFor(projectPath string) []string {
	return SecretEnvForAccount(Binding(projectPath))
}

// secretEnvFromFile stats, mode-checks and parses one store file.
//
// The mode check is the reason this is not a bare os.ReadFile. A secret store
// that silently tolerates 0644 is not a secret store: it would hand every local
// process the credentials while reporting success. Refusal logs the path and the
// MODE — never a name, never a value — because the operator has to be told which
// file to chmod.
func secretEnvFromFile(path string) []string {
	info, err := os.Stat(path)
	if err != nil {
		return nil // no store for this account: the normal case
	}
	if info.IsDir() {
		log.Printf("claudeacct: secret store %s is a directory; ignoring it", path)
		return nil
	}
	if mode := info.Mode().Perm(); mode&secretsGroupOther != 0 {
		log.Printf("claudeacct: REFUSING secret store %s: mode %04o is readable beyond its owner "+
			"(want %04o) — no secret was loaded; fix it with: chmod %04o %s",
			path, mode, secretsFileMode, secretsFileMode, path)
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("claudeacct: cannot read secret store %s: %v", path, err)
		return nil
	}
	defer f.Close()
	return parseSecretEnv(path, f)
}

// parseSecretEnv reads the KEY=value lines of a store.
//
// Format, deliberately the intersection of what a .env file and a shell both
// accept, so an existing .env can be moved in unchanged:
//
//	KEY=value            # a pair
//	export KEY=value     # the leading `export ` is tolerated and dropped
//	KEY="value"          # ONE matched pair of surrounding " or ' is stripped
//	# comment            # skipped, as are blank lines
//
// A malformed line is reported BY NUMBER ONLY and skipped. Neither its name nor
// its content is logged: the line that fails to parse is exactly the line most
// likely to be a mangled credential.
func parseSecretEnv(path string, r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	// A credential can be long (a base64 blob, a PEM on one line); the stock
	// 64 KiB token limit is generous but the default 4 KiB start buffer would
	// grow repeatedly, so start at 64 KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !validEnvName(name) {
			log.Printf("claudeacct: secret store %s: skipping malformed line %d", path, n)
			continue
		}
		// The binding OWNS the config dir: the store is keyed by the account the
		// binding named, so a store line re-pointing it would make the two spawn
		// seams disagree (os/exec is last-wins, raw execve is first-wins). It is
		// refused, by name — this one name is not a secret.
		if name == configDirEnv {
			log.Printf("claudeacct: secret store %s: line %d sets %s, which the account binding owns; ignoring it",
				path, n, configDirEnv)
			continue
		}
		out = append(out, name+"="+unquote(strings.TrimSpace(value)))
	}
	if err := sc.Err(); err != nil {
		log.Printf("claudeacct: secret store %s: read error: %v", path, err)
	}
	return out
}

// unquote strips ONE matched pair of surrounding quotes. Nothing else: no escape
// processing, no variable expansion. A value is delivered to the child byte for
// byte, because it is a credential and a "helpful" transformation of one
// produces an authentication failure nobody can read off the wire.
func unquote(v string) string {
	if len(v) < 2 {
		return v
	}
	q := v[0]
	if (q == '"' || q == '\'') && v[len(v)-1] == q {
		return v[1 : len(v)-1]
	}
	return v
}

// validEnvName reports whether name is a POSIX-shaped variable name. An invalid
// one is dropped rather than passed through: execve happily carries "a b=c" into
// the child, where no getenv can ever retrieve it.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
