package api

// Step 03 tests — same-origin embedding surfaces: the serena reverse proxy
// (/api/projects/{id}/serena/{rest...}, incl. websocket upgrade passthrough)
// and the graphify static jail (/api/projects/{id}/graphify/{rest...}).
// Proxy state is arranged through a real stub process: the manager launches a
// bash script that prints the fake backend's URL on a "dashboard" line, so no
// toolproc test hook is needed.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/toolproc"
)

// startProxyStub attaches a toolproc.Manager whose stub script prints
// backendURL on a serena-style dashboard line, starts it for project 1, and
// blocks until the manager parses the line and flips the state to running.
func startProxyStub(t *testing.T, backendURL string) {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "stub.sh")
	body := "#!/bin/bash\necho 'Serena web dashboard started at " + backendURL + "/dashboard/index.html'\nsleep 60\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	m := toolproc.NewManager(toolproc.Config{Command: func(projectDir string, mcpPort int) (string, []string) {
		return stub, nil
	}})
	AttachToolManager(m)
	t.Cleanup(func() {
		AttachToolManager(nil)
		m.StopAll()
	})
	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatalf("start stub: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status(1).State == toolproc.StateRunning {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stub serena never reached running (tail: %v)", m.Status(1).LogTail)
}

func TestSerenaProxyPassthrough(t *testing.T) {
	srv, _ := projectsTestServer(t)

	var (
		mu       sync.Mutex
		gotPath  string
		gotQuery string
		gotProbe string
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotProbe = r.Header.Get("X-Probe")
		mu.Unlock()
		w.Header().Set("X-Backend", "serena-stub")
		io.WriteString(w, "serena-marker-body") //nolint:errcheck
	}))
	t.Cleanup(backend.Close)
	startProxyStub(t, backend.URL)

	req, err := http.NewRequest("GET", srv.URL+"/api/projects/1/serena/dashboard/index.html?foo=bar&baz=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Probe", "hello-probe")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("proxied GET = %d, want 200", res.StatusCode)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "serena-marker-body" {
		t.Errorf("body = %q, want the backend marker", raw)
	}
	if res.Header.Get("X-Backend") != "serena-stub" {
		t.Errorf("X-Backend = %q, want serena-stub (response headers must pass through)", res.Header.Get("X-Backend"))
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/dashboard/index.html" {
		t.Errorf("backend path = %q, want /dashboard/index.html", gotPath)
	}
	if gotQuery != "foo=bar&baz=1" {
		t.Errorf("backend query = %q, want foo=bar&baz=1", gotQuery)
	}
	if gotProbe != "hello-probe" {
		t.Errorf("backend X-Probe = %q, want hello-probe (request headers must pass through)", gotProbe)
	}
}

// wsAccept computes the RFC 6455 Sec-WebSocket-Accept for a handshake key.
func wsAccept(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

func TestSerenaProxyWebsocketUpgrade(t *testing.T) {
	srv, _ := projectsTestServer(t)

	// Minimal ws backend: hijack, answer the 101 handshake, then echo one raw
	// line so the test proves the tunnel is bidirectional after the upgrade.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n" + //nolint:errcheck
			"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsAccept(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n")
		buf.Flush() //nolint:errcheck
		if line, rerr := buf.ReadString('\n'); rerr == nil {
			buf.WriteString(line) //nolint:errcheck
			buf.Flush()           //nolint:errcheck
		}
	}))
	t.Cleanup(backend.Close)
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
		"Host: " + u.Host + "\r\n" +
		"Connection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", res.StatusCode)
	}
	if got, want := res.Header.Get("Sec-WebSocket-Accept"), wsAccept(key); got != want {
		t.Errorf("Sec-WebSocket-Accept = %q, want %q", got, want)
	}
	if !strings.EqualFold(res.Header.Get("Upgrade"), "websocket") {
		t.Errorf("Upgrade = %q, want websocket", res.Header.Get("Upgrade"))
	}

	// Post-upgrade tunnel: a raw line must round-trip through the proxy.
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	echo, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read echo through tunnel: %v", err)
	}
	if echo != "ping\n" {
		t.Errorf("tunnel echo = %q, want ping", echo)
	}
}

func TestSerenaProxyNotRunning(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t) // attached but never started → stopped

	out := doJSON(t, "GET", srv.URL+"/api/projects/1/serena/dashboard/index.html", nil, http.StatusBadGateway)
	if msg, _ := out["error"].(string); msg != "serena is not running for this project" {
		t.Errorf("error = %q, want the not-running message", msg)
	}

	// Unknown project → 404; bad id → 400.
	doJSON(t, "GET", srv.URL+"/api/projects/9999/serena/dashboard/index.html", nil, http.StatusNotFound)
	doJSON(t, "GET", srv.URL+"/api/projects/bad/serena/dashboard/index.html", nil, http.StatusBadRequest)

	// Nil manager → 503, same contract as the sidebar feed.
	AttachToolManager(nil)
	out = doJSON(t, "GET", srv.URL+"/api/projects/1/serena/dashboard/index.html", nil, http.StatusServiceUnavailable)
	if msg, _ := out["error"].(string); msg != "tool manager not attached" {
		t.Errorf("nil-manager error = %q, want \"tool manager not attached\"", msg)
	}
}

