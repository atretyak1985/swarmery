package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExposedListenerSessionRefusalBody: on an exposed listener the unsigned
// refusal (requireSignedWhenExposed) is 401 either way, and its body names the
// locked dashboard exactly when the request presents a dashboard session — the
// X-Swarmery-Session header, or the swarmery.session.<token> subprotocol entry
// on an upgrade. A request with no credential keeps the plain body, no code.
func TestExposedListenerSessionRefusalBody(t *testing.T) {
	enableOriginlessAuth(t)
	srv, _, _ := serverWithRoutines(t)
	_, s := mintTestSession(t)
	const lan = "192.168.1.5:7777"

	for name, tc := range map[string]struct {
		path     string
		header   func(h http.Header)
		wantCode string
	}{
		"session header": {"/api/session", func(h http.Header) {
			h.Set(SessionHeader, s)
		}, "dashboard_locked"},
		"session subprotocol on an upgrade": {"/api/ws", func(h http.Header) {
			h.Set("Connection", "Upgrade")
			h.Set("Upgrade", "websocket")
			h.Set("Sec-WebSocket-Version", "13")
			h.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			h.Set("Sec-WebSocket-Protocol", WSSubprotocol+", "+WSSessionPrefix+s)
		}, "dashboard_locked"},
		"no credential": {"/api/session", func(http.Header) {}, ""},
	} {
		req := listenerRequest(t, http.MethodGet, tc.path, lan, nil)
		tc.header(req.Header)
		rec := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(rec, req)
		body := strings.TrimSpace(rec.Body.String())
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %s on an exposed listener = %d %s, want 401", name, tc.path, rec.Code, body)
			continue
		}
		if got := errorCode(body); got != tc.wantCode {
			t.Errorf("%s: 401 code = %q (%s), want %q", name, got, body, tc.wantCode)
		}
		if !strings.Contains(body, "beyond loopback") {
			t.Errorf("%s: 401 body %s, want the exposed-listener error text", name, body)
		}
	}
}
