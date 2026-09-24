package surprise

// Area matching — how a forecast area and a run's actual area are judged to be
// "the same place".
//
// THE VOCABULARY MISMATCH. Actual areas (internal/actuals) are repo-root-relative
// directories truncated to `learning.areaDepth` segments (default 2), so in a
// repo that nests its module (`tools/<app>/internal/<pkg>`) every change lands in
// the one area `tools/<app>`. Forecast areas are free text written by a planner
// or an executor: a directory from the repo root (`tools/app/internal/store`), a
// directory from a sub-module root (`internal/store`), or a bare module name
// (`store`). A string comparison would call all of those different places.
//
// THE RULE. Paths are compared by SEGMENTS, never by substring (so `store` never
// matches `restore`). An actual area A matches a forecast area F when:
//
//  1. prefix overlap — F is a segment-prefix of A, after optionally stripping a
//     leading sub-module prefix off A (A = tools/app/internal/store matches
//     F = internal or F = internal/store: A lies inside F);
//  2. file evidence — some file the run changed inside A lies inside F, after
//     optionally stripping a leading sub-module prefix off the file path
//     (A = tools/app, F = internal/store, file tools/app/internal/store/x.go).
//     This is what reconciles a module-relative forecast with a coarse area;
//  3. only when the run's files are unknown: A is a segment-prefix of F (after
//     the same optional strip) — F lies inside the coarse area A. With files
//     known this direction is decided by rule 2 instead, because "the area
//     contains the forecast area" does not mean the run touched it: A =
//     tools/app would otherwise "cover" every forecast area under it and the
//     metric could never fire in a nested repo.
//
// The root area "." (a file at the repo root) matches only a forecast area of
// "." or "" — it is not a prefix of everything.
//
// Precision follows learning.areaDepth: unexpected_areas is a share of the
// ACTUAL areas, so at depth 2 in a nested repo the whole change is one area and
// that area is expected as soon as one of its files is. A repo that wants the
// finer signal sets the depth (4 for a `tools/<app>/internal/<pkg>` layout).

import (
	"path"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/actuals"
)

// normArea strips the decoration an area entry carries — quotes, a leading
// `./` or `/`, a trailing `/`, `/...` (Go's wildcard) or glob tail — and cuts at
// the first glob segment. "" when nothing path-like is left; "." for the root.
func normArea(s string) string {
	t := strings.Trim(strings.TrimSpace(s), "\"'`")
	for strings.HasPrefix(t, "./") {
		t = t[2:]
	}
	t = strings.TrimLeft(t, "/")
	t = strings.TrimSuffix(t, "...")
	var kept []string
	for _, seg := range strings.Split(t, "/") {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		kept = append(kept, seg)
	}
	t = strings.Join(kept, "/")
	if t == "" {
		if strings.TrimSpace(s) == "." || strings.TrimSpace(s) == "./" {
			return "."
		}
		return ""
	}
	return path.Clean(t)
}

// normAreas normalizes, drops empties, dedupes and sorts.
func normAreas(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, a := range in {
		if n := normArea(a); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// segs splits a normalized path; the root "." has no segments.
func segs(p string) []string {
	if p == "" || p == "." {
		return nil
	}
	return strings.Split(p, "/")
}

// hasSegPrefix reports whether prefix's segments are the leading segments of s.
func hasSegPrefix(s, prefix []string) bool {
	if len(prefix) == 0 || len(prefix) > len(s) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

// insideAfterStrip reports whether inner lies inside outer once some leading
// sub-module prefix is stripped off inner: ∃ i: outer is a prefix of inner[i:].
func insideAfterStrip(inner, outer []string) bool {
	for i := range inner {
		if hasSegPrefix(inner[i:], outer) {
			return true
		}
	}
	return false
}

// areaMatches applies the rule in the file comment. files are the segment
// lists of the run's files inside area; filesKnown is false only when the run's
// diff was not measured.
func areaMatches(area string, files [][]string, forecast string, filesKnown bool) bool {
	a, f := segs(area), segs(forecast)
	if len(a) == 0 || len(f) == 0 {
		return len(a) == 0 && len(f) == 0 // the root matches only the root
	}
	if insideAfterStrip(a, f) { // rule 1
		return true
	}
	for _, file := range files { // rule 2
		if insideAfterStrip(file, f) {
			return true
		}
	}
	return !filesKnown && insideAfterStrip(f, a) // rule 3
}

// overlaps is the file-less, symmetric form used to compare two FORECASTS
// (the prior with the posterior): either lies inside the other, after
// stripping a leading sub-module prefix off either.
func overlaps(x, y string) bool {
	a, b := segs(x), segs(y)
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	return insideAfterStrip(a, b) || insideAfterStrip(b, a)
}

// NormArea is normArea for other packages: the area/path/glob entry with its
// decoration stripped and cut at the first glob segment ("" when nothing
// path-like is left, "." for the root). internal/lessons uses it to place a
// lesson's glob as a directory (phase 15), so there is one definition of "the
// directory an area entry names".
func NormArea(s string) string { return normArea(s) }

// AreaOverlap is the file-less, symmetric rule `overlaps` applies to two
// forecasts, over two RAW entries: either names a place inside the other, by
// segments, after stripping a leading sub-module prefix off either. Exported for
// internal/lessons, which matches a lesson's area globs against a run's prior
// forecast with it (phase 15) instead of writing a third matcher.
func AreaOverlap(x, y string) bool {
	a, b := normArea(x), normArea(y)
	if a == "" || b == "" {
		return false
	}
	return overlaps(a, b)
}

func anyOverlap(x string, in []string) bool {
	for _, y := range in {
		if overlaps(x, y) {
			return true
		}
	}
	return false
}

// areasOf derives areas from file paths with actuals' own definition, so a
// row stored without areas is judged exactly as one stored with them.
func areasOf(files []string, depth int) []string {
	return actuals.Areas(files, depth)
}

type areaDiff struct {
	unexpected, missed, matched []string
	actualN, forecastN          int
}

// diffAreas compares the forecast's areas with the run's. files nil means the
// diff was not measured.
func diffAreas(forecast, actual, files []string, depth int) areaDiff {
	fAreas := normAreas(forecast)
	aAreas := make([]string, 0, len(actual))
	aAreas = append(aAreas, actual...)
	sort.Strings(aAreas)

	// Group the changed files under the area actuals would have filed them in.
	byArea := map[string][][]string{}
	for _, p := range files {
		clean := normArea(p)
		if clean == "" {
			continue
		}
		areas := areasOf([]string{clean}, depth)
		if len(areas) == 1 {
			byArea[areas[0]] = append(byArea[areas[0]], segs(clean))
		}
	}
	known := files != nil

	d := areaDiff{
		unexpected: []string{}, missed: []string{}, matched: []string{},
		actualN: len(aAreas), forecastN: len(fAreas),
	}
	for _, a := range aAreas {
		hit := false
		for _, f := range fAreas {
			if areaMatches(a, byArea[a], f, known) {
				hit = true
				break
			}
		}
		if !hit {
			d.unexpected = append(d.unexpected, a)
		}
	}
	for _, f := range fAreas {
		hit := false
		for _, a := range aAreas {
			if areaMatches(a, byArea[a], f, known) {
				hit = true
				break
			}
		}
		if hit {
			d.matched = append(d.matched, f)
		} else {
			d.missed = append(d.missed, f)
		}
	}
	return d
}
