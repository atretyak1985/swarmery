package usage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Sentinel outcomes of LoadCreds. Neither is a failure the caller should log
// as an error: ErrNoCreds means "the operator has not logged in with the
// `claude` CLI", ErrDisabled means "the operator opted out".
var (
	// ErrNoCreds means no Claude credential was found in any source.
	ErrNoCreds = errors.New("usage: no Claude credentials found")
	// ErrDisabled means SWARMERY_USAGE_OAUTH=0 — the credential read is
	// switched off and nothing on disk or in the keychain was touched.
	ErrDisabled = errors.New("usage: OAuth usage lookup disabled (SWARMERY_USAGE_OAUTH=0)")
	// errNoStore means swarmery's own credential store has no resolvable
	// location (no home directory, or an unusable account key), so a rung-2
	// credential cannot be persisted. Unexported: callers only ever report it
	// as "could not save the connection".
	errNoStore = errors.New("usage: swarmery credential store is unavailable")
)

const (
	// oauthOptOutEnv disables the credential read entirely when set to "0".
	oauthOptOutEnv = "SWARMERY_USAGE_OAUTH"
	// configDirEnv mirrors the `claude` CLI's own credential-location override.
	configDirEnv = "CLAUDE_CONFIG_DIR"
	// credentialsFile is the CLI's credential file name in every source dir.
	credentialsFile = ".credentials.json"
	// keychainService is the macOS login-keychain item the CLI writes.
	keychainService = "Claude Code-credentials"
	// keychainTimeout bounds the `security` invocation so a keychain prompt
	// can never wedge a dashboard poll.
	keychainTimeout = 5 * time.Second
)

// Creds is the operator's Claude OAuth credential. It never leaves this
// package: it is not persisted, not logged, and not serialized into any API
// response. See the package doc's policy note.
type Creds struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        int64 // unix millis; 0 = unknown
	Scopes           []string
	SubscriptionType string
	RateLimitTier    string

	// FromStore marks a credential that came from SWARMERY'S OWN store
	// (store.go), i.e. one the operator authorized through the dashboard rather
	// than through the `claude` CLI. It is the provenance the refresh path keys
	// on: our own session persists a rotated refresh token, the CLI's is never
	// written back. Set only by storedCreds; never serialized.
	FromStore bool
}

// String redacts. This is the whole point: an accidental %v, %s or %+v on a
// Creds (or *Creds) anywhere in the tree prints a constant, never a token.
func (c Creds) String() string { return "usage.Creds{redacted}" }

// securityBin is the macOS keychain reader binary. A package var only so tests
// can point it at a stub script and never touch the real login keychain.
var securityBin = "security"

// keychainCreds is a seam over readKeychainCreds so tests can guarantee no
// `security` invocation (and therefore no keychain prompt) ever happens.
var keychainCreds = readKeychainCreds

// Source names WHOSE credential to resolve — one Claude Code account, keyed the
// same way ingest keys a transcript (ingest.AccountFor: "default", "nabu-org", …)
// and located by the config dir the `claude` CLI logs in under.
//
// The ZERO Source is the default account resolved through the legacy chain, so
// LoadCredsFor(ctx, Source{}) is byte-identical to what LoadCreds always did. A
// non-empty ConfigDir switches to the EXCLUSIVE scoped lookup — the only way a
// second account is ever read.
//
// It is also the seam the planned rung 2 (swarmery's own PKCE session) plugs
// into: per-account resolution — swarmery store → config dir → keychain — is
// decided from a Source, so adding it needs no second refactor here.
type Source struct {
	// Account is the account key, for labelling only; resolution never uses it.
	Account string
	// ConfigDir is the account's `claude` config dir (the parent of its
	// projects root). "" means the legacy chain — see LoadCredsFor.
	ConfigDir string
	// IgnoreConfigDirEnv drops the CLAUDE_CONFIG_DIR rung from the legacy
	// chain (meaningful only with an empty ConfigDir). The DEFAULT account is
	// the one `claude` runs with that variable UNSET, so a process that merely
	// inherited it (a daemon started from a shell bound to another account)
	// would otherwise read that other account's credential and publish its
	// numbers under "default". The ~/.claude file, ~/.config/claude and the
	// plain keychain item — the default login's own sources — stay.
	IgnoreConfigDirEnv bool
}

