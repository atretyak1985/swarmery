package repopath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hand-written Repo cell names a checkout the way the operator types it, with a
// leading "~". Before expandHome the token stayed relative, was joined onto the
// project path, resolved to nothing, and ResolveTrusted fell through to the
// project root — dispatching the phase into the WRONG repository with err == nil.
func TestTokens_ExpandsLeadingTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this machine")
	}
	for _, tc := range []struct{ name, cell, want string }{
		{"backticked path", "`~/projects/ae/tools/claude-code-plugin`", filepath.Join(home, "projects/ae/tools/claude-code-plugin")},
		{"bare path", "~/projects/ae", filepath.Join(home, "projects/ae")},
		{"bare tilde", "`~`", home},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Tokens(tc.cell)
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("Tokens(%q) = %q, want [%q]", tc.cell, got, tc.want)
			}
			if !filepath.IsAbs(got[0]) {
				t.Fatalf("expanded token %q is still relative — it would be joined onto the project path", got[0])
			}
		})
	}
}

// The ~user/ form is deliberately NOT expanded: guessing it would resolve to a
// different operator's home. It must stay a miss, never a wrong answer.
func TestTokens_LeavesUserTildeAlone(t *testing.T) {
	got := Tokens("`~someoneelse/projects/x`")
	if len(got) != 1 || got[0] != "~someoneelse/projects/x" {
		t.Fatalf("Tokens = %q, want the token untouched", got)
	}
}

// The end-to-end property the bug actually broke: a tilde cell naming a real
// repository inside a trusted root must resolve to THAT repository, not fall
// through to the project root.
func TestResolveTrusted_HonoursATildeCell(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtimeHome, err := os.UserHomeDir(); err != nil || runtimeHome != home {
		t.Skipf("UserHomeDir does not follow HOME here (%q, %v)", runtimeHome, err)
	}

	project := filepath.Join(home, "project")
	other := filepath.Join(home, "repos", "other")
	for _, d := range []string{project, other} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ResolveTrusted(project, []string{filepath.Join(home, "repos")}, "`~/repos/other`")
	if err != nil {
		t.Fatalf("ResolveTrusted: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("repos", "other")) {
		t.Fatalf("resolved to %q, want the declared repo %q — a fall-through to the project root is the bug", got, other)
	}
}
