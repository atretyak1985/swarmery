package mdfence

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture reads a golden phase doc shared with the bash side (phase 3 reuses the
// same files), so the Go and shell parsers are held to one corpus.
func fixture(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", dir, name))
	if err != nil {
		t.Fatalf("read fixture %s/%s: %v", dir, name, err)
	}
	return string(b)
}

func TestParseGuardFixtures(t *testing.T) {
	cases := []struct {
		file      string
		errKind   string // "" = parses
		errLine   int
		paths     []string
		stopIf    []string
		nWarnings int
	}{
		{
			file: "ok.md",
			paths: []string{".github/workflows/*.yml", "plugins/*/hooks/hooks.json", "**/*_test.go",
				"**/*.test.ts", "**/*.test.tsx", "scripts/tests/**/*"},
			stopIf: []string{"out-of-scope change", "protected path", "access or login error", "criterion impossible"},
		},
		{file: "absent.md", errKind: ErrAbsent},
		// line 10 is `  - **/*_test.go`: an unquoted glob opening with * is a yaml alias.
		{file: "unparsable.md", errKind: ErrUnparsable, errLine: 10},
		{
			file:      "dirs-not-files.md",
			paths:     []string{".github/workflows/**", "scripts/tests/", "**/*_test.go"},
			stopIf:    []string{"protected path"},
			nWarnings: 2,
		},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			g, perr := ParseGuard(fixture(t, "guard-blocks", c.file))
			if c.errKind != "" {
				if perr == nil {
					t.Fatalf("ParseGuard = %+v, nil; want a %s error", g, c.errKind)
				}
				if perr.Kind != c.errKind {
					t.Fatalf("error kind = %q (%v), want %q", perr.Kind, perr, c.errKind)
				}
				if perr.Line != c.errLine {
					t.Errorf("error line = %d (%v), want %d", perr.Line, perr, c.errLine)
				}
				if perr.Reason == "" {
					t.Error("error carries no reason")
				}
				return
			}
			if perr != nil {
				t.Fatalf("ParseGuard error: %v", perr)
			}
			if !reflect.DeepEqual(g.ProtectedPaths, c.paths) {
				t.Errorf("protected_paths = %q, want %q", g.ProtectedPaths, c.paths)
			}
			if !reflect.DeepEqual(g.StopIf, c.stopIf) {
				t.Errorf("stop_if = %q, want %q", g.StopIf, c.stopIf)
			}
			if len(g.Warnings) != c.nWarnings {
				t.Errorf("warnings = %q, want %d", g.Warnings, c.nWarnings)
			}
		})
	}
}

// The guard-trap fixture's phase doc is what a live trap run enforces; the bash
// hook's tests parse it too, so both parsers must read the same one glob.
func TestParseGuardTrapFixture(t *testing.T) {
	g, perr := ParseGuard(fixture(t, filepath.Join("guard-trap", "plan"), "phase-1-trap.md"))
	if perr != nil {
		t.Fatal(perr)
	}
	if !reflect.DeepEqual(g.ProtectedPaths, []string{"**/*_test.go"}) || len(g.Warnings) != 0 {
		t.Fatalf("guard = %+v, want exactly **/*_test.go and no warnings", g)
	}
}

func TestParseGuardDirectoryWarningsNameTheGlob(t *testing.T) {
	g, perr := ParseGuard(fixture(t, "guard-blocks", "dirs-not-files.md"))
	if perr != nil {
		t.Fatal(perr)
	}
	joined := strings.Join(g.Warnings, "\n")
	for _, want := range []string{`".github/workflows/**"`, `"scripts/tests/"`, `"scripts/tests/**/*"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q do not mention %s", g.Warnings, want)
		}
	}
}

func TestParseGuardShapes(t *testing.T) {
	cases := []struct {
		name, body string
		wantErr    string // substring of the reason; "" = parses
	}{
		{"missing stop_if", "kind: guard\nprotected_paths: [\"a.go\"]", "missing key stop_if"},
		{"missing protected_paths", "kind: guard\nstop_if: [x]", "missing key protected_paths"},
		{"nested entry", "kind: guard\nprotected_paths:\n  - {a: b}\nstop_if: [x]", "plain string"},
		{"map instead of list", "kind: guard\nprotected_paths: {a: b}\nstop_if: [x]", "list of strings"},
		{"empty entry", "kind: guard\nprotected_paths: [\"\"]\nstop_if: [x]", "empty entry"},
		{"not a mapping", "kind: guard\n- a", "not valid yaml"},
		{"empty lists and unknown keys are fine", "kind: guard\nprotected_paths: []\nstop_if: []\nowner: planner", ""},
		{"a bare scalar is a one-element list", "kind: guard\nprotected_paths: a.go\nstop_if: protected path", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, perr := ParseGuard("# P\n\n```yaml\n" + c.body + "\n```\n")
			switch {
			case c.wantErr == "" && perr != nil:
				t.Fatalf("unexpected error: %v", perr)
			case c.wantErr != "" && perr == nil:
				t.Fatalf("parsed; want an error containing %q", c.wantErr)
			case c.wantErr != "" && (perr.Kind != ErrUnparsable || !strings.Contains(perr.Reason, c.wantErr)):
				t.Fatalf("error = %v, want unparsable containing %q", perr, c.wantErr)
			}
		})
	}
}

func TestParseGuardIgnoresOtherKindsAndBareFences(t *testing.T) {
	doc := "```\nkind: guard\nprotected_paths: [x]\nstop_if: [y]\n```\n\n```yaml\nkind: prior\nareas: [a]\n```\n"
	if _, perr := ParseGuard(doc); perr == nil || perr.Kind != ErrAbsent {
		t.Fatalf("ParseGuard = %v, want absent — a bare fence and a forecast are not a guard", perr)
	}
}

func TestParseGuardSecondBlockWarns(t *testing.T) {
	doc := "```yaml\nkind: guard\nprotected_paths: [a.go]\nstop_if: [x]\n```\n\n```yaml\nkind: guard\nprotected_paths: [b.go]\nstop_if: [y]\n```\n"
	g, perr := ParseGuard(doc)
	if perr != nil {
		t.Fatal(perr)
	}
	if !reflect.DeepEqual(g.ProtectedPaths, []string{"a.go"}) || len(g.Warnings) != 1 {
		t.Fatalf("guard = %+v, want the first block and one warning", g)
	}
	if g.Line != 1 {
		t.Errorf("line = %d, want 1", g.Line)
	}
}

func TestNamesDirectory(t *testing.T) {
	for glob, want := range map[string]bool{
		"scripts/tests/":          true,
		"scripts/tests/**":        true,
		"**":                      true,
		"scripts/tests/**/*":      false,
		".github/workflows/*.yml": false,
		"**/*_test.go":            false,
		"go.mod":                  false,
		"":                        false,
	} {
		if got := NamesDirectory(glob); got != want {
			t.Errorf("NamesDirectory(%q) = %v, want %v", glob, got, want)
		}
	}
}
