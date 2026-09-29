package main

// `swarmery account` — the TERMINAL surface of the multi-account feature:
//
//	swarmery account list                            every installed account
//	swarmery account which  [--path <dir>]           which account and estate a path runs under, and why
//	swarmery account use    <key> [--path <dir>]     bind a project to an account
//	swarmery account clear  [--path <dir>]           drop the binding
//	swarmery account env    [--path <dir>]           the env line for a project (zero or one)
//	swarmery account exec   [--path <dir>] -- <cmd…> run a command under a project's account
//	swarmery account estate use|show|clear           declare, inspect, remove an estate root
//	swarmery account doctor [--fast|--probe] [--json] the account doctor, names only (account_doctor.go)
//	swarmery account switch <key> [--estate <root>]  move a declared estate's payer (account_switch.go)
//	swarmery account move-session <uuid> --to <key>  copy a session to another account (account_switch.go)
//	swarmery account prune  [--path <dir>] [--dry-run] remove settings keys the estate already supplies (internal/accountprune)
//
// # Two properties this file exists to preserve
//
//  1. NO DAEMON. which|use|clear|env|exec|doctor|estate read and write the binding
//     file directly and never open a socket. The daemon is a dashboard, not a
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
	"bufio"
	"context"
	"encoding/json"
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

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/accountprune"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runsettings"
	// Aliased: this package already has a `usage()` function (main.go's help
	// text), and the import would shadow it for the whole file.
	usagepkg "github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

const accountUsage = `usage:
  swarmery account list                              every account: key, config dir, default?, connected?, plan
  swarmery account which [--path <dir>]              which account and estate this path runs under, and which rung decided
  swarmery account use <key> [--path <dir>] [--clear-pins | --clear-pins=all | --keep-pins]
                                                     bind this directory to an account
  swarmery account clear [--path <dir>]              drop the binding at <dir> (an estate declared there stays)
  swarmery account env [--path <dir>]                the project's env line — ZERO or ONE line, nothing else
  swarmery account exec [--path <dir>] -- <cmd ...>  run a command under this project's account and estate
  swarmery account estate use <key> [--path <dir>]   declare <dir> as the root of estate <key>
  swarmery account estate show [--path <dir>]       the estate this path resolves to, and where it was declared
  swarmery account estate clear [--path <dir>]      remove the declaration AT <dir> (the account binding stays)
  swarmery account doctor [--fast | --probe] [--json] [--path <dir>] [--timeout <dur>] [--no-record]
                                                     everything that decides whether the next session here
                                                     works: resolution, credential coverage (names only,
                                                     never a value), trust findings, settings delta and
                                                     plugin parity between accounts, stale duplicates.
                                                     --fast: the turn-zero arms (no claude spawn); bare: plus
                                                     the git-tracked findings; --probe: measure the installed
                                                     CLI's channels and store the verdict (the only spawn)
  swarmery account switch <key> [--estate <root>] [--force] [--clear-pins] [--dry-run]
                                                     move a whole declared ESTATE's payer to <key>; refuses
                                                     an estate-less path and an account whose quota
                                                     headroom is unknown (unless --force)
  swarmery account move-session <uuid> --to <key> [--from <key>] [--cwd <path>]
                                [--project-dir <name>] [--force] [--overwrite] [--dry-run]
                                                     copy a session's transcript, its <uuid>/ dir and the
                                                     project memory/ into <key>'s config dir, and re-point
                                                     its database row; prints the resume command
  swarmery account prune [--path <dir>] [--dry-run] [--include-tracked] [--json]
                                                     remove the settings keys the estate already supplies
                                                     (pluginConfigs, extraKnownMarketplaces — whole keys,
                                                     only when every entry is the estate's) from the files
                                                     under <dir>; see ` + "`swarmery account prune --help`" + `

  --path defaults to the current directory. A binding lives in
  <path>/` + claudeacct.BindingFile + `; a path with none inherits the account
  of the nearest ancestor that pins one, up to but never including the home
  directory — and ` + "`which`" + ` names the rung that decided.

  The optional "estate" field in that same file makes its directory an ESTATE
  ROOT: every descendant takes that estate's credential store (and its
  .claude/settings.json), whichever account pays — the estate walk is
  independent of the account walk. An estate with no credential store on this
  machine is a valid state: it supplies zero credentials.

  use on an estate root lists every descendant pin that shadows the write.
  --clear-pins clears only the pins that EQUAL the account being written;
  a pin that disagrees stays and is named. --clear-pins=all clears every
  listed pin. --keep-pins lists and clears nothing. With no flag, a terminal
  is asked; anything else only lists.

  which|use|clear|env|exec|doctor|estate|switch|move-session|prune never contact the
  daemon — the terminal has to keep working with swarmery stopped. switch and
  move-session read the database without migrating it.`

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
		return accountUse(rest, os.Stdout, os.Stderr, os.Stdin)
	case "clear":
		return accountClear(rest, os.Stdout)
	case "env":
		return accountEnv(rest, os.Stdout)
	case "exec":
		return accountExec(rest)
	case "estate":
		return accountEstate(rest, os.Stdout, os.Stderr)
	case "doctor":
		return accountDoctor(rest, os.Stdout)
	case "switch":
		return accountSwitch(rest, os.Stdout)
	case "move-session":
		return accountMoveSession(rest, os.Stdout)
	case "prune":
		return accountPrune(rest, os.Stdout, os.Stderr)
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

