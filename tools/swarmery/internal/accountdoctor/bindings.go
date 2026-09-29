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
	own := func(dir string) {
		if dir == "" || !claudeacct.BindingFileExists(dir) {
			return
		}
		file := filepath.Join(dir, filepath.FromSlash(claudeacct.BindingFile))
		root, reason := claudeacct.ReadTrustedSettings(file)
		if reason != "" { // its mode, owner or type: nothing reads it
			put(Binding{Path: dir, Ignored: true, Reason: claudeacct.BindingFileUntrusted(dir)})
			return
		}
		if _, ok := root["swarmery"].(map[string]any); !ok {
			return // a settings file that binds nothing
		}
		if key := claudeacct.Binding(dir); key != "" {
			put(Binding{Path: dir, Key: key})
			return
		}
		if key := declaredKey(dir); key != "" { // declared, and Lock 1 ignores it
			put(Binding{Path: dir, Key: key, Ignored: true, Reason: claudeacct.BindingFileUntrusted(dir)})
			return
		}
		if key, _ := claudeacct.Estate(dir); key != "" {
			put(Binding{Path: dir}) // an estate declaration with no account pin
		}
	}
	scan := func(root string) {
		for _, e := range claudeacct.ScanPinsDetail(root) {
			put(Binding{Path: e.Dir, Key: e.Key, Ignored: e.Ignored != "", Reason: e.Ignored})
		}
	}
	scanned := map[string]bool{}
	for _, seed := range seeds {
		if strings.TrimSpace(seed) == "" {
			continue
		}
		seed = filepath.Clean(seed)
		res := claudeacct.Resolve(seed)
		own(seed)
		own(res.AccountRoot)
		own(res.EstateRoot)
		for _, ig := range res.IgnoredRungs() {
			dir := filepath.Dir(filepath.Dir(ig.Path))
			put(Binding{Path: dir, Key: declaredKey(dir), Ignored: true, Reason: ig.Reason})
		}
		if res.EstateRoot != "" && !scanned[res.EstateRoot] {
			scanned[res.EstateRoot] = true
			scan(res.EstateRoot)
		}
	}
	for _, root := range scanRoots {
		if root = strings.TrimSpace(root); root == "" || scanned[filepath.Clean(root)] {
			continue
		}
		scanned[filepath.Clean(root)] = true
		own(root)
		scan(root)
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
