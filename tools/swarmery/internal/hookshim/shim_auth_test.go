package hookshim

import (
	"bytes"
	"io"
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
	signed    []string // the key each request's signature verified under ("" = none)
	keys      []string // the keys the stub tries (default tokenA, tokenB)
	onRequest func(i int)
}

func newAuthStub(t *testing.T, script ...[2]string) *authStub {
	t.Helper()
	s := &authStub{keys: []string{tokenA, tokenB}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		i := len(s.auth)
		key, nonce := verifiedKey(r, body, s.keys...)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.signed = append(s.signed, key)
		hook := s.onRequest
		s.mu.Unlock()
		if hook != nil {
			hook(i)
		}
		step := script[min(i, len(script)-1)]
		status := http.StatusUnauthorized
		switch step[0] {
		case "200":
			status = http.StatusOK
		case "202":
			status = http.StatusAccepted
		case "204":
			status = http.StatusNoContent
		}
		answerSigned(w, key, nonce, status, step[1])
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *authStub) headers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

// signedWith is the key each request was signed with, "" for an unsigned one.
func (s *authStub) signedWith() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.signed...)
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
	for i, key := range stub.signedWith() {
		if key != tokenA || strings.Contains(got[i], tokenA) {
			t.Errorf("request %d: signed with %q (Authorization %q), want signed with the file's token, the token itself not sent", i, key, got[i])
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
	if got := stub.signedWith(); len(got) != 2 || got[0] != tokenA || got[1] != tokenB {
		t.Errorf("signed with = %q, want the old then the rotated token", got)
	}
	if strings.Contains(logged, tokenA) || strings.Contains(logged, tokenB) {
		t.Errorf("a token leaked into the log: %q", logged)
	}
}

// TestRefusedTokenFileSendsNoHeader: a symlink to a valid token, a directory
// and an oversized file all count as no token — no Authorization header, the
// 401 is logged as auth-missing, the shim fails open.
func TestRefusedTokenFileSendsNoHeader(t *testing.T) {
	fastAuthRetry(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "real-token")
	writeToken(t, target, tokenA)

	symlink := filepath.Join(dir, "symlink-token")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(dir, "oversized-token")
	writeToken(t, oversized, strings.Repeat("a", maxTokenFile+1))
	directory := filepath.Join(dir, "dir-token")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{"symlink": symlink, "oversized": oversized, "directory": directory} {
		logPath := filepath.Join(dir, name+".log")
		stub := newAuthStub(t, [2]string{"401", `{"error":"link token required"}`})
		stdout, logged := runAuthShim(t, EventPermissionRequest, stub.srv.URL, path, logPath, stdinFixture)
		if stdout != "" {
			t.Errorf("%s: stdout = %q, want silent fail-open", name, stdout)
		}
		if got := stub.headers(); len(got) != 1 || got[0] != "" {
			t.Errorf("%s: Authorization headers = %q, want one request with none", name, got)
		}
		if !strings.Contains(logged, "outcome=auth-missing") {
			t.Errorf("%s: log = %q, want outcome=auth-missing", name, logged)
		}
		if strings.Contains(logged, tokenA) {
			t.Errorf("%s: the token leaked into the log: %q", name, logged)
		}
	}
}

// TestTokenAtTheCapIsAccepted: a file of exactly maxTokenFile bytes is still
// read (the cap refuses only what exceeds it).
func TestTokenAtTheCapIsAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "link-token")
	tok := strings.Repeat("c", maxTokenFile-1) // + "\n" = maxTokenFile bytes
	writeToken(t, path, tok)
	stub := newAuthStub(t, [2]string{"202", ""})
	stub.keys = []string{tok}
	runAuthShim(t, EventStop, stub.srv.URL, path, filepath.Join(dir, "hook.log"), `{}`)
	if got := stub.signedWith(); len(got) != 1 || got[0] != tok {
		t.Errorf("Authorization headers = %d entries, want the at-cap token sent once", len(got))
	}
}

// TestNoTokenThenTokenAppearsRetries: with no file the first request carries
// no header; on its 401 the shim re-reads at once and, the file having
// appeared, retries once with it.
func TestNoTokenThenTokenAppearsRetries(t *testing.T) {
	prev := authRetryDelay
	authRetryDelay = time.Hour // must not be waited on when no token was sent
	t.Cleanup(func() { authRetryDelay = prev })

	dir := t.TempDir()
	tokenPath, logPath := filepath.Join(dir, "link-token"), filepath.Join(dir, "hook.log")
	stub := newAuthStub(t,
		[2]string{"401", `{"error":"link token required"}`},
		[2]string{"202", ""})
	stub.onRequest = func(i int) {
		if i == 0 {
			writeToken(t, tokenPath, tokenA) // minted while the first attempt was in flight
		}
	}

	start := time.Now()
	_, logged := runAuthShim(t, EventStop, stub.srv.URL, tokenPath, logPath, `{}`)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s: the no-token re-read must not wait", elapsed)
	}
	got, signed := stub.headers(), stub.signedWith()
	if len(got) != 2 || got[0] != "" || signed[1] != tokenA {
		t.Errorf("Authorization headers = %q, want none then the new token", got)
	}
	if !strings.Contains(logged, "outcome=http-202") {
		t.Errorf("log = %q, want outcome=http-202", logged)
	}
}
