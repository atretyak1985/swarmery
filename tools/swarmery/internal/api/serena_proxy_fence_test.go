package api

// The serena reverse proxy relays to a dashboard whose own API carries
// state-changing endpoints (shutdown, clear stats/logs, memory and config
// writes). A request that can change state — any method but GET/HEAD, and any
// protocol upgrade — must pass the same browser-origin fence as every other
// mutating route, while plain reads keep proxying. The proxied documents are
// served under a CSP sandbox so they never run on the daemon's own origin.

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const foreignOrigin = "http://evil.example"

// countingBackend is a serena stand-in that counts every request it receives,
// so a test can prove a refused request never reached it.
func countingBackend(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "serena-backend-body") //nolint:errcheck
	}))
	t.Cleanup(backend.Close)
	return backend, &hits
}

func proxyDo(t *testing.T, method, rawURL, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestSerenaProxyFencesForeignOriginWrites(t *testing.T) {
	srv, _ := projectsTestServer(t)
	backend, hits := countingBackend(t)
	startProxyStub(t, backend.URL)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		res := proxyDo(t, method, srv.URL+"/api/projects/1/serena/clear_logs", foreignOrigin)
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s with a foreign Origin = %d, want 403", method, res.StatusCode)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("backend saw %d request(s) from refused writes, want 0", n)
	}

	// The dashboard's own origin (the daemon) and Origin-less clients still pass.
	if res := proxyDo(t, http.MethodPost, srv.URL+"/api/projects/1/serena/clear_logs", srv.URL); res.StatusCode != http.StatusOK {
		t.Errorf("same-origin POST = %d, want 200", res.StatusCode)
	}
	if res := proxyDo(t, http.MethodPost, srv.URL+"/api/projects/1/serena/clear_logs", ""); res.StatusCode != http.StatusOK {
		t.Errorf("Origin-less POST = %d, want 200", res.StatusCode)
	}
}

func TestSerenaProxyReadsStillProxyWithAnyOrigin(t *testing.T) {
	srv, _ := projectsTestServer(t)
	backend, hits := countingBackend(t)
	startProxyStub(t, backend.URL)

	for _, origin := range []string{"", foreignOrigin} {
		res := proxyDo(t, http.MethodGet, srv.URL+"/api/projects/1/serena/dashboard/index.html", origin)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET (Origin %q) = %d, want 200", origin, res.StatusCode)
		}
		raw, _ := io.ReadAll(res.Body)
		if string(raw) != "serena-backend-body" {
			t.Errorf("GET (Origin %q) body = %q, want the backend body", origin, raw)
		}
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("backend hits = %d, want 2", n)
	}
}

func TestSerenaProxyFencesForeignOriginUpgrade(t *testing.T) {
	srv, _ := projectsTestServer(t)
	backend, hits := countingBackend(t)
	startProxyStub(t, backend.URL)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	if _, err := conn.Write([]byte("GET /api/projects/1/serena/ws HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\nOrigin: " + foreignOrigin + "\r\n" +
		"Connection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("foreign-Origin upgrade = %d, want 403", res.StatusCode)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("backend saw %d request(s) from a refused upgrade, want 0", n)
	}
}

func TestSerenaProxyResponsesAreSandboxed(t *testing.T) {
	srv, _ := projectsTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A backend policy must not survive: the daemon's sandbox replaces it.
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<!doctype html><p>dash</p>") //nolint:errcheck
	}))
	t.Cleanup(backend.Close)
	startProxyStub(t, backend.URL)

	res := proxyDo(t, http.MethodGet, srv.URL+"/api/projects/1/serena/dashboard/index.html", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", res.StatusCode)
	}
	csp := res.Header.Values("Content-Security-Policy")
	if len(csp) != 1 || csp[0] != serenaProxyCSP {
		t.Errorf("CSP = %q, want exactly [%q]", csp, serenaProxyCSP)
	}
	if strings.Contains(serenaProxyCSP, "allow-same-origin") {
		t.Error("serena proxy CSP must never grant allow-same-origin")
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff = %q, want nosniff", got)
	}
}
