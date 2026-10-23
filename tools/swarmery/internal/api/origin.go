// Browser-facing trust policy for the loopback API (D4 hardening).
//
// The daemon binds loopback. Two browser-side attacks need a fence on top of
// the credentials below, because a browser lends the user's loopback
// reachability to every page it renders:
//
//   - CSRF from a page on another origin. A state-changing request (and the
//     two WebSocket upgrades) carries the page's Origin, so a present Origin
//     must be the daemon's OWN — scheme, host AND port. A page served by a
//     different local dev server (http://localhost:5173) is as foreign as one
//     on the internet: "localhost" is a name every local process can serve.
//   - DNS rebinding. A page on attacker.example re-points that name at
//     127.0.0.1 and then reads every GET as same-origin. The Host header still
//     says attacker.example, so every route refuses a Host that does not name
//     this daemon — reads included; a rebinding page reads, it need not write.
//
// "This daemon" is derived per request from the listener the connection
// arrived on (http.LocalAddrContextKey): the loopback names on THAT port, and
// the listener's own address literal (what a `--bind <ip>` daemon is reached
// by). The operator extends both fences with SWARMERY_TRUSTED_ORIGINS, matched
// as full origins (scheme://host[:port]); a trusted origin's host[:port] is an
// accepted Host as well. There is no built-in trust of any other name or port.
//
// Requests without an Origin header come from non-browser clients (the hook
// shim, `swarmery api`, `swarmery console`, the notch companion). A browser
// always sends Origin on a write and on a WebSocket upgrade, so a missing one
// says nothing about where a request came from — any local process, or anyone
// on the network when the daemon is bound beyond loopback, can omit it.
// Non-browser clients therefore authenticate to the daemon: an Origin-less
// write or upgrade behind requireLocalOrigin must carry link auth (a request
// signed with the link token, internal/linksig, or during the transition the
// legacy bearer scheme), the same credential the /api/link/* routes take.
//
// A present Origin is no proof of a browser either: any local process can
// write the daemon's own. So the dashboard authenticates its writes and
// upgrades with a session token (session.go) — minted at POST
// /api/session/unlock for a ticket signed with the link token, which
// `swarmery open` and the notch hand to the browser — and link auth is
// accepted wherever a session is.
//
// On a loopback listener plain reads (GET/HEAD without an upgrade) stay open:
// a same-origin browser GET carries no Origin either. Every request still has
// to name the daemon in Host, which every HTTP client does by default from the
// URL it dials.
//
// An exposed listener (isExposedListener: a non-loopback --bind, or a
// connection that arrived on a non-loopback address) serves only signed
// clients: every /api/* request, reads included, needs a Swarmery-HMAC
// signature (requireSignedWhenExposed), except GET /api/health and the SPA's
// static shell, which carry no data. A dashboard session authorises nothing
// there, and the legacy Bearer scheme is refused (link.go).
package api

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// trustedOrigins is the OPT-IN allow-list of extra browser origins that pass
// the origin fence, parsed from SWARMERY_TRUSTED_ORIGINS. Empty by default,
// and deliberately so: the daemon controls no name beyond the loopback ones.
// A bare friendly hostname like "swarmery" resolves wherever the resolver says
// — a DNS search domain can expand it to swarmery.<corp>, and a page served
// from THAT host would then pass the CSRF fence. So an alias is trusted only
// when the operator names it, and it is matched as a full origin
// (scheme://host[:port]): trusting http://swarmery:7777 does not also trust
// https://swarmery:9999.
var trustedOrigins map[string]bool

// trustedHosts is derived from trustedOrigins: the host[:port] each one is
// reached by, as a browser (or curl) writes it in the Host header. An origin on
// its scheme's default port is listed both bare and with the port spelled out,
// because clients differ on which form they send.
var trustedHosts map[string]bool

// AttachTrustedOrigins installs the opt-in allow-list (startup, before serve).
// Entries that are not http(s) origins are dropped rather than half-matched.
func AttachTrustedOrigins(origins []string) {
	m := make(map[string]bool, len(origins))
	hosts := make(map[string]bool, 2*len(origins))
	for _, o := range origins {
		n, ok := normalizeOrigin(o)
		if !ok {
			continue
		}
		m[n] = true
		u, err := url.Parse(n) // n is well-formed by construction
		if err != nil {
			continue
		}
		hosts[u.Host] = true
		if u.Port() == "" {
			hosts[net.JoinHostPort(u.Hostname(), defaultPort(u.Scheme))] = true
		}
	}
	trustedOrigins = m
	trustedHosts = hosts
}

