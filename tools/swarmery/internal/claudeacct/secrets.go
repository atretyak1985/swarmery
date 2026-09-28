package claudeacct

// Per-account SECRETS, delivered through the one channel that survives every
// route a swarmery-spawned `claude` can take: the PARENT PROCESS ENVIRONMENT.
//
// # Why the environment and nothing else
//
// The MCP servers a plugin ships reference their credentials as ${VAR}, and the
// CLI expands those. A measurement across every route and every
// --setting-sources value established three rules. Re-measured 2026-09-23
// against CLI 2.1.280 by scripts/tests/cc-channel-probe.sh; the record is
// tools/swarmery/docs/claude-cli-config-channels.md.
//
//  1. the parent process environment expands ${VAR} EVERYWHERE — every route,
//     every setting-source combination;
//  2. of the three setting sources, a settings `env` block expands for a
//     plugin-shipped .mcp.json ONLY under `user` — i.e. only out of
//     <configDir>/settings.json, which makes it an ACCOUNT-KEYED channel. (A
//     --settings file's env block expands too, under every --setting-sources
//     value; planrun and phaserun pass --settings only for a multi-repo
//     project — when repopath.InheritedSettings returns a path — and no other
//     seam passes one.)
//     Note what this does NOT say: it is NOT true that every daemon spawn
//     closes the user tier. Twelve seams pass --setting-sources project,local
//     and there the block is inert; but planning, planrun and phaserun pass NO
//     --setting-sources at all (they build a runcore.Spec with SettingSources
//     empty, and runcore.Args emits the flag only when it is non-empty), so on
//     those three the user tier is live and the account's env block DOES reach
//     the child;
//  3. a daemon-cut worktree inherits nothing from the checkout by any CLI rule —
//     it lives under ~/.swarmery/worktrees/, outside the project tree. What it
//     carries is what internal/worktree/configsync.go COPIED in at Acquire
//     time, and copyMissing leaves an existing worktree file alone, so the
//     worktree's own copy wins whenever it has one.
//
// Rule 2 is why credentials must never be written into a settings file: the
// only setting source that expands them is the tier keyed to the account, and a
// --settings file is still a credential at rest on disk. Rule 1 is what this
// file builds on.
//
// # Why a per-account store and not the launchd plist
//
// Baking the variables into the daemon's LaunchAgent plist also satisfies rule
// 1, and was rejected for two reasons: it is MACHINE-WIDE, so every daemon run
// of every project would inherit one account's credentials regardless of its
// binding; and a LaunchAgent plist does not reach a login shell, so it covers
// neither half of the terminal channel. Keying the store per binding fixes both,
// and the bindings needed to key it are already on disk (binding.go).
//
// # Which key names a store
//
// A store is named by the ESTATE — the project tree (resolve.go) — not by the
// account. The credentials in it authenticate against a project's own
// infrastructure; which Claude subscription pays for tokens has nothing to do
// with them, so a tree's store must not vanish when one of its sub-repos is
// pinned to a different account. The account-keyed store <account>.env is still
// read, FIRST, as a back-compat layer (SecretEnvForAccount), and the estate's
// store is read after it and wins any name collision (spawnenv.go). An estate
// with no <key>.env file is healthy and contributes nothing.
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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
	// file. Both are also the CEILINGS the loader enforces (checkStoreDir,
	// secretEnvFromFile).
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

// SecretsPath is one STORE's file, <SecretsDir>/<store>.env, or "" when there is
// no store dir or the key is not usable as a bare file name.
//
// The store key is the PROJECT axis: normally an estate key (resolve.go), and
// for the back-compat layer an account key. It is not rejected for being
// "default" — that rejection belongs to the ACCOUNT layer only
// (SecretEnvForAccount), because which subscription pays has nothing to do with
// which credentials a project tree needs.
//
// The key ultimately comes from a file the OPERATOR controls, so it is
// validated rather than trusted (ValidKey): a "/" or a ".." in it would
// otherwise escape the store directory.
func SecretsPath(store string) string {
	store = strings.TrimSpace(store)
	if !ValidKey(store) {
		return ""
	}
	base := SecretsDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, store+".env")
}

