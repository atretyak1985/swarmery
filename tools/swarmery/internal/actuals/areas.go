package actuals

import (
	"encoding/json"
	"os"
	"path"
	"sort"
	"strings"
)

// DefaultAreaDepth is how many directory segments from the repo root name an
// "area" when the project does not say otherwise.
//
// 2, not 1: depth 1 names only the bucket a layout files things under — `apps`,
// `packages`, `internal`, `src` — which every change in a monorepo shares and
// which therefore says nothing about WHERE the work was. Depth 2 names the unit
// inside the bucket (`apps/web`, `packages/ui`, `internal/store`, `src/orders`)
// for the common single-module and apps/packages layouts. A repo that nests its
// modules deeper (a Go module under `tools/<name>/internal/<pkg>`, say) sets
// `learning.areaDepth` in its project.json — for that shape, 4.
const DefaultAreaDepth = 2

// maxAreaDepth bounds the knob. Deeper than this an "area" is a single file's
// directory, and the areas list stops being a summary of the change set.
const maxAreaDepth = 8

// runScaffoldDir is where the daemon lends the phase doc into a run's worktree
// (worktree.PlanDocDir / ReportPath). An executor that commits with `git add -A`
// sweeps it into the run branch; it is daemon plumbing, not the run's work, so
// it is excluded from what the run "changed".
const runScaffoldDir = ".swarmery/"

// SizeBand maps a line count (added + removed) onto the forecast format's size
// vocabulary (plan-format.md; wsingest.ForecastSizeBands): XS <20 | S <100 |
// M <400 | L <1500 | XL.
func SizeBand(lines int) string {
	switch {
	case lines < 20:
		return "XS"
	case lines < 100:
		return "S"
	case lines < 400:
		return "M"
	case lines < 1500:
		return "L"
	default:
		return "XL"
	}
}

// Areas returns the distinct directories the given repo-relative paths live in,
// each truncated to at most depth segments, sorted. A file at the repo root is
// area "." — it is a real place the run touched, and dropping it would make a
// README-only change look like it touched nothing.
func Areas(paths []string, depth int) []string {
	if depth < 1 {
		depth = DefaultAreaDepth
	}
	seen := map[string]bool{}
	for _, p := range paths {
		dir := path.Dir(strings.TrimPrefix(path.Clean(p), "./"))
		area := "."
		if dir != "." && dir != "/" {
			segs := strings.Split(dir, "/")
			if len(segs) > depth {
				segs = segs[:depth]
			}
			area = strings.Join(segs, "/")
		}
		seen[area] = true
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// projectLearning is the subset of a consumer's project.json this package reads
// (overlays/_schema/project.schema.json → learning).
type projectLearning struct {
	Learning *struct {
		AreaDepth *int `json:"areaDepth"`
	} `json:"learning"`
}

// AreaDepth returns the first valid `learning.areaDepth` declared by the given
// project.json files, in priority order, or DefaultAreaDepth. A missing file, an
// unparseable file, or an out-of-range value is skipped rather than fatal — the
// knob is advisory, like every other hint the daemon reads from project.json.
func AreaDepth(projectJSONPaths ...string) int {
	for _, p := range projectJSONPaths {
		if p == "" {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg projectLearning
		if err := json.Unmarshal(raw, &cfg); err != nil {
			continue
		}
		if cfg.Learning == nil || cfg.Learning.AreaDepth == nil {
			continue
		}
		if d := *cfg.Learning.AreaDepth; d >= 1 && d <= maxAreaDepth {
			return d
		}
	}
	return DefaultAreaDepth
}
