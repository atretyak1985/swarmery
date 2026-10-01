package accountdoctor

import (
	"path/filepath"
	"sort"
)

// EnabledPacks is the union of enabled plugin ids for a session at projectPath
// under the account whose config dir is configDir — the THREE layers Claude
// Code merges for enablement, in its precedence order:
//
//	<configDir>/settings.json
//	<project>/.claude/settings.json
//	<project>/.claude/settings.local.json
//
// A later layer's explicit false disables. Sorted, never nil.
//
// The estate's settings file is NEVER a fourth layer: the estate delivers no
// enabledPlugins (runsettings.EstateKeys), so counting its list would report
// packs no session enables and would hide a sub-repo that turned a pack off.
// projectscan.ReadEnabledPlugins is not reused either: it reads settings.json
// only, by design, and would miss every pack a project enables locally.
func EnabledPacks(projectPath, configDir string) []string {
	state := map[string]bool{}
	var layers []string
	if configDir != "" {
		layers = append(layers, filepath.Join(configDir, "settings.json"))
	}
	layers = append(layers,
		filepath.Join(projectPath, ".claude", "settings.json"),
		filepath.Join(projectPath, ".claude", "settings.local.json"),
	)
	for _, f := range layers {
		var doc struct {
			EnabledPlugins map[string]any `json:"enabledPlugins"`
		}
		if !readJSON(f, &doc) {
			continue
		}
		for id, v := range doc.EnabledPlugins {
			if b, ok := v.(bool); ok {
				state[id] = b
			}
		}
	}
	ids := make([]string, 0, len(state))
	for id, on := range state {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
