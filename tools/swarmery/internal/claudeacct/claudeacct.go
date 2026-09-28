// Package claudeacct is the single source of truth for "which Claude Code
// account does this project run under".
//
// Accounts are keyed exactly as ingest.AccountFor keys a transcript, and an
// account's config dir is the directory that key was derived from — there is no
// second naming scheme by design: usage quota (usage.Source), ingested sessions
// (sessions.account) and spawned processes (CLAUDE_CONFIG_DIR) must agree on the
// key or the operator sees three different truths.
//
// The DEFAULT account is deliberately env-LESS: it lives in ~/.claude, which is
// where the CLI looks with no CLAUDE_CONFIG_DIR set, so binding a project to it
// must produce an empty env delta rather than an explicit variable. That keeps
// "no binding" and "bound to default" byte-identical to today's behaviour.
//
// Switching the config dir is sufficient to switch the account: a measurement on
// CLI 2.1.220 confirmed that a non-default CLAUDE_CONFIG_DIR does NOT fall back
// to the default credential and that the CLI namespaces its keychain item per
// config dir. swarmery therefore only ever points a process at a dir that holds
// a login — one the operator made with the CLI, or the write-once credential
// handoff the connect flow performs (usage.HandoffToConfigDir, never refreshed;
// see internal/usage/handoff.go and docs/claude-cli-credential-behaviour.md).
package claudeacct

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// configDirEnv is the CLI's config-dir override — the ONLY switch swarmery uses
// to move a process onto another account (see the package doc).
const configDirEnv = "CLAUDE_CONFIG_DIR"

// userHomeDir is the $HOME seam. A package var only so tests can point discovery
// at a t.TempDir() and never read the real home — the same trick usage.go plays
// with securityBin, and for the same reason: a test that reads the operator's
// actual ~/.claude would pass or fail depending on whose machine runs it.
var userHomeDir = os.UserHomeDir

// Account is one Claude Code subscription installed on this machine.
type Account struct {
	Key       string // ingest.AccountFor key: "default", "nabu-org", …
	ConfigDir string // ~/.claude for default, ~/.claude-<key> otherwise
	IsDefault bool
}

// Discover lists the accounts that physically exist under $HOME, in stable
// (sorted) order — on a stock layout the default account comes first, since
// filepath.Glob sorts on the ".claude*" component and ".claude" sorts before
// ".claude-<key>". Duplicate keys are dropped and roots with no account context
// are skipped, mirroring api.accountsFromRoots so the account list and the quota
// cards cannot disagree.
//
// Account.ConfigDir is populated for the default account too (~/.claude), unlike
// usage.Source.ConfigDir which is deliberately EMPTY for the default: there the
// empty value selects the legacy credential-resolution chain, here the field is
// purely descriptive. Do not "align" the two — they answer different questions.
//
// Two directories can key the same way (".claude-work" and ".claude.work",
// or ".claude--work", which a stray binding can make exist). The CANONICAL one
// — ConfigDirFor(key) — wins whenever it is present, whatever the sort order;
// otherwise the first seen. An alias can therefore never take over an account
// that already has its canonical directory.
func Discover() []Account {
	roots := ProjectsRoots()
	out := make([]Account, 0, len(roots))
	at := make(map[string]int, len(roots))
	for _, root := range roots {
		key := ingest.AccountFor(root)
		if key == "" {
			continue
		}
		dir := filepath.Dir(filepath.Clean(root))
		if i, ok := at[key]; ok {
			if canon, err := ConfigDirFor(key); err == nil && filepath.Clean(canon) == dir {
				out[i].ConfigDir = dir
			}
			continue
		}
		at[key] = len(out)
		out = append(out, Account{
			Key:       key,
			ConfigDir: dir,
			IsDefault: key == ingest.DefaultAccount,
		})
	}
	return out
}

// DiscoverWithDefault returns Discover's result with the default account
// guaranteed present. Discover only reports config dirs that exist on disk, so a
// machine whose ~/.claude/projects has not been created yet would otherwise hide
// the operator's primary login from every surface that lists accounts.
//
// The synthesised entry is the canonical ~/.claude path, NOT a claim that the
// directory exists. Callers that need existence must check it themselves.
//
// One function, two callers (the dashboard's account screen and the CLI's
// `account list`): they must never disagree about which accounts exist.
func DiscoverWithDefault() []Account {
	found := Discover()
	for _, a := range found {
		if a.IsDefault {
			return found
		}
	}
	dir, err := ConfigDirFor(ingest.DefaultAccount)
	if err != nil {
		// Only reachable when userHomeDir() fails; a fabricated relative path
		// would be worse than an honestly short list.
		return found
	}
	def := Account{Key: ingest.DefaultAccount, ConfigDir: dir, IsDefault: true}
	return append([]Account{def}, found...)
}

// ProjectsRoots discovers every Claude Code config dir's transcript tree under
// $HOME — ~/.claude/projects plus each ~/.claude-<account>/projects a
// CLAUDE_CONFIG_DIR setup creates. Only existing directories survive; Glob
// already returns sorted matches, so the order is stable across runs.
//
// This is cmd/swarmery's former globClaudeProjectsRoots, moved here so the
// daemon and the CLI cannot disagree about what accounts exist — and so the
// logic is covered, since cmd/swarmery is excluded from the coverage gate.
func ProjectsRoots() []string {
	home, err := userHomeDir()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".claude*", "projects"))
	var roots []string
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			roots = append(roots, m)
		}
	}
	return roots
}

// ConfigDirFor maps a key to its config dir WITHOUT touching the filesystem, so
// callers can build an env for an account that is being provisioned.
//
// It returns the CANONICAL location. For an account that already exists, prefer
// the ConfigDir Discover reports: an operator whose dir is ~/.claude.work (dot,
// not dash) still keys as "work", and only discovery knows where that account
// actually lives. EnvForAccount does exactly that.
func ConfigDirFor(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !ValidKey(key) {
		return "", fmt.Errorf("claudeacct: %q is not a valid account key", key)
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("claudeacct: cannot resolve home dir: %w", err)
	}
	if home == "" {
		return "", errors.New("claudeacct: cannot resolve home dir: empty")
	}
	if key == ingest.DefaultAccount {
		return filepath.Join(home, ".claude"), nil
	}
	return filepath.Join(home, ".claude-"+key), nil
}

// ValidKey reports whether key is usable as a config-dir suffix and as a store
// file name. Mirrors usage.safeAccountKey plus a charset restriction: the key
// becomes a directory name under $HOME, so it is validated, never trusted.
//
// A leading "-" is refused as well as a leading ".": the key becomes the
// directory ".claude-<key>", and Discover derives a key back from a directory
// name by trimming "-" and "." after ".claude" (ingest.AccountFor). A key
// "-work" would name ".claude--work", which that trim reads back as "work" —
// an alias that a binding could use to re-point every "work" project at a
// directory it made.
func ValidKey(key string) bool {
	if key == "" || key == "." || key == ".." {
		return false
	}
	if strings.HasPrefix(key, ".") || strings.HasPrefix(key, "-") {
		return false
	}
	if strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
		return false
	}
	if key != filepath.Base(key) {
		return false
	}
	// A space or a control character would survive filepath.Base but makes a
	// key that cannot be typed back, logged unambiguously, or used as a file
	// name without quoting.
	for _, r := range key {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