// normalizeOrigin reduces an origin to lowercase scheme://host[:port] with the
// scheme's default port dropped (browsers omit it), or reports false when the
// string is not an http(s) origin.
func normalizeOrigin(origin string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	port := u.Port()
	if port == defaultPort(scheme) {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, true
}

// defaultPort is the port a browser omits from Origin and Host for the scheme.
func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// requestScheme is the scheme a browser sees this daemon as. The daemon serves
// plain HTTP; a TLS listener (none today) would be https.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// listenerAddr returns the host and port of the listener this request arrived
// on. ok is false when the handler was invoked without a server (a direct
// ServeHTTP call in a test): nothing is "self" then, so only the opt-in list
// can vouch for an Origin or a Host.
func listenerAddr(r *http.Request) (host, port string, ok bool) {
	addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if addr == nil {
		return "", "", false
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", "", false
	}
	return host, port, true
}

// splitHostPort splits a Host header or URL authority into host and optional
// port. IPv6 literals come back without their brackets. ok is false for an
// empty or malformed value (a bare unbracketed IPv6 address, two ports, …).
func splitHostPort(hostport string) (host, port string, ok bool) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", "", false
	}
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p, h != "" && p != ""
	}
	if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		return hostport[1 : len(hostport)-1], "", len(hostport) > 2
	}
	if strings.Contains(hostport, ":") {
		return "", "", false
	}
	return hostport, "", true
}

// isSelfHost reports whether hostport — a Host header, or the authority of an
// Origin; the port may be empty for the scheme's default — names the listener
// this request arrived on: a loopback name or address on the listener's port,
// or the listener's own address literal. A rebinding page cannot produce any
// of these: its Host is the attacker's DNS name, never an address literal or
// "localhost", and a page on another local port carries that other port.
func isSelfHost(r *http.Request, hostport string) bool {
	host, port, ok := splitHostPort(hostport)
	if !ok {
		return false
	}
	if port == "" {
		port = defaultPort(requestScheme(r))
	}
	lhost, lport, ok := listenerAddr(r)
	if !ok || port != lport {
		return false
	}
	host = strings.ToLower(host)
	if host == "localhost" || host == strings.ToLower(lhost) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isTrustedOrigin reports whether a browser Origin may drive this daemon: the
// daemon's own origin (requestScheme + a self host, above) or an opted-in one.
func isTrustedOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	if n, ok := normalizeOrigin(origin); ok && trustedOrigins[n] {
		return true
	}
	return u.Scheme == requestScheme(r) && isSelfHost(r, u.Host)
}

// isStrictLocalOrigin is the Origin check for browser-only endpoints (the PTY
// terminal; the session unlock is stricter still, selfUnlockOrigin in
// session.go): unlike requireLocalOrigin, an ABSENT Origin
// is rejected too. The daemon's own origin and the opt-in list apply as
// everywhere else — a dashboard reached by a trusted alias must not get
// working writes and a dead terminal. It is a fence, not a credential: any
// local process can send the daemon's own Origin, so the terminal also needs
// a dashboard session (authenticateDashboard).
//
// It says nothing on an exposed listener (isExposedListener): there any client
// can send the daemon's own Origin, so a caller must refuse first (term.go).
func isStrictLocalOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return o != "" && isTrustedOrigin(r, o)
}

// isTrustedHost reports whether the request's Host header names this daemon:
// a self host (loopback on the listener's port, or the listener's address) or
// the host of an opted-in origin. An empty Host is refused: every client that
// matters sends one, and a request that names no server names not this one.
func isTrustedHost(r *http.Request) bool {
	h := strings.ToLower(strings.TrimSpace(r.Host))
	if h == "" {
		return false
	}
	return trustedHosts[h] || isSelfHost(r, h)
}

// originlessAuthRequired gates Origin-less writes and upgrades (and every
// write on an exposed listener) on link auth. Always on in the daemon; the api
// package's TestMain turns it off for the tests written before the gate, which
// drive the routes with no Origin and no credential. It does NOT cover the
// dashboard-session gate on a trusted Origin: that one holds in every test.
var originlessAuthRequired = true

// isPlainRead is a GET or HEAD that asks for no protocol upgrade.
func isPlainRead(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Header.Get("Upgrade") == ""
}

// requireLocalOrigin guards state-changing requests (and WebSocket upgrades).
// A request that carries a browser Origin must carry the daemon's own or an
// opted-in one (CSRF hardening, D4), or it is 403. A plain read (GET/HEAD, no
// upgrade) then passes: a same-origin browser read carries no Origin, and
// reads are not what this fence is for. Everything else needs a credential:
//
//   - the daemon's own Origin on a loopback listener: a dashboard session
//     (X-Swarmery-Session, or the swarmery.session.<token> subprotocol on an
//     upgrade) or link auth, else 401 dashboard_locked (session.go). Any local
//     process can write that Origin, so the header alone authorises nothing.
//   - no Origin: link auth (authenticateLink: a signed request, or the legacy
//     bearer credential), else 401 — 503 while the daemon has no link token.
//   - an EXPOSED listener (D7: the daemon was started with a non-loopback
//     --bind, or this connection arrived on a non-loopback address): link
//     auth, Origin or not. A dashboard session is not accepted there. On the
//     /api/ mux requireSignedWhenExposed has already demanded a signature for
//     every request; this branch is the same rule for any route mounted
//     outside it.
func requireLocalOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		if o != "" && !isTrustedOrigin(r, o) {
			writeJSONStatus(w, http.StatusForbidden,
				map[string]string{"error": "cross-origin request rejected"})
			return
		}
		if isPlainRead(r) {
			next(w, r)
			return
		}
		exposed := isExposedListener(r)
		if o != "" && !exposed {
			// The daemon's own Origin on loopback: the dashboard — or any
			// local process that wrote that header. Its credential is a
			// dashboard session, or link auth (session.go).
			r, ok := authenticateDashboard(w, r)
			if !ok {
				return
			}
			next(w, r)
			return
		}
		if !originlessAuthRequired {
			next(w, r)
			return
		}
		if o != "" && r.Header.Get("Authorization") == "" {
			writeJSONStatus(w, http.StatusUnauthorized, map[string]string{
				"error": exposedUnauthenticatedMsg,
			})
			noteOriginlessRefusal(r, "an exposed listener")
			return
		}
		r, _, ok := authenticateLink(w, r)
		if !ok {
			noteOriginlessRefusal(r, "no Origin")
			return
		}
		next(w, r)
	}
}

