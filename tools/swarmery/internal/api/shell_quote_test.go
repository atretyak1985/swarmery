package api

// Tests for shellQuote (tasks_diff.go): every copy-paste land hint embeds plan
// titles, card titles, branches and paths through it, so pasting a hint into a
// shell must never execute anything those values carry.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// hostileTitle carries a command substitution, a backtick substitution and a
// single quote — each one a way out of the old strconv.Quote double quotes.
const hostileTitle = "Fix $(touch x) and `touch y` in Bob's plan"

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"main", "main"},
		{"feat/landing-6", "feat/landing-6"},
		{"/var/folders/ab/T/repo_1.x", "/var/folders/ab/T/repo_1.x"},
		{"origin/user@host:1+2=3,4%", "origin/user@host:1+2=3,4%"},
		{"two words", "'two words'"},
		{"$(touch x)", "'$(touch x)'"},
		{"`touch y`", "'`touch y`'"},
		{"Bob's", `'Bob'\''s'`},
		{"a;b|c&d", "'a;b|c&d'"},
		{"~/x", "'~/x'"},
		{"line\nbreak", "'line\nbreak'"},
		{hostileTitle, `'Fix $(touch x) and ` + "`touch y`" + ` in Bob'\''s plan'`},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestShellQuoteRoundTripsThroughSh pastes quoted values into a real POSIX
// shell: each must come back byte for byte, and none may run a command.
func TestShellQuoteRoundTripsThroughSh(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	dir := t.TempDir()
	for _, in := range []string{hostileTitle, "", "plain", "'", "''", `\'"$HOME"`, "a\nb", "it's $(touch z) `touch w`"} {
		cmd := exec.Command(sh, "-c", "printf '%s' "+shellQuote(in))
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("sh on %q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("sh printed %q, want %q", out, in)
		}
	}
	for _, name := range []string{"x", "y", "z", "w"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("pasting a quoted value created %q: a substitution ran", name)
		}
	}
}

// TestShellQuoteChangeRequestCmd is the land hint itself: a hostile title, base
// and branch render single-quoted for both providers.
func TestShellQuoteChangeRequestCmd(t *testing.T) {
	quoted := `'Fix $(touch x) and ` + "`touch y`" + ` in Bob'\''s plan'`
	cases := []struct {
		kind repoprovider.Kind
		want string
	}{
		{repoprovider.KindGitHub,
			"gh pr create --head 'run/$(id)' --base 'dev;rm' --title " + quoted + " --draft"},
		{repoprovider.KindGitLab,
			"glab mr create --source-branch 'run/$(id)' --target-branch 'dev;rm' --title " + quoted + " --draft"},
	}
	for _, tc := range cases {
		if got := changeRequestCmd(tc.kind, "run/$(id)", "dev;rm", hostileTitle, true); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.kind, got, tc.want)
		}
	}
	// A plain branch stays bare; the title with spaces is single-quoted.
	if got, want := changeRequestCmd(repoprovider.KindGitHub, "feat/x", "", "Add line items", false),
		"gh pr create --head feat/x --title 'Add line items'"; got != want {
		t.Errorf("plain: got %s, want %s", got, want)
	}
}
