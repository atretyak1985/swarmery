package gitstat

import (
	"reflect"
	"testing"
)

func TestParseNumstat(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    []FileStat
		wantErr bool
	}{
		{name: "empty output is no rows", out: "", want: nil},
		{
			name: "text and binary rows",
			out:  "3\t1\tsrc/a.go\n-\t-\tassets/logo.png\n",
			want: []FileStat{
				{Path: "src/a.go", Added: 3, Removed: 1},
				{Path: "assets/logo.png", Binary: true},
			},
		},
		{
			name: "path with a space is kept verbatim",
			out:  "12\t0\tdocs/x.md docs/SKILL.md\n",
			want: []FileStat{{Path: "docs/x.md docs/SKILL.md", Added: 12}},
		},
		{
			name: "blank and CRLF rows",
			out:  "\r\n2\t2\ta.md\r\n\n",
			want: []FileStat{{Path: "a.md", Added: 2, Removed: 2}},
		},
		{name: "space-separated row is a hard error", out: "3 1 a.go\n", wantErr: true},
		{name: "empty path column is a hard error", out: "3\t1\t\n", wantErr: true},
		{
			name: "unreadable count degrades to zero, never negative",
			out:  "x\t-4\ta.go\n",
			want: []FileStat{{Path: "a.go"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseNumstat(tc.out)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseNumstat: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTotalsAndPaths(t *testing.T) {
	files := []FileStat{
		{Path: "a.go", Added: 10, Removed: 2},
		{Path: "b.png", Binary: true},
		{Path: "c.go", Added: 1, Removed: 7},
	}
	add, rem := Totals(files)
	if add != 11 || rem != 9 {
		t.Fatalf("Totals = %d/%d, want 11/9", add, rem)
	}
	if got := Paths(files); !reflect.DeepEqual(got, []string{"a.go", "b.png", "c.go"}) {
		t.Fatalf("Paths = %q", got)
	}
}