// SecretEnvForStore is the env DELTA of one store's secrets. nil for an unusable
// key, for a missing store file, and for a store file whose mode is too
// permissive. A declared estate whose store does not exist lands here and gets
// nil SILENTLY — an estate with no credential store is a first-class state, not
// a degraded one, so no error, warning or log line is emitted for it.
func SecretEnvForStore(store string) []string {
	path := SecretsPath(store)
	if path == "" {
		return nil
	}
	return secretEnvFromFile(path)
}

// SecretEnvForAccount is the env DELTA of secrets for one ACCOUNT key — the
// back-compat layer, from before stores were keyed by estate: a machine whose
// only store is <account>.env keeps working exactly as it did. nil for the
// default account (unchanged semantics: the default account never had a store
// of its own), for an unresolvable key, for a missing store file, and for a
// store file whose mode is too permissive.
//
// nil is the answer for "there is nothing here", never an error: most projects
// on a typical machine have no store at all, and a spawn must not fail because
// an optional file is absent.
func SecretEnvForAccount(key string) []string {
	key = strings.TrimSpace(key)
	if key == "" || key == ingest.DefaultAccount {
		return nil
	}
	return SecretEnvForStore(key)
}

// SecretEnvFor resolves the project's OWN account binding (no walk, no estate)
// and delegates to the back-compat account layer. Spawn sites do not use it: they
// compose through SpawnEnvResolved, which adds the estate's store.
func SecretEnvFor(projectPath string) []string {
	return SecretEnvForAccount(Binding(projectPath))
}

// secretEnvFromFile opens, checks and parses one store file.
//
// The checks are the reason this is not a bare os.ReadFile. A secret store that
// silently tolerates 0644 is not a secret store: it would hand every local
// process the credentials while reporting success. So the store is opened
// WITHOUT following a symlink, and every check is made on the OPENED file (no
// window to swap one in): a regular file, owned by the current user, not
// readable by group or other — inside a directory owned by the current user and
// closed to group and other, since a directory another user can write lets them
// replace the file. Refusal logs the path and the MODE — never a name, never a
// value — because the operator has to be told which file to chmod.
func secretEnvFromFile(path string) []string {
	f, c := openStore(path)
	if c.log != "" {
		log.Print(c.log)
	}
	if f == nil {
		return nil
	}
	defer f.Close()
	return parseSecretEnv(path, f)
}

// StoreState is what one credential store is, as the LOADER sees it — the same
// checks secretEnvFromFile applies, so a surface can never report a store the
// loader refuses as "present".
type StoreState int

const (
	// StoreAbsent: no file at the store's path. Healthy — zero credentials.
	StoreAbsent StoreState = iota
	// StorePresent: the loader would read it.
	StorePresent
	// StoreRefused: something is there, and the loader will not read it (a
	// symlink, a mode open beyond its owner, a foreign owner, an open store
	// directory, …). Zero credentials, and the operator has something to fix.
	StoreRefused
	// StoreUnadmitted: the loader would read it, but its root lines do not admit
	// the estate root asking for it — or, for an estate store, it carries none
	// (the unanchored state, D5). Zero credentials; the fix is a root line in
	// the store, which only the operator writes.
	StoreUnadmitted
)

// storeCheck is the loader's verdict on one store file.
type storeCheck struct {
	State StoreState
	// Reason says why a StoreRefused store is refused. It names neither the
	// store's path nor anything inside it, so a surface may print it.
	Reason string
	// log is the operator-facing log line for a refusal (or an I/O error): it
	// names the path and the MODE, never a variable name or a value.
	log string
}

// openStore opens path WITHOUT following a symlink and makes every loader check
// on the OPENED file (no window to swap one in) and on its directory. The file is
// returned open only when the verdict is StorePresent; the caller closes it.
// It logs nothing itself: secretEnvFromFile logs c.log, a surface prints
// c.Reason.
func openStore(path string) (*os.File, storeCheck) {
	f, err := openNoFollow(path)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// no store for this key: the normal case, silent
			return nil, storeCheck{State: StoreAbsent}
		case errors.Is(err, syscall.ELOOP):
			return nil, storeCheck{
				State:  StoreRefused,
				Reason: "the store is a symlink",
				log: fmt.Sprintf("claudeacct: REFUSING secret store %s: it is a symlink — no secret was loaded; "+
					"replace it with a regular file (mode %04o)", path, secretsFileMode),
			}
		default:
			return nil, storeCheck{
				State:  StoreRefused,
				Reason: fmt.Sprintf("the store cannot be opened (%v)", unwrapPathErr(err)),
				log:    fmt.Sprintf("claudeacct: cannot read secret store %s: %v", path, err),
			}
		}
	}
	c := checkOpenedStore(path, f)
	if c.State != StorePresent {
		f.Close()
		return nil, c
	}
	return f, c
}

