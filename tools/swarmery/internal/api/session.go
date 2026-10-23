// Dashboard sessions: how the browser dashboard authenticates its writes.
//
// On a loopback listener a request that carries the daemon's own Origin used to
// pass the write fence on that header alone. But Origin is only evidence of a
// browser: any local process can send it. So a trusted-Origin write or
// WebSocket upgrade must also carry a dashboard session token — or link auth,
// which is accepted wherever a session is.
//
// The dashboard never holds the link token. A holder of it (`swarmery open`,
// the notch) signs a short-lived unlock ticket and hands it to the browser in
// the URL fragment; the SPA redeems it once at POST /api/session/unlock for a
// session token (internal/linksig: both are HMAC-SHA256 under the link token,
// stateless, and a token rotation invalidates them all). The SPA then sends
//
//	X-Swarmery-Session: <session>                         on HTTP writes
//	Sec-WebSocket-Protocol: swarmery.v1, swarmery.session.<session>  on upgrades
//
// and the daemon selects exactly swarmery.v1 on the 101, never the entry that
// carries the token. No cookie, no query string: the token never rides along
// on a request the page did not build itself.
//
// The session is a loopback credential only. On an exposed listener (D7) it
// is not accepted and no session is minted: every request there, reads
// included, needs link auth (requireSignedWhenExposed, origin.go).
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

const (
	// SessionHeader carries the dashboard session on an HTTP write.
	SessionHeader = "X-Swarmery-Session"
	// WSSubprotocol is the subprotocol every dashboard WebSocket offers and
	// the one the daemon selects.
	WSSubprotocol = "swarmery.v1"
	// WSSessionPrefix marks the subprotocol entry that carries the session
	// on an upgrade (a browser cannot set headers on a WebSocket).
	WSSessionPrefix = "swarmery.session."

	// maxUnlockBody bounds a POST /api/session/unlock body.
	maxUnlockBody = 4 << 10

	dashboardLockedMsg = "dashboard locked: open it with `swarmery open` or from the notch"
)

// wsAcceptProtocols is the subprotocol list the daemon's WebSocket routes
// negotiate: the library selects an entry from THIS list, so the session
// entry a client offers is never echoed.
var wsAcceptProtocols = []string{WSSubprotocol}

// unlockVerifier remembers redeemed ticket nonces, so a ticket unlocks once.
var unlockVerifier = linksig.NewUnlockVerifier()

// presentedSession is the session token a request carries: the header, or —
// on a WebSocket upgrade — the swarmery.session.<token> subprotocol entry.
func presentedSession(r *http.Request) string {
	if s := strings.TrimSpace(r.Header.Get(SessionHeader)); s != "" {
		return s
	}
	if r.Header.Get("Upgrade") == "" {
		return ""
	}
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if s, ok := strings.CutPrefix(strings.TrimSpace(p), WSSessionPrefix); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// stripDashboardSession removes the dashboard session from headers relayed
// to an upstream (the serena proxy): the header, and the session entry of a
// WebSocket subprotocol list (the other entries stay).
func stripDashboardSession(h http.Header) {
	h.Del(SessionHeader)
	vals := h.Values("Sec-WebSocket-Protocol")
	if len(vals) == 0 {
		return
	}
	var keep []string
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" && !strings.HasPrefix(p, WSSessionPrefix) {
				keep = append(keep, p)
			}
		}
	}
	h.Del("Sec-WebSocket-Protocol")
	if len(keep) > 0 {
		h.Set("Sec-WebSocket-Protocol", strings.Join(keep, ", "))
	}
}

// hasDashboardSession reports whether r carries a session that verifies under
// the current link token. False with no link attached: nothing can verify.
func hasDashboardSession(r *http.Request) bool {
	h := linkHolder
	if h == nil {
		return false
	}
	s := presentedSession(r)
	if s == "" {
		return false
	}
	return linksig.VerifySession(h.Current(), s, time.Now()) == nil
}

// authenticateDashboard is the credential check for a trusted-Origin write or
// upgrade on a loopback listener. A request with an Authorization header is
// judged by link auth (authenticateLink) alone; otherwise it needs a valid
// dashboard session, or it is 401 dashboard_locked. On failure the answer has
// been written.
func authenticateDashboard(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if r.Header.Get("Authorization") != "" {
		r, _, ok := authenticateLink(w, r)
		return r, ok
	}
	if hasDashboardSession(r) {
		return r, true
	}
	writeDashboardLocked(w)
	return r, false
}

func writeDashboardLocked(w http.ResponseWriter) {
	writeJSONStatus(w, http.StatusUnauthorized, map[string]string{
		"error": dashboardLockedMsg,
		"code":  "dashboard_locked",
	})
}

