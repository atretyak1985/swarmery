// A phase doc's `Blast radius:` line — who calls what the phase changes.
//
// The planner computes it before writing the doc's `## Risks` section (core
// workspace-plans skill, resources/blast-radius.md) and records one line:
//
//	- Blast radius: 5 callers in internal/orders — internal/orders/service.go:412, … (sources: rg, serena)
//
// The daemon reads it for two things only: the reviewer brief repeats it so the
// reviewer can fill `checked_callers`, and the Plans page lints a phase whose
// forecast lists files but whose doc carries no such line. Like a forecast it is
// DATA, never a gate: nothing refuses a run or holds a phase over it.

package wsingest

import (
	"fmt"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
)

// LintBlastRadiusMissing is the lint for a phase whose forecast lists files and
// whose doc has no `Blast radius:` line. Shown beside the forecast lints.
const LintBlastRadiusMissing = "blast_radius_missing"

// blastRadiusPrefix opens the line once any list marker or emphasis is stripped;
// matched case-insensitively.
const blastRadiusPrefix = "blast radius:"

// ParseBlastRadius returns the doc's first `Blast radius:` line with its list
// marker and leading emphasis stripped, or "" when the doc carries none.
//
// The line counts in any section of the doc's OWN text: the format puts it under
// `## Risks`, but a doc whose author kept it under `## Notes` still said who the
// callers are. A line inside a fenced block does not count (mdfence.ForEachLine
// skips fences): an agent prompt or a format example quoting the line describes
// someone else's phase. Nor does one opening with inline code — that is the
// format being cited mid-sentence, not a measurement. Pure; unit-tested.
func ParseBlastRadius(md string) string {
	found := ""
	mdfence.ForEachLine(md, func(_ int, line string) {
		if found != "" {
			return
		}
		t := strings.TrimLeft(strings.TrimSpace(line), "-*+ ")
		if strings.HasPrefix(strings.ToLower(t), blastRadiusPrefix) {
			found = t
		}
	})
	return found
}

// LintBlastRadius reports a phase whose forecasts list files but whose doc has
// no `Blast radius:` line (line is ParseBlastRadius's result). nil when the line
// is present or no forecast names a file — a forecast with only `areas` gives
// the planner nothing to look callers up for. Pure; unit-tested.
func LintBlastRadius(fs []Forecast, line string) []ForecastLint {
	if strings.TrimSpace(line) != "" {
		return nil
	}
	files := map[string]bool{}
	for _, f := range fs {
		for _, p := range f.Files {
			files[p] = true
		}
	}
	if len(files) == 0 {
		return nil
	}
	return []ForecastLint{{
		Code: LintBlastRadiusMissing,
		Message: fmt.Sprintf("forecast lists %d file(s) but the doc has no `Blast radius:` line — "+
			"nobody recorded who calls them (resources/blast-radius.md)", len(files)),
	}}
}
