package claudeacct

// PROVENANCE, Lock 1: a settings.local.json that git TRACKS does not choose the
// Claude account, and therefore does not unlock ~/.swarmery/secrets/<key>.env.
//
// # The leak this closes
//
// The binding file is a machine-local tier by design (binding.go's header): two
// engineers on one repo legitimately run different accounts, so the choice never
// belongs in a commit. Nothing enforced that. Clone a repository that commits
//
//	.claude/settings.local.json  →  {"swarmery":{"claudeAccount":"…"}}
//
// and the very first session in that clone ran under the committed account AND
// composed every variable in that account's secret store, because SecretEnvFor
// is SecretEnvForAccount(Binding(p)). A file a stranger wrote chose whose
// credentials a local process got handed.
//
// # The rule
//
//	tracked      → IGNORE the binding (default account, zero store names)
//	unknown      → IGNORE it too (fail closed)
//	untracked    → honour it, exactly as before (gitignored counts as untracked —
//	               a gitignored file cannot have arrived from someone else)
//	not-a-repo   → honour it, exactly as before
//
// Ignoring never blocks a launch: it degrades to the default account and logs
// ONE warning per path per process naming the path, the reason and the fix. A
// copied or extracted tree has no .git, so it reads as not-a-repo and is
// honoured here — that residual is closed for credentials by the store anchor
// (Lock 2), not by this file.
//
// # Why git is asked, and why it is caged while being asked
//
// "Is this file tracked?" is not answerable from the file alone: the answer
// lives in the index of whatever repository contains it. So git runs — and it
// runs against a repository the operator has not decided to trust yet, which is
// a code-execution surface. Every cage below is load-bearing:
//
//   - a Go pre-check (hasGitAncestor) means a path with no .git above it never
//     starts git at all, so a broken git installation cannot take out the
//     machine's unbound projects;
//   - -c core.fsmonitor= kills the per-repo hook git would otherwise SPAWN just
//     to answer this question;
//   - -c core.hooksPath=/dev/null takes the repository's hooks off the table;
//   - -c safe.bareRepository=explicit refuses a bare repository planted where a
//     settings directory belongs (that refusal classifies as unknown, i.e. the
//     binding is ignored);
//   - gitProbeEnv drops every GIT_* variable that could re-point discovery,
//     the index, or config at another repository;
//   - ls-files runs no filters, no diff driver and no pager, and the whole probe
//     is bounded by a 2 s timeout.

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// trackVerdict is what the provenance probe concluded about one path.
type trackVerdict int

const (
	// trackUnknown is the ZERO value on purpose: a verdict nobody managed to set
	// must read as "we do not know", which bindingDistrusted turns into "ignore".
	trackUnknown trackVerdict = iota
	trackUntracked
	trackNotRepo
	trackTracked
)

func (v trackVerdict) String() string {
	switch v {
	case trackUntracked:
		return "untracked"
	case trackNotRepo:
		return "not-a-repo"
	case trackTracked:
		return "tracked"
	default:
		return "unknown"
	}
}

const (
	// gitProbeTimeout bounds one invocation. A git that hangs — a network
	// fsmonitor, an NFS stall — must degrade to "unknown" (ignore the binding),
	// never to a stuck spawn.
	gitProbeTimeout = 2 * time.Second

	// gitUntrackedMarker is git's own wording for "that path is not in the
	// index", which is exactly the untracked verdict. Matching the TEXT and not
	// merely exit 1 is what keeps every other exit-1 failure fail-closed.
	gitUntrackedMarker = "did not match any file(s) known to git"

	// gitNotRepoPrefix is the PREFIX shared by both of git's no-repository
	// messages: "(or any of the parent directories): .git" when discovery walked
	// to the root, and "(or any parent up to mount point /…)" when it stopped at
	// a filesystem boundary. The second one is why this is a prefix and not the
	// full sentence: because the probe scrubs GIT_DISCOVERY_ACROSS_FILESYSTEM,
	// every binding on an external disk, a separate /home or a tmpfs /tmp
	// produces the mount-point wording — and matching only the first form would
	// have classified all of them as unknown and ignored them.
	gitNotRepoPrefix = "fatal: not a git repository (or any"
)