// selfUnlockOrigin is the canonical origin (linksig.UnlockOrigin) of the
// listener this request arrived on — scheme, the listener's own address
// literal and its port — when the request's Origin names exactly that; ok is
// false otherwise: no Origin, a name (localhost, an opted-in alias), another
// loopback address or port, another scheme, or no listener in the context.
func selfUnlockOrigin(r *http.Request) (string, bool) {
	lhost, lport, ok := listenerAddr(r)
	if !ok {
		return "", false
	}
	self, err := linksig.UnlockOrigin(requestScheme(r) + "://" + net.JoinHostPort(lhost, lport))
	if err != nil {
		return "", false
	}
	got, err := linksig.UnlockOrigin(r.Header.Get("Origin"))
	if err != nil || got != self {
		return "", false
	}
	return self, true
}

func writeUnlockInvalid(w http.ResponseWriter, msg string) {
	writeJSONStatus(w, http.StatusUnauthorized, map[string]string{
		"error": msg,
		"code":  "unlock_invalid",
	})
}

// GET /api/session — does the session the dashboard holds still verify? The
// SPA asks at boot and whenever a WebSocket is refused before it opened, so a
// session the daemon no longer accepts (a rotated link token, an expired
// session) is dropped and the locked banner says how to unlock:
//
//	204  X-Swarmery-Session verifies under the current link token
//	401  {"code":"dashboard_locked"} otherwise — and always on an exposed
//	     listener, where a session is never accepted
//	403  a present Origin that is not the daemon's own or opted in
//
// A same-origin browser GET carries no Origin, so none is required.
func (h *Handler) sessionCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if o := r.Header.Get("Origin"); o != "" && !isTrustedOrigin(r, o) {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		return
	}
	if isExposedListener(r) || !hasDashboardSession(r) {
		writeDashboardLocked(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/session/unlock — redeem an unlock ticket for a dashboard session.
// Exempt from the session requirement (it is how a session is obtained), so
// it runs its own fence:
//
//	401  {"code":"unlock_invalid"} on an exposed listener
//	403  no Origin, or one that is not this listener's own address-literal
//	     origin (selfUnlockOrigin)
//	401  {"code":"unlock_invalid"} with no link attached, or for a malformed,
//	     stale, wrongly signed, spent or other-origin ticket
//	200  {"session":"<token>"}
//
// A ticket is bound to the origin it was minted for (linksig.UnlockSig), and
// the daemon verifies it for the request's Origin. That Origin must be the
// listener's own, spelled with its address literal: `localhost` and the
// SWARMERY_TRUSTED_ORIGINS aliases are refused here (and only here — they
// keep working for everything else), because a name may resolve to a
// listener that is not this daemon. A ticket's nonce is spent only once its
// signature verified.
func (h *Handler) sessionUnlock(w http.ResponseWriter, r *http.Request) {
	if isExposedListener(r) {
		writeUnlockInvalid(w, "this daemon listens beyond loopback: the dashboard cannot be unlocked here")
		return
	}
	origin, ok := selfUnlockOrigin(r)
	if !ok {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{
			"error": "unlock needs the dashboard opened by the daemon's own address (http://127.0.0.1:<port>, as `swarmery open` does), not a name",
		})
		return
	}
	lh := linkHolder
	if lh == nil {
		writeUnlockInvalid(w, "link disabled: no link token to verify the ticket")
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUnlockBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeUnlockInvalid(w, "malformed body: want {\"ticket\":\"…\"}")
		return
	}
	key := lh.Current()
	now := time.Now()
	nonce, err := linksig.VerifyUnlock(key, origin, strings.TrimSpace(req.Ticket), now)
	switch {
	case errors.Is(err, linksig.ErrSkew):
		writeUnlockInvalid(w, "unlock ticket expired: open the dashboard again with `swarmery open`")
		return
	case err != nil:
		writeUnlockInvalid(w, "unlock ticket does not verify")
		return
	}
	if !unlockVerifier.Claim(nonce, now) {
		writeUnlockInvalid(w, "unlock ticket already used")
		return
	}
	s, err := linksig.MintSession(key, now)
	if err != nil {
		writeErr(w, err)
		return
	}
	logUnlock(now, r.RemoteAddr)
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, http.StatusOK, map[string]string{"session": s})
}

// logUnlock records one line per successful unlock: when, and from which
// remote address. Never the ticket or the session — the log is readable by
// anything that can read the daemon's output.
func logUnlock(at time.Time, remoteAddr string) {
	log.Printf("session: dashboard unlocked at %s from %s", at.UTC().Format(time.RFC3339), remoteAddr)
}
