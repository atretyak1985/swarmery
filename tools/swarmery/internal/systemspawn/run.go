package systemspawn

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// tailBytes caps how much of EACH captured stream lands in a Run error, and so
// in whatever error column the caller writes it to.
const tailBytes = 4096

// Run feeds prompt to cmd on stdin and returns the child's stdout. It is the
// one error path for every System-project runner, and it exists because of
// what the CLI does on failure: some failures — an expired login, a plugin
// that refused to load, a model the account cannot use — are printed to
// STDOUT as an ordinary message before `claude -p` exits 1 with an EMPTY
// stderr. Five runners each built their error from stderr alone, so the
// daemon logged `claude -p: exit status 1; stderr:` and the reason was gone.
//
// Both streams are quoted, each capped to its tail; an empty one is written as
// "(empty)" so a reader sees the stream WAS captured and looks at the other.
// The exec error is wrapped, so errors.As(*exec.ExitError) still works.
func Run(ctx context.Context, cmd *exec.Cmd, prompt string) (string, error) {
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Run(); err != nil {
		streams := fmt.Sprintf("stderr: %s; stdout: %s", quote(stderr.String()), quote(stdout.String()))
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude -p timed out after %s; %s",
				time.Since(start).Truncate(100*time.Millisecond), streams)
		}
		return "", fmt.Errorf("claude -p: %w; %s", err, streams)
	}
	return stdout.String(), nil
}

// quote is the last ≤ tailBytes of a stream, trimmed, or "(empty)".
func quote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	if len(s) > tailBytes {
		s = s[len(s)-tailBytes:]
	}
	return s
}