// distrustedGitVars are the caller-environment variables that can re-point git's
// repository discovery, its index, its config or its pathspec interpretation.
// Every one of them is dropped before the probe runs, so the answer describes
// the file's OWN repository and nothing else.
//
// Dropping GIT_CONFIG_GLOBAL and friends is deliberate too: it puts the probe on
// the operator's real git config rather than on whatever a parent process
// substituted, and the three settings that actually matter for safety are pinned
// with -c on the command line, where they outrank any config file.
var distrustedGitVars = map[string]struct{}{
	"GIT_DIR":                          {},
	"GIT_WORK_TREE":                    {},
	"GIT_INDEX_FILE":                   {},
	"GIT_INDEX_VERSION":                {},
	"GIT_COMMON_DIR":                   {},
	"GIT_OBJECT_DIRECTORY":             {},
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
	"GIT_CEILING_DIRECTORIES":          {},
	"GIT_DISCOVERY_ACROSS_FILESYSTEM":  {},
	"GIT_NAMESPACE":                    {},
	"GIT_IMPLICIT_WORK_TREE":           {},
	"GIT_CONFIG":                       {},
	"GIT_CONFIG_PARAMETERS":            {},
	"GIT_CONFIG_COUNT":                 {},
	"GIT_CONFIG_GLOBAL":                {},
	"GIT_CONFIG_SYSTEM":                {},
	"GIT_CONFIG_NOSYSTEM":              {},
	"GIT_LITERAL_PATHSPECS":            {},
	"GIT_GLOB_PATHSPECS":               {},
	"GIT_NOGLOB_PATHSPECS":             {},
	"GIT_ICASE_PATHSPECS":              {},
	"GIT_ATTR_NOSYSTEM":                {},
	"GIT_EXTERNAL_DIFF":                {},
	"GIT_PAGER":                        {},
	"GIT_ASKPASS":                      {},
	"GIT_SSH":                          {},
	"GIT_SSH_COMMAND":                  {},
	"GIT_PROXY_COMMAND":                {},
}

// distrustedGitVarPrefixes covers the numbered config pairs — GIT_CONFIG_KEY_0,
// GIT_CONFIG_VALUE_0, … — which are unbounded in count and so cannot be listed.
// They are the most direct redirection of all: a single pair sets core.fsmonitor
// to any executable.
var distrustedGitVarPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}

// gitProbeEnv is base minus every distrusted variable, plus the three the probe
// wants pinned: a C locale (so git's messages are the ones classifyGitProbe
// matches, whatever the operator's LANG is), no opportunistic index rewrite, and
// no credential prompt.
func gitProbeEnv(base []string) []string {
	out := make([]string, 0, len(base)+3)
	for _, kv := range base {
		if isDistrustedGitVar(envKey(kv)) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
}

func isDistrustedGitVar(name string) bool {
	if _, ok := distrustedGitVars[name]; ok {
		return true
	}
	for _, p := range distrustedGitVarPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// gitProbeResult is one invocation's outcome. exitCode is -1 when there was no
// exit status to read (the process never started, or the context expired); err is
// set only for those non-exit failures, so a plain exit 1 arrives with err nil.
type gitProbeResult struct {
	exitCode int
	stderr   string
	err      error
}

// runGitProbe is a package var so a test can count invocations and stand in for
// git without a real repository. Production never replaces it.
var runGitProbe = func(dir, name string) gitProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), gitProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git",
		"--no-pager",
		"-c", "core.fsmonitor=",
		"-c", "core.hooksPath=/dev/null",
		"-c", "safe.bareRepository=explicit",
		"-C", dir,
		"ls-files", "--error-unmatch", "--", ":(icase,literal)"+name,
	)
	cmd.Dir = dir
	cmd.Env = gitProbeEnv(os.Environ())
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := gitProbeResult{exitCode: -1, stderr: stderr.String()}
	switch {
	case err == nil:
		res.exitCode = 0
	case ctx.Err() != nil:
		// Report the timeout itself, not the SIGKILL exit status it produced:
		// a killed git's exit code carries no verdict.
		res.err = ctx.Err()
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.exitCode = ee.ExitCode()
		} else {
			res.err = err
		}
	}
	return res
}

// classifyGitProbe is the whole decision, as a PURE function: it is the part
// worth a table test, and keeping it free of exec and the filesystem is what
// lets the mount-boundary and dubious-ownership wordings be pinned without
// arranging a real mount or a foreign-owned repository.
func classifyGitProbe(exitCode int, stderr string, runErr error) trackVerdict {
	if runErr != nil {
		return trackUnknown
	}
	switch {
	case exitCode == 0:
		return trackTracked
	case exitCode == 1 && strings.Contains(stderr, gitUntrackedMarker):
		return trackUntracked
	case exitCode == 128 && strings.HasPrefix(strings.TrimSpace(stderr), gitNotRepoPrefix):
		return trackNotRepo
	}
	// Everything left is a git that did not answer the question: dubious
	// ownership, a refused bare repository, an exit 1 with some other text, a
	// corrupt index. Unknown, and therefore ignored.
	return trackUnknown
}

// hasGitAncestor reports whether dir or any ancestor holds a .git entry. Lstat,
// and a DIRECTORY OR A FILE, because a worktree and a submodule spell it as a
// file containing "gitdir: …".
//
// This is the pre-check that keeps a broken git from becoming a machine-wide
// outage: the common workspace parent is not itself a repository, so the
// projects under it that carry no .git never reach exec at all.
func hasGitAncestor(dir string) bool {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// resolveProbePath is the canonical absolute path of the binding file.
//
// EvalSymlinks first, because the multi-repo overlay shape points .claude at a
// directory in ANOTHER repository — the question "is it tracked?" is about the
// file's real location, never about the link. Abs after it, because Binding("")
// hands over the RELATIVE ".claude/settings.local.json"; without Abs the probe's
// -C would be "." and the answer would describe the daemon's own cwd.
func resolveProbePath(path string) (string, bool) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return "", false
	}
	return abs, true
}

