package term

import (
	"os"
	"slices"
	"strings"
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
		t.Errorf("starter got env names %v (values withheld: it may be the real environment), want none", envNames(got))
	}
}

// ptyEnv with nil is the daemon's own environment plus TERM — and the default
// locale only when that environment names none.
func TestPtyEnvNilMatchesLegacyEnvironment(t *testing.T) {
	want := append(os.Environ(), "TERM=xterm-256color")
	if !hasLocale(os.Environ()) {
		want = append(want, defaultLocale)
	}
	if got := ptyEnv(nil); !slices.Equal(got, want) {
		t.Fatalf("ptyEnv(nil) names =\n  %v\nwant\n  %v\n(values withheld: this is the real environment)", envNames(got), envNames(want))
	}
}

// envNames is env with every VALUE dropped — what a failure message may print
// for a slice built on the real os.Environ(), which can hold live tokens.
func envNames(env []string) []string {
	out := make([]string, len(env))
	for i, kv := range env {
		out[i], _, _ = strings.Cut(kv, "=")
	}
	return out
}

// A caller-supplied env is used AS the environment — not appended to the
// daemon's — with TERM last so it wins over an inherited value. That is what
// lets the handler REMOVE a variable (an inherited CLAUDE_CONFIG_DIR for a
// project bound explicitly to the default account), which no delta could do.
func TestPtyEnvUsesTheCallersEnvWholeAndAppendsTerm(t *testing.T) {
	env := []string{"PATH=/usr/bin", "TERM=dumb", "CLAUDE_CONFIG_DIR=/home/u/.claude-nabu-org", "LANG=uk_UA.UTF-8"}
	got := ptyEnv(env)

	want := append(slices.Clone(env), "TERM=xterm-256color")
	if !slices.Equal(got, want) {
		t.Fatalf("ptyEnv =\n  %v\nwant\n  %v", got, want)
	}
	if len(env) != 4 {
		t.Errorf("ptyEnv mutated the caller's slice: %v", env)
	}
}

// A daemon under launchd/systemd passes no locale; the dock shell must still get
// a UTF-8 one, or bash's readline echoes Cyrillic input as octal escapes.
func TestPtyEnvAddsUTF8LocaleWhenNoneIsSet(t *testing.T) {
	got := ptyEnv([]string{"PATH=/usr/bin", "LANG="})
	if !slices.Contains(got, "LANG=en_US.UTF-8") {
		t.Fatalf("ptyEnv = %v, want LANG=en_US.UTF-8 added", got)
	}
}

// Any of LC_ALL, LC_CTYPE or LANG set by the operator wins over the default.
func TestPtyEnvKeepsAnOperatorLocale(t *testing.T) {
	for _, kv := range []string{"LANG=uk_UA.UTF-8", "LC_ALL=C.UTF-8", "LC_CTYPE=UTF-8"} {
		got := ptyEnv([]string{"PATH=/usr/bin", kv})
		if slices.Contains(got, defaultLocale) {
			t.Errorf("with %s: ptyEnv = %v, want no default locale", kv, got)
		}
	}
}
