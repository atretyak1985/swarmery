package accountdoctor

// Arm (h) — staleDuplicates: every SECOND LIVE COPY this arm can see of a
// credential store, of an estate settings block, or of a binding. A later
// phase gates the plan's one irreversible deletion on this list being empty,
// so it always exists, is always an array, and marshals as [] — never null —
// when nothing is found. Three detectors, all NAME / path only:
//
//  1. credential-store — every <key>.env in claudeacct.SecretsDir() (ONE flat
//     listing of one directory, not a walk), loaded through
//     claudeacct.SecretEnvForStore so the 0700/0600 gate still applies; only
//     the text before '=' is kept. One entry per PAIR of stores whose name
//     sets intersect. A store the gate refuses loads nothing and is a warn
//     finding, never a duplicate. A store moved out of SecretsDir() (a
//     quarantine) is out of scope by construction.
//  2. settings-block — exactly what `swarmery account prune` would remove
//     under the estate root: accountprune.Redundancies, the prune's own
//     per-file verdict (runsettings.EstateKeys only, the SUBSET rule, each
//     file judged against the estate it resolves to). So every exclusion the
//     prune applies holds here too: an estate's own two files, a nested
//     estate and everything below it, a symlinked path, and an unadmitted
//     estate — which delivers nothing, so its sub-repos' copies are the only
//     live ones — are never reported.
//  3. binding — the `swarmery` object internal/worktree/configsync.go lent
//     into a daemon worktree: a fixed two-level glob under the worktree root.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/accountprune"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeproj"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// Duplicate kinds.
const (
	KindCredentialStore = "credential-store"
	KindSettingsBlock   = "settings-block"
	KindBinding         = "binding"
)

// StaleDuplicates is arm (h) for one resolution: sorted, never nil.
func StaleDuplicates(res claudeacct.Resolution) []Duplicate {
	dups, _ := staleDuplicates(res)
	return dups
}

func (r *run) staleDuplicates() {
	dups, findings := staleDuplicates(r.res)
	r.rep.StaleDuplicates = dups
	for _, f := range findings {
		r.add(f)
	}
}

// staleDuplicates runs the three detectors; findings are the refused stores.
func staleDuplicates(res claudeacct.Resolution) ([]Duplicate, []Finding) {
	out := []Duplicate{}
	var findings []Finding

	stores := listStores()
	for _, s := range stores {
		if s.state == claudeacct.StoreRefused {
			findings = append(findings, Finding{ID: "store-refused", Severity: SevWarn,
				Title:  "a credential store is refused by the loader and supplies nothing",
				Detail: s.path + ": " + s.reason, File: s.path})
		}
	}
	for i := 0; i < len(stores); i++ {
		for j := i + 1; j < len(stores); j++ {
			if overlap := intersect(stores[i].names, stores[j].names); len(overlap) > 0 {
				out = append(out, Duplicate{Kind: KindCredentialStore, Paths: []string{stores[i].path, stores[j].path},
					Key: "", Overlap: overlap, Count: len(overlap)})
			}
		}
	}

	out = append(out, settingsBlocks(res)...)
	out = append(out, lentBindings(res)...)

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return strings.Join(a.Paths, "\x00") < strings.Join(b.Paths, "\x00")
	})
	return out, findings
}

// storeInfo is one store file in SecretsDir(): its path, the loader's verdict,
// and — when present — its variable NAMES (sorted). No value is kept.
type storeInfo struct {
	key    string
	path   string
	state  claudeacct.StoreState
	reason string
	names  []string
}

// listStores is the ONE flat listing of SecretsDir().
func listStores() []storeInfo {
	dir := claudeacct.SecretsDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []storeInfo
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".env")
		if !ok || !claudeacct.ValidKey(key) {
			continue
		}
		path := claudeacct.SecretsPath(key)
		state, reason := claudeacct.StoreStatus(path)
		s := storeInfo{key: key, path: path, state: state, reason: reason}
		if state == claudeacct.StorePresent {
			s.names = namesOf(claudeacct.SecretEnvForStore(key))
		}
		out = append(out, s)
	}
	return out
}

// namesOf keeps the text before the first '=' of each pair and drops the rest
// at once. Sorted, de-duplicated.
func namesOf(pairs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, kv := range pairs {
		name, _, _ := strings.Cut(kv, "=")
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// intersect is the sorted names in both a and b (both sorted).
func intersect(a, b []string) []string {
	in := make(map[string]bool, len(a))
	for _, n := range a {
		in[n] = true
	}
	var out []string
	for _, n := range b {
		if in[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// settingsBlocks is detector 2: accountprune.Redundancies, one Duplicate per
// key it would remove. The prune's own verdict, never a second copy of its
// rules — a duplicate this reports is one `account prune` clears.
func settingsBlocks(res claudeacct.Resolution) []Duplicate {
	if res.EstateRoot == "" {
		return nil
	}
	var out []Duplicate
	for _, r := range accountprune.Redundancies(res.EstateRoot) {
		out = append(out, Duplicate{Kind: KindSettingsBlock, Paths: []string{r.EstateFile, r.Path},
			Key: r.Key, Overlap: r.Entries, Count: len(r.Entries)})
	}
	return out
}

// entryNames is the sorted top-level names of a JSON object value.
func entryNames(v any) []string {
	m, _ := v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// lentBindings is detector 3: <home>/<worktree.DefaultRoot>/*/*/.claude/
// settings.local.json carrying a `swarmery` object. A fixed-depth glob, never a
// walk. The source project's binding file joins Paths when the worktree's slug
// directory equals claudeproj.Slug of a binding this resolution knows about
// (encoded forward, never decoded).
func lentBindings(res claudeacct.Resolution) []Duplicate {
	home, err := userHomeDir()
	if err != nil || home == "" {
		return nil
	}
	pattern := filepath.Join(home, filepath.FromSlash(worktree.DefaultRoot), "*", "*",
		filepath.FromSlash(claudeacct.BindingFile))
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		return nil
	}
	sources := map[string]string{} // slug -> binding file
	for _, dir := range knownBindingDirs(res) {
		if claudeacct.BindingFileExists(dir) {
			sources[claudeproj.Slug(dir)] = filepath.Join(dir, filepath.FromSlash(claudeacct.BindingFile))
		}
	}
	var out []Duplicate
	for _, m := range matches {
		var doc map[string]any
		if !readJSON(m, &doc) {
			continue
		}
		ns, ok := doc["swarmery"].(map[string]any)
		if !ok {
			continue
		}
		fields := entryNames(ns)
		paths := []string{m}
		slug := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(m))))
		if src, ok := sources[slug]; ok {
			paths = append(paths, src)
		}
		out = append(out, Duplicate{Kind: KindBinding, Paths: paths, Key: "swarmery", Overlap: fields, Count: len(fields)})
	}
	return out
}

// knownBindingDirs is every directory this resolution can name as holding a
// binding: the rungs that decided the account and the estate, and every pin
// the shared downward walk finds under the estate root.
func knownBindingDirs(res claudeacct.Resolution) []string {
	var dirs []string
	for _, d := range []string{res.AccountRoot, res.EstateRoot} {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	if res.EstateRoot != "" {
		for _, e := range claudeacct.ScanPinsDetail(res.EstateRoot) {
			dirs = append(dirs, e.Dir)
		}
	}
	return dirs
}
