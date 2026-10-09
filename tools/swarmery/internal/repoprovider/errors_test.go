package repoprovider

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	exit1 := fmt.Errorf("exit status 1")
	cases := []struct {
		name   string
		stderr string
		err    error
		want   error // nil = unclassified
	}{
		{"gh not logged in", "You are not logged into any GitHub hosts. To log in, run: gh auth login", exit1, ErrNotAuthenticated},
		{"gh 401", "HTTP 401: Bad credentials (https://api.github.com/user)", exit1, ErrNotAuthenticated},
		{"git https auth", "fatal: Authentication failed for 'https://github.com/acme/w.git/'", exit1, ErrNotAuthenticated},
		{"ssh key", "git@github.com: Permission denied (publickey).", exit1, ErrNotAuthenticated},
		{"permission to", "remote: Permission to acme/widgets.git denied to bot.\nfatal: unable to access", exit1, ErrNoPushAccess},
		{"git 403", "fatal: unable to access '…': The requested URL returned error: 403", exit1, ErrNoPushAccess},
		{"protected", "remote: error: GH006: Protected branch update failed\n ! [remote rejected] main -> main (protected branch hook declined)", exit1, ErrNoPushAccess},
		{"gh 403", "HTTP 403: Resource not accessible by integration", exit1, ErrNoPushAccess},
		{"non-ff", " ! [rejected]        b -> b (non-fast-forward)", exit1, ErrRemoteDiverged},
		{"fetch first", " ! [rejected]        b -> b (fetch first)\nhint: Updates were rejected", exit1, ErrRemoteDiverged},
		{"no remote", "error: No such remote 'origin'", exit1, ErrNoRemote},
		{"not a repo", "fatal: 'origin' does not appear to be a git repository", exit1, ErrNoRemote},
		{"binary missing", "", &exec.Error{Name: "gh", Err: exec.ErrNotFound}, ErrBinaryMissing},
		{"other", "fatal: something else entirely", exit1, nil},
	}
	all := []error{ErrNotAuthenticated, ErrNoPushAccess, ErrRemoteDiverged, ErrNoRemote, ErrBinaryMissing}
	for _, c := range cases {
		got := Classify(c.stderr, c.err)
		if got == nil {
			t.Fatalf("%s: Classify returned nil for a failure", c.name)
		}
		for _, s := range all {
			if errors.Is(got, s) != (s == c.want) {
				t.Errorf("%s: errors.Is(%v, %v) = %v", c.name, got, s, errors.Is(got, s))
			}
		}
		if !errors.Is(got, c.err) {
			t.Errorf("%s: cause not unwrappable", c.name)
		}
	}
	if Classify("anything", nil) != nil {
		t.Fatal("Classify(nil err) != nil")
	}
}

func TestClassifyRedactsAndBounds(t *testing.T) {
	tok := "ghp_" + strings.Repeat("Z", 36)
	err := Classify("remote: invalid token "+tok+"\nfatal: Authentication failed", fmt.Errorf("exit status 128"))
	if strings.Contains(err.Error(), tok) || !strings.Contains(err.Error(), "***") {
		t.Fatalf("token not redacted: %v", err)
	}
	if !strings.HasPrefix(err.Error(), ErrNotAuthenticated.Error()+": ") {
		t.Fatalf("message head: %v", err)
	}
	long := strings.Repeat("x", 5000) + "TAIL"
	var e *Error
	if !errors.As(Classify(long, fmt.Errorf("exit status 1")), &e) {
		t.Fatal("not an *Error")
	}
	if len(e.Detail) != OutputTail || !strings.HasSuffix(e.Detail, "TAIL") {
		t.Fatalf("detail len = %d", len(e.Detail))
	}
	if !strings.HasPrefix(e.Error(), "command failed: ") {
		t.Fatalf("unclassified head: %q", e.Error()[:30])
	}
	// Empty stderr falls back to the process error text.
	if got := Classify("", fmt.Errorf("signal: killed")).Error(); got != "command failed: signal: killed" {
		t.Fatalf("fallback = %q", got)
	}
	if (&Error{Sentinel: ErrBaseRefused}).Error() != ErrBaseRefused.Error() {
		t.Fatal("detail-less message")
	}
	if len((&Error{}).Unwrap()) != 0 {
		t.Fatal("empty Unwrap")
	}
}

// Regression (review fix 1): a token straddling the OutputTail cut must be
// redacted before the cut, or its tail survives as an unmatchable fragment.
func TestClassifyRedactsBeforeTruncating(t *testing.T) {
	tok := "ghp_" + strings.Repeat("R", 36)
	in := tok + strings.Repeat("f", 2018) // cut lands 10 bytes into the token
	msg := Classify(in, fmt.Errorf("exit status 1")).Error()
	for i := 0; i+8 <= len(tok); i++ {
		if strings.Contains(msg, tok[i:i+8]) {
			t.Fatalf("token fragment %q survived", tok[i:i+8])
		}
	}
	if got := RedactedTail("", fmt.Errorf("token %s", tok)); strings.Contains(got, tok) {
		t.Fatalf("process-error text not redacted: %q", got)
	}
}
