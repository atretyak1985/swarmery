package improve

import (
	"strings"
	"testing"
)

// TestParseNumstatTabDelimited pins the numstat row grammar: git never quotes
// spaces in a numstat path, so the row must be split on TABs. A whitespace split
// truncated `…/x.md …/SKILL.md` to its last token, which made the smuggled file
// parse as a second row equal to the target and slip past checkPathScope.
func TestParseNumstatTabDelimited(t *testing.T) {
	const target = "plugins/core/skills/docker-build/SKILL.md"
	cases := []struct {
		name      string
		out       string
		wantPaths []string
		wantTotal int
		wantErr   bool
	}{
		{
			name:      "plain path",
			out:       "3\t1\t" + target + "\n",
			wantPaths: []string{target},
			wantTotal: 4,
		},
		{
			name:      "path containing a space is kept verbatim",
			out:       "12\t0\tplugins/core/skills/docker-build/x.md plugins/core/skills/docker-build/SKILL.md\n",
			wantPaths: []string{"plugins/core/skills/docker-build/x.md plugins/core/skills/docker-build/SKILL.md"},
			wantTotal: 12,
		},
		{
			name: "smuggled sibling stays a distinct path",
			out: "12\t0\tplugins/core/skills/docker-build/x.md plugins/core/skills/docker-build/SKILL.md\n" +
				"4\t2\t" + target + "\n",
			wantPaths: []string{
				"plugins/core/skills/docker-build/x.md plugins/core/skills/docker-build/SKILL.md",
				target,
			},
			wantTotal: 18,
		},
		{
			name:      "binary row counts the path but adds no lines",
			out:       "-\t-\tplugins/core/skills/docker-build/logo.png\n",
			wantPaths: []string{"plugins/core/skills/docker-build/logo.png"},
			wantTotal: 0,
		},
		{
			name: "rename token survives as one path (fail-closed at the gate)",
			out:  "1\t1\tplugins/core/agents/old.md => plugins/core/agents/new.md\n",
			wantPaths: []string{
				"plugins/core/agents/old.md => plugins/core/agents/new.md",
			},
			wantTotal: 2,
		},
		{
			name:      "blank and CRLF rows",
			out:       "\r\n2\t2\t" + target + "\r\n\n",
			wantPaths: []string{target},
			wantTotal: 4,
		},
		{
			name:    "fewer than three tab columns is a hard error",
			out:     "3 1 " + target + "\n",
			wantErr: true,
		},
		{
			name:    "empty path column is a hard error",
			out:     "3\t1\t\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths, total, err := parseNumstat(tc.out)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got paths=%q total=%d", paths, total)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNumstat: %v", err)
			}
			if len(paths) != len(tc.wantPaths) {
				t.Fatalf("paths = %q, want %q", paths, tc.wantPaths)
			}
			for i := range paths {
				if paths[i] != tc.wantPaths[i] {
					t.Errorf("paths[%d] = %q, want %q", i, paths[i], tc.wantPaths[i])
				}
			}
			if total != tc.wantTotal {
				t.Errorf("total = %d, want %d", total, tc.wantTotal)
			}
		})
	}
}

// TestCheckPathScopeRejectsSpacedSibling is the end of the F1 chain: with the
// TAB split in place the smuggled file is its own path, so the hard apply-scope
// gate rejects the diff instead of waving two "equal to target" rows through.
func TestCheckPathScopeRejectsSpacedSibling(t *testing.T) {
	const target = "plugins/core/skills/docker-build/SKILL.md"
	out := "12\t0\tplugins/core/skills/docker-build/x.md " + target + "\n4\t2\t" + target + "\n"
	paths, _, err := parseNumstat(out)
	if err != nil {
		t.Fatalf("parseNumstat: %v", err)
	}
	if err := checkPathScope(paths, target); err == nil {
		t.Fatal("checkPathScope accepted a diff that also creates a spaced sibling path")
	} else if !strings.Contains(err.Error(), "outside the target file") {
		t.Fatalf("unexpected error: %v", err)
	}
}
