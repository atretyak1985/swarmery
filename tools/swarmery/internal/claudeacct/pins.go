package claudeacct

// The ONLY downward walk in the program.
//
// An estate root's descendants can carry their own `swarmery.claudeAccount`
// pin, and each such pin SHADOWS the account written at the root. Anything that
// needs to enumerate them — `swarmery account use` reporting what a write will
// not reach, a switch that clears redundant pins, a doctor that lists stale
// duplicates — calls ShadowingPins (or ScanPins / ScanPinsDetail, the same walk
// reporting what it skipped). Anything that needs the SETTINGS FILES under a
// root — the doctor's settings-block detector, the prune that removes what it
// reports — calls ScanSettingsFiles, which is the same walk again. Nothing else
// may re-implement the walk or pick its own bounds: three walks with three
// depth limits would disagree the first time a file sits at the edge of one of
// them.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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

// walkBounded is the one bounded visitor behind every downward scan: it calls
// visit for root (depth 0) and for every descendant directory down to
// PinScanMaxDepth, never entering a PinScanSkipDirs name and never following a
// symlinked directory (a DirEntry for a link is not IsDir), so a link cycle
// cannot make it unbounded. An unreadable directory is skipped, never fatal.
func walkBounded(dir string, depth int, visit func(dir string, depth int)) {
	walkBoundedPruning(dir, depth, nil, visit)
}

// walkBoundedPruning is walkBounded that also never enters a directory in
// prune (cleaned absolute paths) — a subtree a caller already walked.
func walkBoundedPruning(dir string, depth int, prune map[string]bool, visit func(dir string, depth int)) {
	visit(dir, depth)
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
		child := filepath.Join(dir, e.Name())
		if prune[child] {
			continue
		}
		walkBoundedPruning(child, depth+1, prune, visit)
	}
}

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
// sorted, the reason — it names the path, never the contents — for every
// descendant whose binding file exists but that the read side ignores: its
// mode, owner or type (untrustedSettings), or its provenance (Lock 1: a pin in
// a file git tracks, or whose status git cannot tell). Such a file may well
// hold a pin, but nothing reads it, so it is neither listed as one nor ever
// cleared (the writer refuses it) — a caller that clears pins reports these
// instead of passing over them in silence.
func ScanPins(root string) (pins, untrusted []string) {
	for _, e := range ScanPinsDetail(root) {
		if e.Ignored != "" {
			untrusted = append(untrusted, e.Ignored)
		} else {
			pins = append(pins, e.Dir)
		}
	}
	sort.Strings(pins)
	sort.Strings(untrusted)
	return pins, untrusted
}

// PinEntry is one descendant binding file ScanPinsDetail found.
type PinEntry struct {
	Dir string // the directory whose .claude/settings.local.json it is
	// Key is the account key the file declares — set for an honoured pin and
	// for a pin Lock 1 ignores, "" when the file could not be read at all.
	Key string
	// Ignored is "" for a pin in effect; otherwise why the read side ignores
	// the file, opening with its path. Never the file's contents.
	Ignored string
}

// ScanPinsDetail is ScanPins with each entry's declared key kept, sorted by
// directory: the pins in effect and every binding file the read side ignores,
// in one list. It is the same walk, and the only one.
func ScanPinsDetail(root string) []PinEntry {
	return scanPinsDetail(root, nil, bindingDistrusted)
}

// ScanPinsDetailForDisplay is ScanPinsDetail for the dashboard's read-only
// loops: Lock 1 is answered through the display verdict cache
// (bindingDistrustedCached), and the directories in prune — subtrees the
// caller already scanned — are not entered again. Same walk, same bounds. No
// spawn path may use it.
func ScanPinsDetailForDisplay(root string, prune ...string) []PinEntry {
	skip := make(map[string]bool, len(prune))
	for _, p := range prune {
		skip[cleanAbs(p)] = true
	}
	return scanPinsDetail(root, skip, bindingDistrustedCached)
}

func scanPinsDetail(root string, prune map[string]bool, distrust func(string) string) []PinEntry {
	var out []PinEntry
	walkBoundedPruning(cleanAbs(root), 0, prune, func(dir string, depth int) {
		if depth == 0 {
			return
		}
		if e, ok := pinAt(dir, distrust); ok {
			out = append(out, e)
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// pinAt classifies dir's binding file for the downward scan: a pin in effect,
// a pin Lock 1 ignores, a file the trusted loader refuses, or nothing (ok
// false) — no file, or a trusted file declaring no valid claudeAccount.
func pinAt(dir string, distrust func(string) string) (PinEntry, bool) {
	path := bindingPath(dir)
	root := readTrustedSettings(path)
	if root == nil {
		if why := untrustedSettings(path); why != "" {
			return PinEntry{Dir: dir, Ignored: why}, true
		}
		return PinEntry{}, false
	}
	ns, _ := root[bindingNamespace].(map[string]any)
	key := validField(ns, bindingField)
	if key == "" {
		return PinEntry{}, false
	}
	if why := distrust(path); why != "" {
		logDistrusted(path, why)
		return PinEntry{Dir: dir, Key: key, Ignored: fmt.Sprintf("%s is ignored — %s", path, why)}, true
	}
	return PinEntry{Dir: dir, Key: key}, true
}

// SkippedPinHint is the follow-up advice for one entry of ScanPins' skipped
// list, by cause: a pin Lock 1 ignores already carries its own remedy (untrack
// it, or ask git why it cannot tell), so the mode advice would be wrong there.
func SkippedPinHint(reason string) string {
	if strings.Contains(reason, "tracked by git") || strings.Contains(reason, "git cannot classify") {
		return "the provenance gate ignores it — apply the remedy above and re-run"
	}
	return "fix it (chmod go-w, or replace a file you do not own) and re-run"
}

// ScanSettingsFiles returns, sorted, absolute and cleaned, every
// .claude/settings.json and .claude/settings.local.json at root and below —
// found by the same bounded walk as ScanPins (PinScanMaxDepth,
// PinScanSkipDirs, symlinked directories not followed). Presence only (Lstat):
// what a caller may read from each file is the caller's trusted loader's call.
// The doctor's settings-block detector and the prune both enumerate through
// this, so the two always see the same files.
func ScanSettingsFiles(root string) []string {
	if root == "" {
		return nil
	}
	var out []string
	walkBounded(cleanAbs(root), 0, func(dir string, _ int) {
		for _, name := range []string{ProjectSettingsFile, BindingFile} {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if _, err := os.Lstat(p); err == nil {
				out = append(out, p)
			}
		}
	})
	sort.Strings(out)
	return out
}
