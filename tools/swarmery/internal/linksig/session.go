// Dashboard sessions. The browser dashboard cannot sign requests — it never
// holds the link token — so it authenticates its writes with a session token
// the daemon minted for it, in exchange for an unlock ticket that a holder of
// the link token signed (`swarmery open`, the notch). Both are HMAC-SHA256
// under the link token, so rotating the token invalidates every ticket and
// every session at once; the daemon keeps no session state.
//
//	ticket  = <ts>.<nonce>.<sig>         sig = hex HMAC(token, "swarmery-unlock\n" + origin + "\n" + ts + "\n" + nonce)
//	session = v1.<issuedAt>.<rand>.<sig> sig = hex HMAC(token, "swarmery-session\n" + issuedAt + "\n" + rand)
//
// ts and issuedAt are decimal unix seconds; nonce and rand are 1–64
// characters of [A-Za-z0-9_-]. origin is the dashboard origin the ticket is
// meant for, in the canonical form UnlockOrigin returns
// (scheme://<IP literal>:<port>, e.g. http://127.0.0.1:7777). It is bound into
// the MAC but never transmitted: the daemon derives it from the unlock
// request's Origin, so a ticket redeemed from any other origin does not
// verify. A ticket is valid within UnlockMaxSkew of the
// daemon's clock and once per nonce (the caller keeps the nonce memory,
// NewUnlockVerifier); a session for SessionTTL after issuedAt.
package linksig

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// UnlockMaxSkew bounds how far an unlock ticket's ts may sit from the
	// daemon's clock: a ticket is meant to be redeemed the moment the
	// dashboard opens.
	UnlockMaxSkew = 120 * time.Second
	// SessionTTL is how long a dashboard session stays valid after it was issued.
	SessionTTL = 30 * 24 * time.Hour
	// SessionMaxFuture tolerates a session issued slightly ahead of this clock.
	SessionMaxFuture = 120 * time.Second
	// SessionVersion prefixes every session token.
	SessionVersion = "v1"

	unlockTag  = "swarmery-unlock"
	sessionTag = "swarmery-session"
)

// ErrOrigin: an unlock origin that is not http(s)://<IP address literal>[:port].
var ErrOrigin = errors.New("linksig: unlock origin must be http(s)://<IP address literal>[:port]")

// UnlockOrigin reduces a URL or a browser Origin to the canonical origin an
// unlock ticket is bound to: lowercase http or https, an IP address literal
// host (never a name — not even localhost — no zone, not the IPv4-mapped
// IPv6 form) and the port spelled out (the scheme's default when absent).
// IPv6 comes back bracketed and compressed (http://[::1]:7777). A path is
// ignored; user info is refused.
func UnlockOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Opaque != "" {
		return "", ErrOrigin
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	switch scheme {
	case "http":
		if port == "" {
			port = "80"
		}
	case "https":
		if port == "" {
			port = "443"
		}
	default:
		return "", ErrOrigin
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || addr.Zone() != "" || addr.Is4In6() {
		return "", ErrOrigin
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", ErrOrigin
	}
	return scheme + "://" + netip.AddrPortFrom(addr, uint16(n)).String(), nil
}

// UnlockSig is the hex signature of the ticket for origin, ts and nonce.
func UnlockSig(key, origin, ts, nonce string) string {
	return mac(key, unlockTag+"\n"+origin+"\n"+ts+"\n"+nonce)
}

// SessionSig is the hex signature of the session for issuedAt and rnd.
func SessionSig(key, issuedAt, rnd string) string {
	return mac(key, sessionTag+"\n"+issuedAt+"\n"+rnd)
}

// UnlockTicket mints a ticket for origin (canonicalised by UnlockOrigin;
// ErrOrigin when it is not an IP-literal origin) at now with a fresh random
// nonce.
func UnlockTicket(key, origin string, now time.Time) (string, error) {
	o, err := UnlockOrigin(origin)
	if err != nil {
		return "", err
	}
	nonce, err := NewNonce()
	if err != nil {
		return "", err
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	return ts + "." + nonce + "." + UnlockSig(key, o, ts, nonce), nil
}

// VerifyUnlock checks a ticket for origin (in UnlockOrigin's canonical form)
// under key at now and returns its nonce; the caller still has to claim the
// nonce (Verifier.Claim) so the ticket is redeemed once. Errors:
// ErrMalformed, ErrSkew, ErrBadSignature (also for an empty key or origin,
// and for a ticket minted for another origin).
func VerifyUnlock(key, origin, ticket string, now time.Time) (string, error) {
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 {
		return "", ErrMalformed
	}
	tsText, nonce, sig := parts[0], parts[1], parts[2]
	ts, ok := parseUnix(tsText)
	if !ok || !validToken(nonce) || len(sig) != 64 {
		return "", ErrMalformed
	}
	skew := now.Sub(time.Unix(ts, 0))
	if skew > UnlockMaxSkew || skew < -UnlockMaxSkew {
		return "", ErrSkew
	}
	if key == "" || origin == "" || !equalHex(UnlockSig(key, origin, tsText, nonce), sig) {
		return "", ErrBadSignature
	}
	return nonce, nil
}

// MintSession issues a session at now: v1.<issuedAt>.<32 hex>.<sig>.
func MintSession(key string, now time.Time) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	issuedAt := strconv.FormatInt(now.Unix(), 10)
	rnd := hex.EncodeToString(b)
	return SessionVersion + "." + issuedAt + "." + rnd + "." + SessionSig(key, issuedAt, rnd), nil
}

// VerifySession checks a session under key at now: well-formed, issued no
// more than SessionTTL ago and no more than SessionMaxFuture ahead, and
// signed with key. Errors: ErrMalformed, ErrSkew (expired or from the
// future), ErrBadSignature (also for an empty key).
func VerifySession(key, session string, now time.Time) error {
	parts := strings.Split(session, ".")
	if len(parts) != 4 || parts[0] != SessionVersion {
		return ErrMalformed
	}
	issuedText, rnd, sig := parts[1], parts[2], parts[3]
	issued, ok := parseUnix(issuedText)
	if !ok || !validToken(rnd) || len(sig) != 64 {
		return ErrMalformed
	}
	age := now.Sub(time.Unix(issued, 0))
	if age > SessionTTL || age < -SessionMaxFuture {
		return ErrSkew
	}
	if key == "" || !equalHex(SessionSig(key, issuedText, rnd), sig) {
		return ErrBadSignature
	}
	return nil
}

// NewUnlockVerifier returns a nonce memory sized for unlock tickets: a nonce
// is remembered for as long as its ticket could still verify (2×UnlockMaxSkew).
func NewUnlockVerifier() *Verifier {
	return newVerifier(2 * UnlockMaxSkew)
}

// Claim records nonce at now; false when it was already claimed inside the
// verifier's TTL. Claim a nonce only after its signature verified, so
// unauthenticated traffic cannot fill the memory.
func (v *Verifier) Claim(nonce string, now time.Time) bool {
	return v.remember(nonce, now)
}

// parseUnix reads a decimal unix-seconds value: digits only, no sign.
func parseUnix(s string) (int64, bool) {
	if s == "" || len(s) > 19 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// validToken: 1 to 64 characters of [0-9A-Za-z_-].
func validToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
