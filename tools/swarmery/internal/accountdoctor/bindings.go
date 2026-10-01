package accountdoctor

// DiscoverBindings — every binding file the dashboard should know about, with
// no `projects` row required. It contains NO directory walk of its own: the
// upward half is claudeacct.Resolve(seed) (the bounded ladder), the downward
// half is claudeacct.ScanPinsDetail — the detailed form of ScanPins, the ONE
// downward walk, with its one depth bound (PinScanMaxDepth) and one skip list
// (PinScanSkipDirs, whose ".swarmery" entry keeps a daemon worktree's lent
// binding copy out: that copy is indistinguishable from a real binding by
// content and is excluded by path alone). What is added here is set logic.

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// Binding is one on-disk binding: the project directory, the account key its
// file declares, and — when the read side ignores the file (Lock 1: tracked or
// indeterminate; or its mode, owner or type) — Ignored with the reason. An
// ignored binding is counted under no account; it is listed, never dropped.
type Binding struct {
	Path    string // the directory holding .claude/settings.local.json
	Key     string // the declared account key ("" when unreadable or estate-only)
	Ignored bool
	Reason  string
}

// DiscoverBindings returns the bindings reachable from seeds, keyed by cleaned
// absolute path, sorted. For each seed: the seed's own binding, the rungs
// Resolve found for its account and estate, and — below its estate root —
// every pin ScanPins' walk finds, ignored ones included. scanRoots are walked
// downward directly (the same walk) whether or not an estate declares them:
// the daemon's configured project roots, for a binding with no row and no
// estate above it.
func DiscoverBindings(seeds []string, scanRoots ...string) []Binding {
	byPath := map[string]Binding{}
	put := func(b Binding) {
		b.Path = filepath.Clean(b.Path)
		if old, ok := byPath[b.Path]; ok && !old.Ignored && old.Key != "" {
			return // an honoured reading wins
		}
		byPath[b.Path] = b
	}
	// own classifies one directory's binding file, once per directory. It is
	// the dashboard's path, so Lock 1 is answered through the display verdict
	// cache (BindingForDisplayWithReason) — never a fresh git probe per call.
	visited := map[string]bool{}
	own := func(dir string) {
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		if visited[dir] {
			return
		}
		visited[dir] = true
		if !claudeacct.BindingFileExists(dir) {
			return
		}
		file := filepath.Join(dir, filepath.FromSlash(claudeacct.BindingFile))
		root, reason := claudeacct.ReadTrustedSettings(file)
		if reason != "" { // its mode, owner or type: nothing reads it (no git needed)
			put(Binding{Path: dir, Ignored: true, Reason: claudeacct.BindingFileUntrusted(dir)})
			return
		}
		ns, ok := root["swarmery"].(map[string]any)
		if !ok {
			return // a settings file that binds nothing
		}
		key, ignored := claudeacct.BindingForDisplayWithReason(dir)
		switch {
		case key != "":
			put(Binding{Path: dir, Key: key})
		case ignored != "": // declared, and Lock 1 ignores it
			put(Binding{Path: dir, Key: declaredKey(dir), Ignored: true, Reason: ignored})
		default:
			if e, _ := ns["estate"].(string); claudeacct.ValidKey(strings.TrimSpace(e)) {
				put(Binding{Path: dir}) // an estate declaration with no account pin
			}
		}
	}
	var scanned []string
	isScanned := func(dir string) bool {
		for _, s := range scanned {
			if s == dir {
				return true
			}
		}
		return false
	}
	scan := func(root string, prune []string) {
		for _, e := range claudeacct.ScanPinsDetailForDisplay(root, prune...) {
			if !visited[e.Dir] {
				visited[e.Dir] = true
				put(Binding{Path: e.Dir, Key: e.Key, Ignored: e.Ignored != "", Reason: e.Ignored})
			}
		}
	}
	for _, seed := range seeds {
		if strings.TrimSpace(seed) == "" {
			continue
		}
		seed = filepath.Clean(seed)
		res := claudeacct.ResolveForDisplay(seed)
		own(seed)
		own(res.AccountRoot)
		own(res.EstateRoot)
		for _, ig := range res.IgnoredRungs() {
			dir := filepath.Dir(filepath.Dir(ig.Path))
			if !visited[dir] {
				visited[dir] = true
				put(Binding{Path: dir, Key: declaredKey(dir), Ignored: true, Reason: ig.Reason})
			}
		}
		if res.EstateRoot != "" && !isScanned(res.EstateRoot) {
			scanned = append(scanned, res.EstateRoot)
			scan(res.EstateRoot, nil)
		}
	}
	// The onboard roots are walked with every estate subtree already scanned
	// pruned out: each directory is visited by one walk, not two.
	estates := append([]string(nil), scanned...)
	for _, root := range scanRoots {
		if root = strings.TrimSpace(root); root == "" || isScanned(filepath.Clean(root)) {
			continue
		}
		root = filepath.Clean(root)
		scanned = append(scanned, root)
		own(root)
		scan(root, estates)
	}
	out := make([]Binding, 0, len(byPath))
	for _, b := range byPath {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// declaredKey is the account key a binding file NAMES even though the read
// side ignores it — for display ("declares") only, read from a file the
// trusted loader accepts; "" otherwise. Never used to decide anything.
func declaredKey(dir string) string {
	root, _ := claudeacct.ReadTrustedSettings(filepath.Join(dir, filepath.FromSlash(claudeacct.BindingFile)))
	ns, _ := root["swarmery"].(map[string]any)
	key, _ := ns["claudeAccount"].(string)
	key = strings.TrimSpace(key)
	if !claudeacct.ValidKey(key) {
		return ""
	}
	return key
}
