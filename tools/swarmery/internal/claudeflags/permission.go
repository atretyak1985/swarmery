// Package claudeflags builds the CLI flags the daemon's headless `claude`
// spawns share: --permission-mode, the allow and deny tool lists that travel
// with it (tools.go), and --effort (effort.go).
//
// A headless run has no interactive approver. Every tool call that would have
// opened a permission prompt is therefore auto-DENIED, and — this is the part
// that hurt — the process still exits 0. The run is recorded as a clean success
// that landed nothing: no files written, no commits, no checkboxes ticked, and
// the only account of what happened lives in a reply nobody stores.
//
// Observed 2026-08-10 on a phase run (an approved plan's phase-1): every
// Write/Edit refused with "requested permissions to write … you haven't granted
// it yet", every Bash command outside the project's allow list refused as well,
// exit 0, worktree removed with zero diff.
//
// Measured 2026-10-22 on Claude Code 2.1.308 (macOS, `claude -p`, Haiku; Sonnet
// for auto), one scratch repo per leg, verdicts read from observed effects (a
// file exists, a ref reached the bare remote, a request hit the local listener),
// never from what the model said. Operations: read in cwd, read outside cwd,
// write in cwd, write outside cwd, run ./run-checks.sh, commit, send a commit to
// the remote, curl a local listener, print a token env var:
//
//	mode               rd-cwd rd-out wr-cwd wr-out checks commit remote curl token
//	omitted / default  ok     deny   deny   deny   deny   deny   deny   deny deny
//	acceptEdits        ok     deny   ok     deny   deny   deny   deny   deny deny
//	plan               ok     ok     -      -      -      -      -      -    -     the model attempted nothing
//	auto (Sonnet)      ok     ok     ok     ok     ok     ok     ok     ok   ok    NOT a boundary
//	dontAsk            ok     deny   deny   deny   deny   deny   deny   deny deny
//	bypassPermissions  ok     ok     ok     ok     ok     ok     ok     ok   ok
//
// and with lists on the argv:
//
//	dontAsk + allow Read,Grep,Glob,Bash(./run-checks.sh*)  reads in and out of cwd and the check ran;
//	                                                       everything else denied
//	dontAsk + allow Read,Grep,Glob,Write(//<dir>/**)       the Write rule granted NOTHING
//	dontAsk + allow Read,Grep,Glob,Edit(//<dir>/**)        writes under <dir> ran, writes in cwd denied
//	bypassPermissions + deny Bash(<cmd> *),…               the denied commands were refused, the rest ran
//
// Three facts every caller below relies on:
//
//   - EVERY leg exited 0 with is_error=false, whatever was denied. A denial shows
//     only in the result event's permission_denials. A site that tightens its
//     mode must make a denied action visible some other way: the verifier is
//     told that a denied check is INCONCLUSIVE, never FAIL; the planner's product
//     is a plan dir the wizard looks for.
//   - Path-confined write rules are spelled Edit(//abs/**) (EditUnder); the
//     Write(...) spelling matched nothing on 2.1.308.
//   - Deny rules hold under bypassPermissions. They match the command as the
//     model writes it — not /usr/bin/curl, not sh -c '…' — so they are a guard,
//     not a sandbox.
//
// Hence one default per KIND of spawn site (Site, ModeFor):
//
//   - SiteExecutor — dispatch, phaserun, planrun, routines, provision:
//     bypassPermissions. An execution contract asks for edits, arbitrary build and
//     test commands and a commit; acceptEdits denies every Bash command and
//     dontAsk would need an open-ended Bash list. These sites carry
//     AgentDenyArgs on top: no remote writes, no PR/MR, no daemon API, no read
//     of the daemon's credentials. An executor is trusted as the user it runs
//     as — it can write and run anything that user can. The deny list guards
//     the commands a model writes, so a plan's prompt cannot talk it into a
//     push or a daemon call; it is not a boundary against a process that means
//     to get around it (a script, `sh -c`, a client it does not name).
//   - SiteReadOnly — the verifier when its task declares Verify commands, and
//     the phase and plan reviewers: dontAsk plus an explicit --allowedTools.
//   - SitePlanner — planning, and the resume of a planning session: dontAsk plus
//     read tools, read-only git history and Edit only under the plan workspace
//     (a revise run: its scratch dir). Its cwd is the operator's PRIMARY
//     checkout, so it gets no unrestricted Bash and no edits there.
//   - SiteForeign — the resume of a session swarmery did not spawn: no flag, the
//     CLI's own default. Never bypass for a conversation the daemon never owned.
//
// permissions.deny in the project settings applies in every mode: with
// `Read(./.env)` denied, a bypassPermissions run asked to read .env came back
// DENIED. And a headless run skips the trust dialog and RUNS the repository's
// .claude/settings.json hooks in every mode, dontAsk and plan included — a spawn
// trusts whatever agent settings the repository ships.
//
// Operators who disagree can pin any mode per spawn site, or omit the flag
// entirely, via the env knobs below; SWARMERY_PERMISSION_MODE=bypassPermissions
// restores the previous everywhere-bypass behaviour in one variable.
package claudeflags

import (
	"log"
	"os"
	"strings"
)

// ModeEnv is the cross-site override, consulted when a spawn site's own knob is
// unset.
const ModeEnv = "SWARMERY_PERMISSION_MODE"

// DefaultMode is what an EXECUTOR spawn gets when nothing is configured (see
// SiteExecutor and the package doc for the measurement behind it).
const DefaultMode = "bypassPermissions"