// exposedBind is set when the daemon was started with a non-loopback --bind
// (AttachExposedBind, startup). A wildcard bind is exposed even for a
// connection that happens to arrive on 127.0.0.1.
var exposedBind bool

// AttachExposedBind records whether the daemon listens beyond loopback.
func AttachExposedBind(exposed bool) { exposedBind = exposed }

// isExposedListener reports whether this request arrived on a listener others
// can reach: the configured bind is not loopback, or the connection's local
// address is not a loopback address. Decided per listener, never from the
// Host header (which the client writes). A request with no listener in its
// context (a handler invoked directly in a test) is judged by the bind alone.
func isExposedListener(r *http.Request) bool {
	if exposedBind {
		return true
	}
	host, _, ok := listenerAddr(r)
	if !ok {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// exposedUnauthenticatedMsg answers a request with no credential on an exposed
// listener.
const exposedUnauthenticatedMsg = "this daemon listens beyond loopback: every request needs link auth " +
	"(`swarmery api`, signed with the link token); the browser dashboard cannot load data here"

// openOnExposed reports whether r may pass an exposed listener unsigned: the
// liveness probe GET /api/health, which carries no session data. The SPA's
// static shell and assets are not on the /api/ mux at all (server.go), so
// they never reach requireSignedWhenExposed.
func openOnExposed(r *http.Request) bool {
	return isPlainRead(r) && r.URL.Path == "/api/health"
}

// requireSignedWhenExposed is the exposed-listener gate on the whole /api/
// mux: on a listener beyond loopback every request — reads included — must be
// signed with the link token (authenticateLink; the Bearer scheme is refused
// there), or it is 401 before the mux dispatches. A dashboard session
// authorises nothing: the browser dashboard cannot load data from an exposed
// daemon. The proof rides on in the request's context, so a route's own guard
// (requireLocalOrigin, linkAuth) does not verify the spent nonce again.
// GET /api/health stays open. On a loopback listener it does nothing.
func requireSignedWhenExposed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isExposedListener(r) || openOnExposed(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Swarmery-HMAC realm="swarmery-link"`)
			writeJSONStatus(w, http.StatusUnauthorized, map[string]string{"error": exposedUnauthenticatedMsg})
			noteOriginlessRefusal(r, "an exposed listener")
			return
		}
		r, _, ok := authenticateLink(w, r)
		if !ok {
			noteOriginlessRefusal(r, "an exposed listener")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refusalLogged remembers the routes whose first refused unauthenticated
// request was logged, so the hint appears once per route per process.
var refusalLogged sync.Map

func noteOriginlessRefusal(r *http.Request, why string) {
	key := r.Pattern
	if key == "" {
		key = r.Method + " " + r.URL.Path
	}
	if _, seen := refusalLogged.LoadOrStore(key, true); seen {
		return
	}
	log.Printf("api: refused %s %s without link auth (%s); non-browser clients sign their requests: use `swarmery api` (signs with the link token)",
		r.Method, r.URL.Path, why)
}

// requireLocalOriginUnlessRead applies requireLocalOrigin to every request that
// can change state and lets plain reads through. It is for a route registered
// without a method — the serena reverse proxy relays any method to an upstream
// whose own API has mutating endpoints — so the fence follows the method: only
// a GET or HEAD that asks for no protocol upgrade skips it.
func requireLocalOriginUnlessRead(next http.HandlerFunc) http.HandlerFunc {
	fenced := requireLocalOrigin(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if isPlainRead(r) {
			next(w, r)
			return
		}
		fenced(w, r)
	}
}

// requireLocalHost refuses any request whose Host header does not name this
// daemon (DNS-rebinding fence). Applied to every route, reads included, before
// the mux dispatches: a rebinding page reads, it does not need to write.
func requireLocalHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isTrustedHost(r) {
			writeJSONStatus(w, http.StatusForbidden, map[string]string{
				"error": "request Host does not name this daemon: reach it as localhost:<port>, " +
					"or opt the alias in with SWARMERY_TRUSTED_ORIGINS",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
