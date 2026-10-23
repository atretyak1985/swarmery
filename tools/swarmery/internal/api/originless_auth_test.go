package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

// Non-browser clients authenticate to the daemon (D2): a state-changing
// request that carries no Origin must carry link auth — a Swarmery-HMAC
// signature, or the legacy bearer [REDACTED:bearer] the transition. A browser's own
// Origin keeps the origin fence it always had, and plain reads stay open.

// enableOriginlessAuth turns the Origin-less gate on for one test. The api
// TestMain turns it off for the tests written before the gate existed.
func enableOriginlessAuth(t *testing.T) {
	t.Helper()
	originlessAuthRequired = true
	t.Cleanup(func() { originlessAuthRequired = false })
}

const originlessRoutineBody = `{"name":"originless","steps":[{"type":"command","name":"c","command":"true"}]}`

func randomNonce(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// originlessDo sends one request with no Origin header. auth is the
// Authorization value ("" = none).
func originlessDo(t *testing.T, srv *httptest.Server, method, path, body, auth string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func signedAuth(t *testing.T, key, method, path, body string) string {
	t.Helper()
	return linksig.Authorization(key, method, path, time.Now().Unix(), randomNonce(t), []byte(body))
}

func routineCount(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	status, body := originlessDo(t, srv, http.MethodGet, "/api/routines", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/routines = %d %s", status, body)
	}
	return strings.Count(body, `"name":"originless"`)
}

func TestOriginlessWriteWithoutAuthIs401(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	attachHookToken(t)

	status, body := originlessDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("Origin-less POST /api/routines without auth = %d %s, want 401", status, body)
	}
	if n := routineCount(t, srv); n != 0 {
		t.Errorf("a refused request created %d routine(s)", n)
	}
}

func TestOriginlessWriteSignedIsAccepted(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	auth := signedAuth(t, key, http.MethodPost, "/api/routines", originlessRoutineBody)
	if status, body := originlessDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, auth); status != http.StatusCreated {
		t.Fatalf("signed Origin-less POST /api/routines = %d %s, want 201", status, body)
	}
	// The same signed request again is a replay.
	if status, _ := originlessDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, auth); status != http.StatusUnauthorized {
		t.Errorf("replayed signed request = %d, want 401", status)
	}
	if n := routineCount(t, srv); n != 1 {
		t.Errorf("routines created = %d, want 1", n)
	}
}

func TestOriginlessWriteRejectsBadSignature(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	wrongKey := signedAuth(t, strings.Repeat("0", 64), http.MethodPost, "/api/routines", originlessRoutineBody)
	otherBody := signedAuth(t, key, http.MethodPost, "/api/routines", `{"name":"something else"}`)
	otherPath := signedAuth(t, key, http.MethodPost, "/api/routines/x/run", originlessRoutineBody)
	stale := linksig.Authorization(key, http.MethodPost, "/api/routines", time.Now().Add(-5*time.Minute).Unix(), randomNonce(t), []byte(originlessRoutineBody))
	for name, auth := range map[string]string{
		"wrong key": wrongKey, "signed over another body": otherBody,
		"signed over another path": otherPath, "outside the skew": stale,
		"wrong bearer": "Bearer " + strings.Repeat("f", 64),
	} {
		if status, _ := originlessDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, auth); status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, status)
		}
	}
	if n := routineCount(t, srv); n != 0 {
		t.Errorf("refused requests created %d routine(s)", n)
	}
}

// TestOriginlessWriteLegacyBearerAccepted: during the transition the raw link
// token in a Bearer header still authenticates (a follow-up drops it).
func TestOriginlessWriteLegacyBearerAccepted(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	if status, body := originlessDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, "Bearer "+key); status != http.StatusCreated {
		t.Fatalf("legacy bearer [REDACTED:bearer] = %d %s, want 201", status, body)
	}
}

