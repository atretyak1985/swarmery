package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

// The dashboard authenticates its writes with a session token: a request that
// carries the daemon's own Origin is no proof of a browser on loopback either —
// any local process can write that header — so a trusted-Origin write or
// upgrade must also carry a dashboard session (or link auth). The session is
// minted by POST /api/session/unlock in exchange for a short-lived ticket
// signed with the link token (`swarmery open`, the notch).

// mintTestSession attaches a fresh link token and returns it with a valid
// dashboard session under it.
func mintTestSession(t *testing.T) (key, session string) {
	t.Helper()
	key = attachHookToken(t)
	s, err := linksig.MintSession(key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return key, s
}

// originDo sends one request with the given Origin ("" = none), dashboard
// session ("" = none) and Authorization ("" = none).
func originDo(t *testing.T, srv *httptest.Server, method, path, body, origin, session, auth string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if session != "" {
		req.Header.Set(SessionHeader, session)
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

func errorCode(body string) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	return e.Code
}

// (a) A trusted-Origin write with no session is locked out.
func TestDashboardWriteWithoutSessionIsLocked(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	attachHookToken(t)

	status, body := originDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, srv.URL, "", "")
	if status != http.StatusUnauthorized || errorCode(body) != "dashboard_locked" {
		t.Fatalf("own-Origin POST without a session = %d %s, want 401 dashboard_locked", status, body)
	}
	if !strings.Contains(body, "swarmery open") {
		t.Errorf("locked answer %s does not say how to unlock", body)
	}
	if n := routineCount(t, srv); n != 0 {
		t.Errorf("a refused request created %d routine(s)", n)
	}
}

// The gate is the daemon's, not a test switch: it holds with the legacy
// Origin-less switch off too.
func TestDashboardSessionGateIndependentOfLegacySwitch(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	attachHookToken(t)
	if status, body := originDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, srv.URL, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("own-Origin POST without a session (legacy switch off) = %d %s, want 401", status, body)
	}
}

// (b) A valid session passes the gate to the handler.
func TestDashboardWriteWithSessionPasses(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	_, s := mintTestSession(t)

	if status, body := originDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, srv.URL, s, ""); status != http.StatusCreated {
		t.Fatalf("own-Origin POST with a session = %d %s, want 201", status, body)
	}
	// A session is not single-use: the dashboard sends it on every write.
	if status, body := originDo(t, srv, http.MethodPost, "/api/routines", `{"name":"x"}`, srv.URL, s, ""); status == http.StatusUnauthorized {
		t.Fatalf("second write with the same session = 401 %s", body)
	}
}

// (c) Expired, wrong-key and garbage sessions are refused.
func TestDashboardWriteRejectsBadSessions(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	expired, err := linksig.MintSession(key, time.Now().Add(-linksig.SessionTTL-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := linksig.MintSession(strings.Repeat("0", 64), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{
		"expired":   expired,
		"wrong key": wrongKey,
		"garbage":   "v1.garbage",
		"the token": key,
	} {
		status, body := originDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, srv.URL, s, "")
		if status != http.StatusUnauthorized || errorCode(body) != "dashboard_locked" {
			t.Errorf("%s session: %d %s, want 401 dashboard_locked", name, status, body)
		}
	}
	if n := routineCount(t, srv); n != 0 {
		t.Errorf("refused requests created %d routine(s)", n)
	}
}

// Plain reads stay open on loopback, Origin or not.
func TestDashboardPlainReadNeedsNoSession(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	attachHookToken(t)
	if status, body := originDo(t, srv, http.MethodGet, "/api/routines", "", srv.URL, "", ""); status != http.StatusOK {
		t.Fatalf("own-Origin GET without a session = %d %s, want 200", status, body)
	}
}

func sessionProtocols(s string) []string {
	return []string{WSSubprotocol, WSSessionPrefix + s}
}

// (d) The terminal upgrade needs a session in the subprotocol list, and the
// daemon selects exactly swarmery.v1 — never the entry carrying the token.
func TestTermUpgradeNeedsDashboardSession(t *testing.T) {
	enableOriginlessAuth(t)
	srv, projectDir, _ := termTestServer(t)
	_, s := mintTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, resp, err := websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{HTTPHeader: localOrigin(srv)})
	if err == nil {
		c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("own-Origin terminal upgrade without a session connected, want refused")
	}
	if resp == nil || resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatalf("no-session terminal upgrade: resp %v, want a non-101 status", resp)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-session terminal upgrade = %d, want 401", resp.StatusCode)
	}

	c, resp, err = websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: []string{WSSubprotocol, WSSessionPrefix + "v1.1.bad.bad"},
	})
	if err == nil {
		c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("terminal upgrade with a garbage session connected")
	}

	c, resp, err = websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: sessionProtocols(s),
	})
	if err != nil {
		t.Fatalf("terminal upgrade with a session: %v (resp %v)", err, resp)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != WSSubprotocol {
		t.Errorf("101 Sec-WebSocket-Protocol = %q, want %q", got, WSSubprotocol)
	}
	if c.Subprotocol() != WSSubprotocol {
		t.Errorf("negotiated subprotocol = %q, want %q", c.Subprotocol(), WSSubprotocol)
	}
}

