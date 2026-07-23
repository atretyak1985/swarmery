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
