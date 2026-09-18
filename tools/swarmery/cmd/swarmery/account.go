package main

// `swarmery account` — the TERMINAL surface of the multi-account feature:
//
//	swarmery account list                            every installed account
//	swarmery account which  [--path <dir>]           which account a project runs under, and why
//	swarmery account use    <key> [--path <dir>]     bind a project to an account
//	swarmery account clear  [--path <dir>]           drop the binding
//	swarmery account env    [--path <dir>]           the env line for a project (zero or one)
//	swarmery account exec   [--path <dir>] -- <cmd…> run a command under a project's account
//
// # Two properties this file exists to preserve
//
//  1. NO DAEMON. which|use|clear|env|exec read and write the binding file
//     directly and never open a socket. The daemon is a dashboard, not a
//     dependency: an operator whose terminal cannot switch accounts because a
//     background service is stopped would rightly stop trusting the feature.
//     (`list` reads credentials to answer "connected?" — still no HTTP.)
//
//  2. NO LOGIC HERE. Every decision — what an account is, where its config dir
//     lives, what the env delta is — belongs to internal/claudeacct, because
//     cmd/swarmery is excluded from the coverage gate (swarmery-ci.yml) and
//     logic placed here would be untested by construction. What is left is
//     argument parsing and formatting.
//
// `account env` is the load-bearing one: both shell surfaces in
// plugins/accounts-pack consume its stdout, so it prints EXACTLY zero or one
// line and every diagnostic goes to stderr.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	// Aliased: this package already has a `usage()` function (main.go's help
	// text), and the import would shadow it for the whole file.
	usagepkg "github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

const accountUsage = `usage:
  swarmery account list                              every account: key, config dir, default?, connected?, plan
  swarmery account which [--path <dir>]              which account this project runs under, and why
  swarmery account use <key> [--path <dir>]          bind this project to an account
  swarmery account clear [--path <dir>]              drop the binding (back to the default account)
  swarmery account env [--path <dir>]                the project's env line — ZERO or ONE line, nothing else
  swarmery account exec [--path <dir>] -- <cmd ...>  run a command under this project's account

  --path defaults to the current directory and must be the PROJECT ROOT: the
  binding lives in <path>/` + claudeacct.BindingFile + ` and is never searched
  for in parent directories.

  which|use|clear|env|exec never contact the daemon — the terminal has to keep
  working with swarmery stopped.`

// cmdAccount dispatches the `account` subcommands.
func cmdAccount(args []string) error {
	if len(args) == 0 {
		return errors.New(accountUsage)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return accountList(rest, os.Stdout)
	case "which":
		return accountWhich(rest, os.Stdout)
	case "use":
		return accountUse(rest, os.Stdout)
	case "clear":
		return accountClear(rest, os.Stdout)
	case "env":
		return accountEnv(rest, os.Stdout)
	case "exec":
		return accountExec(rest)
	case "-h", "--help", "help":
		fmt.Fprintln(os.Stderr, accountUsage)
		return nil
	default:
		return fmt.Errorf("unknown account subcommand %q\n%s", sub, accountUsage)
	}
}

// ── shared parsing ──────────────────────────────────────────────────────────

// pathFlag registers the --path flag shared by every subcommand but `list`.
func pathFlag(fs *flag.FlagSet) *string {
	return fs.String("path", "", "project root the binding belongs to (default: current directory)")
}

// projectPath resolves --path to an absolute directory.
//
// Absolute because the binding path is joined onto it and because the value is
// echoed back to the operator: a relative "." in a confirmation line does not
// say WHICH project was just re-bound.
func projectPath(v string) (string, error) {
	if strings.TrimSpace(v) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		v = cwd
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	return abs, nil
}

// ── list ────────────────────────────────────────────────────────────────────

// accountList prints every installed account with its live state.
//
// It prints NO credential material — key, config dir, default?, connected?,
// plan and nothing else. `plan` is the credential's RAW rateLimitTier, exactly
// as api.accountRow reports it, so the CLI and the dashboard cannot end up with
// two subtly different answers to "what plan is this account on".
func accountList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account list", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return errors.New("usage: swarmery account list")
	}

	accounts := claudeacct.DiscoverWithDefault()
	if len(accounts) == 0 {
		fmt.Fprintln(out, "no accounts found")
		return nil
	}

	ctx := context.Background()
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tCONFIG DIR\tDEFAULT\tCONNECTED\tPLAN")
	for _, a := range accounts {
		connected, plan := accountState(ctx, a)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			a.Key, a.ConfigDir, yesNo(a.IsDefault), connected, orDash(plan))
	}
	return w.Flush()
}

