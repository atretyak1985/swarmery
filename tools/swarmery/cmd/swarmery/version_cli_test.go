package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/version"
)

func TestIsVersionCommand(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"version", true},
		{"--version", true},
		{"-v", true},
		{"serve", false},
		{"-V", false},
		{"versions", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isVersionCommand(c.in); got != c.want {
			t.Errorf("isVersionCommand(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// One line, the build identity, a trailing newline — and nothing that needs a
// daemon: version.String() is resolved from the binary alone.
func TestCmdVersionPrintsOneLine(t *testing.T) {
	var buf bytes.Buffer
	cmdVersion(&buf)
	out := buf.String()
	if want := version.String() + "\n"; out != want {
		t.Fatalf("cmdVersion output = %q, want %q", out, want)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("cmdVersion printed an empty identity")
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("cmdVersion must print exactly one line, got %q", out)
	}
}