// (d') Origin-less terminal upgrades stay refused, session or not: the
// terminal is browser-only.
func TestTermOriginlessStaysRefused(t *testing.T) {
	enableOriginlessAuth(t)
	srv, projectDir, _ := termTestServer(t)
	_, s := mintTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{Subprotocols: sessionProtocols(s)})
	if err == nil {
		c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("Origin-less terminal upgrade with a session connected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Origin-less terminal upgrade: resp %v, want 403", resp)
	}
}

// (e) The event stream upgrade: same pair.
func TestEventStreamUpgradeNeedsDashboardSession(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	_, s := mintTestSession(t)
	AttachBus(ingest.NewBus())
	t.Cleanup(func() { AttachBus(nil) })
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: localOrigin(srv)})
	if err == nil {
		c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("own-Origin /api/ws upgrade without a session connected, want refused")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-session /api/ws upgrade: resp %v, want 401", resp)
	}

	c, resp, err = websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: sessionProtocols(s),
	})
	if err != nil {
		t.Fatalf("/api/ws upgrade with a session: %v (resp %v)", err, resp)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != WSSubprotocol {
		t.Errorf("101 Sec-WebSocket-Protocol = %q, want %q", got, WSSubprotocol)
	}
}

// unlockDo POSTs a ticket to /api/session/unlock with the given Origin.
func unlockDo(t *testing.T, srv *httptest.Server, origin, ticket string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"ticket": ticket})
	return originDo(t, srv, http.MethodPost, "/api/session/unlock", string(body), origin, "", "")
}

// (f) The unlock exchange.
func TestSessionUnlock(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	ticket, err := linksig.UnlockTicket(key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	status, body := unlockDo(t, srv, srv.URL, ticket)
	if status != http.StatusOK {
		t.Fatalf("unlock with a fresh ticket = %d %s, want 200", status, body)
	}
	var got struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Session == "" {
		t.Fatalf("unlock answer %s: want {\"session\":…}", body)
	}
	if err := linksig.VerifySession(key, got.Session, time.Now()); err != nil {
		t.Fatalf("minted session does not verify: %v", err)
	}
	// The session unlocks writes.
	if status, body := originDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, srv.URL, got.Session, ""); status != http.StatusCreated {
		t.Errorf("write with the unlocked session = %d %s, want 201", status, body)
	}

	// A ticket is single-use.
	if status, body := unlockDo(t, srv, srv.URL, ticket); status != http.StatusUnauthorized || errorCode(body) != "unlock_invalid" {
		t.Errorf("replayed ticket = %d %s, want 401 unlock_invalid", status, body)
	}

	stale, _ := linksig.UnlockTicket(key, time.Now().Add(-121*time.Second))
	future, _ := linksig.UnlockTicket(key, time.Now().Add(121*time.Second))
	wrongKey, _ := linksig.UnlockTicket(strings.Repeat("0", 64), time.Now())
	for name, tk := range map[string]string{
		"stale": stale, "future": future, "wrong key": wrongKey,
		"garbage": "garbage", "empty": "",
	} {
		if status, body := unlockDo(t, srv, srv.URL, tk); status != http.StatusUnauthorized || errorCode(body) != "unlock_invalid" {
			t.Errorf("%s ticket = %d %s, want 401 unlock_invalid", name, status, body)
		}
	}

	fresh, _ := linksig.UnlockTicket(key, time.Now())
	if status, body := unlockDo(t, srv, "http://evil.example", fresh); status != http.StatusForbidden {
		t.Errorf("foreign Origin unlock = %d %s, want 403", status, body)
	}
	if status, body := unlockDo(t, srv, "", fresh); status != http.StatusForbidden && status != http.StatusBadRequest {
		t.Errorf("Origin-less unlock = %d %s, want 400 or 403", status, body)
	}
	// Neither refusal spent the ticket.
	if status, body := unlockDo(t, srv, srv.URL, fresh); status != http.StatusOK {
		t.Errorf("fresh ticket after refused attempts = %d %s, want 200", status, body)
	}
}