// probeGitTracked classifies one binding file, running git only when the
// pre-check says a repository could plausibly contain it.
func probeGitTracked(path string) trackVerdict {
	abs, ok := resolveProbePath(path)
	if !ok {
		return trackUnknown // unresolvable: fail closed
	}
	dir, name := filepath.Dir(abs), filepath.Base(abs)
	if !hasGitAncestor(dir) {
		return trackNotRepo
	}
	r := runGitProbe(dir, name)
	return classifyGitProbe(r.exitCode, r.stderr, r.err)
}

// distrustReason is the operator-facing half of a verdict: "" when the binding
// may be honoured, the reason when it may not. Untracked and not-a-repo are the
// only two honoured verdicts, so anything new or unforeseen is ignored.
//
// The reason never quotes the file's CONTENTS — not the account key, not a
// store name. It is written into a log the operator may paste anywhere.
func distrustReason(v trackVerdict) string {
	switch v {
	case trackUntracked, trackNotRepo:
		return ""
	case trackTracked:
		return "it is tracked by git, so it can have arrived from a clone, a pull or a teammate's commit"
	default:
		return "git could not say whether it is tracked, and an unclassifiable binding is not trusted"
	}
}

// bindingDistrusted is the gate Binding() calls. It probes FRESH every time:
// this is a spawn path, and a cached verdict there would be a cached security
// decision.
func bindingDistrusted(path string) string {
	return distrustReason(probeGitTracked(path))
}

// distrustedWarned is the warn-once ledger, keyed by resolved absolute path. One
// line per path per process: Binding() is called on every spawn and on several
// dashboard endpoints, so a line per CALL would bury the operator's log — and a
// warning nobody reads is not a warning.
// A POINTER, so a test can swap in a fresh ledger; go vet rejects assigning a
// zero sync.Map over one (copylocks).
var distrustedWarned = &sync.Map{}

func logDistrusted(path, why string) {
	key := path
	if abs, ok := resolveProbePath(path); ok {
		key = abs
	}
	if _, seen := distrustedWarned.LoadOrStore(key, struct{}{}); seen {
		return
	}
	log.Printf("claudeacct: IGNORING binding in %s — %s. "+
		"Running under the default account with no account secrets. "+
		"To make it count, untrack it: git rm --cached %s, then re-run `swarmery account use <key>`.",
		path, why, path)
}

// ── the display-only verdict cache ───────────────────────────────────────────

// displayEntry is one cached verdict plus the identity of the file it describes.
type displayEntry struct {
	verdict trackVerdict
	size    int64
	modTime time.Time
	at      time.Time
}

var (
	displayCacheMu sync.Mutex
	displayCache   = map[string]displayEntry{}

	// displayCacheTTL bounds staleness. Keying on (realpath, size, mtime) is not
	// enough on its own: `git add` and `git rm --cached` change the INDEX and
	// leave the file untouched, so the verdict can flip with no observable file
	// change. The TTL is what makes the dashboard notice.
	displayCacheTTL = 60 * time.Second

	// displayNow is the clock, as a var so a test can expire the TTL without
	// sleeping.
	displayNow = time.Now
)

// bindingDistrustedCached is bindingDistrusted through that cache. It exists for
// ONE caller shape — the dashboard's read-only loops, which resolve every
// indexed project on every request and would otherwise start a git process per
// project per page load. No spawn path may use it.
func bindingDistrustedCached(path string) string {
	abs, ok := resolveProbePath(path)
	if !ok {
		return bindingDistrusted(path)
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return bindingDistrusted(path) // nothing stable to key on
	}
	now := displayNow()

	displayCacheMu.Lock()
	e, hit := displayCache[abs]
	fresh := hit && e.size == info.Size() && e.modTime.Equal(info.ModTime()) &&
		now.Sub(e.at) < displayCacheTTL
	displayCacheMu.Unlock()
	if fresh {
		return distrustReason(e.verdict)
	}

	v := probeGitTracked(path)
	displayCacheMu.Lock()
	displayCache[abs] = displayEntry{verdict: v, size: info.Size(), modTime: info.ModTime(), at: now}
	displayCacheMu.Unlock()
	return distrustReason(v)
}

// BindingForDisplay answers exactly what Binding answers, through the verdict
// cache above. Its only callers are internal/api/accounts.go's two read-only
// loops (bindingsByAccount and bindingRow).
//
// It re-reads and re-parses the file rather than delegating to Binding, because
// delegating would probe a second time and defeat the cache it exists for. The
// parse below is Binding's, and TestBindingForDisplayAgreesWithBinding pins the
// two answers together so the duplication cannot drift.
func BindingForDisplay(projectPath string) string {
	path := bindingPath(projectPath)
	_, root, _, err := readSettings(path)
	if err != nil {
		return ""
	}
	ns, _ := root[bindingNamespace].(map[string]any)
	key, _ := ns[bindingField].(string)
	key = strings.TrimSpace(key)
	if !ValidKey(key) {
		return ""
	}
	if why := bindingDistrustedCached(path); why != "" {
		logDistrusted(path, why)
		return ""
	}
	return key
}