// accountState answers "connected?" and "which plan?" for one account.
//
// Connected is TRI-STATE — "yes"/"no"/"unknown" — because SWARMERY_USAGE_OAUTH=0
// switches credential resolution off wholesale (usage.ErrDisabled). Rendering
// that kill switch as "every account is disconnected" would be a different, and
// false, statement.
//
// The DEFAULT account is asked for with an EMPTY ConfigDir on purpose: that
// selects usage's legacy resolution chain, which on macOS is the only source
// that resolves the stock account (its credential lives in the login Keychain
// and has no file at all). Naming its dir here would switch resolution to the
// exclusive scoped file lookup and report the primary login as disconnected.
// This mirrors api.accountRow exactly — the two must not drift.
func accountState(ctx context.Context, a claudeacct.Account) (connected, plan string) {
	src := usagepkg.Source{Account: a.Key}
	if !a.IsDefault {
		src.ConfigDir = a.ConfigDir
	}
	creds, err := usagepkg.LoadCredsFor(ctx, src)
	switch {
	case errors.Is(err, usagepkg.ErrDisabled):
		return "unknown", ""
	case err != nil:
		return "no", ""
	default:
		// creds carries token material. Only the plan tier is ever read out of
		// it, and it is never printed whole.
		return "yes", strings.TrimSpace(creds.RateLimitTier)
	}
}

// ── which ───────────────────────────────────────────────────────────────────

// accountWhich prints the account a project effectively runs under, and whether
// that came from an explicit binding or from the default.
//
// Both are reported because they are visibly identical for a project pinned to
// the default account, and mean different things the day the default changes.
func accountWhich(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account which", flag.ExitOnError)
	path := pathFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return errors.New("usage: swarmery account which [--path <dir>]")
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}

	key, source := effectiveAccount(dir)
	configDir, installed := configDirOf(key)

	fmt.Fprintf(out, "project:    %s\n", dir)
	fmt.Fprintf(out, "account:    %s\n", key)
	fmt.Fprintf(out, "source:     %s\n", source)
	fmt.Fprintf(out, "config dir: %s\n", orDash(configDir))
	if !installed {
		// The dir is still reported above (it is where a spawn WOULD point), but
		// silence about its absence would read as "nothing is wrong here".
		fmt.Fprintf(os.Stderr,
			"warning: no config dir for account %q on this machine — a session started here "+
				"would land in a directory with no login in it\n", key)
	}
	return nil
}

// effectiveAccount resolves the stored binding into the account that actually
// applies, plus where that answer came from.
func effectiveAccount(dir string) (key, source string) {
	if bound := claudeacct.Binding(dir); bound != "" {
		return bound, "binding"
	}
	return ingest.DefaultAccount, "default"
}

// configDirOf resolves an account key to its config dir, reporting whether the
// account is actually installed. An existing account is reported where it
// really lives (Discover), because an operator whose dir is ~/.claude.work
// still keys as "work" and only discovery knows that; ConfigDirFor is the
// canonical-location fallback for one that is not installed.
func configDirOf(key string) (dir string, installed bool) {
	for _, a := range claudeacct.Discover() {
		if a.Key == key {
			return a.ConfigDir, true
		}
	}
	canonical, err := claudeacct.ConfigDirFor(key)
	if err != nil {
		return "", false
	}
	return canonical, false
}

// ── use / clear ─────────────────────────────────────────────────────────────

// accountUse binds a project to an account.
//
// An account that is not installed is REFUSED, the same way PUT
// /api/projects/{id}/account refuses it: without that check every session in
// this project would start in a config dir with no login in it — a failure that
// looks like "the CLI is broken" rather than "the binding is wrong".
func accountUse(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account use", flag.ExitOnError)
	path := pathFlag(fs)

	// `use <key> --path <dir>` and `use --path <dir> <key>` must both work; the
	// flag package stops at the first positional, so split them first (the same
	// trick cmdOnboard plays).
	positional, flagArgs := splitPositional(args)
	fs.Parse(flagArgs)
	rest := append(append([]string{}, positional...), fs.Args()...)
	if len(rest) != 1 {
		return errors.New("usage: swarmery account use <key> [--path <dir>]")
	}
	key := strings.TrimSpace(rest[0])

	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	if !claudeacct.ValidKey(key) {
		return fmt.Errorf("%q is not a valid account key — it becomes a directory name under the home directory", key)
	}
	if _, installed := configDirOf(key); !installed && key != ingest.DefaultAccount {
		return fmt.Errorf(
			"unknown account %q — every session in this project would start in a config dir "+
				"with no login in it. Installed accounts: %s", key, strings.Join(accountKeys(), ", "))
	}
	if err := claudeacct.SetBinding(dir, key); err != nil {
		return err
	}

	configDir, _ := configDirOf(key)
	fmt.Fprintf(out, "bound %s → %s (%s)\n", dir, key, configDir)
	return nil
}

// accountClear drops a project's binding, returning it to the default account.
func accountClear(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account clear", flag.ExitOnError)
	path := pathFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return errors.New("usage: swarmery account clear [--path <dir>]")
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	if err := claudeacct.SetBinding(dir, ""); err != nil {
		return err
	}
	fmt.Fprintf(out, "cleared %s — it now runs under the %s account\n", dir, ingest.DefaultAccount)
	return nil
}