// accountWhich prints what a path effectively runs under — the account, the
// RUNG that decided it (its own pin, an ancestor's, or the default), the estate
// and its root, and every ancestor pin the winning rung shadows.
//
// The source is reported because "default" looks identical whether pinned or
// inherited, and means different things the day the default changes. The
// estate is reported by KEY and ROOT only — never a store path, a variable name
// or a value.
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

	r := claudeacct.Resolve(dir)
	key := effectiveKey(r)
	configDir, installed := configDirOf(key)

	fmt.Fprintf(out, "project:    %s\n", dir)
	if r.AccountRoot != "" {
		fmt.Fprintf(out, "account:    %s (pin at %s)\n", key, r.AccountRoot)
	} else {
		fmt.Fprintf(out, "account:    %s\n", key)
	}
	fmt.Fprintf(out, "source:     %s\n", r.Source)
	if r.Estate != "" {
		fmt.Fprintf(out, "estate:     %s (root %s)\n", r.Estate, r.EstateRoot)
	}
	if inherited := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); r.Account == "" && inherited != "" {
		// Nothing binds this path, so a spawn here keeps whatever config dir it
		// inherits — say so rather than print the default's.
		fmt.Fprintf(out, "config dir: %s (inherited from this environment — no binding decides it)\n", inherited)
	} else {
		fmt.Fprintf(out, "config dir: %s\n", orDash(configDir))
	}
	// Every rung whose binding the provenance gate ignored (D5 Lock 1): whatever
	// it declares is NOT in effect above. Said by path and reason, never contents.
	ownBinding := filepath.Join(dir, filepath.FromSlash(claudeacct.BindingFile))
	ownIgnored := false
	for _, ig := range r.IgnoredRungs() {
		ownIgnored = ownIgnored || ig.Path == ownBinding
	}
	for _, line := range r.IgnoredLines() {
		fmt.Fprintln(out, line)
	}
	// The project's own binding file exists but no reader trusts it (mode, owner,
	// type — or provenance, already said above).
	if why := claudeacct.BindingFileUntrusted(dir); why != "" && !ownIgnored {
		fmt.Fprintf(out, "ignored:    %s\n", why)
	}
	// Which stores a spawn here receives, and why (D5 Lock 2).
	for _, line := range r.AdmissionLines() {
		fmt.Fprintln(out, line)
	}
	for _, s := range claudeacct.Shadowed(dir) {
		fmt.Fprintf(out, "shadowed:   %s says %s\n", s.Dir, s.Account)
	}
	if !installed {
		// The dir is still reported above (it is where a spawn WOULD point), but
		// silence about its absence would read as "nothing is wrong here". On
		// stdout like everything else `which` says, so one capture holds it all.
		fmt.Fprintf(out,
			"warning:    no config dir for account %q on this machine — a session started here "+
				"would land in a directory with no login in it\n", key)
	}
	return nil
}

