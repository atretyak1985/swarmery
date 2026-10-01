package accountdoctor

// Arm (d) — plugin install and version parity between the accounts, computed
// per (id, scope) and never per id alone: one account can carry a pack at
// 0.5.1 in its user scope and 0.5.0 in four local scopes, which a per-id
// comparison would call "the same". Two kinds of difference, labelled apart:
//
//	version       the (id, scope) is installed under both accounts, and the
//	              version sets differ (or one account is split within a scope)
//	missing       the (id, scope) is installed under some accounts only
//	orphan-cache  a checkout under <configDir>/plugins/cache that no install
//	              record of that account references — NOT an install
//
// Keeping orphan-cache apart matters: an unreferenced checkout of a newer
// version under one account is not version drift, and calling it one is a
// false alarm.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// ParityEntry is one parity difference.
type ParityEntry struct {
	ID    string `json:"id"`
	Scope string `json:"scope"` // user | project | local | … ; "cache" for orphan-cache
	Kind  string `json:"kind"`  // version | missing | orphan-cache
	// Versions maps each account to the sorted versions it has at this
	// (id, scope); an account without it is absent from the map.
	Versions map[string][]string `json:"versions"`
}

// AccountPlugins is one account's plugins directory.
type AccountPlugins struct {
	Account   string
	ConfigDir string
}

// parity runs arm (d) over every account on this machine.
func (r *run) parity() {
	var accts []AccountPlugins
	for _, a := range claudeacct.DiscoverWithDefault() {
		accts = append(accts, AccountPlugins{Account: a.Key, ConfigDir: a.ConfigDir})
	}
	r.rep.Parity = Parity(accts)
}

// parityRecord is the part of an install record parity reads.
type parityRecord struct {
	Scope       string `json:"scope"`
	Version     string `json:"version"`
	InstallPath string `json:"installPath"`
}

// Parity compares the accounts' installed_plugins.json. Sorted by id, scope,
// kind; never nil. With fewer than two accounts only orphan-cache can appear.
func Parity(accts []AccountPlugins) []ParityEntry {
	type key struct{ id, scope string }
	versions := map[key]map[string]map[string]bool{} // (id,scope) -> account -> version set
	out := []ParityEntry{}
	for _, a := range accts {
		var doc struct {
			Plugins map[string][]parityRecord `json:"plugins"`
		}
		referenced := map[string]bool{}
		if readJSON(filepath.Join(a.ConfigDir, "plugins", "installed_plugins.json"), &doc) {
			for id, recs := range doc.Plugins {
				for _, rec := range recs {
					k := key{id, rec.Scope}
					if versions[k] == nil {
						versions[k] = map[string]map[string]bool{}
					}
					if versions[k][a.Account] == nil {
						versions[k][a.Account] = map[string]bool{}
					}
					versions[k][a.Account][rec.Version] = true
					if rec.InstallPath != "" {
						referenced[filepath.Clean(rec.InstallPath)] = true
					}
				}
			}
		}
		out = append(out, orphanCheckouts(a, referenced)...)
	}
	if len(accts) > 1 {
		for k, byAcct := range versions {
			entry := ParityEntry{ID: k.id, Scope: k.scope, Versions: map[string][]string{}}
			var sig string
			same := true
			for acct, set := range byAcct {
				vs := sortedSet(set)
				entry.Versions[acct] = vs
				s := strings.Join(vs, ",")
				if sig == "" {
					sig = s
				} else if s != sig {
					same = false
				}
				if len(vs) > 1 {
					same = false // split within one account's scope
				}
			}
			switch {
			case len(byAcct) < len(accts):
				entry.Kind = "missing"
			case !same:
				entry.Kind = "version"
			default:
				continue
			}
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// orphanCheckouts lists <configDir>/plugins/cache/<marketplace>/<plugin>/<version>
// directories no install record of the account references. A fixed three-level
// listing, not a walk.
func orphanCheckouts(a AccountPlugins, referenced map[string]bool) []ParityEntry {
	var out []ParityEntry
	cache := filepath.Join(a.ConfigDir, "plugins", "cache")
	for _, mk := range subdirs(cache) {
		for _, plugin := range subdirs(filepath.Join(cache, mk)) {
			var orphans []string
			for _, ver := range subdirs(filepath.Join(cache, mk, plugin)) {
				if !referenced[filepath.Join(cache, mk, plugin, ver)] {
					orphans = append(orphans, ver)
				}
			}
			if len(orphans) > 0 {
				out = append(out, ParityEntry{ID: plugin + "@" + mk, Scope: "cache", Kind: "orphan-cache",
					Versions: map[string][]string{a.Account: orphans}})
			}
		}
	}
	return out
}

// subdirs is the sorted names of dir's subdirectories (not following links).
func subdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
