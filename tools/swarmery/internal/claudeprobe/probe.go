// Package claudeprobe answers ONE question authoritatively: can the `claude`
// CLI actually run under a given config dir? It runs the cheapest
// authenticating invocation and classifies the outcome — no storage, no HTTP,
// no knowledge of the account registry beyond the dir it is handed.
//
// This is a DIFFERENT question from usage's `connected` ("swarmery can read
// this account's quota"): the two legitimately disagree, and exposing that
// disagreement is why this package exists.
//
// # The invocation, and why (measured 2026-08-12, CLI 2.1.220 —
// docs/claude-cli-credential-behaviour.md)
//
//   - `claude auth status` is sub-second, costs no tokens, and its exit code
//     alone separates a logged-in config dir (0, `"loggedIn": true`) from one
//     with no login (1, `"loggedIn": false`).
//   - a config dir with no credential fails outright and does NOT fall back to
//     the default account, so probing a dir really does probe that account.
//
// # Classification is exit-status first, wording second
//
// Zero exit → ready, unconditionally. A non-zero exit is no-login only when
// the output matches one of the CLI's recorded no-login shapes; everything
// else — binary missing, timeout, unrecognised non-zero — is unknown, never
// ready. Reason is always one of the fixed constants below: CLI output is
// never interpolated into it, the same discipline as usage/login.go's fixed
// sentinels, so nothing the CLI prints can carry credential material upstream.
package claudeprobe

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procgroup"
)

// Status is the authoritative answer to "can the CLI run under this config dir?".
type Status string

const (
	StatusReady   Status = "ready"    // the CLI authenticated
	StatusNoLogin Status = "no-login" // the CLI demanded a login for this config dir, or refused the account's access (ReasonAccessRefused)
	StatusUnknown Status = "unknown"  // could not be determined (timeout, no binary, unrecognised failure)
	// StatusLimited: the run failed because the account hit a Claude usage
	// limit. It says NOTHING about the login — a limited account is logged in —
	// so it is never stored as an account_runnable verdict (runtruth routes it
	// to account_limit_hits instead).
	StatusLimited Status = "limited"
)

// The fixed set of operator-facing reasons. Nothing outside this list may ever
// reach Result.Reason — see the package doc.
const (
	ReasonNoLogin      = "Claude login required for this account"
	ReasonNoBinary     = "claude CLI not found on this machine"
	ReasonTimeout      = "the claude CLI did not answer within the probe timeout"
	ReasonUnrecognised = "the claude CLI failed in an unrecognised way"
	ReasonStartFailed  = "the claude CLI could not be started"
	ReasonRateLimited  = "this account has hit a Claude usage limit"
	// ReasonAccessRefused: the CLI is logged in as far as `auth status` can
	// tell, yet a real run was refused — the organisation disabled subscription
	// access, or the API answered 401. Logging in again may not be the fix,
	// which is why it is not ReasonNoLogin.
	ReasonAccessRefused = "Claude refused this account's access"
	// ReasonAPIError: the ping died on an API error (overloaded, unreachable).
	// That is about the API, not the account, so its status is unknown.
	ReasonAPIError = "the Claude API failed while the account was being checked"
)

// Result is what a probe run produced. Reason is a SHORT operator-facing
// phrase from the constants above — never raw CLI output and never anything
// that could carry credential material. Empty for StatusReady.
type Result struct {
	Status Status
	Reason string
}

// defaultTimeout bounds a probe whose caller did not bring a deadline of its
// own. `claude auth status` answers in well under a second when healthy, so
// 45s is generous headroom for a cold start, not an expected wait.
const defaultTimeout = 45 * time.Second

// resolveBin locates the claude executable. A package var only so tests can
// simulate a machine with no CLI installed at all: claudebin.Resolve probes
// fixed system dirs (/opt/homebrew/bin, …) that a test cannot empty out.
var resolveBin = claudebin.Resolve

// probeArgs is the invocation under test — see the package doc for the
// measurement that chose it.
var probeArgs = []string{"auth", "status"}

// Probe runs the cheapest authenticating `claude` invocation under configDir
// and classifies the outcome.
//
// An empty configDir probes the DEFAULT account, and means the child env
// carries no CLAUDE_CONFIG_DIR AT ALL — absence, not an empty value, is what
// selects the default (claudeacct.EnvForAccount's contract; the dispatch and
// verify runner tests assert exactly this shape). Any CLAUDE_CONFIG_DIR the
// daemon itself inherited is stripped first, for the same reason: a probe's
// whole job is account identity, so the child's account must come from the
// argument and nowhere else.
//
// The default timeout is 45s; a caller-supplied ctx deadline overrides it.
// The child runs in its own process group (internal/procgroup), so a hung CLI
// is killed as a tree, not as a lone leader.
func Probe(ctx context.Context, configDir string) Result {
	env := withoutConfigDir(os.Environ())
	if configDir != "" {
		env = append(env, configDirEnv+"="+configDir)
	}
	return ProbeEnv(ctx, env)
}