// TestOriginlessBrowserOriginUnchanged: a request that carries the daemon's own
// Origin passes with a dashboard session instead of link auth (and is locked
// without one, session_test.go); a foreign Origin is 403 whatever it carries.
func TestOriginlessBrowserOriginUnchanged(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)
	session := testDashboardSession(t)

	do := func(origin, auth string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/routines", strings.NewReader(originlessRoutineBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set(SessionHeader, session)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := do(srv.URL, ""); got != http.StatusCreated {
		t.Errorf("own Origin with a session, without link auth = %d, want 201", got)
	}
	if got := do("http://evil.example", ""); got != http.StatusForbidden {
		t.Errorf("foreign Origin = %d, want 403", got)
	}
	if got := do("http://evil.example", signedAuth(t, key, http.MethodPost, "/api/routines", originlessRoutineBody)); got != http.StatusForbidden {
		t.Errorf("foreign Origin with a valid signature = %d, want 403", got)
	}
}

// TestOriginlessPlainReadUnaffected: reads never need link auth — including a
// fenced GET (a same-origin browser GET carries no Origin at all).
func TestOriginlessPlainReadUnaffected(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	attachHookToken(t)

	if status, body := originlessDo(t, srv, http.MethodGet, "/api/routines", "", ""); status != http.StatusOK {
		t.Errorf("GET /api/routines = %d %s, want 200", status, body)
	}
	// The fenced vcs login poll answers from its handler (404: no such login),
	// not from the auth gate.
	if status, body := originlessDo(t, srv, http.MethodGet, "/api/projects/1/vcs/login/nope", "", ""); status == http.StatusUnauthorized {
		t.Errorf("fenced GET without an Origin = 401 %s, want the handler's answer", body)
	}
}

// TestOriginlessWebSocketUpgradeNeedsAuth: the event stream upgrade is gated
// like a write; a signed dial connects.
func TestOriginlessWebSocketUpgradeNeedsAuth(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)
	AttachBus(ingest.NewBus())
	t.Cleanup(func() { AttachBus(nil) })

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("unsigned Origin-less /api/ws dial connected, want 401")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned dial: resp %v err %v, want 401", resp, err)
	}

	h := http.Header{}
	h.Set("Authorization", signedAuth(t, key, http.MethodGet, "/api/ws", ""))
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("signed /api/ws dial: %v", err)
	}
	conn.Close(websocket.StatusNormalClosure, "")
}

