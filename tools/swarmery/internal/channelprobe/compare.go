package channelprobe

import (
	"sort"
	"strconv"
)

// Drift is one observation a fresh run made differently from the baseline.
// CLIVersion is the version that produced Got, so a drift is always attributable
// to the CLI upgrade that caused it.
type Drift struct {
	Fact        string `json:"fact"`
	Observation string `json:"observation"`
	Want        string `json:"want"`
	Got         string `json:"got"`
	CLIVersion  string `json:"cliVersion"`
}

// Compare returns every observation that got made and that disagrees with
// baseline, sorted by fact then observation. Pure: no I/O, no clock.
//
// An observation got did NOT make — a fact missing entirely, or a key absent from
// its Observed map — is not a drift: the harness could not see it, and "could
// not see" is Inconclusive, never a verdict in either direction. Observations got
// made that the baseline does not know are ignored for the same reason: there is
// nothing to have drifted from.
func Compare(baseline, got Result) []Drift {
	var out []Drift
	for _, name := range sortedFacts(baseline.Facts) {
		want := baseline.Facts[name]
		have, ok := got.Facts[name]
		if !ok {
			continue
		}
		for _, key := range sortedKeys(want.Observed) {
			g, seen := have.Observed[key]
			if !seen || g == want.Observed[key] {
				continue
			}
			out = append(out, Drift{
				Fact:        name,
				Observation: key,
				Want:        strconv.FormatBool(want.Observed[key]),
				Got:         strconv.FormatBool(g),
				CLIVersion:  got.CLIVersion,
			})
		}
	}
	return out
}

// Unobserved lists "<fact>.<observation>" for every baseline observation got did
// not make. A caller that must not mistake silence for agreement — the live test,
// a doctor report — reads this alongside Compare.
func Unobserved(baseline, got Result) []string {
	var out []string
	for _, name := range sortedFacts(baseline.Facts) {
		have := got.Facts[name]
		for _, key := range sortedKeys(baseline.Facts[name].Observed) {
			if _, seen := have.Observed[key]; !seen {
				out = append(out, name+"."+key)
			}
		}
	}
	return out
}

func sortedFacts(m map[string]Fact) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