// ProbeEnv is Probe under a COMPLETE, caller-built environment: the child gets
// exactly env, nothing is stripped and nothing is added.
//
// It exists for the caller whose question is not "is this config dir logged
// in?" but "will the run I am about to admit be able to authenticate?" — the
// admission pre-flight (internal/runcore). That question is only answered by
// probing under the environment the run itself gets
// (claudeacct.SpawnEnvResolved): an unbound project's run keeps whatever
// CLAUDE_CONFIG_DIR the daemon inherited, so a probe that stripped it — Probe's
// rule, right for an account-management screen — would vouch for, or condemn, a
// different account than the one the run uses.
//
// A nil env means the current process's environment (os/exec's own rule).
// Timeout and process-group handling are Probe's.
func ProbeEnv(ctx context.Context, env []string) Result {
	bin, err := resolveBin()
	if err != nil {
		return Result{Status: StatusUnknown, Reason: ReasonNoBinary}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, bin, probeArgs...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	procgroup.Isolate(cmd, 0)

	runErr := cmd.Run()
	if cmd.Process != nil {
		procgroup.Drain(cmd.Process.Pid, 0)
	}

	switch {
	case ctx.Err() != nil:
		// Deadline or caller cancellation: either way the CLI was cut off
		// before answering, so nothing was determined.
		return Result{Status: StatusUnknown, Reason: ReasonTimeout}
	case runErr == nil:
		return ClassifyExit(0, out.String())
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		// The process could not be started or observed at all (fork failure,
		// permission). Distinct from a CLI that ran and failed.
		return Result{Status: StatusUnknown, Reason: ReasonStartFailed}
	}
	return ClassifyExit(exitErr.ExitCode(), out.String())
}

// The ping: stage two of an account check. `claude auth status` (Probe) reads
// the stored login and nothing else — it never talks to the API — so it answers
// "ready" for an account whose organisation disabled subscription access, whose
// refresh token the server no longer honours, or which is out of usage. Only a
// real model call sees those, so ProbeRun makes the smallest one there is: one
// fixed prompt, the cheapest model, the lowest effort, one turn.
const (
	// PingPrompt has a fixed expected answer on purpose: with nothing but "OK"
	// expected on stdout, a line that is a recorded failure shape can only be the
	// failure itself (classifyPing).
	PingPrompt = "Reply with exactly: OK"
	// PingModel is the cheapest tier. A full ID, not an alias — aliases
	// re-resolve over time.
	PingModel = "claude-haiku-4-5"
	// PingEffort is pinned because an omitted --effort is the CLI's xhigh.
	PingEffort = "low"
)

// pingTimeout bounds a ping whose caller brought no deadline. The measured ping
// takes about four seconds (docs/claude-cli-credential-behaviour.md §3); a
// minute is headroom for a cold start, not an expected wait.
const pingTimeout = 60 * time.Second

// pingArgs is the ping's argv. The prompt is an ARGUMENT — the measured shape
// is `claude -p <prompt> --max-turns 1` — and it is never empty: an empty
// prompt fails argument validation before any auth check, so it would tell a
// working account from a broken one no better than a coin.
func pingArgs() []string {
	return []string{"-p", PingPrompt, "--model", PingModel, "--effort", PingEffort, "--max-turns", "1"}
}

// ProbeRun is stage two of an account check: a minimal `claude -p` ping,
// classified from its OUTPUT regardless of the exit code (see classifyPing).
// Call it only after stage one answered ready — it costs one short model turn,
// and an account with no login is already answered for free.
//
//	ready    the account authenticated and the model answered
//	no-login a recorded auth shape — a login demand, or access refused
//	limited  a recorded usage-limit shape
//	unknown  no binary, a timeout, an API error, an unrecognised failure
//
// env is the child's COMPLETE environment, exactly as ProbeEnv takes it: the
// caller builds the environment the run it is vouching for would get, and the
// ping runs under that and nothing else. A nil env means the current process's
// environment.
//
// dir is the child's working directory ("" leaves it alone). Production passes
// the System project's directory (systemspawn.Dir), so the ping's transcript
// lands there instead of in whatever directory the daemon was started in. It is
// a parameter rather than an import because systemspawn sits above this
// package (it imports ingest, which imports claudeprobe). Only the DIRECTORY is
// taken from there: no `--settings` is spliced into the argv, because the
// System project's composed settings belong to a different project than the
// run being vouched for.
//
// The default timeout is 60s; a caller-supplied ctx deadline overrides it.
func ProbeRun(ctx context.Context, env []string, dir string) Result {
	bin, err := resolveBin()
	if err != nil {
		return Result{Status: StatusUnknown, Reason: ReasonNoBinary}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pingTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, bin, pingArgs()...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	procgroup.Isolate(cmd, 0)

	runErr := cmd.Run()
	if cmd.Process != nil {
		procgroup.Drain(cmd.Process.Pid, 0)
	}

	if ctx.Err() != nil {
		return Result{Status: StatusUnknown, Reason: ReasonTimeout}
	}
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return Result{Status: StatusUnknown, Reason: ReasonStartFailed}
		}
		exitCode = exitErr.ExitCode()
	}
	return classifyPing(exitCode, stdout.String(), stderr.String())
}

// configDirEnv is the variable that selects the CLI's account.
const configDirEnv = "CLAUDE_CONFIG_DIR"

// withoutConfigDir returns env minus every CLAUDE_CONFIG_DIR entry.
func withoutConfigDir(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, configDirEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