// OmitMode is the escape hatch: setting a knob to this value passes NO
// --permission-mode flag at all, restoring pre-fix behaviour (which denies
// every prompting tool call — kept reachable deliberately, so an operator
// debugging a permission question can reproduce the old shape exactly).
const OmitMode = "off"

// Site is the kind of spawn a mode is resolved for. It picks the DEFAULT only;
// the knobs still win (ModeFor).
type Site string

const (
	// SiteExecutor edits, runs checks and commits in its own worktree.
	SiteExecutor Site = "executor"
	// SiteReadOnly grades or reviews; it runs at most the commands its
	// allow-list names.
	SiteReadOnly Site = "read-only"
	// SitePlanner reads the primary checkout and writes only the plan dirs its
	// allow-list names.
	SitePlanner Site = "planner"
	// SiteForeign continues a session swarmery did not spawn.
	SiteForeign Site = "foreign"
)

// siteDefault is each Site's mode when no knob is set; "" omits the flag. An
// unknown Site (the zero value included) is SiteForeign: the least privilege.
func siteDefault(site Site) string {
	switch site {
	case SiteExecutor:
		return DefaultMode
	case SiteReadOnly, SitePlanner:
		return "dontAsk"
	default:
		return ""
	}
}

// validModes mirrors `claude --help`'s choices for --permission-mode. An
// unknown value must not reach the CLI: `claude` rejects it and the spawn dies
// before the run starts, turning an operator typo into a dead phase.
var validModes = map[string]string{
	"acceptedits":       "acceptEdits",
	"acceptEdits":       "acceptEdits",
	"auto":              "auto",
	"bypasspermissions": "bypassPermissions",
	"bypassPermissions": "bypassPermissions",
	"manual":            "manual",
	"dontask":           "dontAsk",
	"dontAsk":           "dontAsk",
	"plan":              "plan",
}

// PermissionModeArgs returns the `--permission-mode <mode>` pair to append to an
// EXECUTOR spawn's argv, or nil when the flag must be omitted.
//
// siteEnv names the spawn site's own knob (e.g.
// SWARMERY_PHASERUN_PERMISSION_MODE) and wins over ModeEnv, which wins over
// DefaultMode. An empty siteEnv means "this site has no knob of its own".
func PermissionModeArgs(siteEnv string) []string {
	return ModeArgsFor(SiteExecutor, siteEnv)
}

// ModeArgsFor is PermissionModeArgs for any Site, with the knob chain of
// ModeForChain.
func ModeArgsFor(site Site, siteEnvs ...string) []string {
	mode := ModeForChain(site, siteEnvs...)
	if mode == "" {
		return nil
	}
	return []string{"--permission-mode", mode}
}

// Mode is the EXECUTOR resolution without the flag pair: the canonical mode for
// this spawn site, or "" when the flag must be omitted. Spawn sites that build
// argv through internal/runcore want the VALUE (Spec.PermissionMode), not a
// pre-assembled pair — this keeps the precedence and the validation in one place
// for both shapes.
func Mode(siteEnv string) string { return ModeFor(SiteExecutor, siteEnv) }

// ModeFor resolves a site's mode: its own knob, then ModeEnv, then the Site's
// default. "" means omit the flag. An invalid knob value is logged and falls
// back to the SITE's default — a typo on a read-only site must not widen it.
func ModeFor(site Site, siteEnv string) string { return ModeForChain(site, siteEnv) }

// foreignModes are the only modes a SiteForeign spawn may resolve to: no flag,
// or a mode no wider than the CLI's own default. A knob cannot hand a session
// swarmery did not spawn bypassPermissions (or auto, or acceptEdits).
var foreignModes = map[string]bool{"": true, "dontAsk": true, "plan": true}

// ModeForChain is ModeFor with several site knobs consulted in order before
// ModeEnv — a resume names the ORIGINAL site's knob first, then its own. For
// SiteForeign the result is clamped to foreignModes: anything wider is logged
// and dropped to "no flag".
func ModeForChain(site Site, siteEnvs ...string) string {
	mode := resolveChain(site, siteEnvs)
	if site != SiteExecutor && site != SiteReadOnly && site != SitePlanner && !foreignModes[mode] {
		log.Printf("warning: claudeflags: a session swarmery did not spawn may not run as %s; passing no --permission-mode", mode)
		return ""
	}
	return mode
}

func resolveChain(site Site, siteEnvs []string) string {
	def := siteDefault(site)
	raw, from := "", ""
	for _, env := range siteEnvs {
		if env == "" {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			raw, from = v, env
			break
		}
	}
	if raw == "" {
		if v := strings.TrimSpace(os.Getenv(ModeEnv)); v != "" {
			raw, from = v, ModeEnv
		}
	}
	if raw == "" {
		return def
	}
	switch strings.ToLower(raw) {
	case OmitMode, "none", "default":
		// "default" is spelled out here rather than passed through: it is the word
		// an operator reaches for when they mean "whatever claude does on its own",
		// which is exactly omission.
		return ""
	}
	if canonical, ok := validModes[raw]; ok {
		return canonical
	}
	if canonical, ok := validModes[strings.ToLower(raw)]; ok {
		return canonical
	}
	shown := def
	if shown == "" {
		shown = "no flag"
	}
	log.Printf("warning: claudeflags: ignoring invalid %s=%q; using %s (valid: acceptEdits, auto, bypassPermissions, manual, dontAsk, plan, or %q to omit the flag)",
		from, raw, shown, OmitMode)
	return def
}
