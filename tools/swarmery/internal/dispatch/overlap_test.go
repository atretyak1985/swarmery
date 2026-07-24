package dispatch

import "testing"

func TestPathsOverlap(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want bool
	}{
		// Empty-scope-conservative rule: undeclared = global.
		{"both empty conflict", nil, nil, true},
		{"empty vs nonempty conflicts", nil, []string{"src/api"}, true},
		{"nonempty vs empty conflicts", []string{"src/api"}, []string{}, true},
		{"empty-string entries treated as empty", []string{"", "  "}, []string{"src/api"}, true},

		// Disjoint prefixes never conflict.
		{"disjoint dirs", []string{"src/api"}, []string{"web/src"}, false},
		{"sibling dirs disjoint", []string{"src/api"}, []string{"src/store"}, false},
		{"prefix lookalike not a dir prefix", []string{"src/api"}, []string{"src/apiv2"}, false},

		// Prefix-related conflict in both directions.
		{"identical", []string{"src/api"}, []string{"src/api"}, true},
		{"parent contains child", []string{"src"}, []string{"src/api/x.go"}, true},
		{"child under parent reversed", []string{"src/api/x.go"}, []string{"src"}, true},
		{"trailing slash normalized", []string{"src/api/"}, []string{"src/api"}, true},
		{"dot-slash normalized", []string{"./src/api"}, []string{"src/api/h.go"}, true},

		// Any-entry-overlaps-any-entry.
		{"one of many overlaps", []string{"docs", "src/api"}, []string{"web", "src/api/x"}, true},
		{"none of many overlaps", []string{"docs", "src/api"}, []string{"web", "src/store"}, false},

		// Globs.
		{"glob matches path", []string{"src/*"}, []string{"src/api"}, true},
		{"glob deeper via root", []string{"src/*"}, []string{"src/api/x.go"}, true},
		{"double-star root empty conflicts all", []string{"**/*.ts"}, []string{"web/src/x.ts"}, true},
		{"glob roots disjoint", []string{"src/api/*.go"}, []string{"web/**/*.ts"}, false},
		{"glob root prefix-related", []string{"src/api/*.go"}, []string{"src/api/handlers"}, true},
		{"two globs same root", []string{"src/*.go"}, []string{"src/*.ts"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathsOverlap(tc.a, tc.b); got != tc.want {
				t.Errorf("pathsOverlap(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// Symmetry: overlap must be commutative.
			if got := pathsOverlap(tc.b, tc.a); got != tc.want {
				t.Errorf("pathsOverlap(%v, %v) [reversed] = %v, want %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

func TestGlobRoot(t *testing.T) {
	cases := map[string]string{
		"src/api/*.go": "src/api",
		"**/x":         "",
		"*.ts":         "",
		"src/api":      "src/api",
		"a/b/c*/d":     "a/b",
	}
	for in, want := range cases {
		if got := globRoot(in); got != want {
			t.Errorf("globRoot(%q) = %q, want %q", in, got, want)
		}
	}
}
