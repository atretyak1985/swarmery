package improve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubClaude installs an executable `claude` stub on PATH. The script body
// receives the prompt on stdin exactly like the real binary.
func stubClaude(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The runner resolves `claude` via plain PATH lookup (the toolproc pattern),
// feeds the prompt on stdin, and returns stdout.
func TestClaudeRunnerRun(t *testing.T) {
	// The stub echoes a marker plus everything it got on stdin.
	stubClaude(t, `printf 'GOT: '; cat`)
	out, err := ClaudeRunner{}.Run(context.Background(), "the prompt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "GOT: the prompt" {
		t.Errorf("out = %q, want the stdin round-trip", out)
	}
}

func TestClaudeRunnerStderrCapture(t *testing.T) {
	stubClaude(t, `echo "model exploded" >&2; exit 3`)
	_, err := ClaudeRunner{}.Run(context.Background(), "p")
	if err == nil {
		t.Fatal("want error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "model exploded") || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("err = %v, want exit status + captured stderr", err)
	}
}

func TestClaudeRunnerTimeout(t *testing.T) {
	stubClaude(t, `sleep 5`)
	_, err := ClaudeRunner{Timeout: 100 * time.Millisecond}.Run(context.Background(), "p")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want timeout error", err)
	}
}

func TestClaudeRunnerMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no claude anywhere
	_, err := ClaudeRunner{}.Run(context.Background(), "p")
	if err == nil {
		t.Fatal("want error when claude is not on PATH")
	}
}

// TestModelAndEffortPins: headless runs that carry no override MUST name a
// full model id (an alias would silently re-resolve; no --model inherits the
// expensive account default) and pin effort (no --effort inherits the CLI's
// xhigh ceiling — high is the cost/quality sweet spot for proposal generation).
//
// The effort pin earns its keep twice over since the Opus 5.5 cutover: that
// model's own default effort is medium, where Opus 5's was high. An unpinned
// site would therefore have quietly dropped a level on the swap — pinning is
// what makes the model change a price change and nothing else.
func TestModelAndEffortPins(t *testing.T) {
	if defaultModel != "claude-opus-5-5" {
		t.Errorf("defaultModel = %q, want claude-opus-5-5", defaultModel)
	}
	if defaultEffort != "high" {
		t.Errorf("defaultEffort = %q, want high", defaultEffort)
	}
}