// effectiveKey is the account a resolution runs under, for display: an
// unpinned path runs under the default account.
func effectiveKey(r claudeacct.Resolution) string {
	if r.Account == "" {
		return ingest.DefaultAccount
	}
	return r.Account
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
//
// On an ESTATE ROOT (a --path whose binding file declares `estate`) every
// descendant pin that would shadow the write is listed on stderr BEFORE the
// write, each marked as equal to or disagreeing with the account being written
// (claudeacct.ShadowingPins is the one downward walk; the key each pin holds is
// read with claudeacct.Binding). What happens to them is the operator's choice,
// never a default:
//
//	--clear-pins      clears ONLY the pins that EQUAL the account being written
//	                  (redundant — clearing them changes no resolution). A pin
//	                  that disagrees stays, named on stderr as a deliberate
//	                  divergence. A blanket clear would delete exactly the pins
//	                  that are there ON PURPOSE.
//	--clear-pins=all  clears every listed pin, whatever it holds.
//	--keep-pins       lists, writes, clears nothing.
//	(neither)         a terminal on stdin is asked about the redundant set only;
//	                  anything else only lists.
func accountUse(args []string, out, errOut io.Writer, in *os.File) error {
	fs := flag.NewFlagSet("account use", flag.ExitOnError)
	path := pathFlag(fs)
	var clear clearPinsFlag
	fs.Var(&clear, "clear-pins", "clear the listed pins that equal the account being written (bare), or every listed pin (with the value all)")
	keep := fs.Bool("keep-pins", false, "list the shadowing pins and clear none of them")

	// `use <key> --path <dir>` and `use --path <dir> <key>` must both work; the
	// flag package stops at the first positional, so split them first (the same
	// trick cmdOnboard plays).
	positional, flagArgs := splitPositional(args)
	fs.Parse(flagArgs)
	rest := append(append([]string{}, positional...), fs.Args()...)
	if len(rest) != 1 {
		return errors.New("usage: swarmery account use <key> [--path <dir>] [--clear-pins | --clear-pins=all | --keep-pins]")
	}
	if clear.mode != clearPinsNone && *keep {
		return errors.New("--clear-pins and --keep-pins contradict each other — pass one")
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

	// List every shadowing pin BEFORE writing anything — and every descendant
	// binding file the walk had to skip as untrusted: it may hold a pin, but
	// nothing reads it and no writer will rewrite it, so it is reported by path
	// and reason rather than passed over in silence.
	var pins, untrusted []string
	if estate, _ := claudeacct.Estate(dir); estate != "" {
		pins, untrusted = claudeacct.ScanPins(dir)
	}
	for _, why := range untrusted {
		fmt.Fprintf(errOut, "skipped: %s — any pin in it is neither listed nor cleared; %s\n", why, claudeacct.SkippedPinHint(why))
	}
	var redundant, divergent []string
	for _, p := range pins {
		held := claudeacct.Binding(p)
		if held == key {
			redundant = append(redundant, p)
			fmt.Fprintf(errOut, "shadowing pin: %s holds %s — equals the account being written\n", p, held)
		} else {
			divergent = append(divergent, p)
			fmt.Fprintf(errOut, "shadowing pin: %s holds %s — DISAGREES with %s\n", p, held, key)
		}
	}

	existedBefore := claudeacct.BindingFileExists(dir)
	if err := claudeacct.SetBinding(dir, key); err != nil {
		return err
	}
	// Read it back: a write the walk ignores (an untrusted file SetBinding had
	// nothing to rewrite) must not be reported as "bound" — and a file this
	// write CREATED is removed again rather than left declaring nothing.
	if err := claudeacct.VerifyBinding(dir, key); err != nil {
		if rerr := claudeacct.RevertBinding(dir, existedBefore); rerr != nil {
			return fmt.Errorf("%w — and removing the file this write created FAILED (%v)", err, rerr)
		}
		return err
	}

	var toClear []string
	switch {
	case clear.mode == clearPinsAll:
		toClear = pins
	case clear.mode == clearPinsSelective:
		toClear = redundant
		for _, p := range divergent {
			fmt.Fprintf(errOut, "left in place: %s holds %s — a deliberate divergence from %s\n", p, claudeacct.Binding(p), key)
		}
	case *keep || len(redundant) == 0:
		// nothing to clear, nothing to ask
	case isTerminal(in):
		fmt.Fprintf(errOut, "clear the %d redundant pin(s) that equal %s? (pins that disagree are kept either way) [y/N] ", len(redundant), key)
		if answer, _ := bufio.NewReader(in).ReadString('\n'); strings.EqualFold(strings.TrimSpace(answer), "y") {
			toClear = redundant
		}
	default:
		fmt.Fprintf(errOut, "%d redundant pin(s) kept — rerun with --clear-pins to clear them\n", len(redundant))
	}
	// One pin that cannot be cleared (a refused write) is skipped and named; the
	// rest are still cleared, and the run ends non-zero listing what was skipped.
	var failed []string
	for _, p := range toClear {
		if err := claudeacct.SetBinding(p, ""); err != nil {
			fmt.Fprintf(errOut, "skipped pin: %s — %v\n", p, err)
			failed = append(failed, p)
			continue
		}
		fmt.Fprintf(errOut, "cleared pin: %s\n", p)
	}

	configDir, _ := configDirOf(key)
	fmt.Fprintf(out, "bound %s → %s (%s)\n", dir, key, configDir)
	// D5: the payer is written whatever the roots say, but a ROOTED store that
	// does not admit this path releases none of its credentials here. Say so
	// once, with the exact line that would admit it — swarmery never writes it.
	if note := unadmittedNote(dir, key); note != "" {
		fmt.Fprintln(out, note)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d pin(s) could not be cleared and still shadow the binding: %s — see the skipped pin lines above",
			len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// unadmittedNote is the one line `use` prints when key's store is rooted and
// does not admit dir: the payer changed, the credentials did not follow.
func unadmittedNote(dir, key string) string {
	if key == ingest.DefaultAccount {
		return ""
	}
	rooted, admitted, add := claudeacct.StoreAdmits(key, dir)
	if !rooted || admitted {
		return ""
	}
	return fmt.Sprintf("%s pays with %s but receives 0 of %s.env's credentials; to release them add: %s",
		dir, key, key, add)
}

// clearPinsFlag is --clear-pins: a BOOL-shaped flag (bare = selective) that
// also takes exactly one value, =all (blanket). IsBoolFlag is what lets the bare
// form parse; it is also why the space form `--clear-pins all` cannot work —
// `all` becomes a positional and the one-key check refuses it — so the refusal
// below names the = form explicitly.
type clearPinsFlag struct{ mode int }

const (
	clearPinsNone = iota
	clearPinsSelective
	clearPinsAll
)

func (f *clearPinsFlag) IsBoolFlag() bool { return true }

func (f *clearPinsFlag) String() string {
	if f == nil {
		return ""
	}
	switch f.mode {
	case clearPinsSelective:
		return "true"
	case clearPinsAll:
		return "all"
	}
	return ""
}

func (f *clearPinsFlag) Set(v string) error {
	switch v {
	case "true":
		f.mode = clearPinsSelective
	case "all":
		f.mode = clearPinsAll
	default:
		return fmt.Errorf("accepted forms are --clear-pins (only pins equal to the account) or --clear-pins=all (every pin), got %q", v)
	}
	return nil
}

// isTerminal reports whether f is an operator at a keyboard, as opposed to a
// pipe, a file or /dev/null. /dev/null is a character device too, so it is
// excluded by identity (os.SameFile), not by mode — without a new dependency.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}

// accountClear drops the binding AT a directory. It clears the payer and
// nothing else: an estate declared at the same directory survives (the two axes
// are independent), and is named on stdout so a `clear` that was expected to
// undo everything does not leave it invisible.
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
	r := claudeacct.Resolve(dir)
	if r.AccountRoot != "" {
		fmt.Fprintf(out, "cleared %s — it now runs under the %s account, inherited from %s\n", dir, effectiveKey(r), r.AccountRoot)
	} else {
		fmt.Fprintf(out, "cleared %s — it now runs under the %s account\n", dir, ingest.DefaultAccount)
	}
	if key, root := claudeacct.Estate(dir); key != "" {
		fmt.Fprintf(out, "estate %s still declared here; its root is still %s — remove it with: swarmery account estate clear --path %s\n", key, root, root)
	}
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

// ── prune ───────────────────────────────────────────────────────────────────

const accountPruneUsage = `usage: swarmery account prune [--path <dir>] [flags]

  Remove, from every settings file under <dir>, the keys the estate <dir>
  resolves to already supplies: pluginConfigs and extraKnownMarketplaces, each
  removed WHOLE and only when every entry of the file's copy is in the estate's
  with an identical value. permissions, enabledMcpjsonServers, enabledPlugins
  and the swarmery binding are never touched. The estate's own two files are
  listed "estate source" and never written.

  --path <dir>        where to look (default: the current directory)
  --dry-run           list every file considered and write nothing
  --include-tracked   also write an eligible file git TRACKS (or cannot classify);
                      without it such a file refuses the WHOLE run, exit 1
  --json              print {"targets": […], "result": {…}} instead of text

  Every listed line ends in its git status: TRACKED, untracked or NO-REPO.
  Before its first write to a file the prune copies it to
  ~/.swarmery/quarantine/<date>/prune/ — the only rollback for an ignored file.
  It never reads a credential store and never prints a settings value.`

// accountPrune parses the flags, plans, prints the targets, and applies
// (internal/accountprune holds every decision). Text mode: the target lines on
// stdout — nothing else in a dry run, so every stdout line ends in a git status
// — and, after an apply, one line per changed file plus "<n> files changed".
func accountPrune(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("account prune", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := pathFlag(fs)
	dryRun := fs.Bool("dry-run", false, "list what would change and write nothing")
	includeTracked := fs.Bool("include-tracked", false, "also write eligible git-tracked files")
	asJSON := fs.Bool("json", false, "print the plan and result as one JSON object")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError{accountPruneUsage}
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	targets, err := accountprune.Plan([]string{dir})
	if err != nil {
		return err
	}
	res, applyErr := accountprune.Apply(targets, accountprune.Options{DryRun: *dryRun, IncludeTracked: *includeTracked})
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			Targets []accountprune.Target `json:"targets"`
			Result  accountprune.Result   `json:"result"`
		}{targets, res}); err != nil {
			return err
		}
		return applyErr
	}
	if err := accountprune.RenderTargets(out, targets); err != nil {
		return err
	}
	if applyErr != nil {
		return applyErr
	}
	if *dryRun {
		return accountprune.RenderResult(errOut, res)
	}
	return accountprune.RenderResult(out, res)
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
	for _, line := range claudeacct.Resolve(dir).EnvLines() {
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
	bin, err := resolveExecBin(argv[0])
	if err != nil {
		return err
	}
	// The MCP secrets — the estate's store, and the account's for back-compat —
	// ride along here and NOT in `account env`: exec hands the array to the
	// child, `env` prints it to the terminal. This is the terminal half of the
	// channel the daemon's spawner has in internal/runcore —
	// claudeacct.SpawnEnvResolved is the same composition over the same
	// resolution, so a session started by hand and one dispatched from the
	// dashboard see the same variables.
	//
	// This is the only raw execve(2) path in the program, and execve does no
	// normalisation: it copies the array to the child verbatim, where a libc
	// getenv() returns the FIRST match. SpawnEnvResolved therefore removes every
	// name its delta sets before appending it, and holds at most one entry per
	// name in the delta itself (estate wins) — a CLAUDE_CONFIG_DIR the caller's
	// shell had already exported would otherwise sort first and silently win,
	// running the command under the WRONG account while `swarmery account which`
	// reports the right one.
	//
	// ONE resolution feeds the environment and the settings splice (a second
	// Resolve would run D5's git probe twice).
	res := claudeacct.Resolve(dir)
	// SWARMERY_LAUNCH_PATH is the launch MARKER the accounts-pack SessionStart
	// preflight reads (via `account doctor`): it names the project this exec
	// composed the environment for. It is appended HERE and nowhere else — not in
	// claudeacct.EnvFor / SpawnEnv — so no daemon seam carries it. It is never a
	// loop guard: the shim's guard is the absolute path in argv.
	env := claudeacct.SpawnEnvResolved(os.Environ(), res)
	env = append(withoutEnvKey(env, launchPathEnv), launchPathEnv+"="+dir)
	argv = spliceSettings(argv, res, os.Stderr)
	//
	// Returns only on failure — on success this process IS the command.
	return syscall.Exec(bin, argv, env)
}

// composeQuiet is the terminal composer; a package var only so a test can
// prove it is never called for a non-claude argv[0].
var composeQuiet = runsettings.ComposeQuiet

// spliceSettings is the terminal twin of the daemon seams (internal/runsettings):
// the admitted estate's EstateKeys ride as --settings right after argv[0].
// argv[0] is checked FIRST — nothing is composed for any other command — and a
// caller's own --settings always wins (SpliceTerminal). When the estate's file is
// unusable, exactly one line goes to stderr and the command runs without the
// flag; stdout is never touched, and nothing is logged.
func spliceSettings(argv []string, res claudeacct.Resolution, stderr io.Writer) []string {
	if len(argv) == 0 || filepath.Base(argv[0]) != "claude" {
		return argv
	}
	f, reason := composeQuiet(res, runsettings.Inputs{})
	if reason != "" {
		fmt.Fprintf(stderr, "swarmery: project settings not composed (%s); running without them\n", reason)
	}
	return runsettings.SpliceTerminal(argv, f)
}

// launchPathEnv is the marker accountExec sets for the child (see above).
const launchPathEnv = "SWARMERY_LAUNCH_PATH"

// withoutEnvKey drops every KEY=… entry for key, so the marker appended after
// it is the only one execve hands down (libc getenv returns the FIRST match).
// It copies: the caller's slice is never mutated.
func withoutEnvKey(env []string, key string) []string {
	out := make([]string, 0, len(env)+1)
	prefix := key + "="
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

// resolveExecBin finds the executable `account exec` replaces itself with.
//
// PATH first, like any shell. The fallback exists for one caller: the shell
// function plugins/accounts-pack installs is itself named `claude` and hands the
// bare word "claude" here — and the official local install
// (`claude migrate-installer`) defines `claude` as a shell ALIAS to
// ~/.claude/local/claude with nothing on PATH. execve cannot see an alias, so a
// bare LookPath fails and the operator's `claude` stops launching altogether.
// claudebin.Resolve probes exactly that location (and the other common install
// dirs), the same way every daemon spawn already finds the binary under launchd's
// minimal PATH. Any other command name gets the plain PATH answer.
//
// Neither answer may be the accounts-pack PATH shim (claudebin.IsShim): the
// shim execs `swarmery account exec`, so resolving to it would re-enter this
// very function on every terminal launch (risk R5). The filter matches the
// shim ONLY — a `claude` in the shim dir. Every other binary there (the
// swarmery CLI itself: `account exec -- swarmery …`) resolves normally, by
// bare name or absolute path. A shim hit is discarded and the search
// continues past it; for "claude" the probe then runs as before.
func resolveExecBin(name string) (string, error) {
	bin, err := exec.LookPath(name)
	if err == nil && !claudebin.IsShim(bin) {
		return bin, nil
	}
	if err == nil {
		// The shim dir answered first; look past it.
		bin, err = claudebin.LookPathSkippingShim(name)
		if err == nil {
			return bin, nil
		}
	}
	if name == "claude" {
		if probed, perr := claudebin.Resolve(); perr == nil {
			return probed, nil
		}
	}
	return "", fmt.Errorf("account exec: %w", err)
}

// ── estate ──────────────────────────────────────────────────────────────────

// accountEstate dispatches `account estate use|show|clear` — the ONLY supported
// way to create, inspect or remove an estate declaration; nothing tells an
// operator to hand-edit JSON. None of the three ever prints a store path, a
// variable name or a value: a COUNT of names is the most a store is described
// by.
func accountEstate(args []string, out, errOut io.Writer) error {
	const usage = "usage: swarmery account estate use <key> [--path <dir>] | show [--path <dir>] | clear [--path <dir>]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "use":
		return estateUse(args[1:], out)
	case "show":
		return estateShow(args[1:], out)
	case "clear":
		return estateClear(args[1:], out, errOut)
	default:
		return fmt.Errorf("unknown estate subcommand %q\n%s", args[0], usage)
	}
}

// estateUse declares --path as the root of estate <key> through
// claudeacct.SetEstate (foreign keys preserved, .bak once, idempotent, abort on
// unparseable JSON). A key with no credential store is accepted — a store-less
// estate is a first-class state — and the confirmation says which it is.
func estateUse(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account estate use", flag.ExitOnError)
	path := pathFlag(fs)
	allowTracked := fs.Bool("allow-tracked-settings", false,
		"declare the estate even though git tracks <dir>/.claude/settings.json (its keys then travel with every clone)")
	positional, flagArgs := splitPositional(args)
	fs.Parse(flagArgs)
	rest := append(append([]string{}, positional...), fs.Args()...)
	if len(rest) != 1 {
		return errors.New("usage: swarmery account estate use <key> [--path <dir>] [--allow-tracked-settings]")
	}
	key := strings.TrimSpace(rest[0])
	if !claudeacct.ValidKey(key) {
		return fmt.Errorf("%q is not a valid estate key — it names a file in the credential store directory", key)
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	// Check first, write second: a declaration Resolve would never read back as
	// this directory's own rung ($HOME, a daemon worktree, a directory the walk
	// stops below, a binding file the trust checks ignore) is refused before any
	// byte is written, rather than left on disk looking effective.
	if why := claudeacct.DeclarationUnreadable(dir); why != "" {
		return fmt.Errorf("refusing to declare estate %s at %s: the declaration would never be read — %s", key, dir, why)
	}
	// An estate's settings file is what the estate hands every descendant. One
	// that git tracks arrived with the repository, so it is refused unless the
	// operator says so in the argv.
	if why := claudeacct.EstateSettingsTracked(dir); why != "" && !*allowTracked {
		return fmt.Errorf("refusing to declare estate %s at %s: %s — pass --allow-tracked-settings to declare it anyway", key, dir, why)
	}
	prev, _ := claudeacct.Estate(dir)
	existedBefore := claudeacct.BindingFileExists(dir)
	if err := claudeacct.SetEstate(dir, key); err != nil {
		return err
	}
	r := claudeacct.Resolve(dir)
	if r.Estate != key || !claudeacct.SameFile(r.EstateRoot, dir) {
		// The pre-check should make this unreachable; if it is not, put the
		// directory back rather than leave a declaration nothing reads — and a
		// file the write created is removed, not left holding `{}`.
		msg := fmt.Sprintf("refusing to declare estate %s at %s: after writing, %s still resolves to estate %q at %q",
			key, dir, dir, r.Estate, r.EstateRoot)
		if rerr := claudeacct.RevertEstate(dir, prev, existedBefore); rerr != nil {
			return fmt.Errorf("%s — and reverting the declaration FAILED (%v); check %s by hand", msg, rerr, dir)
		}
		return fmt.Errorf("%s — the declaration was reverted", msg)
	}
	fmt.Fprintf(out, "estate %s declared at %s (%s)\n", key, dir, storeSummary(r))
	// Unanchored: the declaration stands, and nothing flows until the store
	// names this root. One line, with the exact root line to add. (A store the
	// loader REFUSES has its own fix, already named above.)
	if state, _ := r.CredentialStore(); !r.EstateAdmitted && state != claudeacct.StoreRefused {
		fmt.Fprintf(out, "estate %s is unanchored — it supplies 0 credentials and no estate settings until %s.env carries: %s\n",
			key, key, claudeacct.RootLineFor(dir))
	}
	return nil
}

// storeSummary describes the estate's credential store in the words `estate
// use` and `estate show` share: present with a COUNT of names, absent (a
// healthy, zero-credential state), or REFUSED by the loader with the reason. It
// never prints the store's path, a variable name or a value.
func storeSummary(r claudeacct.Resolution) string {
	switch state, why := r.CredentialStore(); state {
	case claudeacct.StorePresent:
		return fmt.Sprintf("credential store present: %d names", r.CredentialCount())
	case claudeacct.StoreRefused:
		return fmt.Sprintf("credential store REFUSED: %s — it supplies 0 credentials until that is fixed", why)
	case claudeacct.StoreUnadmitted:
		return fmt.Sprintf("credential store not admitted: %s — it supplies 0 credentials", why)
	default:
		return "no credential store on this machine — it supplies 0 credentials"
	}
}

// estateShow prints the estate --path resolves to (by the walk), its root,
// whether the root is the path itself, and the credential COUNT.
func estateShow(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("account estate show", flag.ExitOnError)
	path := pathFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return errors.New("usage: swarmery account estate show [--path <dir>]")
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	r := claudeacct.Resolve(dir)
	fmt.Fprintf(out, "path:        %s\n", dir)
	if r.Estate == "" {
		fmt.Fprintln(out, "estate:      none — no ancestor declares one")
		return nil
	}
	where := "an ancestor"
	if claudeacct.SameFile(r.EstateRoot, dir) {
		where = "this path itself"
	}
	fmt.Fprintf(out, "estate:      %s\n", r.Estate)
	fmt.Fprintf(out, "root:        %s (%s)\n", r.EstateRoot, where)
	n := 0
	if state, _ := r.CredentialStore(); state == claudeacct.StorePresent {
		n = r.CredentialCount()
	}
	fmt.Fprintf(out, "credentials: %d (%s)\n", n, storeSummary(r))
	return nil
}

// estateClear removes the declaration AT --path. The account binding there is
// untouched; every descendant keeps resolving its account, and its estate falls
// back to whatever an ancestor declares (or to none) — said once on stderr, by
// key and root.
func estateClear(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("account estate clear", flag.ExitOnError)
	path := pathFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 0 {
		return errors.New("usage: swarmery account estate clear [--path <dir>]")
	}
	dir, err := projectPath(*path)
	if err != nil {
		return err
	}
	// An untrusted file declares nothing any reader sees, and rewriting it would
	// hand it a trusted mode and owner — activating whatever else it holds. It is
	// the operator's to fix, never this command's to edit.
	if why := claudeacct.BindingFileUntrusted(dir); why != "" {
		return fmt.Errorf("refusing to clear the estate at %s: %s — nothing in it is in effect, and rewriting it "+
			"would make it trusted; inspect it, then fix or remove it yourself", dir, why)
	}
	key, _ := claudeacct.Estate(dir)
	if err := claudeacct.SetEstate(dir, ""); err != nil {
		return err
	}
	if key == "" {
		fmt.Fprintf(out, "no estate declared at %s — nothing to clear\n", dir)
		return nil
	}
	fmt.Fprintf(out, "cleared estate %s at %s\n", key, dir)
	// Say what the tree falls back to: with a nested layout an OUTER estate
	// still covers it, and "zero credentials" would then be false.
	if after := claudeacct.Resolve(dir); after.Estate != "" {
		fmt.Fprintf(errOut, "warning: estate %s no longer has a root at %s — paths under it with no nearer declaration now fall back to estate %s (root %s) (the account binding is unchanged)\n",
			key, dir, after.Estate, after.EstateRoot)
	} else {
		fmt.Fprintf(errOut, "warning: estate %s no longer has a root at %s — paths under it with no nearer declaration now resolve to no estate and compose zero estate credentials (the account binding is unchanged)\n", key, dir)
	}
	return nil
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