// checkOpenedStore is openStore's checks on an opened file: the directory first
// (whoever can write it can swap the file), then the file's type, mode and owner.
func checkOpenedStore(path string, f *os.File) storeCheck {
	if c := checkStoreDir(filepath.Dir(path)); c.State != StorePresent {
		return c
	}
	info, err := f.Stat()
	if err != nil {
		return storeCheck{
			State:  StoreRefused,
			Reason: fmt.Sprintf("the store cannot be inspected (%v)", unwrapPathErr(err)),
			log:    fmt.Sprintf("claudeacct: cannot stat secret store %s: %v", path, err),
		}
	}
	if info.IsDir() {
		return storeCheck{
			State:  StoreRefused,
			Reason: "the store is a directory",
			log:    fmt.Sprintf("claudeacct: secret store %s is a directory; ignoring it", path),
		}
	}
	mode := info.Mode().Perm()
	if !info.Mode().IsRegular() {
		return storeCheck{
			State:  StoreRefused,
			Reason: "the store is not a regular file",
			log:    fmt.Sprintf("claudeacct: REFUSING secret store %s: not a regular file — no secret was loaded", path),
		}
	}
	if mode&secretsGroupOther != 0 {
		return storeCheck{
			State:  StoreRefused,
			Reason: fmt.Sprintf("the store's mode %04o is open beyond its owner (want %04o)", mode, secretsFileMode),
			log: fmt.Sprintf("claudeacct: REFUSING secret store %s: mode %04o is readable beyond its owner "+
				"(want %04o) — no secret was loaded; fix it with: chmod %04o %s",
				path, mode, secretsFileMode, secretsFileMode, path),
		}
	}
	if uid, ok := fileOwner(info); !ok || uid != currentUID() {
		return storeCheck{
			State:  StoreRefused,
			Reason: "the store is not owned by you",
			log: fmt.Sprintf("claudeacct: REFUSING secret store %s (mode %04o): it is not owned by the current user "+
				"— no secret was loaded", path, mode),
		}
	}
	return storeCheck{State: StorePresent}
}

// checkStoreDir is whether the store directory may hold secrets: owned by the
// current user and closed to group and other (secretsDirMode is the ceiling).
// StorePresent means "the directory passes".
func checkStoreDir(dir string) storeCheck {
	info, err := os.Stat(dir)
	if err != nil {
		return storeCheck{
			State:  StoreRefused,
			Reason: fmt.Sprintf("the store directory cannot be inspected (%v)", unwrapPathErr(err)),
			log:    fmt.Sprintf("claudeacct: cannot stat secret store directory %s: %v", dir, err),
		}
	}
	mode := info.Mode().Perm()
	if mode&^secretsDirMode != 0 {
		return storeCheck{
			State:  StoreRefused,
			Reason: fmt.Sprintf("the store directory's mode %04o is open beyond its owner (want %04o)", mode, secretsDirMode),
			log: fmt.Sprintf("claudeacct: REFUSING secret store directory %s: mode %04o is accessible beyond its owner "+
				"(want %04o) — no secret was loaded; fix it with: chmod %04o %s",
				dir, mode, secretsDirMode, secretsDirMode, dir),
		}
	}
	if uid, ok := fileOwner(info); !ok || uid != currentUID() {
		return storeCheck{
			State:  StoreRefused,
			Reason: "the store directory is not owned by you",
			log: fmt.Sprintf("claudeacct: REFUSING secret store directory %s (mode %04o): it is not owned by the current user "+
				"— no secret was loaded", dir, mode),
		}
	}
	return storeCheck{State: StorePresent}
}

// unwrapPathErr drops the path an *fs.PathError carries, so a Reason built from
// it never names the store's path.
func unwrapPathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
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
