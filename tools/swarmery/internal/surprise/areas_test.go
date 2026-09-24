package surprise

import (
	"reflect"
	"testing"
)

func TestNormArea(t *testing.T) {
	for in, want := range map[string]string{
		"./internal/store/":     "internal/store",
		"`internal/api`":        "internal/api",
		"internal/...":          "internal",
		"web/src/**":            "web/src",
		"web/*/x":               "web",
		"/abs/path":             "abs/path",
		".":                     ".",
		"":                      "",
		"  tools/app/internal ": "tools/app/internal",
	} {
		if got := normArea(in); got != want {
			t.Errorf("normArea(%q) = %q, want %q", in, got, want)
		}
	}
}

// The matching rule, one row per clause of the doc comment in areas.go.
func TestAreaMatches(t *testing.T) {
	f := func(p ...string) [][]string {
		out := make([][]string, 0, len(p))
		for _, x := range p {
			out = append(out, segs(x))
		}
		return out
	}
	cases := []struct {
		name       string
		area       string
		files      [][]string
		forecast   string
		filesKnown bool
		want       bool
	}{
		{"equal", "internal/store", nil, "internal/store", true, true},
		{"rule 1: area inside the forecast area", "internal/store", nil, "internal", true, true},
		{"rule 1: after stripping a sub-module prefix", "tools/app/internal/store", nil, "internal/store", true, true},
		{"rule 1: a bare module name", "tools/app/internal/store", nil, "store", true, true},
		{"segments, never substrings", "internal/restore", nil, "store", true, false},
		{"rule 2: a module-relative forecast under a coarse area, confirmed by a file",
			"tools/app", f("tools/app/internal/store/a.go"), "internal/store", true, true},
		{"rule 2: a coarse area whose files are elsewhere",
			"tools/app", f("tools/app/web/x.ts"), "internal/store", true, false},
		{"rule 2: the forecast names the file itself",
			"tools/app", f("tools/app/internal/api/epics.go"), "internal/api/epics.go", true, true},
		{"rule 3 needs files unknown: coarse area contains the forecast area",
			"tools/app", nil, "tools/app/internal/store", false, true},
		{"rule 3 does not apply when files are known",
			"tools/app", f("tools/app/web/x.ts"), "tools/app/internal/store", true, false},
		{"the root matches only the root", ".", f("README.md"), ".", true, true},
		{"the root is not a prefix of everything", ".", f("README.md"), "internal", true, false},
		{"a real area is not inside the root", "internal", nil, ".", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := areaMatches(tc.area, tc.files, normArea(tc.forecast), tc.filesKnown); got != tc.want {
				t.Errorf("areaMatches(%q, %v, %q, known=%v) = %v, want %v",
					tc.area, tc.files, tc.forecast, tc.filesKnown, got, tc.want)
			}
		})
	}
}

// The motivating case: a nested module at the default depth 2. Every change is
// the one area tools/app, and a module-relative forecast still matches it.
func TestDiffAreasReconcilesAModuleRelativeForecast(t *testing.T) {
	files := []string{"tools/app/internal/store/a.go", "tools/app/internal/api/b.go"}
	d := diffAreas([]string{"internal/store", "internal/cost"}, []string{"tools/app"}, files, 2)
	if len(d.unexpected) != 0 {
		t.Errorf("unexpected = %v, want none (tools/app holds a forecast area's file)", d.unexpected)
	}
	if !reflect.DeepEqual(d.missed, []string{"internal/cost"}) || !reflect.DeepEqual(d.matched, []string{"internal/store"}) {
		t.Errorf("missed %v matched %v, want [internal/cost] / [internal/store]", d.missed, d.matched)
	}
}

// A measured-empty diff: nothing unexpected, every forecast area missed.
func TestDiffAreasOnAnEmptyChangeSet(t *testing.T) {
	d := diffAreas([]string{"internal/store"}, []string{}, []string{}, 2)
	if len(d.unexpected) != 0 || !reflect.DeepEqual(d.missed, []string{"internal/store"}) {
		t.Errorf("empty diff: unexpected %v missed %v", d.unexpected, d.missed)
	}
}
