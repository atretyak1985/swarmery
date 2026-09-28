// Package claudebin resolves the Claude Code executable for daemon-launched
// subprocesses. launchd starts the daemon with a minimal PATH
// (/usr/bin:/bin:/usr/sbin:/sbin) that omits the npm/homebrew/local install
// dirs, so a bare exec.LookPath("claude") fails under the service even though
// `claude` is on the operator's interactive PATH.
//
// This is the single home of a resolver the repo previously carried twice
// verbatim (planning.ClaudeBin, api.claudeBin); both now delegate here, and
// mcpcfg's `claude mcp …` shell-out uses it. Resolution is driven entirely by
// the environment and the filesystem, so callers stay testable without a real
// binary installed.
package claudebin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotFound reports that no claude executable could be located by any of the
// three strategies. Callers that surface it to a user should mention
// SWARMERY_CLAUDE_BIN — that is the escape hatch.
var ErrNotFound = errors.New("claude not found in PATH or common install locations")

// systemProbeDirs are the machine-wide install dirs probed after PATH, in
// order. A package var rather than an inline literal purely so tests can point
// it at a temp tree and stay hermetic on hosts that genuinely have a claude
// installed in one of them; production always uses these defaults.
var systemProbeDirs = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
}

// ShimDir is the directory the accounts-pack PATH shim is installed into:
// $SWARMERY_BIN_DIR when set, else ~/.swarmery/bin. Everything that resolves
// the REAL claude must skip it — a `claude` found there is the shim, which
// re-enters `swarmery account exec` and would discard an account the Go code
// already resolved (risk R5). "" only when neither the override nor a home
// directory is available.
func ShimDir() string {
	if v := strings.TrimSpace(os.Getenv("SWARMERY_BIN_DIR")); v != "" {
		return filepath.Clean(v)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".swarmery", "bin")
}

// UnderShimDir reports whether path lives inside ShimDir, judged both as
// given and with symlinks resolved (on the path and on the dir). A symlink
// placed IN the shim dir that points at a real binary elsewhere is still under
// it: anything reached through the shim dir is skipped.
func UnderShimDir(path string) bool {
	dir := ShimDir()
	if dir == "" || path == "" {
		return false
	}
	dirs := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r != dir {
		dirs = append(dirs, r)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	paths := []string{abs}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		paths = append(paths, r)
	}
	// The containing directory resolved on its own: catches a symlink inside
	// the shim dir whose target lies elsewhere, when the dir itself is reached
	// through another symlink.
	if r, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		paths = append(paths, filepath.Join(r, filepath.Base(abs)))
	}
	for _, p := range paths {
		for _, d := range dirs {
			if within(d, p) {
				return true
			}
		}
	}
	return false
}

// within reports whether p is dir itself or a descendant of it.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// IsShim reports whether path is the accounts-pack shim: a file named `claude`
// (as given, or once symlinks are resolved) under ShimDir. Only that name is
// the shim — every other binary in the dir (the swarmery CLI itself lives
// there) is an ordinary executable and must stay resolvable.
func IsShim(path string) bool {
	if !UnderShimDir(path) {
		return false
	}
	if filepath.Base(path) == "claude" {
		return true
	}
	r, err := filepath.EvalSymlinks(path)
	return err == nil && filepath.Base(r) == "claude"
}

// LookPathSkippingShim is exec.LookPath(name) over PATH with the shim (see
// IsShim) skipped, so the next real hit wins instead of it. A name that
// contains a separator is checked verbatim and rejected only when it IS the
// shim. The error mirrors exec.LookPath's (*exec.Error wrapping
// exec.ErrNotFound).
func LookPathSkippingShim(name string) (string, error) {
	notFound := &exec.Error{Name: name, Err: exec.ErrNotFound}
	if strings.ContainsRune(name, filepath.Separator) {
		p, err := exec.LookPath(name)
		if err != nil {
			return "", err
		}
		if IsShim(p) {
			return "", notFound
		}
		return p, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		// exec.LookPath refuses a relative-PATH hit (ErrDot); an empty entry
		// means "." — skip both rather than execute from the cwd.
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		c := filepath.Join(dir, name)
		if isExecutable(c) && !IsShim(c) {
			return c, nil
		}
	}
	return "", notFound
}

// isExecutable accepts only a non-directory with an executable bit set.
func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// Resolve returns a path to the claude executable.
// Order: SWARMERY_CLAUDE_BIN override → PATH lookup → probe the common install
// dirs (system dirs first, then the home-relative ones), accepting only a
// non-directory with an executable bit set. A PATH hit or probe candidate under
// ShimDir is skipped and resolution continues with the next one (risk R5); the
// explicit SWARMERY_CLAUDE_BIN override is NOT filtered — it is the escape
// hatch. Returns ErrNotFound when every strategy misses.
func Resolve() (string, error) {
	if v := strings.TrimSpace(os.Getenv("SWARMERY_CLAUDE_BIN")); v != "" {
		return v, nil
	}
	if p, err := LookPathSkippingShim("claude"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := make([]string, 0, len(systemProbeDirs)+4)
	for _, dir := range systemProbeDirs {
		candidates = append(candidates, filepath.Join(dir, "claude"))
	}
	candidates = append(candidates,
		filepath.Join(home, ".claude", "local", "claude"),
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, ".npm-global", "bin", "claude"),
		filepath.Join(home, "bin", "claude"),
	)
	for _, c := range candidates {
		if isExecutable(c) && !UnderShimDir(c) {
			return c, nil
		}
	}
	return "", ErrNotFound
}