// LoadCredsFor resolves ONE account's Claude OAuth credential.
//
// Resolution order, for every account:
//
//  1. swarmery's OWN store — ~/.swarmery/credentials/<account>.json (store.go),
//     written by the dashboard's Connect flow. Checked first because it is the
//     credential the operator most recently and most explicitly gave THIS
//     daemon: once an account is connected here, that connection is the answer.
//  2. then the behaviour rung 1 shipped, widened by one same-account source:
//     · non-empty src.ConfigDir → <ConfigDir>/.credentials.json, then (darwin)
//     the SUFFIXED keychain item "Claude Code-credentials-<sha256(dir)[0:8]>"
//     the CLI itself writes when logging in under that config dir. No home-dir
//     sources, no CLAUDE_CONFIG_DIR (that env var names the default account's
//     dir and would be a lie here), and NEVER the plain keychain item;
//     · empty src.ConfigDir → the legacy chain (see LoadCreds).
//
// The exclusivity at step 2 is the whole safety property of multi-account: any
// cross-account fallback would resolve the DEFAULT account's credential and
// publish its quota under a second account's name — a wrong number the operator
// cannot spot. The suffixed item does not weaken this: its name is derived from
// the account's own config dir, so it can only ever hold that account's
// credential. It is also the source that matters in practice — on macOS the CLI
// writes the login to the keychain and no .credentials.json exists at all
// (measured live, 2026-08-07: /login under a fresh config dir → suffixed item
// created, NO FILE), so without this rung every CLI-logged-in second account
// reads as "not connected".
//
// Step 1 is a no-op when the store holds nothing for this account (including an
// empty src.Account, which has no store file by construction), so a machine that
// has never used the Connect flow behaves exactly as rung 1 did.
//
// A scoped credential is returned even when expired: the refresh path is the
// backstop, the same way the chain falls back to its latest-expiring candidate.
//
// SWARMERY_USAGE_OAUTH=0 returns ErrDisabled before any source is touched, for
// every account.
func LoadCredsFor(ctx context.Context, src Source) (*Creds, error) {
	if os.Getenv(oauthOptOutEnv) == "0" {
		return nil, ErrDisabled
	}
	if c := storedCreds(src.Account); c != nil {
		return c, nil
	}
	if src.ConfigDir != "" {
		return scopedCreds(ctx, src.ConfigDir)
	}
	return chainCreds(ctx, src.IgnoreConfigDirEnv)
}

// scopedCreds resolves one non-default account's credential from that account's
// OWN two sources, and nothing else: its credential file, then (darwin) its
// suffixed keychain item. Expiry breaks ties exactly as in chainCreds — an
// unexpired hit wins immediately, otherwise the later-expiring stale candidate
// is the fallback (its refresh token is the likeliest to still be live).
func scopedCreds(ctx context.Context, dir string) (*Creds, error) {
	var stale *Creds
	if raw, err := os.ReadFile(filepath.Join(dir, credentialsFile)); err == nil {
		if c := parseCreds(raw); c != nil {
			if !c.expired() {
				return c, nil
			}
			stale = laterExpiry(stale, c)
		}
	}
	if runtime.GOOS == "darwin" {
		if c := keychainCreds(ctx, scopedKeychainService(dir)); c != nil {
			if !c.expired() {
				return c, nil
			}
			stale = laterExpiry(stale, c)
		}
	}
	if stale != nil {
		return stale, nil
	}
	return nil, ErrNoCreds
}

