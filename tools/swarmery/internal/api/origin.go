// Browser-facing trust policy for the loopback API (D4 hardening).
//
// The daemon has no auth in v1: it binds loopback and trusts whoever can reach
// it. Two browser-side attacks still need a fence, because a browser lends the
// user's loopback reachability to every page it renders:
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
// Requests without an Origin header (the hook shim, curl, `swarmery console`,
// the notch companion) keep passing the origin fence: a non-browser client
// cannot be driven by a web page, and localhost trust is the v1 model. They
// still have to name the daemon in Host, which every HTTP client does by
// default from the URL it dials.
package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
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

// isStrictLocalOrigin is the gate for browser-only endpoints (the PTY
// terminal): unlike requireLocalOrigin, an ABSENT Origin is rejected too, so a
// raw ws:// dial from a non-browser client cannot ride the no-Origin exemption
// into a shell. The daemon's own origin and the opt-in list apply as everywhere
// else — a dashboard reached by a trusted alias must not get working writes and
// a dead terminal.
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

// requireLocalOrigin rejects state-changing requests (and WebSocket upgrades)
// that carry a browser Origin other than the daemon's own or an opted-in one
// (CSRF hardening, D4). Requests without an Origin header (the shim, curl, the
// console, notch) pass — localhost trust is the v1 model.
func requireLocalOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && !isTrustedOrigin(r, o) {
			writeJSONStatus(w, http.StatusForbidden,
				map[string]string{"error": "cross-origin request rejected"})
			return
		}
		next(w, r)
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