func TestSerenaProxyNonLoopbackBlocked(t *testing.T) {
	srv, _ := projectsTestServer(t)
	// A rogue serena announcing a non-loopback dashboard origin must be
	// refused before any dial — no backend exists, and none is needed: the
	// 502 body alone proves the guard fired instead of a proxy attempt.
	startProxyStub(t, "http://93.184.216.34:8080")

	out := doJSON(t, "GET", srv.URL+"/api/projects/1/serena/dashboard/index.html", nil, http.StatusBadGateway)
	if msg, _ := out["error"].(string); msg != "serena dashboard URL is not a loopback address" {
		t.Errorf("error = %q, want the non-loopback message", msg)
	}
}

func TestSerenaRootRedirect(t *testing.T) {
	srv, _ := projectsTestServer(t)
	backend := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(backend.Close)
	startProxyStub(t, backend.URL)

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(srv.URL + "/api/projects/1/serena/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("GET dashboardPath = %d, want 302", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/api/projects/1/serena/dashboard/index.html" {
		t.Errorf("Location = %q, want /api/projects/1/serena/dashboard/index.html", loc)
	}
}

// seedGraphifyOut writes graph.html + sub/data.json under project 1's dir and
// returns the project path.
func seedGraphifyOut(t *testing.T, srvURL string) string {
	t.Helper()
	path := projectPath(t, srvURL, "1")
	out := filepath.Join(path, "graphify-out")
	if err := os.MkdirAll(filepath.Join(out, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "graph.html"), []byte("<html>graphify-viz</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "sub", "data.json"), []byte(`{"nodes":[1]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(raw)
}

func TestGraphifyStaticServes(t *testing.T) {
	srv, _ := projectsTestServer(t)
	seedGraphifyOut(t, srv.URL)

	if code, body := getBody(t, srv.URL+"/api/projects/1/graphify/graph.html"); code != 200 || body != "<html>graphify-viz</html>" {
		t.Errorf("graph.html = %d %q, want 200 + seeded content", code, body)
	}
	if code, body := getBody(t, srv.URL+"/api/projects/1/graphify/sub/data.json"); code != 200 || body != `{"nodes":[1]}` {
		t.Errorf("sub/data.json = %d %q, want 200 + seeded content", code, body)
	}
	// Empty rest → default document graph.html.
	if code, body := getBody(t, srv.URL+"/api/projects/1/graphify/"); code != 200 || body != "<html>graphify-viz</html>" {
		t.Errorf("default doc = %d %q, want 200 + graph.html content", code, body)
	}
	// Missing file → 404; unknown project → 404.
	if code, _ := getBody(t, srv.URL+"/api/projects/1/graphify/nope.html"); code != 404 {
		t.Errorf("missing file = %d, want 404", code)
	}
	if code, _ := getBody(t, srv.URL+"/api/projects/9999/graphify/graph.html"); code != 404 {
		t.Errorf("unknown project = %d, want 404", code)
	}
}

func TestGraphifyStaticJail(t *testing.T) {
	srv, _ := projectsTestServer(t)
	seedGraphifyOut(t, srv.URL)

	// Encoded traversal reaches the handler as a decoded "../…" rest → 403,
	// and the jailed file content must never leak.
	for _, rest := range []string{
		"..%2F.claude%2Fsettings.json",
		"..%2F..%2Fetc%2Fpasswd",
		"sub%2F..%2F..%2F.claude%2Fsettings.json",
	} {
		code, body := getBody(t, srv.URL+"/api/projects/1/graphify/"+rest)
		if code != http.StatusForbidden {
			t.Errorf("rest=%s: status = %d, want 403", rest, code)
		}
		if strings.Contains(body, "enabledPlugins") || strings.Contains(body, "root:") {
			t.Errorf("rest=%s: leaked file content outside graphify-out:\n%s", rest, body)
		}
	}

	// Literal ../ is cleaned away by the mux (301 → cleaned path) and must end
	// as a 404 elsewhere — never the settings.json content.
	code, body := getBody(t, srv.URL+"/api/projects/1/graphify/../.claude/settings.json")
	if code == http.StatusOK && strings.Contains(body, "enabledPlugins") {
		t.Errorf("literal traversal served settings.json (status %d):\n%s", code, body)
	}
}

func TestGraphifyStaticMethodGuard(t *testing.T) {
	srv, _ := projectsTestServer(t)
	seedGraphifyOut(t, srv.URL)

	for _, method := range []string{"POST", "PUT", "DELETE"} {
		req, err := http.NewRequest(method, srv.URL+"/api/projects/1/graphify/graph.html", nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, res.StatusCode)
		}
	}

	// HEAD rides along with GET and must succeed without a body.
	req, err := http.NewRequest("HEAD", srv.URL+"/api/projects/1/graphify/graph.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("HEAD = %d, want 200", res.StatusCode)
	}
}
