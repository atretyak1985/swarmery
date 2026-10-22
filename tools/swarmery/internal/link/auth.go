package link

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"
)

// unauthorizedBody is the exact 401 body of every /api/link/* route.
const unauthorizedBody = `{"error":"link token required"}`

// reloadInterval bounds how often Holder.Reload touches the disk: a burst of
// requests with a wrong token costs at most one file read per interval.
const reloadInterval = time.Second

// RequireToken guards a handler with `Authorization: Bearer <token>`, where the
// expected token is whatever get returns at request time (a getter, so the
// value can change without re-wiring routes). Comparison is constant-time. Any
// other request — no header, another scheme, a wrong token, or an empty
// expected token — gets 401 {"error":"link token required"}.
func RequireToken(get func() string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			presented, ok := bearer(r)
			if !ok || !matches(presented, get()) {
				writeUnauthorized(w)
				return
			}
			next(w, r)
		}
	}
}

// bearer extracts the credential of an `Authorization: Bearer <x>` header. The
// scheme is case-insensitive (RFC 7235 §2.1).
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const scheme = "bearer "
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(scheme):])
	return tok, tok != ""
}

// matches compares in constant time; an empty expected token never matches, so
// a daemon that failed to load its token refuses everything instead of
// accepting an empty credential.
func matches(presented, want string) bool {
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(want)) == 1
}

// WriteUnauthorized answers the exact 401 of every link route; the signed
// scheme (internal/linksig) answers a request that does not verify with it.
func WriteUnauthorized(w http.ResponseWriter) {
	writeUnauthorized(w)
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="swarmery-link"`)
	// the signed scheme (internal/linksig) every in-repo client now uses
	w.Header().Add("WWW-Authenticate", `Swarmery-HMAC realm="swarmery-link"`)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(unauthorizedBody))
}

// Holder keeps the daemon's current link token in memory and re-reads it from
// disk at most once per reloadInterval. `swarmery link token --rotate` rewrites
// the file; every authenticated request refreshes through the throttled
// reload before comparing, so the daemon adopts the new value — and refuses
// the old one — within ~1 s of a rotate, whichever token clients present. No
// restart, no signal.
type Holder struct {
	path string
	now  func() time.Time // injectable for tests

	mu         sync.Mutex
	tok        string
	lastReload time.Time
}

// NewHolder returns a Holder serving initial (normally EnsureToken's result)
// and reloading from path (normally TokenPath(home)).
func NewHolder(path, initial string) *Holder {
	return &Holder{path: path, tok: initial, now: time.Now}
}

// Get returns the current token. It is the getter RequireToken takes.
func (h *Holder) Get() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tok
}

// Reload re-reads the token file, at most once per second, and reports whether
// the in-memory value changed. A missing, empty or unreadable file keeps the
// current value: a half-finished rotation must never lock the mod out.
func (h *Holder) Reload() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reloadLocked()
}

// Current refreshes through the throttled reload, then returns the token. It is
// the getter Require hands RequireToken, so a rotated file is noticed within
// reloadInterval even when every client still presents the old token.
func (h *Holder) Current() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reloadLocked()
	return h.tok
}

// reloadLocked is Reload's body; the caller holds h.mu.
func (h *Holder) reloadLocked() bool {
	now := h.now()
	if !h.lastReload.IsZero() && now.Sub(h.lastReload) < reloadInterval {
		return false
	}
	h.lastReload = now
	tok, err := readToken(h.path)
	if err != nil || tok == h.tok {
		return false
	}
	h.tok = tok
	return true
}

// Require is RequireToken bound to this Holder's Current: each request refreshes
// (≤ 1 file read per reloadInterval) before the comparison. Inside one interval
// after a rotate the cached value still answers — the old token keeps working
// and the new one gets 401 — until the next reload. This is the middleware the
// /api/link/* routes use.
func (h *Holder) Require() func(http.HandlerFunc) http.HandlerFunc {
	return RequireToken(h.Current)
}
