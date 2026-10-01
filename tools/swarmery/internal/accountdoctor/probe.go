package accountdoctor

// Arm (f) and the live probe (D13). The channel probe's verdict is a fact
// about the CLI VERSION, measured in a throwaway config dir, so it lives in a
// file keyed by that version — channelprobe.Path()/<V>.json — and never in
// SQLite, never per account.
//
//   - probeVerdict (Fast) READS it. The installed version V comes from the
//     resolved binary's PATH (…/versions/<V>), never from `claude --version`:
//     Fast runs at turn zero and must not start a claude process.
//   - Probe RUNS the harness (cc-channel-probe.sh --json) and writes the
//     verdict through channelprobe.Save. It is the ONLY arm that spawns
//     anything; Fast and Full never reach it.
//   - ProbeIfNewVersion is what the daemon's ticker calls: Probe, but only
//     when no <V>.json exists yet — i.e. only after a CLI version change.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/channelprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
)

// ProbeScriptEnv points --probe at a harness on disk instead of the copy
// embedded in the binary.
const ProbeScriptEnv = "SWARMERY_PROBE_SCRIPT"

// claudeBin resolves the CLI (the shim dir skipped). A seam for tests.
var claudeBin = claudebin.Resolve

// runHarness runs the harness script with env and waits for it. Its output goes
// to stderr — the doctor's stdout carries the report and nothing else. It
// returns the exit code (-1 when the process could not run). A seam: tests
// inject a runner and never start a real CLI.
var runHarness = func(ctx context.Context, script string, env []string) (int, error) {
	cmd := exec.CommandContext(ctx, "/bin/bash", script, "--json")
	cmd.Env = env
	cmd.Dir = filepath.Dir(script)
	cmd.Stdin = nil
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// CLIVersionFromPath derives the installed CLI version from the binary's path:
// the symlinks resolved, then the base name when the parent directory is named
// `versions` (…/claude/versions/<V>). ok false for any other layout.
func CLIVersionFromPath(bin string) (string, bool) {
	if strings.TrimSpace(bin) == "" {
		return "", false
	}
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", false
	}
	if filepath.Base(filepath.Dir(real)) != "versions" {
		return "", false
	}
	v := filepath.Base(real)
	if channelprobe.FileName(v) != v+".json" {
		return "", false // not a version-shaped name
	}
	return v, true
}

// installedVersion is V for the CLI claudebin resolves, and a reason when it
// cannot be derived.
func installedVersion() (v, bin, why string) {
	bin, err := claudeBin()
	if err != nil {
		return "", "", "no claude CLI could be resolved"
	}
	v, ok := CLIVersionFromPath(bin)
	if !ok {
		return "", bin, fmt.Sprintf("the CLI at %s is not installed under a versions/<V> directory, so its version cannot be read without running it", bin)
	}
	return v, bin, ""
}

// probeVerdict is arm (f): read the stored verdict for the installed V.
func (r *run) probeVerdict() {
	v, _, why := installedVersion()
	if why != "" {
		r.add(Finding{ID: "probe-stale", Severity: SevWarn, Title: "no channel-probe verdict for the installed CLI",
			Detail: why + "; run `swarmery account doctor --probe`"})
		return
	}
	dir := channelprobe.Path()
	file := filepath.Join(dir, channelprobe.FileName(v))
	res, err := channelprobe.Load(file)
	if err != nil {
		r.add(Finding{ID: "probe-stale", Severity: SevWarn,
			Title:  fmt.Sprintf("no usable channel-probe verdict for CLI %s", v),
			Detail: fmt.Sprintf("%s: %v; run `swarmery account doctor --probe`", file, err), File: file})
		return
	}
	base, err := channelprobe.Baseline()
	if err != nil {
		return
	}
	if drift := channelprobe.Compare(base, res); len(drift) > 0 {
		seen := map[string]bool{}
		var facts []string
		for _, d := range drift {
			if !seen[d.Fact] {
				seen[d.Fact] = true
				facts = append(facts, d.Fact)
			}
		}
		sort.Strings(facts)
		r.add(Finding{ID: "probe-drift", Severity: SevError,
			Title:  fmt.Sprintf("CLI %s drifted from the channel baseline", v),
			Detail: "drifted facts: " + strings.Join(facts, ", ") + " — re-read the plan decisions that rest on them",
			File:   file})
	}
}

