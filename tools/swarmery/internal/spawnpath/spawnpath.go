// Package spawnpath widens the daemon's PATH so the processes it spawns can find
// the operator's tooling.
//
// launchd starts the daemon with PATH=/usr/bin:/bin:/usr/sbin:/sbin. That is
// enough to exec `claude` only because internal/claudebin probes the usual
// install dirs itself — but the CLI then starts every stdio MCP server a project
// declares, and those are `npx …`, `uvx …`, `node …`, `python …` resolved
// through the PATH the CLI inherited. Under the service that PATH has none of
// them, so every MCP server needing node or python tooling fails to start in
// every daemon-spawned run, on every project, while the same run from the
// operator's shell works. Same class of failure as the bare `claude` lookup
// fixed in #340, one layer out.
//
// The fix is applied ONCE, in-process, when `swarmery serve` boots: the
// well-known user and system tool dirs that exist on this machine are prepended
// to the daemon's own PATH. Every child then inherits the widened PATH through
// os.Environ() with no spawn site knowing — including claudebin's PATH lookup,
// the dock terminal's login shell (which re-derives its own PATH anyway), and
// the `claude mcp …` shell-outs.
//
// Deliberately NOT done: running the operator's login shell to read its PATH.
// It is the most faithful source, but it executes rc files with side effects
// (nvm/pyenv shims, `brew shellenv`, prompts that block) inside a background
// daemon on every boot. The probe list below covers what those rc files add in
// practice; SWARMERY_SPAWN_PATH is the escape hatch for anything else.
package spawnpath

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// EnvExtra names dirs to prepend VERBATIM, colon-separated, ahead of every
// probed dir. Baked into the plist like any other knob (`swarmery install`
// preserves hand-baked vars). No existence check: an operator who names a dir
// means it.
const EnvExtra = "SWARMERY_SPAWN_PATH"

// systemDirs are the package-manager bin dirs launchd's PATH omits.
var systemDirs = []string{
	"/opt/homebrew/bin",
	"/opt/homebrew/sbin",
	"/usr/local/bin",
}

// userDirs are the per-user tool dirs, relative to home, in the order a typical
// rc file ends up with them. ~/.claude/local is where `claude migrate-installer`
// puts the CLI (an alias in the shell, nothing on PATH).
func userDirs(home string) []string {
	rel := []string{
		".local/bin",
		".npm-global/bin",
		"bin",
		".bun/bin",
		".cargo/bin",
		".volta/bin",
		".local/share/fnm/aliases/default/bin",
		".claude/local",
	}
	out := make([]string, 0, len(rel))
	for _, r := range rel {
		out = append(out, filepath.Join(home, filepath.FromSlash(r)))
	}
	return out
}

// nvmDir is the bin dir of the NEWEST node nvm has installed under home, or "".
// nvm exposes no "default" symlink on disk (the alias is a text file resolved
// by shell code), so the newest version is the deterministic stand-in.
func nvmDir(home string, glob func(string) []string) string {
	matches := glob(filepath.Join(home, ".nvm", "versions", "node", "v*"))
	if len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		return semverLess(filepath.Base(matches[i]), filepath.Base(matches[j]))
	})
	return filepath.Join(matches[len(matches)-1], "bin")
}

// semverLess orders "vMAJOR.MINOR.PATCH" numerically; anything unparsable sorts
// first so it can never be picked over a real version.
func semverLess(a, b string) bool {
	pa, oka := parseV(a)
	pb, okb := parseV(b)
	if oka != okb {
		return !oka
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return a < b
}

func parseV(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Augment returns path with the missing tool dirs prepended: extra (verbatim)
// first, then the user dirs under home that exist, then nvm's newest node, then
// the system dirs that exist. A dir already on path is never duplicated; the
// existing entries keep their order behind the additions. Pure: existence and
// globbing come in as functions so the policy is testable without a filesystem.
func Augment(path, home, extra string, isDir func(string) bool, glob func(string) []string) string {
	existing := strings.Split(path, string(os.PathListSeparator))
	seen := make(map[string]bool, len(existing))
	for _, p := range existing {
		if p != "" {
			seen[p] = true
		}
	}
	var add []string
	push := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		add = append(add, dir)
	}
	for _, dir := range strings.Split(extra, string(os.PathListSeparator)) {
		push(strings.TrimSpace(dir))
	}
	if home != "" {
		for _, dir := range userDirs(home) {
			if isDir(dir) {
				push(dir)
			}
		}
		if dir := nvmDir(home, glob); dir != "" && isDir(dir) {
			push(dir)
		}
	}
	for _, dir := range systemDirs {
		if isDir(dir) {
			push(dir)
		}
	}
	if len(add) == 0 {
		return path
	}
	if path == "" {
		return strings.Join(add, string(os.PathListSeparator))
	}
	return strings.Join(add, string(os.PathListSeparator)) + string(os.PathListSeparator) + path
}

// Apply widens this process's PATH in place and reports both values. Called
// once from `swarmery serve`; a no-op (and silent) when nothing is missing,
// which is the case in an operator's interactive shell.
func Apply() (before, after string) {
	before = os.Getenv("PATH")
	home, _ := os.UserHomeDir()
	after = Augment(before, home, os.Getenv(EnvExtra), isDir, globDirs)
	if after != before {
		if err := os.Setenv("PATH", after); err != nil {
			log.Printf("spawnpath: cannot widen PATH: %v", err)
			return before, before
		}
		log.Printf("spawnpath: PATH widened for spawned tooling (npx/uvx/node/python): %s", after)
	}
	return before, after
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func globDirs(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	return matches
}