// (f') An exposed listener mints no sessions.
func TestSessionUnlockRefusedOnExposedListener(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)
	const lan = "192.168.1.5:7777"

	ticket, _ := linksig.UnlockTicket(key, time.Now())
	body, _ := json.Marshal(map[string]string{"ticket": ticket})
	req := listenerRequest(t, http.MethodPost, "/api/session/unlock", lan, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+lan)
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unlock on an exposed listener = %d %s, want 401", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"session"`) {
		t.Errorf("exposed listener answered a session: %s", rec.Body.String())
	}
}

// (g) Link auth is accepted wherever a session is: a signed request with the
// daemon's own Origin passes, a write and the terminal alike.
func TestDashboardGateAcceptsLinkAuth(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	key := attachHookToken(t)

	auth := signedAuth(t, key, http.MethodPost, "/api/routines", originlessRoutineBody)
	if status, body := originDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, srv.URL, "", auth); status != http.StatusCreated {
		t.Fatalf("own-Origin signed POST = %d %s, want 201", status, body)
	}
	// A wrong signature is judged by link auth (401), and a valid session
	// does not rescue it: an Authorization header means link auth.
	s, err := linksig.MintSession(key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bad := signedAuth(t, strings.Repeat("0", 64), http.MethodPost, "/api/routines", originlessRoutineBody)
	if status, _ := originDo(t, srv, http.MethodPost, "/api/routines", originlessRoutineBody, srv.URL, s, bad); status != http.StatusUnauthorized {
		t.Errorf("own-Origin POST with a bad signature and a session = %d, want 401", status)
	}
}

func TestTermAcceptsLinkAuth(t *testing.T) {
	enableOriginlessAuth(t)
	srv, projectDir, _ := termTestServer(t)
	key := attachHookToken(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	u := termWSURL(srv, projectDir)
	target := strings.TrimPrefix(u, strings.Replace(srv.URL, "http://", "ws://", 1))
	h := localOrigin(srv)
	h.Set("Authorization", signedAuth(t, key, http.MethodGet, target, ""))
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("own-Origin signed terminal upgrade: %v (resp %v)", err, resp)
	}
	c.Close(websocket.StatusNormalClosure, "")
}

// (h) On an exposed listener a session authorises nothing: writes there need
// link auth (D7).
func TestExposedListenerIgnoresSession(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	_, s := mintTestSession(t)
	const lan = "192.168.1.5:7777"

	req := listenerRequest(t, http.MethodPost, "/api/routines", lan, strings.NewReader(originlessRoutineBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+lan)
	req.Header.Set(SessionHeader, s)
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("exposed listener, own Origin, valid session = %d %s, want 401", rec.Code, rec.Body.String())
	}
	if n := routineCount(t, srv); n != 0 {
		t.Errorf("a refused request created %d routine(s)", n)
	}
}
