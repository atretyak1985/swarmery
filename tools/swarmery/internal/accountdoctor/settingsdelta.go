package accountdoctor

// Arm (c) — the settings delta between the accounts: what one account's
// <configDir>/settings.json carries that another's does not, for the four keys
// that decide what a session loads — enabledPlugins, pluginConfigs,
// extraKnownMarketplaces, and env (by KEY NAME only). Entries present on both
// sides are compared by a SHA-256 prefix of their canonical JSON; no value is
// ever printed or kept.
//
// The healthy live baseline for pluginConfigs is ZERO entries: project config
// travels through the estate's --settings file, and a pluginConfigs block that
// reappears in an account file is exactly the drift this arm exists to catch.

import (
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"sort"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// SettingsDelta is one difference between two accounts' settings.json.
type SettingsDelta struct {
	Key string `json:"key"` // enabledPlugins | pluginConfigs | extraKnownMarketplaces | env
	// Kind is "only-in" (Names exist in OnlyIn's file and not in Other's) or
	// "differs" (Names exist in both with different values).
	Kind   string   `json:"kind"`
	OnlyIn string   `json:"onlyIn"` // the account holding the names; "" for "differs"
	Other  string   `json:"other"`  // the account compared against ("" for "differs")
	// Between is the compared pair, sorted — set for every kind.
	Between []string `json:"between"`
	Names  []string `json:"names"`  // entry / variable NAMES — never values
	Count  int      `json:"count"`
}

// deltaKeys are the keys compared, in report order.
var deltaKeys = []string{"enabledPlugins", "pluginConfigs", "extraKnownMarketplaces", "env"}

// AccountSettings is one account's settings.json, by key.
type AccountSettings struct {
	Account string
	Path    string
}

// settingsDelta runs arm (c) over every account on this machine.
func (r *run) settingsDelta() {
	var accts []AccountSettings
	for _, a := range claudeacct.DiscoverWithDefault() {
		accts = append(accts, AccountSettings{Account: a.Key, Path: filepath.Join(a.ConfigDir, "settings.json")})
	}
	r.rep.SettingsDelta = SettingsDeltas(accts)
}

// SettingsDeltas compares every pair of accounts' settings files, in the
// order given (sorted by account key first). Never nil.
func SettingsDeltas(accts []AccountSettings) []SettingsDelta {
	sort.Slice(accts, func(i, j int) bool { return accts[i].Account < accts[j].Account })
	docs := make([]map[string]map[string][32]byte, len(accts))
	for i, a := range accts {
		docs[i] = hashedKeys(a.Path)
	}
	out := []SettingsDelta{}
	for i := 0; i < len(accts); i++ {
		for j := i + 1; j < len(accts); j++ {
			a, b := accts[i].Account, accts[j].Account
			for _, key := range deltaKeys {
				da, db := docs[i][key], docs[j][key]
				if names := onlyIn(da, db); len(names) > 0 {
					out = append(out, SettingsDelta{Key: key, Kind: "only-in", OnlyIn: a, Other: b, Between: []string{a, b}, Names: names, Count: len(names)})
				}
				if names := onlyIn(db, da); len(names) > 0 {
					out = append(out, SettingsDelta{Key: key, Kind: "only-in", OnlyIn: b, Other: a, Between: []string{a, b}, Names: names, Count: len(names)})
				}
				var differ []string
				for name, ha := range da {
					if hb, ok := db[name]; ok && hb != ha {
						differ = append(differ, name)
					}
				}
				if len(differ) > 0 {
					sort.Strings(differ)
					out = append(out, SettingsDelta{Key: key, Kind: "differs", Between: []string{a, b}, Names: differ, Count: len(differ)})
				}
			}
		}
	}
	return out
}

// hashedKeys reads one settings.json and returns, for each compared key, its
// entry names mapped to the SHA-256 of the entry's canonical JSON. The values
// themselves are dropped as soon as they are hashed.
func hashedKeys(path string) map[string]map[string][32]byte {
	out := map[string]map[string][32]byte{}
	var doc map[string]json.RawMessage
	if !readJSON(path, &doc) {
		return out
	}
	for _, key := range deltaKeys {
		raw, ok := doc[key]
		if !ok {
			continue
		}
		var entries map[string]any
		if json.Unmarshal(raw, &entries) != nil {
			continue
		}
		m := make(map[string][32]byte, len(entries))
		for name, v := range entries {
			canon, err := json.Marshal(v)
			if err != nil {
				continue
			}
			m[name] = sha256.Sum256(canon)
		}
		out[key] = m
	}
	return out
}

// onlyIn is the sorted names in a that b lacks.
func onlyIn(a, b map[string][32]byte) []string {
	var out []string
	for name := range a {
		if _, ok := b[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