// Probe runs the channel-probe harness against the installed CLI, stores its
// verdict with channelprobe.Save, and returns a fresh Fast report. The child
// runs with a SCRUBBED environment — every variable NAME found in any store
// under claudeacct.SecretsDir() removed, so it carries none of them — a fresh
// temporary CLAUDE_CONFIG_DIR, and SWARMERY_CLAUDE_BIN set to the resolved CLI.
// The harness is $SWARMERY_PROBE_SCRIPT when set, else the copy embedded in the
// binary, written into a fresh 0700 temp dir; with neither, Probe fails naming
// both. A harness that reports drift (exit 1 with a result) is a stored
// verdict, not a failure: the report then carries probe-drift.
func Probe(ctx context.Context, opts Options) (Report, error) {
	if err := runProbe(ctx); err != nil {
		return emptyReport(""), err
	}
	return Fast(opts)
}

// ProbeIfNewVersion runs Probe's measurement only when the installed CLI's
// verdict file does not exist yet — the daemon's A8 ticker calls it every
// interval, so a probe happens once per CLI version and never otherwise. ran
// reports whether it measured. A version it cannot derive is skipped.
func ProbeIfNewVersion(ctx context.Context) (ran bool, err error) {
	v, _, why := installedVersion()
	if why != "" {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(channelprobe.Path(), channelprobe.FileName(v))); err == nil {
		return false, nil
	}
	return true, runProbe(ctx)
}

// runProbe is the measurement itself.
func runProbe(ctx context.Context) error {
	work, err := os.MkdirTemp("", "swarmery-probe-")
	if err != nil {
		return fmt.Errorf("probe: temp dir: %w", err)
	}
	defer os.RemoveAll(work)
	if err := os.Chmod(work, 0o700); err != nil {
		return fmt.Errorf("probe: temp dir: %w", err)
	}

	script := strings.TrimSpace(getenv(ProbeScriptEnv))
	if script == "" {
		script, err = channelprobe.WriteHarness(filepath.Join(work, "harness"))
		if errors.Is(err, channelprobe.ErrNoHarness) {
			return fmt.Errorf("probe: no harness — this binary embeds none (build it with `make build`, whose copy-probe step snapshots scripts/tests/cc-channel-probe.sh) and $%s is unset", ProbeScriptEnv)
		}
		if err != nil {
			return err
		}
	}
	cfg := filepath.Join(work, "config")
	out := filepath.Join(work, "out")
	for _, d := range []string{cfg, out} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return fmt.Errorf("probe: %w", err)
		}
	}

	env := probeEnv(os.Environ(), cfg, out)
	code, err := runHarness(ctx, script, env)
	if err != nil {
		return fmt.Errorf("probe: run %s: %w", script, err)
	}
	if code != 0 && code != 1 {
		return fmt.Errorf("probe: %s exited %d", script, code)
	}
	res, err := readHarnessResult(out)
	if err != nil {
		return fmt.Errorf("probe: %s exited %d without a usable result: %w", script, code, err)
	}
	if _, err := channelprobe.Save(channelprobe.Path(), res); err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	return nil
}

// probeEnv is base with every store NAME and every CLAUDE_CONFIG_DIR /
// SWARMERY_PROBES_DIR / SWARMERY_CLAUDE_BIN removed, then the probe's own three
// set. Names only are read from the stores; each value is discarded at once.
func probeEnv(base []string, cfg, out string) []string {
	drop := map[string]bool{"CLAUDE_CONFIG_DIR": true, "SWARMERY_PROBES_DIR": true, "SWARMERY_CLAUDE_BIN": true}
	for _, name := range allStoreNames() {
		drop[name] = true
	}
	env := make([]string, 0, len(base)+3)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			env = append(env, kv)
		}
	}
	env = append(env, "CLAUDE_CONFIG_DIR="+cfg, "SWARMERY_PROBES_DIR="+out)
	if bin, err := claudeBin(); err == nil {
		env = append(env, "SWARMERY_CLAUDE_BIN="+bin)
	}
	return env
}

// allStoreNames is the union of variable NAMES in every loadable store under
// SecretsDir() — one flat listing of one directory.
func allStoreNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range listStores() {
		if s.state != claudeacct.StorePresent {
			continue
		}
		for _, n := range s.names {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// readHarnessResult parses the one <V>.json the harness wrote into dir.
func readHarnessResult(dir string) (channelprobe.Result, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(matches) != 1 {
		return channelprobe.Result{}, fmt.Errorf("expected one result file in the output dir, found %d", len(matches))
	}
	return channelprobe.Load(matches[0])
}
