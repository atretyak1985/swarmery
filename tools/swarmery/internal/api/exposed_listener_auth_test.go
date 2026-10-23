package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// An exposed listener (D7: a non-loopback --bind, or a connection that arrived
// on a non-loopback address) serves only signed clients. Every /api/* request,
// reads included, needs a Swarmery-HMAC signature there; the legacy Bearer
// scheme, which would carry the link token in cleartext, is refused; and a
// dashboard session authorises nothing. GET /api/health and the SPA's static
// shell carry no data and stay open. On loopback nothing changes, except that
// an accepted Bearer answer says it is deprecated.

const exposedLAN = "192.168.1.5:7777"

// exposedBearerRefusal is the exact 401 body of a Bearer request on an
// exposed listener.
const exposedBearerRefusal = `{"error":"the Bearer scheme is refused on an exposed listener; sign the request (swarmery api)"}`

// exposedDo drives the route table as a connection that arrived on the LAN
// listener. hdr holds extra request headers (Authorization, a session, …).
func exposedDo(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := listenerRequest(t, method, path, exposedLAN, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	return rec
}

// (a) A valid legacy Bearer token is refused on an exposed listener — reads,
// writes and the link routes alike — with the exact refusal body.
func TestExposedListenerRefusesBearer(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)
	bearer := map[string]string{"Authorization": "Bearer " + key}

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/sessions", ""},
		{http.MethodPost, "/api/routines", originlessRoutineBody},
		{http.MethodGet, "/api/link/health", ""},
		{http.MethodPost, "/api/hooks/stop", `{"session_id":"x","hook_event_name":"Stop"}`},
	} {
		rec := exposedDo(t, srv, c.method, c.path, c.body, bearer)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("exposed %s %s with a valid Bearer = %d %s, want 401", c.method, c.path, rec.Code, rec.Body.String())
			continue
		}
		if got := strings.TrimSpace(rec.Body.String()); got != exposedBearerRefusal {
			t.Errorf("exposed %s %s Bearer refusal body = %s, want %s", c.method, c.path, got, exposedBearerRefusal)
		}
	}
	if n := routineCount(t, srv); n != 0 {
		t.Errorf("a refused Bearer request created %d routine(s)", n)
	}
}

// (b) A signed write still works on an exposed listener.
func TestExposedListenerSignedWriteWorks(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	auth := signedAuth(t, key, http.MethodPost, "/api/routines", originlessRoutineBody)
	rec := exposedDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, map[string]string{"Authorization": auth})
	if rec.Code != http.StatusCreated {
		t.Fatalf("exposed signed POST /api/routines = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if n := routineCount(t, srv); n != 1 {
		t.Errorf("routines created = %d, want 1", n)
	}
}

// (c) Unauthenticated reads are refused on an exposed listener, the serena
// proxy's GETs included — and the refused GET never reaches the upstream.
func TestExposedListenerUnauthenticatedReadsAre401(t *testing.T) {
	srv, _ := projectsTestServer(t)
	key := attachHookToken(t)

	var mu sync.Mutex
	var hits int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(backend.Close)
	startProxyStub(t, backend.URL)

	const serenaGet = "/api/projects/1/serena/get_log_messages"
	for _, path := range []string{"/api/sessions", "/api/approvals", "/api/projects", serenaGet} {
		rec := exposedDo(t, srv, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("exposed unauthenticated GET %s = %d %s, want 401", path, rec.Code, rec.Body.String())
		}
	}
	mu.Lock()
	if hits != 0 {
		t.Errorf("backend hits after a refused GET = %d, want 0", hits)
	}
	mu.Unlock()

	// The same proxy GET, signed, reaches the upstream.
	rec := exposedDo(t, srv, http.MethodGet, serenaGet, "", map[string]string{"Authorization": signedAuth(t, key, http.MethodGet, serenaGet, "")})
	if rec.Code != http.StatusNoContent {
		t.Errorf("exposed signed serena GET = %d %s, want the backend's 204", rec.Code, rec.Body.String())
	}
}

// (d) GET /api/health and the SPA shell carry no data: open on an exposed
// listener. (The test binary embeds an unbuilt SPA, so the shell answers the
// handler's own "not built" 404 — what matters is that the auth gate never
// sees it; the live script checks the 200 of a built binary.)
func TestExposedListenerHealthAndShellOpen(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	attachHookToken(t)

	if rec := exposedDo(t, srv, http.MethodGet, "/api/health", "", nil); rec.Code != http.StatusOK {
		t.Errorf("exposed GET /api/health = %d %s, want 200", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/", "/index.html", "/assets/index-abc.js", "/favicon.svg", "/sessions/42"} {
		rec := exposedDo(t, srv, http.MethodGet, path, "", nil)
		if rec.Code == http.StatusUnauthorized || strings.Contains(rec.Body.String(), "link token required") {
			t.Errorf("exposed GET %s = %d %s, want the SPA handler's answer, not the auth gate", path, rec.Code, rec.Body.String())
		}
	}
}

// (e) A signed read works on an exposed listener.
func TestExposedListenerSignedReadWorks(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	rec := exposedDo(t, srv, http.MethodGet, "/api/sessions", "", map[string]string{"Authorization": signedAuth(t, key, http.MethodGet, "/api/sessions", "")})
	if rec.Code != http.StatusOK {
		t.Errorf("exposed signed GET /api/sessions = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// (f) A valid dashboard session authorises nothing on an exposed listener,
// not even a read.
func TestExposedListenerSessionDoesNotAuthoriseReads(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	_, s := mintTestSession(t)

	rec := exposedDo(t, srv, http.MethodGet, "/api/sessions", "", map[string]string{
		SessionHeader: s,
		"Origin":      "http://" + exposedLAN,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("exposed GET /api/sessions with a valid session = %d %s, want 401", rec.Code, rec.Body.String())
	}
}

// (g) On loopback the legacy Bearer still works, and its answer is marked
// deprecated (removed in swarmery-v0.3.0).
func TestLoopbackBearerDeprecated(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/hooks/stop", strings.NewReader(`{"session_id":"x","hook_event_name":"Stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("loopback Bearer POST /api/hooks/stop = %d %s, want 202", resp.StatusCode, b)
	}
	if got := resp.Header.Get("Deprecation"); got != "true" {
		t.Errorf("loopback Bearer answer Deprecation = %q, want \"true\"", got)
	}

	// A signed request is not deprecated.
	body := `{"session_id":"y","hook_event_name":"Stop"}`
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/hooks/stop", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", signedAuth(t, key, http.MethodPost, "/api/hooks/stop", body))
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted || resp2.Header.Get("Deprecation") != "" {
		t.Errorf("loopback signed POST = %d Deprecation %q, want 202 and no Deprecation", resp2.StatusCode, resp2.Header.Get("Deprecation"))
	}
}

// (h) On loopback unauthenticated reads still work, with the gate on.
func TestLoopbackUnauthenticatedReadsOpen(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	attachHookToken(t)

	for _, path := range []string{"/api/sessions", "/api/approvals", "/api/projects", "/api/health"} {
		if status, body := originlessDo(t, srv, http.MethodGet, path, "", ""); status != http.StatusOK {
			t.Errorf("loopback unauthenticated GET %s = %d %s, want 200", path, status, body)
		}
	}
}
