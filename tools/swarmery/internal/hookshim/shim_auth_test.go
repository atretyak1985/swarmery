package hookshim

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Distinct, recognisable token values: a test greps the log for them.
const (
	tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// authStub answers each request with the next status/body of the script (the
// last one repeats) and records the Authorization header of every request.
// onRequest, when set, runs before the answer (index = 0-based request no.).
type authStub struct {
	srv       *httptest.Server
	mu        sync.Mutex
	auth      []string
	onRequest func(i int)
}

func newAuthStub(t *testing.T, script ...[2]string) *authStub {
	t.Helper()
	s := &authStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		i := len(s.auth)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		hook := s.onRequest
		s.mu.Unlock()
		if hook != nil {
			hook(i)
		}
		step := script[min(i, len(script)-1)]
		switch step[0] {
		case "200":
			w.WriteHeader(http.StatusOK)
		case "202":
			w.WriteHeader(http.StatusAccepted)
		case "204":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
		if step[1] != "" {
			w.Write([]byte(step[1]))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *authStub) headers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

// fastAuthRetry shortens the post-401 wait for the test.
func fastAuthRetry(t *testing.T) {
	t.Helper()
	prev := authRetryDelay
	authRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { authRetryDelay = prev })
}

func writeToken(t *testing.T, path, tok string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runAuthShim runs one hook call with a token file and an audit log; it
// returns stdout and the log content.
func runAuthShim(t *testing.T, event, baseURL, tokenPath, logPath, stdin string) (string, string) {
	t.Helper()
	var out bytes.Buffer
	if code := Run(event, strings.NewReader(stdin), Config{
		BaseURL:   baseURL,
		Stdout:    &out,
		TokenPath: tokenPath,
		LogPath:   logPath,
	}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	logged, _ := os.ReadFile(logPath)
	return out.String(), string(logged)
}

// TestTokenHeaderSent: the token file's (trimmed) content goes out as
// `Authorization: Bearer <token>` on every hook event, and never into the log.
func TestTokenHeaderSent(t *testing.T) {
	dir := t.TempDir()
	tokenPath, logPath := filepath.Join(dir, "link-token"), filepath.Join(dir, "hook.log")
	writeToken(t, tokenPath, tokenA)

	stub := newAuthStub(t, [2]string{"200", `{"decision":"allow"}`})
	stdout, logged := runAuthShim(t, EventPermissionRequest, stub.srv.URL, tokenPath, logPath, stdinFixture)
	if !strings.Contains(stdout, `"behavior":"allow"`) {
		t.Errorf("stdout = %q, want the allow decision", stdout)
	}
	for _, ev := range []string{EventStop, EventSessionStart} {
		_, logged = runAuthShim(t, ev, stub.srv.URL, tokenPath, logPath, `{"session_id":"sid-1"}`)
	}
	got := stub.headers()
	if len(got) != 3 {
		t.Fatalf("requests = %d, want 3 (one per event, no retry)", len(got))
	}
	for i, h := range got {
		if h != "Bearer "+tokenA {
			t.Errorf("request %d Authorization = %q, want the file's token", i, h)
		}
	}
	if strings.Contains(logged, tokenA) {
		t.Errorf("the token leaked into the log: %q", logged)
	}
}

// TestMissingTokenFileSendsNoHeader: no file → no Authorization header; the
// daemon's 401 is logged as auth-missing, without a retry, and fails open.
func TestMissingTokenFileSendsNoHeader(t *testing.T) {
	fastAuthRetry(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "hook.log")
	stub := newAuthStub(t, [2]string{"401", `{"error":"link token required"}`})

	stdout, logged := runAuthShim(t, EventPermissionRequest, stub.srv.URL, filepath.Join(dir, "absent"), logPath, stdinFixture)
	if stdout != "" {
		t.Errorf("stdout = %q, want silent fail-open", stdout)
	}
	got := stub.headers()
	if len(got) != 1 || got[0] != "" {
		t.Errorf("Authorization headers = %q, want exactly one request with none", got)
	}
	if !strings.Contains(logged, "outcome=auth-missing") {
		t.Errorf("log = %q, want outcome=auth-missing", logged)
	}

	// An empty TokenPath behaves the same.
	stub2 := newAuthStub(t, [2]string{"401", ""})
	runAuthShim(t, EventStop, stub2.srv.URL, "", logPath, `{}`)
	if got := stub2.headers(); len(got) != 1 || got[0] != "" {
		t.Errorf("empty TokenPath: Authorization headers = %q, want one request with none", got)
	}
}

// TestUnauthorizedRetriesOnce: a 401 with a token is retried exactly once
// (re-reading the file); a second 401 fails open as http-401, and the token
// is absent from the log.
func TestUnauthorizedRetriesOnce(t *testing.T) {
	fastAuthRetry(t)
	dir := t.TempDir()
	tokenPath, logPath := filepath.Join(dir, "link-token"), filepath.Join(dir, "hook.log")
	writeToken(t, tokenPath, tokenA)
	stub := newAuthStub(t, [2]string{"401", `{"error":"link token required"}`})

	stdout, logged := runAuthShim(t, EventPermissionRequest, stub.srv.URL, tokenPath, logPath, stdinFixture)
	if stdout != "" {
		t.Errorf("stdout = %q, want silent fail-open (native dialog)", stdout)
	}
	if got := stub.headers(); len(got) != 2 {
		t.Fatalf("requests = %d (%q), want 2: the call and one retry", len(got), got)
	}
	if !strings.Contains(logged, "outcome=http-401") {
		t.Errorf("log = %q, want outcome=http-401", logged)
	}
	if strings.Contains(logged, tokenA) {
		t.Errorf("the token leaked into the log: %q", logged)
	}
}

// TestUnauthorizedRetryPicksUpRotatedToken: the retry re-reads the file, so a
// rotation between the two attempts is honoured and the decision goes through.
func TestUnauthorizedRetryPicksUpRotatedToken(t *testing.T) {
	fastAuthRetry(t)
	dir := t.TempDir()
	tokenPath, logPath := filepath.Join(dir, "link-token"), filepath.Join(dir, "hook.log")
	writeToken(t, tokenPath, tokenA)
	stub := newAuthStub(t,
		[2]string{"401", `{"error":"link token required"}`},
		[2]string{"200", `{"decision":"allow"}`})
	stub.onRequest = func(i int) {
		if i == 0 {
			writeToken(t, tokenPath, tokenB) // rotated while the first attempt was in flight
		}
	}

	stdout, logged := runAuthShim(t, EventPermissionRequest, stub.srv.URL, tokenPath, logPath, stdinFixture)
	if !strings.Contains(stdout, `"behavior":"allow"`) {
		t.Errorf("stdout = %q, want the allow decision after the retry", stdout)
	}
	got := stub.headers()
	if len(got) != 2 || got[0] != "Bearer "+tokenA || got[1] != "Bearer "+tokenB {
		t.Errorf("Authorization headers = %q, want old then rotated token", got)
	}
	if strings.Contains(logged, tokenA) || strings.Contains(logged, tokenB) {
		t.Errorf("a token leaked into the log: %q", logged)
	}
}