// TestOriginlessHookRouteVerifiesOnce: the shim routes sit behind both the
// Origin-less gate and hookAuth. One signed request must pass both (the nonce
// is checked once, not replayed against itself) and get a signed answer.
func TestOriginlessHookRouteVerifiesOnce(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	body := `{"session_id":"x","hook_event_name":"Stop"}`
	nonce := randomNonce(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/hooks/stop", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", linksig.Authorization(key, http.MethodPost, "/api/hooks/stop", time.Now().Unix(), nonce, []byte(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("signed POST /api/hooks/stop = %d %s, want 202", resp.StatusCode, answer)
	}
	if !linksig.VerifyResponse(key, nonce, resp.StatusCode, answer, resp.Header.Get(linksig.ResponseHeader)) {
		t.Error("the hook answer is not signed over the request's nonce")
	}
}

// TestOriginlessWriteWithoutLinkIsRefused: a daemon that has no link token
// refuses Origin-less writes instead of falling back to trust.
func TestOriginlessWriteWithoutLinkIsRefused(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	AttachLink(nil, nil)

	status, body := originlessDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, "")
	if status != http.StatusServiceUnavailable && status != http.StatusUnauthorized {
		t.Fatalf("Origin-less POST with no link = %d %s, want 503 or 401", status, body)
	}
	if n := routineCount(t, srv); n != 0 {
		t.Errorf("a refused request created %d routine(s)", n)
	}
}

// TestOriginlessSerenaProxyWrites: an Origin-less write through the serena
// proxy needs link auth like any other, and the credential it carried is
// never relayed to the upstream.
func TestOriginlessSerenaProxyWrites(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _ := projectsTestServer(t)
	key := attachHookToken(t)

	var mu sync.Mutex
	var hits int
	var gotAuth []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(backend.Close)
	startProxyStub(t, backend.URL)

	const path = "/api/projects/1/serena/clear_logs"
	if status, _ := originlessDo(t, srv, http.MethodPost, path, `{}`, ""); status != http.StatusUnauthorized {
		t.Errorf("unsigned Origin-less proxy POST = %d, want 401", status)
	}
	for name, auth := range map[string]string{
		"signed":        signedAuth(t, key, http.MethodPost, path, `{}`),
		"legacy bearer": "Bearer " + key,
	} {
		if status, body := originlessDo(t, srv, http.MethodPost, path, `{}`, auth); status != http.StatusNoContent {
			t.Errorf("%s proxy POST = %d %s, want the backend's 204", name, status, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Errorf("backend hits = %d, want 2 (the refused POST must not reach it)", hits)
	}
	for i, a := range gotAuth {
		if a != "" {
			t.Errorf("request %d relayed Authorization %q to the upstream", i, a)
		}
	}
}

// exposedPost drives the route table as a connection that arrived on listen
// (a listener address, e.g. a LAN address) with the given Origin and auth.
func exposedPost(t *testing.T, srv *httptest.Server, listen, origin, auth string) (int, string) {
	t.Helper()
	req := listenerRequest(t, http.MethodPost, "/api/routines", listen, strings.NewReader(originlessRoutineBody))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// TestExposedListenerOwnOriginNeedsAuth (D7): on a listener beyond loopback an
// Origin header proves nothing — any client can send the daemon's own — so a
// write needs link auth with or without one.
func TestExposedListenerOwnOriginNeedsAuth(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)
	const lan = "192.168.1.5:7777"

	status, body := exposedPost(t, srv, lan, "http://"+lan, "")
	if status != http.StatusUnauthorized || !strings.Contains(body, "beyond loopback") {
		t.Errorf("exposed listener, own Origin, no auth = %d %s, want 401 with the loopback hint", status, body)
	}
	if status, _ := exposedPost(t, srv, lan, "", ""); status != http.StatusUnauthorized {
		t.Errorf("exposed listener, no Origin, no auth = %d, want 401", status)
	}
	if status, _ := exposedPost(t, srv, lan, "http://"+lan, "Bearer "+strings.Repeat("f", 64)); status != http.StatusUnauthorized {
		t.Errorf("exposed listener, own Origin, wrong bearer = %d, want 401", status)
	}
	if status, body := exposedPost(t, srv, lan, "http://"+lan, signedAuth(t, key, http.MethodPost, "/api/routines", originlessRoutineBody)); status != http.StatusCreated {
		t.Errorf("exposed listener, own Origin, signed = %d %s, want 201", status, body)
	}
	// Loopback takes the dashboard's session instead (session_test.go): the
	// own Origin alone is locked.
	if status, body := exposedPost(t, srv, "127.0.0.1:7777", "http://127.0.0.1:7777", ""); status != http.StatusUnauthorized || !strings.Contains(body, "dashboard_locked") {
		t.Errorf("loopback listener, own Origin, no auth = %d %s, want 401 dashboard_locked", status, body)
	}
}

// TestExposedBindCoversLoopbackConnections: with a non-loopback --bind (a
// wildcard bind also accepts 127.0.0.1) even a loopback connection's Origin is
// not trusted for writes.
func TestExposedBindCoversLoopbackConnections(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	attachHookToken(t)
	AttachExposedBind(true)
	t.Cleanup(func() { AttachExposedBind(false) })

	if status, _ := exposedPost(t, srv, "127.0.0.1:7777", "http://127.0.0.1:7777", ""); status != http.StatusUnauthorized {
		t.Errorf("exposed bind, loopback connection, own Origin, no auth = %d, want 401", status)
	}
	if status, _ := originlessDo(t, srv, http.MethodGet, "/api/routines", "", ""); status != http.StatusOK {
		t.Errorf("exposed bind, plain GET = %d, want 200 (reads stay open)", status)
	}
}

// TestOriginNullIsForbidden: an opaque origin (a sandboxed frame, a file: page)
// is foreign, signed or not.
func TestOriginNullIsForbidden(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)
	if status, _ := exposedPost(t, srv, "127.0.0.1:7777", "null", ""); status != http.StatusForbidden {
		t.Errorf("Origin: null = %d, want 403", status)
	}
	if status, _ := exposedPost(t, srv, "127.0.0.1:7777", "null", signedAuth(t, key, http.MethodPost, "/api/routines", originlessRoutineBody)); status != http.StatusForbidden {
		t.Errorf("Origin: null with a valid signature = %d, want 403", status)
	}
}