// accountKeys lists the installed account keys, for error messages.
func accountKeys() []string {
	accounts := claudeacct.DiscoverWithDefault()
	keys := make([]string, 0, len(accounts))
	for _, a := range accounts {
		keys = append(keys, a.Key)
	}
	return keys
}

// ── env ─────────────────────────────────────────────────────────────────────

// accountEnv prints the project's environment delta: EXACTLY zero or one line.
//
// This is the contract both shell surfaces in plugins/accounts-pack rest on, so
// nothing else may ever reach stdout here — no header, no "(none)", no hint.
// Zero lines is the honest answer for an unbound project AND for one pinned to
// the default account: the default account is env-LESS by design (it lives in
// ~/.claude, where the CLI looks with no CLAUDE_CONFIG_DIR set), so binding to
// it must produce an empty delta rather than an explicit variable.
//
// It prints the CONFIG DIR delta only. The account's MCP secrets deliberately
// never reach it: this output goes to a terminal and its scrollback, and a
// second line would fall outside the `CLAUDE_CONFIG_DIR=?*` case the shell
// function matches, silently dropping the binding. Secrets travel through
// `account exec`, which hands them to the child without printing them.
//
// The line is printed RAW, not shell-quoted, because its consumer is
// `env "$(swarmery account env)" command claude` — a single quoted argument,
// which is correct even for a home directory with a space in it. Quoting here
// would put literal quotes inside the value on that path.
func accountEnv(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account env", flag.ExitOnError)
	path := pathFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return errors.New("usage: swarmery account env [--path <dir>]")
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	for _, line := range claudeacct.EnvFor(dir) {
		fmt.Fprintln(out, line)
	}
	return nil
}

// ── exec ────────────────────────────────────────────────────────────────────

// accountExec runs a command under the project's account.
//
// It REPLACES this process (syscall.Exec) rather than supervising a child, so
// the child's exit code is swarmery's exit code by construction, signals and
// job control reach the real process, and an interactive `claude` gets the
// terminal directly instead of through a relay.
//
// The env delta is MERGED into os.Environ() rather than appended to it — see
// mergeEnv for why this path, alone among the spawners, cannot just append. A
// nil delta still leaves the environment untouched: an unbound project runs
// byte-identically to running the command without swarmery — including
// inheriting a CLAUDE_CONFIG_DIR the caller's shell had already exported.
func accountExec(args []string) error {
	fs := flag.NewFlagSet("account exec", flag.ExitOnError)
	path := pathFlag(fs)
	fs.Parse(args)

	argv := fs.Args()
	if len(argv) == 0 {
		return errors.New("usage: swarmery account exec [--path <dir>] -- <cmd> [args ...]")
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("account exec: %w", err)
	}
	// The account's MCP secrets ride along here and NOT in `account env`: exec
	// hands the array to the child, `env` prints it to the terminal. This is the
	// terminal half of the secret channel the daemon's spawner has in
	// internal/runcore — the same store, the same key, so a session started by
	// hand and one dispatched from the dashboard see the same variables.
	delta := append(claudeacct.EnvFor(dir), claudeacct.SecretEnvFor(dir)...)
	// Returns only on failure — on success this process IS the command.
	return syscall.Exec(bin, argv, mergeEnv(os.Environ(), delta))
}

// mergeEnv appends overrides to base, having first removed from base every entry
// whose NAME an override also sets. The result therefore names each overridden
// key exactly once.
//
// The five daemon spawn sites in internal/ hand their delta to exec.Cmd.Env,
// which Go documents as last-wins and normalises before spawning — a duplicate
// there is harmless. This is the only raw execve(2) path, and execve does no
// normalisation whatsoever: it copies the array to the child verbatim, where a
// libc getenv() walks it and returns the FIRST match. So a plain
// append(os.Environ(), delta...) is silently defeated by a CLAUDE_CONFIG_DIR the
// caller's shell had already exported — the stale value sorts first and wins.
//
// That is this feature's worst failure: the command runs under the WRONG account
// while `swarmery account which` reports the right one, and nothing anywhere
// says so.
func mergeEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		// Preserves the byte-identical-passthrough property accountExec
		// documents: no delta, no rewriting of the caller's environment.
		return base
	}
	overridden := make(map[string]struct{}, len(overrides))
	for _, kv := range overrides {
		overridden[envKey(kv)] = struct{}{}
	}
	merged := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		if _, dup := overridden[envKey(kv)]; dup {
			continue
		}
		merged = append(merged, kv)
	}
	return append(merged, overrides...)
}

// envKey is the NAME half of a "NAME=value" entry. An entry with no "=" is not a
// valid assignment, so it is keyed whole — that way it can never be mistaken for
// a name an override is trying to replace.
func envKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}

// ── formatting ──────────────────────────────────────────────────────────────

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// orDash renders an empty field as "-" so a column is never blank-ambiguous.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
