package claudeacct

// The ONLY downward pin walk in the program.
//
// An estate root's descendants can carry their own `swarmery.claudeAccount`
// pin, and each such pin SHADOWS the account written at the root. Anything that
// needs to enumerate them — `swarmery account use` reporting what a write will
// not reach, a switch that clears redundant pins, a doctor that lists stale
// duplicates — calls ShadowingPins (or ScanPins, the same walk reporting what it
// skipped). Nothing else may re-implement the walk or
// pick its own bounds: three walks with three depth limits would disagree the
// first time a binding sits at the edge of one of them.

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// PinScanMaxDepth is how many directory levels below the root the walk looks.
// A pin at exactly this depth is found; one a level deeper is not.
const PinScanMaxDepth = 6

// PinScanSkipDirs are never entered, matched on the directory BASE NAME at every
// level. ".swarmery" is load-bearing: it keeps <home>/.swarmery/worktrees — and
// the binding-file copy a daemon worktree carries — out of every caller's
// result, so a worktree is never reported as a pinned project. The rest are
// size bounds: trees that never hold a project binding and can be enormous.
var PinScanSkipDirs = []string{".git", ".swarmery", "dist", "node_modules", "vendor"}

// ShadowingPins returns, sorted, the absolute descendant directories of root
// (root itself excluded) whose binding file carries a valid
// swarmery.claudeAccount. Paths only: a caller that needs the pinned key reads
// it with Binding(dir). A malformed descendant file is skipped, never fatal; an
// unreadable directory is skipped likewise. Symlinked directories are not
// followed, so a link cycle cannot make the walk unbounded. Pure filesystem.
func ShadowingPins(root string) []string {
	pins, _ := ScanPins(root)
	return pins
}

// ScanPins is ShadowingPins plus what that walk had to SKIP: untrusted holds,
// sorted, the reason (untrustedSettings — it names the path, never the
// contents) for every descendant whose binding file exists but that the read
// side ignores. Such a file may well hold a pin, but nothing reads it, so it is
// neither listed as one nor ever cleared (the writer refuses it) — a caller
// that clears pins reports these instead of passing over them in silence.
func ScanPins(root string) (pins, untrusted []string) {
	root = cleanAbs(root)
	walkPins(root, 0, &pins, &untrusted)
	sort.Strings(pins)
	sort.Strings(untrusted)
	return pins, untrusted
}

func walkPins(dir string, depth int, out, untrusted *[]string) {
	if depth > 0 {
		if Binding(dir) != "" {
			*out = append(*out, dir)
		} else if why := untrustedSettings(bindingPath(dir)); why != "" {
			*untrusted = append(*untrusted, why)
		}
	}
	if depth >= PinScanMaxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || slices.Contains(PinScanSkipDirs, e.Name()) {
			continue
		}
		walkPins(filepath.Join(dir, e.Name()), depth+1, out, untrusted)
	}
}
