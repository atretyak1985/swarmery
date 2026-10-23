package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// seen is one request a verifying stub received.
type seen struct {
	method, target, body string
	verified             bool
	authorization        string
}

// verifyingDaemon is a stub that checks every request's Swarmery-HMAC
// signature under key and answers status with a fixed body.
func verifyingDaemon(t *testing.T, key string, status int) (*httptest.Server, func() []seen) {
	t.Helper()
	v := linksig.NewVerifier()
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := v.VerifyRequest(r, key, time.Now())
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{method: r.Method, target: r.RequestURI, body: string(b), verified: err == nil, authorization: r.Header.Get("Authorization")})
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seen { mu.Lock(); defer mu.Unlock(); return append([]seen(nil), got...) }
}

func keyFile(t *testing.T) (path, key string) {
	t.Helper()
	home := t.TempDir()
	key, err := link.EnsureToken(home)
	if err != nil {
		t.Fatal(err)
	}
	return link.TokenPath(home), key
}

func runAPI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	code := cmdAPI(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestAPISignsRequestsAndNeverSendsTheToken(t *testing.T) {
	kp, key := keyFile(t)
	srv, got := verifyingDaemon(t, key, http.StatusCreated)
	bodyFile := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(bodyFile, []byte(`{"from":"file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		args []string
		in   string
		want seen
	}{
		{[]string{"POST", "/api/routines", "--data", `{"name":"x"}`}, "", seen{method: "POST", target: "/api/routines", body: `{"name":"x"}`}},
		{[]string{"PATCH", "/api/board/tasks/7", "--data", "@" + bodyFile}, "", seen{method: "PATCH", target: "/api/board/tasks/7", body: `{"from":"file"}`}},
		{[]string{"put", "/api/x?y=1&z=2", "--data", "@-"}, `{"from":"stdin"}`, seen{method: "PUT", target: "/api/x?y=1&z=2", body: `{"from":"stdin"}`}},
		{[]string{"DELETE", "/api/projects/3/vcs/token"}, "", seen{method: "DELETE", target: "/api/projects/3/vcs/token"}},
		// flags before the positionals work too
		{[]string{"--data", `{}`, "POST", "/api/approvals/5"}, "", seen{method: "POST", target: "/api/approvals/5", body: `{}`}},
	}
	for _, tc := range cases {
		args := append([]string{"--url", srv.URL, "--key-file", kp}, tc.args...)
		code, out, stderr := runAPI(t, tc.in, args...)
		if code != 0 {
			t.Errorf("%v: exit %d (stderr %q)", tc.args, code, stderr)
		}
		if out != `{"ok":true}` {
			t.Errorf("%v: stdout %q", tc.args, out)
		}
	}
	reqs := got()
	if len(reqs) != len(cases) {
		t.Fatalf("daemon saw %d requests, want %d", len(reqs), len(cases))
	}
	for i, r := range reqs {
		w := cases[i].want
		if r.method != w.method || r.target != w.target || r.body != w.body {
			t.Errorf("request %d = %s %s %q, want %s %s %q", i, r.method, r.target, r.body, w.method, w.target, w.body)
		}
		if !r.verified {
			t.Errorf("request %d (%s %s) did not verify: %q", i, r.method, r.target, r.authorization)
		}
		if strings.Contains(r.authorization, key) {
			t.Errorf("request %d carries the token itself", i)
		}
	}
}

func TestAPIExitCodes(t *testing.T) {
	kp, key := keyFile(t)
	refused, _ := verifyingDaemon(t, key, http.StatusConflict)
	if code, out, stderr := runAPI(t, "", "--url", refused.URL, "--key-file", kp, "POST", "/api/x"); code != 1 || out != `{"ok":true}` || !strings.Contains(stderr, "409") {
		t.Errorf("non-2xx: exit %d stdout %q stderr %q, want 1, the body, the status", code, out, stderr)
	}
	// Nothing listens on :1.
	if code, _, stderr := runAPI(t, "", "--url", "http://127.0.0.1:1", "--key-file", kp, "POST", "/api/x"); code != 2 || !strings.Contains(stderr, "unreachable") {
		t.Errorf("unreachable: exit %d stderr %q, want 2", code, stderr)
	}
	for _, bad := range [][]string{
		{}, {"POST"}, {"TRACE", "/api/x"}, {"POST", "http://elsewhere/api/x"}, {"POST", "api/x"},
		{"POST", "/api/x", "extra"}, {"POST", "/api/x", "--data", "@/no/such/file"}, {"POST", "/api/x", "--nope"},
	} {
		if code, _, _ := runAPI(t, "", append([]string{"--key-file", kp}, bad...)...); code != 2 {
			t.Errorf("%v: exit %d, want 2 (usage)", bad, code)
		}
	}
}

func TestAPIWithoutTokenWarnsAndSendsUnsigned(t *testing.T) {
	_, key := keyFile(t)
	srv, got := verifyingDaemon(t, key, http.StatusUnauthorized)
	missing := filepath.Join(t.TempDir(), "link-token")
	code, _, stderr := runAPI(t, "", "--url", srv.URL, "--key-file", missing, "POST", "/api/x")
	if code != 1 || !strings.Contains(stderr, "no link token") {
		t.Errorf("exit %d stderr %q, want 1 and the warning", code, stderr)
	}
	if r := got(); len(r) != 1 || r[0].authorization != "" {
		t.Errorf("requests %+v, want one with no Authorization", r)
	}
}

// TestAPIPassesTheOriginlessGate: against the real route table, an
// Origin-less write is refused unsigned and passes signed.
func TestAPIPassesTheOriginlessGate(t *testing.T) {
	home := t.TempDir()
	key, err := link.EnsureToken(home)
	if err != nil {
		t.Fatal(err)
	}
	api.AttachLink(link.NewHolder(link.TokenPath(home), key), ingest.ParseExcludeList(ingest.DefaultExclude))
	t.Cleanup(func() { api.AttachLink(nil, nil) })
	db, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h, err := api.NewServer(db, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/dispatch/pause", "application/json", strings.NewReader(`{"scope":"global","paused":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("raw Origin-less POST = %d, want 401", resp.StatusCode)
	}
	// Past the gate the handler answers: no dispatcher is attached here.
	code, out, _ := runAPI(t, "", "--url", srv.URL, "--key-file", link.TokenPath(home), "POST", "/api/dispatch/pause", "--data", `{"scope":"global","paused":true}`)
	if code != 1 || !strings.Contains(out, "dispatcher not attached") {
		t.Errorf("signed POST: exit %d body %q, want the handler's 503", code, out)
	}
}

func TestCheckBindAuth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	loopback := []string{"127.0.0.1", "127.0.0.2", "::1", "[::1]", "localhost", "LOCALHOST"}
	wide := []string{"0.0.0.0", "", "::", "192.168.1.5", "10.0.0.1", "myhost.local"}
	for _, b := range loopback {
		if err := checkBindAuth(b); err != nil {
			t.Errorf("checkBindAuth(%q) without a token = %v, want nil (loopback)", b, err)
		}
	}
	for _, b := range wide {
		if err := checkBindAuth(b); err == nil || !strings.Contains(err.Error(), "link token") {
			t.Errorf("checkBindAuth(%q) without a token = %v, want a refusal", b, err)
		}
	}
	if _, err := os.Stat(link.TokenPath(home)); err == nil {
		t.Fatal("checkBindAuth minted a token")
	}
	if _, err := link.EnsureToken(home); err != nil {
		t.Fatal(err)
	}
	for _, b := range wide {
		if err := checkBindAuth(b); err != nil {
			t.Errorf("checkBindAuth(%q) with a token = %v, want nil", b, err)
		}
	}
}