// scopedKeychainService names the keychain item the `claude` CLI writes when it
// logs in under a non-default config dir: the plain service name suffixed with
// the first 8 hex of sha256 over the RAW config-dir string. The same derivation
// ships in plugins/core/statusline/fetch-fable-usage.sh and was confirmed
// against the CLI binary (2.1.220: `${service}-${sha256(dir).substring(0,8)}`);
// the three implementations must not drift.
func scopedKeychainService(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return keychainService + "-" + hex.EncodeToString(sum[:])[:8]
}

// LoadCreds resolves the DEFAULT account's Claude OAuth credential — the legacy
// chain, unchanged. Sources are consulted in this order, and the first UNEXPIRED
// hit wins:
//
//  1. $CLAUDE_CONFIG_DIR/.credentials.json (when CLAUDE_CONFIG_DIR is set)
//  2. ~/.claude/.credentials.json
//  3. ~/.config/claude/.credentials.json
//  4. macOS only: `security find-generic-password -s "Claude Code-credentials" -w`
//
// Expiry has to break the tie, not mere presence: on macOS the CLI keeps the
// live credential in the login keychain, so a leftover ~/.claude/.credentials.json
// written by an older CLI would otherwise shadow it forever — the daemon would
// report "run `claude` to re-login" no matter how many times the operator did.
// Expired candidates are therefore remembered rather than returned, and the one
// expiring latest is the fallback if every source turns out to be stale (its
// refresh token is the likeliest to still be live).
//
// Every individual failure is silent — a missing or unreadable source is just
// the next candidate. Exhausting all sources returns ErrNoCreds; opting out
// with SWARMERY_USAGE_OAUTH=0 returns ErrDisabled before any source is touched.
func LoadCreds(ctx context.Context) (*Creds, error) {
	return LoadCredsFor(ctx, Source{})
}

// chainCreds is the legacy multi-source resolution documented on LoadCreds;
// skipEnv drops the CLAUDE_CONFIG_DIR rung (Source.IgnoreConfigDirEnv).
func chainCreds(ctx context.Context, skipEnv bool) (*Creds, error) {
	var stale *Creds
	for _, path := range credentialPaths(skipEnv) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		c := parseCreds(raw)
		if c == nil {
			continue
		}
		if !c.expired() {
			return c, nil
		}
		stale = laterExpiry(stale, c)
	}
	if runtime.GOOS == "darwin" {
		if c := keychainCreds(ctx, keychainService); c != nil {
			if !c.expired() {
				return c, nil
			}
			stale = laterExpiry(stale, c)
		}
	}
	if stale != nil {
		return stale, nil
	}
	return nil, ErrNoCreds
}

// credsNow is the clock seam for expiry comparison. A package var rather than a
// Client field because LoadCreds is a package function with no Client to hang it
// off; tests pin it to make staleness deterministic.
var credsNow = time.Now

// expired reports whether the credential's access token is past its expiry,
// using the same grace window (*Client).tokenExpired applies so the two agree
// on what "usable" means. An UNKNOWN expiry (0, absent from the file) is not
// treated as expired: the CLI has shipped credential files without the field,
// and judging those stale would demote a perfectly good login.
func (c *Creds) expired() bool {
	if c.ExpiresAt <= 0 {
		return false
	}
	return credsNow().UnixMilli() >= c.ExpiresAt-tokenExpiryGrace.Milliseconds()
}

// laterExpiry keeps whichever expired candidate expires latest, preferring the
// incumbent on a tie so source precedence still decides between equals.
func laterExpiry(best, next *Creds) *Creds {
	if best == nil || next.ExpiresAt > best.ExpiresAt {
		return next
	}
	return best
}

// credentialPaths lists the file sources in resolution order. Sources whose
// base directory cannot be resolved are simply omitted; skipEnv omits the
// CLAUDE_CONFIG_DIR rung.
func credentialPaths(skipEnv bool) []string {
	var paths []string
	if dir := os.Getenv(configDirEnv); dir != "" && !skipEnv {
		paths = append(paths, filepath.Join(dir, credentialsFile))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return paths
	}
	return append(paths,
		filepath.Join(home, ".claude", credentialsFile),
		filepath.Join(home, ".config", "claude", credentialsFile),
	)
}

