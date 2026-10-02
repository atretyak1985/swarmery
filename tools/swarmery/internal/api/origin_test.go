package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// listenerRequest builds a request the way the daemon's own listener hands it
// to a handler: the accepted connection's local address is in the context and
// Host names it, as a browser at http://<listen>/ would write it.
func listenerRequest(t testing.TB, method, target, listen string, body io.Reader) *http.Request {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", listen)
	if err != nil {
		t.Fatalf("listener %q: %v", listen, err)
	}
	req := httptest.NewRequest(method, target, body)
	req.Host = listen
	return req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, addr))
}

// localRequest is listenerRequest on the daemon's default address — the
// drop-in for httptest.NewRequest in tests that call the route table directly
// (a bare httptest.NewRequest says Host example.com and arrived on no listener,
// which the Host fence rightly refuses).
func localRequest(t testing.TB, method, target string, body io.Reader) *http.Request {
	t.Helper()
	return listenerRequest(t, method, target, "127.0.0.1:7777", body)
}

// serverPort is the port an httptest server listens on; otherPort is one it
// certainly does NOT — the shape of a second local dev server or a port-forward.
func serverPort(t testing.TB, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

func otherPort(t testing.TB, srv *httptest.Server) string {
	t.Helper()
	p, err := strconv.Atoi(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if p < 65535 {
		p++
	} else {
		p--
	}
	return strconv.Itoa(p)
}

// ── origin fence ─────────────────────────────────────────────────────────────

// TestTrustedOriginIsTheDaemonsOwn: out of the box the ONLY trusted origins are
// the daemon's own — the loopback names on the port it is actually serving.
// "localhost" on another port is a different local process (a second dev
// server, a port-forward), and it earns no more trust than evil.example.
func TestTrustedOriginIsTheDaemonsOwn(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	r := listenerRequest(t, http.MethodPost, "/api/anything", "127.0.0.1:7777", nil)

	for _, ok := range []string{
		"http://localhost:7777", "http://127.0.0.1:7777", "http://[::1]:7777",
		"HTTP://LocalHost:7777",
	} {
		if !isTrustedOrigin(r, ok) {
			t.Errorf("isTrustedOrigin(%q) = false, want true (the daemon's own origin)", ok)
		}
	}
	for _, bad := range []string{
		"http://localhost:5173", // another local port — another process
		"http://127.0.0.1:7778",
		"http://[::1]:7778",
		"http://localhost",       // port 80 ≠ the bound port
		"https://localhost:7777", // the scheme is part of the origin
		"http://swarmery:7777", "http://swarmery", "https://swarmery.corp.example",
		"http://evil.example.com", "http://evil.example.com:7777",
		"file:///etc/passwd", "null", "",
	} {
		if isTrustedOrigin(r, bad) {
			t.Errorf("isTrustedOrigin(%q) = true, want false (nothing is opted in)", bad)
		}
	}
}

// TestTrustedOriginFollowsTheListener: "own" means the listener's port and
// address, not a constant — a daemon on :80 is reached without a port, and a
// `--bind 192.168.1.5` daemon is its own origin by that literal.
func TestTrustedOriginFollowsTheListener(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	on80 := listenerRequest(t, http.MethodPost, "/", "127.0.0.1:80", nil)
	for _, ok := range []string{"http://localhost", "http://localhost:80", "http://127.0.0.1"} {
		if !isTrustedOrigin(on80, ok) {
			t.Errorf("on :80, isTrustedOrigin(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"http://localhost:7777", "https://localhost"} {
		if isTrustedOrigin(on80, bad) {
			t.Errorf("on :80, isTrustedOrigin(%q) = true, want false", bad)
		}
	}

	bound := listenerRequest(t, http.MethodPost, "/", "192.168.1.5:7777", nil)
	for _, ok := range []string{"http://192.168.1.5:7777", "http://localhost:7777"} {
		if !isTrustedOrigin(bound, ok) {
			t.Errorf("bound to 192.168.1.5, isTrustedOrigin(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"http://192.168.1.5:7778", "http://192.168.1.6:7777", "https://192.168.1.5:7777"} {
		if isTrustedOrigin(bound, bad) {
			t.Errorf("bound to 192.168.1.5, isTrustedOrigin(%q) = true, want false", bad)
		}
	}
}

// TestTrustedOriginNeedsAListener: a handler invoked without a server (a bare
// httptest.NewRequest) has no "own" address, so loopback earns nothing — only
// the opt-in list can vouch for an Origin. Nothing is trusted by name alone.
func TestTrustedOriginNeedsAListener(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	r := httptest.NewRequest(http.MethodPost, "/api/anything", nil)

	for _, o := range []string{"http://localhost:7777", "http://127.0.0.1:7777"} {
		if isTrustedOrigin(r, o) {
			t.Errorf("isTrustedOrigin(%q) = true without a listener, want false", o)
		}
	}
	AttachTrustedOrigins([]string{"http://swarmery:7777"})
	if !isTrustedOrigin(r, "http://swarmery:7777") {
		t.Error("the opted-in origin must pass without a listener too")
	}
}

// TestTrustedOriginsOptIn: a named origin passes, and ONLY that origin — the
// allow-list matches scheme+host+port, not the hostname, so opting a friendly
// alias in does not hand every port or scheme on that host the same trust.
func TestTrustedOriginsOptIn(t *testing.T) {
	AttachTrustedOrigins([]string{"http://swarmery:7777"})
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	r := localRequest(t, http.MethodPost, "/api/anything", nil)

	if !isTrustedOrigin(r, "http://swarmery:7777") {
		t.Error("the opted-in origin was rejected")
	}
	// Case-insensitive on scheme and host, as origins are.
	if !isTrustedOrigin(r, "HTTP://Swarmery:7777") {
		t.Error("origin comparison must be case-insensitive on scheme and host")
	}
	for _, bad := range []string{
		"https://swarmery:7777", // different scheme
		"http://swarmery:9999",  // different port
		"http://swarmery",       // default port ≠ the named one
		"http://swarmery.corp.example:7777",
		"http://evil.example.com:7777",
	} {
		if isTrustedOrigin(r, bad) {
			t.Errorf("isTrustedOrigin(%q) = true, want false — the allow-list holds only http://swarmery:7777", bad)
		}
	}
}

// TestTrustedOriginsDefaultPort: a named origin without a port matches the
// browser's Origin header for that scheme's default port, and vice versa.
func TestTrustedOriginsDefaultPort(t *testing.T) {
	AttachTrustedOrigins([]string{"http://swarmery", "https://dash.example"})
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	r := localRequest(t, http.MethodPost, "/api/anything", nil)

	for _, ok := range []string{
		"http://swarmery", "http://swarmery:80",
		"https://dash.example", "https://dash.example:443",
	} {
		if !isTrustedOrigin(r, ok) {
			t.Errorf("isTrustedOrigin(%q) = false, want true", ok)
		}
	}
	if isTrustedOrigin(r, "https://swarmery") {
		t.Error("http://swarmery must not trust https://swarmery")
	}
}

// TestAttachTrustedOriginsDropsGarbage: entries that are not http(s) origins are
// dropped whole rather than half-matched — for the Host list as much as the
// origin list.
func TestAttachTrustedOriginsDropsGarbage(t *testing.T) {
	AttachTrustedOrigins([]string{"swarmery", "file:///x", "", "   ", "ftp://swarmery"})
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	r := localRequest(t, http.MethodPost, "/api/anything", nil)

	for _, bad := range []string{"swarmery", "http://swarmery", "file:///x", "ftp://swarmery"} {
		if isTrustedOrigin(r, bad) {
			t.Errorf("isTrustedOrigin(%q) = true, want false — malformed entries must not grant trust", bad)
		}
	}
	if len(trustedHosts) != 0 {
		t.Errorf("trustedHosts = %v, want empty — malformed entries must not admit a Host either", trustedHosts)
	}
}

// TestRequireLocalOriginFence: end-to-end through the middleware — a page on
// another local port (the textbook CSRF source for a loopback daemon) and a
// friendly alias are 403 and the handler never runs; the daemon's own origin,
// an opted-in alias and an Origin-less client pass.
func TestRequireLocalOriginFence(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	var called int
	h := requireLocalOrigin(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})

	do := func(origin string) int {
		req := localRequest(t, http.MethodPost, "/api/anything", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}

	if got := do("http://localhost:5173"); got != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for localhost on another port", got)
	}
	if got := do("http://swarmery:7777"); got != http.StatusForbidden {
		t.Errorf("status = %d, want 403 with an empty allow-list", got)
	}
	if called != 0 {
		t.Errorf("handler ran %d time(s) for a rejected origin", called)
	}
	if got := do("http://localhost:7777"); got != http.StatusOK {
		t.Errorf("status = %d, want 200 for the daemon's own origin", got)
	}

	AttachTrustedOrigins([]string{"http://swarmery:7777"})
	if got := do("http://swarmery:7777"); got != http.StatusOK {
		t.Errorf("status = %d, want 200 once the origin is opted in", got)
	}
	if got := do("http://swarmery:9999"); got != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — a sibling port is not opted in", got)
	}
	// No Origin at all (the hook shim, curl, the console) still passes:
	// localhost trust is v1.
	if got := do(""); got != http.StatusOK {
		t.Errorf("status = %d, want 200 for a request with no Origin header", got)
	}
	if called != 3 {
		t.Errorf("handler ran %d time(s), want 3 (own origin + opted-in origin + no-origin)", called)
	}
}

// TestStrictOriginGateSharesTheAllowList: the terminal's stricter gate honours
// the daemon's own origin and the same opt-in list — a trusted alias must not
// get working writes and a dead terminal — but still rejects an ABSENT Origin,
// which is its whole point, and a foreign-port localhost like everything else.
func TestStrictOriginGateSharesTheAllowList(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	with := func(origin string) *http.Request {
		r := localRequest(t, http.MethodGet, "/api/term/ws", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	if isStrictLocalOrigin(with("http://swarmery:7777")) {
		t.Error("an alias passed the strict gate with an empty allow-list")
	}
	AttachTrustedOrigins([]string{"http://swarmery:7777"})
	if !isStrictLocalOrigin(with("http://swarmery:7777")) {
		t.Error("the opted-in origin was rejected by the strict gate")
	}
	if !isStrictLocalOrigin(with("http://localhost:7777")) {
		t.Error("the daemon's own origin was rejected by the strict gate")
	}
	if isStrictLocalOrigin(with("")) {
		t.Error("the strict gate must still reject a missing Origin")
	}
	if isStrictLocalOrigin(with("http://swarmery:9999")) {
		t.Error("the strict gate must match the full origin, not the hostname")
	}
	if isStrictLocalOrigin(with("http://localhost:9999")) {
		t.Error("the strict gate must reject localhost on a port the daemon does not serve")
	}
}

// ── Host fence ───────────────────────────────────────────────────────────────

// TestTrustedHostNamesTheListener: a request must name this daemon in Host — a
// loopback name or address on the listener's port. A DNS-rebinding page cannot:
// its Host is the attacker's name however that name resolves.
func TestTrustedHostNamesTheListener(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	with := func(host string) *http.Request {
		r := localRequest(t, http.MethodGet, "/api/sessions", nil)
		r.Host = host
		return r
	}

	for _, ok := range []string{
		"localhost:7777", "127.0.0.1:7777", "[::1]:7777", "LOCALHOST:7777", "127.0.0.2:7777",
	} {
		if !isTrustedHost(with(ok)) {
			t.Errorf("isTrustedHost(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"attacker.example:7777", // rebinding: the name resolves here, the header still says who was asked
		"attacker.example",
		"example.com",    // httptest.NewRequest's default — a handler needs localRequest
		"localhost:5173", // another local port
		"localhost",      // port 80 ≠ the bound port
		"",               // no Host at all
		"user@localhost:7777", "localhost:7777:1", "::1:7777",
	} {
		if isTrustedHost(with(bad)) {
			t.Errorf("isTrustedHost(%q) = true, want false", bad)
		}
	}
	// Without a listener there is no "own" address at all.
	bare := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	bare.Host = "localhost:7777"
	if isTrustedHost(bare) {
		t.Error("a request that arrived on no listener must not pass as the daemon's own")
	}
}

// TestTrustedHostsFollowTheOptInList: an opted-in origin's host[:port] is an
// accepted Host — bare and with the default port spelled out — and nothing
// else on that name is.
func TestTrustedHostsFollowTheOptInList(t *testing.T) {
	AttachTrustedOrigins([]string{"http://swarmery:7777", "https://dash.example", "http://[::1]:9999"})
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	with := func(host string) *http.Request {
		r := localRequest(t, http.MethodGet, "/api/sessions", nil)
		r.Host = host
		return r
	}

	for _, ok := range []string{"swarmery:7777", "Swarmery:7777", "dash.example", "dash.example:443", "[::1]:9999"} {
		if !isTrustedHost(with(ok)) {
			t.Errorf("isTrustedHost(%q) = false, want true (host of an opted-in origin)", ok)
		}
	}
	for _, bad := range []string{"swarmery", "swarmery:9999", "dash.example:80", "dash.example:7777", "[::1]:9998"} {
		if isTrustedHost(with(bad)) {
			t.Errorf("isTrustedHost(%q) = true, want false", bad)
		}
	}
}

// TestRequireLocalHostFence: through the middleware — a foreign Host is 403
// with the API's {"error"} body and the handler never runs; the daemon's own
// address passes.
func TestRequireLocalHostFence(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })

	var called int
	h := requireLocalHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	do := func(host string) *httptest.ResponseRecorder {
		req := localRequest(t, http.MethodGet, "/api/sessions", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do("attacker.example:7777")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a foreign Host", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("403 body = %q, want the API's {\"error\": …} convention", rec.Body.String())
	}
	if called != 0 {
		t.Errorf("handler ran %d time(s) behind a rejected Host", called)
	}
	if rec := do("localhost:7777"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for the daemon's own address", rec.Code)
	}
	if called != 1 {
		t.Errorf("handler ran %d time(s), want 1", called)
	}
}

// TestHostFenceEndToEnd: through NewServer on a real listener — the shape a
// DNS-rebinding read takes (a GET with the attacker's name in Host) is refused
// on an API read, the health probe and the SPA shell alike; the same GETs by
// the daemon's own address succeed; "localhost" on a port the daemon does not
// serve is refused; an opted-in alias is reachable by its host, on its port.
func TestHostFenceEndToEnd(t *testing.T) {
	AttachTrustedOrigins(nil)
	t.Cleanup(func() { AttachTrustedOrigins(nil) })
	srv := testServer(t)
	port, other := serverPort(t, srv), otherPort(t, srv)

	get := func(path, host string) int {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	for _, path := range []string{"/api/health", "/api/sessions"} {
		if got := get(path, ""); got != http.StatusOK {
			t.Errorf("GET %s as dialled (Host 127.0.0.1:%s) = %d, want 200", path, port, got)
		}
		if got := get(path, "localhost:"+port); got != http.StatusOK {
			t.Errorf("GET %s with Host localhost:%s = %d, want 200", path, port, got)
		}
	}
	for _, path := range []string{"/api/health", "/api/sessions", "/"} {
		if got := get(path, "attacker.example:"+port); got != http.StatusForbidden {
			t.Errorf("GET %s with a rebinding Host = %d, want 403", path, got)
		}
		if got := get(path, "localhost:"+other); got != http.StatusForbidden {
			t.Errorf("GET %s with Host localhost:%s (not the bound port) = %d, want 403", path, other, got)
		}
	}
	// The SPA shell by the daemon's own address is whatever the embed holds
	// (index.html, or 404 "SPA not built" in a test tree) — never the fence.
	if got := get("/", ""); got == http.StatusForbidden {
		t.Error("GET / by the daemon's own address hit the Host fence")
	}

	AttachTrustedOrigins([]string{"http://swarmery:" + port})
	if got := get("/api/health", "swarmery:"+port); got != http.StatusOK {
		t.Errorf("GET /api/health with the opted-in alias as Host = %d, want 200", got)
	}
	if got := get("/api/health", "swarmery:"+other); got != http.StatusForbidden {
		t.Errorf("GET /api/health with the alias on another port = %d, want 403", got)
	}
}
