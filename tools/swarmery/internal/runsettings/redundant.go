package runsettings

import (
	"bytes"
	"encoding/json"
	"sort"
)

// Redundant is the ONE rule that says whether a settings file's copy of an
// EstateKey is redundant with the estate's own copy — the rule the doctor's
// settings-block detector (internal/accountdoctor) reports on and the prune
// removes by. Neither carries a comparison of its own.
//
// It is the SUBSET rule: redundant is true iff key is one of EstateKeys, both
// values are JSON objects, file holds at least one entry, and every top-level
// entry of file (a plugin id under pluginConfigs, a marketplace name under
// extraKnownMarketplaces) exists in estate with an identical canonical JSON
// marshalling (object keys sorted). The estate may hold MORE entries than the
// file: a sub-repo that keeps an older, smaller copy of the estate's block is
// still wholly supplied by the estate.
//
// notInEstate lists, sorted, the entry NAMES of file that estate lacks or holds
// differently — names only, never a value. It is nil when the key is not an
// EstateKey or either side is not an object (there is nothing entry-wise to
// say), and empty when every entry is supplied.
//
// A non-EstateKey (enabledPlugins, permissions, enabledMcpjsonServers, swarmery,
// anything else) is never redundant, whatever its value: a repo's native
// permissions block is its only live copy, and its enabledPlugins is the only
// place its pack set lives. An empty file object is not redundant either —
// there is nothing in it to prune.
func Redundant(key string, estate, file any) (redundant bool, notInEstate []string) {
	if !isEstateKey(key) {
		return false, nil
	}
	em, ok := estate.(map[string]any)
	if !ok {
		return false, nil
	}
	fm, ok := file.(map[string]any)
	if !ok {
		return false, nil
	}
	notInEstate = []string{}
	for name, fv := range fm {
		ev, present := em[name]
		if !present || !sameCanonical(ev, fv) {
			notInEstate = append(notInEstate, name)
		}
	}
	sort.Strings(notInEstate)
	return len(fm) > 0 && len(notInEstate) == 0, notInEstate
}

// sameCanonical reports whether a and b marshal to the same canonical JSON.
// encoding/json sorts map keys, and every object decoded into `any` is a
// map[string]any, so equal documents marshal to equal bytes at every depth. A
// value that cannot be marshalled is never equal to anything.
func sameCanonical(a, b any) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}