// CredentialSources lists, in resolution order, the sources LoadCreds consults,
// in a form fit for an operator-facing setup hint ("looked in …"). It reports
// LOCATIONS only and never reads or reveals credential content.
func CredentialSources() []string {
	return chainSources(false)
}

// chainSources is CredentialSources with the CLAUDE_CONFIG_DIR rung optional.
func chainSources(skipEnv bool) []string {
	srcs := credentialPaths(skipEnv)
	if runtime.GOOS == "darwin" {
		srcs = append(srcs, "macOS Keychain: "+keychainService)
	}
	return srcs
}

// CredentialSourcesFor is CredentialSources for one account: the exact list
// LoadCredsFor would consult for src, in order, so the setup hint can never
// claim the daemon looked somewhere it did not.
//
// The list therefore opens with swarmery's own store path whenever the account
// has one — that is where the dashboard's Connect flow writes, and an operator
// debugging a card that will not connect needs to see it.
//
// A scoped account then lists ONE config-dir path and never the keychain: the
// plain keychain item is the default account's login, so offering it to a second
// account would point the operator at the wrong subscription's credential.
func CredentialSourcesFor(src Source) []string {
	var srcs []string
	if p := storePath(src.Account); p != "" {
		srcs = append(srcs, p)
	}
	if src.ConfigDir != "" {
		srcs = append(srcs, filepath.Join(src.ConfigDir, credentialsFile))
		if runtime.GOOS == "darwin" {
			srcs = append(srcs, "macOS Keychain: "+scopedKeychainService(src.ConfigDir))
		}
		return srcs
	}
	return append(srcs, chainSources(src.IgnoreConfigDirEnv)...)
}

// readKeychainCreds reads ONE credential item out of the macOS login keychain —
// the plain service for the default account, a suffixed one for a scoped
// account. The 5s timeout keeps a keychain prompt from wedging a poll. Callers
// are responsible for the runtime.GOOS guard (chainCreds and scopedCreds do it)
// so this stays directly testable against a stub binary.
func readKeychainCreds(ctx context.Context, service string) *Creds {
	ctx, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, securityBin, keychainArgs(service)...).Output()
	if err != nil {
		return nil
	}
	return parseCreds(bytes.TrimSpace(out))
}

// keychainArgs is the `security` invocation used to read a credential item.
func keychainArgs(service string) []string {
	return []string{"find-generic-password", "-s", service, "-w"}
}

// rawCreds is the credential JSON as the `claude` CLI writes it (camelCase).
type rawCreds struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

func (r rawCreds) toCreds() *Creds {
	return &Creds{
		AccessToken:      r.AccessToken,
		RefreshToken:     r.RefreshToken,
		ExpiresAt:        r.ExpiresAt,
		Scopes:           r.Scopes,
		SubscriptionType: r.SubscriptionType,
		RateLimitTier:    r.RateLimitTier,
	}
}

// parseCreds accepts both shapes the CLI has shipped: the credential nested
// under "claudeAiOauth", and a bare credential object at the root (Fusion's
// `creds?.claudeAiOauth || creds`, usage.ts:974). Anything without an access
// token is treated as absent, not as an error.
func parseCreds(raw []byte) *Creds {
	var wrapper struct {
		ClaudeAiOauth *rawCreds `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil &&
		wrapper.ClaudeAiOauth != nil && wrapper.ClaudeAiOauth.AccessToken != "" {
		return wrapper.ClaudeAiOauth.toCreds()
	}
	var bare rawCreds
	if err := json.Unmarshal(raw, &bare); err == nil && bare.AccessToken != "" {
		return bare.toCreds()
	}
	return nil
}
