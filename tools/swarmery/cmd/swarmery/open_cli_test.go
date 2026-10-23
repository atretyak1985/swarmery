package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

const openTestKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func openKeyFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "link-token")
	if err := os.WriteFile(p, []byte(openTestKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ticketOf returns the unlock ticket in u's fragment.
func ticketOf(t *testing.T, u string) string {
	t.Helper()
	_, frag, ok := strings.Cut(u, "#")
	if !ok {
		t.Fatalf("URL %q has no fragment", u)
	}
	ticket, ok := strings.CutPrefix(frag, "swarmery-unlock=")
	if !ok {
		t.Fatalf("fragment %q is not swarmery-unlock=<ticket>", frag)
	}
	return ticket
}

func TestOpenPrintURLShape(t *testing.T) {
	kp := openKeyFile(t)
	var out, errb bytes.Buffer
	if code := cmdOpen([]string{"--print", "--port", "7798", "--key-file", kp}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	u := strings.TrimSpace(out.String())
	if !strings.HasPrefix(u, "http://127.0.0.1:7798/#swarmery-unlock=") {
		t.Fatalf("URL %q: want http://127.0.0.1:7798/#swarmery-unlock=<ticket>", u)
	}
	nonce, err := linksig.VerifyUnlock(openTestKey, ticketOf(t, u), time.Now())
	if err != nil || nonce == "" {
		t.Fatalf("printed ticket does not verify: %v", err)
	}
}

func TestOpenSessionPage(t *testing.T) {
	kp := openKeyFile(t)
	const id = "3f2a9c1e-7b4d-4e8a-9c2f-1a2b3c4d5e6f"
	var out, errb bytes.Buffer
	if code := cmdOpen([]string{"--print", "--session", id, "--port", "7798", "--key-file", kp}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	u := strings.TrimSpace(out.String())
	if !strings.HasPrefix(u, "http://127.0.0.1:7798/sessions/"+id+"#swarmery-unlock=") {
		t.Fatalf("URL %q: want the session page with the ticket fragment", u)
	}
	if _, err := linksig.VerifyUnlock(openTestKey, ticketOf(t, u), time.Now()); err != nil {
		t.Fatalf("ticket does not verify: %v", err)
	}
}

func TestOpenRunsTheOpener(t *testing.T) {
	kp := openKeyFile(t)
	var got []string
	prev := openBrowser
	openBrowser = func(u string) error { got = append(got, u); return nil }
	t.Cleanup(func() { openBrowser = prev })

	var out, errb bytes.Buffer
	if code := cmdOpen([]string{"--port", "7798", "--key-file", kp}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "http://127.0.0.1:7798/#swarmery-unlock=") {
		t.Fatalf("opener got %q", got)
	}
	if out.Len() != 0 {
		t.Errorf("without --print the ticket went to stdout: %q", out.String())
	}
	if strings.Contains(errb.String(), ticketOf(t, got[0])) {
		t.Errorf("the ticket was echoed to stderr: %q", errb.String())
	}

	openBrowser = func(string) error { return errors.New("no display") }
	if code := cmdOpen([]string{"--port", "7798", "--key-file", kp}, &out, &errb); code != 2 {
		t.Errorf("failed opener: exit %d, want 2", code)
	}
}

// The dashboard is opened by the literal address the daemon is bound to,
// never by the name localhost: that name may resolve to ::1 first, where the
// daemon does not listen and another process may.
func TestOpenUsesTheBoundLiteral(t *testing.T) {
	kp := openKeyFile(t)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"default":        {[]string{"--port", "7798"}, "http://127.0.0.1:7798/#swarmery-unlock="},
		"other loopback": {[]string{"--url", "http://127.0.0.2:7798"}, "http://127.0.0.2:7798/#swarmery-unlock="},
		"ipv6 loopback":  {[]string{"--url", "http://[::1]:7798/"}, "http://[::1]:7798/#swarmery-unlock="},
	} {
		var out, errb bytes.Buffer
		args := append([]string{"--print", "--key-file", kp}, tc.args...)
		if code := cmdOpen(args, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d, stderr %s", name, code, errb.String())
		}
		if u := strings.TrimSpace(out.String()); !strings.HasPrefix(u, tc.want) {
			t.Errorf("%s: URL %q, want prefix %q", name, u, tc.want)
		}
	}
}

func TestOpenRefusesNamesAndRemoteAddresses(t *testing.T) {
	kp := openKeyFile(t)
	for _, base := range []string{
		"http://localhost:7798", "http://swarmery:7777", "http://192.168.1.5:7777",
		"ftp://127.0.0.1:7798", "http://[fe80::1%25en0]:7798", "127.0.0.1:7798",
	} {
		var out, errb bytes.Buffer
		if code := cmdOpen([]string{"--print", "--url", base, "--key-file", kp}, &out, &errb); code != 2 {
			t.Errorf("--url %s: exit %d, want 2", base, code)
		}
		if out.Len() != 0 {
			t.Errorf("--url %s: printed %q", base, out.String())
		}
	}
}

func TestOpenRefusals(t *testing.T) {
	kp := openKeyFile(t)
	for name, args := range map[string][]string{
		"no token":      {"--print", "--key-file", filepath.Join(t.TempDir(), "missing")},
		"bad session":   {"--print", "--session", "../etc", "--key-file", kp},
		"bad port":      {"--print", "--port", "0", "--key-file", kp},
		"stray arg":     {"--print", "extra", "--key-file", kp},
		"unknown flag":  {"--nope"},
		"session slash": {"--print", "--session", "a/b", "--key-file", kp},
	} {
		var out, errb bytes.Buffer
		if code := cmdOpen(args, &out, &errb); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
		if out.Len() != 0 {
			t.Errorf("%s: printed %q", name, out.String())
		}
	}
}
