package accountprune

// Which keys of a settings file the estate already supplies.
//
// This file is thin on purpose. The redundancy rule is NOT defined here: it is
// runsettings.Redundant — the SUBSET rule the doctor's settings-block detector
// (internal/accountdoctor) reports on — so the doctor reports exactly what the
// prune removes. Nothing below compares two values.

import (
	"sort"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runsettings"
)

// neverRedundant is the guard should runsettings.EstateKeys ever grow: these
// keys are never a candidate, whatever the rule would say about them.
//
//   - permissions, enabledMcpjsonServers: the estate never supplies them — the
//     composed --settings file drops both by name — so a repo's copy is its
//     only live copy.
//   - enabledPlugins: not delivered since it left EstateKeys (at flag
//     precedence an estate `true` overrode a project's own disable), so each
//     project's block is the only place its pack set lives.
//   - swarmery: the binding object (claudeAccount, estate). Pruning it would
//     delete the declaration that made every other key redundant.
var neverRedundant = map[string]bool{
	"permissions":           true,
	"enabledMcpjsonServers": true,
	"enabledPlugins":        true,
	"swarmery":              true,
}

// estateKeys is the key list the prune inspects: a COPY of
// runsettings.EstateKeys (read, never re-typed). A var so a test can widen it
// and prove neverRedundant still holds.
var estateKeys = func() []string { return append([]string(nil), runsettings.EstateKeys[:]...) }

// KeptKey is an EstateKey a file carries that the estate does NOT wholly
// supply: it stays, whole. NotInEstate names the entries (plugin ids,
// marketplace names) the estate lacks or holds differently — names only, never
// a value.
type KeptKey struct {
	Key         string   `json:"key"`
	NotInEstate []string `json:"notInEstate"`
}

// candidateKeys is the EstateKeys doc carries, minus neverRedundant, in
// EstateKeys order.
func candidateKeys(doc map[string]any) []string {
	var out []string
	for _, k := range estateKeys() {
		if neverRedundant[k] {
			continue
		}
		if _, ok := doc[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// classifyKeys splits doc's candidate keys into the ones runsettings.Redundant
// calls redundant with estate (removable, whole) and the ones it keeps. A nil
// estate supplies nothing: every candidate is kept.
func classifyKeys(estate, doc map[string]any) (redundant []string, kept []KeptKey) {
	redundant = []string{}
	kept = []KeptKey{}
	for _, k := range candidateKeys(doc) {
		fv := doc[k]
		var ev any
		if estate != nil {
			ev = estate[k]
		}
		ok, notIn := runsettings.Redundant(k, ev, fv)
		if ok {
			redundant = append(redundant, k)
			continue
		}
		if notIn == nil {
			// Redundant had nothing entry-wise to say (the estate lacks the key,
			// or a side is not an object): every entry of the file's copy is
			// one the estate does not supply.
			notIn = entryNames(fv)
		}
		kept = append(kept, KeptKey{Key: k, NotInEstate: notIn})
	}
	return redundant, kept
}

// entryNames is the sorted top-level names of a JSON object value; [] for
// anything else.
func entryNames(v any) []string {
	m, _ := v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
