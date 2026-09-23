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
	StatusNoLogin Status = "no-login" // the CLI demanded a login for this config dir
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
	env := withoutConfigDir(os.Environ())
	if configDir != "" {
		env = append(env, configDirEnv+"="+configDir)
	}
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
