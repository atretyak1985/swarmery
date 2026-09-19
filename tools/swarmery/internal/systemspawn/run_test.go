package systemspawn

// Run's contract: stdout comes back verbatim on success, and on failure the
// error quotes BOTH streams — the CLI reports some failures on stdout and exits
// 1 with an empty stderr, so an error built from stderr alone is "exit status 1;
// stderr:" and the reason is gone.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stub writes an executable shell script and returns its path.
func stub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

func TestRunReturnsStdoutAndFeedsPrompt(t *testing.T) {
	bin := stub(t, `printf 'got:'; cat`)
	out, err := Run(context.Background(), exec.Command(bin), "hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "got:hello" {
		t.Errorf("stdout = %q, want %q", out, "got:hello")
	}
}

// The regression: a failure reported on stdout with an empty stderr.
func TestRunFoldsStdoutIntoTheError(t *testing.T) {
	bin := stub(t, `echo 'Not logged in. Please run /login'; exit 1`)
	_, err := Run(context.Background(), exec.Command(bin), "p")
	if err == nil {
		t.Fatal("want an error for exit status 1")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Errorf("error should wrap *exec.ExitError, got %T: %v", err, err)
	}
	msg := err.Error()
	for _, want := range []string{"exit status 1", "stderr: (empty)", "stdout: Not logged in. Please run /login"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}

func TestRunQuotesStderrWhenPresent(t *testing.T) {
	bin := stub(t, `echo 'partial' ; echo 'boom' >&2; exit 2`)
	_, err := Run(context.Background(), exec.Command(bin), "p")
	if err == nil {
		t.Fatal("want an error for exit status 2")
	}
	msg := err.Error()
	for _, want := range []string{"exit status 2", "stderr: boom", "stdout: partial"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}

// Only the tail of a stream survives, so a runaway child cannot push kilobytes
// of transcript into an error column.
func TestRunCapsEachStreamToItsTail(t *testing.T) {
	bin := stub(t, `head -c 6000 /dev/zero | tr '\0' 'x'; echo END; exit 1`)
	_, err := Run(context.Background(), exec.Command(bin), "p")
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.HasSuffix(msg, "END") {
		t.Errorf("error should end with the stream's tail, got …%q", msg[len(msg)-20:])
	}
	if got := strings.Count(msg, "x"); got > tailBytes {
		t.Errorf("stdout tail carries %d bytes, cap is %d", got, tailBytes)
	}
}

func TestRunReportsATimeoutAsSuch(t *testing.T) {
	bin := stub(t, `echo 'still working'; exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := Run(ctx, exec.CommandContext(ctx, bin), "p")
	if err == nil {
		t.Fatal("want a timeout error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "timed out after") {
		t.Errorf("error %q should say the run timed out", msg)
	}
	if !strings.Contains(msg, "stdout: still working") {
		t.Errorf("error %q should carry what the child printed before the deadline", msg)
	}
}
