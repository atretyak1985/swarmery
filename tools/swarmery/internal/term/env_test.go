package term

import (
	"os"
	"slices"
	"testing"
)

// Start must hand the caller's environment down to the starter unchanged — that
// is the whole plumbing between the HTTP handler's claudeacct.SpawnEnvFor and
// the shell the operator types into.
func TestStartDeliversEnvToSpawn(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})

	want := []string{"PATH=/usr/bin", "CLAUDE_CONFIG_DIR=/home/u/.claude-nabu-org", "MCP_TOKEN=abc"}
	s, err := m.Start("/tmp", want, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if got := st.lastEnv(); !slices.Equal(got, want) {
		t.Errorf("starter got env %v, want %v", got, want)
	}
}

// A caller with nothing to say passes nil, and nothing about the child's
// environment changes — the dock session behaves exactly as it did before this
// parameter existed.
func TestStartWithEmptyEnvLeavesEnvUnchanged(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})

	s, err := m.Start("/tmp", nil, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if got := st.lastEnv(); len(got) != 0 {
		t.Errorf("starter got env %v, want none", got)
	}
}

// ptyEnv with nil must reproduce the pre-feature line byte for byte:
// append(os.Environ(), "TERM=xterm-256color").
func TestPtyEnvNilMatchesLegacyEnvironment(t *testing.T) {
	want := append(os.Environ(), "TERM=xterm-256color")
	if got := ptyEnv(nil); !slices.Equal(got, want) {
		t.Fatalf("ptyEnv(nil) =\n  %v\nwant\n  %v", got, want)
	}
}

// A caller-supplied env is used AS the environment — not appended to the
// daemon's — with TERM last so it wins over an inherited value. That is what
// lets the handler REMOVE a variable (an inherited CLAUDE_CONFIG_DIR for a
// project bound explicitly to the default account), which no delta could do.
func TestPtyEnvUsesTheCallersEnvWholeAndAppendsTerm(t *testing.T) {
	env := []string{"PATH=/usr/bin", "TERM=dumb", "CLAUDE_CONFIG_DIR=/home/u/.claude-nabu-org"}
	got := ptyEnv(env)

	want := append(slices.Clone(env), "TERM=xterm-256color")
	if !slices.Equal(got, want) {
		t.Fatalf("ptyEnv =\n  %v\nwant\n  %v", got, want)
	}
	if len(env) != 3 {
		t.Errorf("ptyEnv mutated the caller's slice: %v", env)
	}
}
