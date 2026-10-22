// Package linksig is the signed form of link authentication: a client proves
// it holds the link token without sending it, and the daemon proves its
// answer is its own. Contract: docs/mods-link-protocol.md "Auth".
//
// A request carries
//
//	Authorization: Swarmery-HMAC ts=<unix seconds>,nonce=<random>,sig=<hex>
//
// where sig is HMAC-SHA256(token, RequestString(method, target, ts, nonce,
// hex(SHA-256(body)))). The daemon accepts it within MaxSkew of its clock,
// once per nonce, and signs its answer with
//
//	X-Swarmery-Sig: <hex HMAC-SHA256(token, ResponseString(nonce, status, hex(SHA-256(body))))>
//
// so a client acts on an answer only when that signature verifies: a process
// that holds the daemon's port without holding the token can neither read the
// token off the wire nor forge an answer. Every comparison is constant-time.
//
// The package depends on the standard library only, so every link client in
// this module (the daemon, the hook shim, later the notch) shares it.
package linksig

import (
	"bytes"
	"container/list"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Scheme is the Authorization scheme of a signed request.
	Scheme = "Swarmery-HMAC"
	// ResponseHeader carries the daemon's signature of its answer.
	ResponseHeader = "X-Swarmery-Sig"
	// MaxSkew bounds how far a request's ts may sit from the daemon's clock.
	MaxSkew = 60 * time.Second
	// NonceTTL is how long a seen nonce is remembered: past it, the request's
	// ts is outside MaxSkew anyway (a request accepted at its earliest moment,
	// ts+MaxSkew in the daemon's past, expires 2×MaxSkew later).
	NonceTTL = 2 * MaxSkew
	// NonceCap bounds the remembered nonces; the oldest is dropped first.
	NonceCap = 4096
	// MaxBody bounds the body VerifyRequest reads to hash: the largest body any
	// signed route takes (the 4 MiB of the permission routes).
	MaxBody = 4 << 20

	requestTag  = "swarmery-hmac-v1"
	responseTag = "swarmery-hmac-v1-response"
)

var (
	// ErrUnsigned: the request carries no Swarmery-HMAC Authorization.
	ErrUnsigned = errors.New("linksig: request is not signed")
	// ErrMalformed: the Authorization names the scheme but its parameters do not parse.
	ErrMalformed = errors.New("linksig: malformed signature parameters")
	// ErrSkew: ts is more than MaxSkew from the daemon's clock.
	ErrSkew = errors.New("linksig: timestamp outside the allowed skew")
	// ErrBadSignature: the signature does not verify under the key (or the key is empty).
	ErrBadSignature = errors.New("linksig: signature does not verify")
	// ErrReplay: a nonce already accepted inside NonceTTL.
	ErrReplay = errors.New("linksig: nonce already used")
	// ErrBodyTooLarge: the body exceeds MaxBody, so it cannot be hashed here.
	ErrBodyTooLarge = errors.New("linksig: body too large to verify")
)

// BodyHash is hex(SHA-256(body)); an empty body hashes the empty string.
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// RequestString is the text a request signature covers.
func RequestString(method, target string, ts int64, nonce, bodySHA256 string) string {
	return strings.Join([]string{requestTag, strings.ToUpper(method), target, strconv.FormatInt(ts, 10), nonce, bodySHA256}, "\n")
}

// ResponseString is the text a response signature covers.
func ResponseString(nonce string, status int, bodySHA256 string) string {
	return strings.Join([]string{responseTag, nonce, strconv.Itoa(status), bodySHA256}, "\n")
}

func mac(key, text string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(text))
	return hex.EncodeToString(m.Sum(nil))
}

// equalHex compares two hex signatures in constant time; malformed hex never matches.
func equalHex(want, got string) bool {
	a, errA := hex.DecodeString(want)
	b, errB := hex.DecodeString(strings.ToLower(got))
	if errA != nil || errB != nil || len(a) == 0 {
		return false
	}
	return hmac.Equal(a, b)
}

// Sign is the hex request signature over method, target (path and query, as
// on the request line), ts, nonce and the body's SHA-256.
func Sign(key, method, target string, ts int64, nonce, bodySHA256 string) string {
	return mac(key, RequestString(method, target, ts, nonce, bodySHA256))
}

// Authorization is the full header value for one request.
func Authorization(key, method, target string, ts int64, nonce string, body []byte) string {
	sig := Sign(key, method, target, ts, nonce, BodyHash(body))
	return Scheme + " ts=" + strconv.FormatInt(ts, 10) + ",nonce=" + nonce + ",sig=" + sig
}

// SignResponse is the hex X-Swarmery-Sig value for an answer to the request
// that carried nonce.
func SignResponse(key, nonce string, status int, body []byte) string {
	return mac(key, ResponseString(nonce, status, BodyHash(body)))
}

// VerifyResponse reports whether sig is the daemon's signature of this answer
// to the request that carried nonce. An empty key or signature never verifies.
func VerifyResponse(key, nonce string, status int, body []byte, sig string) bool {
	if key == "" || nonce == "" || sig == "" {
		return false
	}
	return equalHex(SignResponse(key, nonce, status, body), sig)
}

// Params are the parsed parameters of a Swarmery-HMAC Authorization.
type Params struct {
	TS    int64
	Nonce string
	Sig   string
}

// IsSigned reports whether the request names the Swarmery-HMAC scheme.
func IsSigned(r *http.Request) bool {
	_, ok := schemeRest(r.Header.Get("Authorization"))
	return ok
}

func schemeRest(h string) (string, bool) {
	prefix := Scheme + " "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return h[len(prefix):], true
}

// validNonce: 16 to 128 characters of [0-9A-Za-z_-].
func validNonce(n string) bool {
	if len(n) < 16 || len(n) > 128 {
		return false
	}
	for _, c := range n {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ParseAuthorization reads a Swarmery-HMAC header value.
func ParseAuthorization(h string) (Params, error) {
	rest, ok := schemeRest(h)
	if !ok {
		return Params{}, ErrUnsigned
	}
	var p Params
	var hasTS bool
	for _, part := range strings.Split(rest, ",") {
		k, v, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			return Params{}, ErrMalformed
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "ts":
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return Params{}, ErrMalformed
			}
			p.TS, hasTS = n, true
		case "nonce":
			p.Nonce = strings.TrimSpace(v)
		case "sig":
			p.Sig = strings.TrimSpace(v)
		}
	}
	if !hasTS || !validNonce(p.Nonce) || len(p.Sig) != sha256.Size*2 {
		return Params{}, ErrMalformed
	}
	return p, nil
}

// Target is the request-target a signature covers: the request line's own
// text (r.RequestURI) on a server request, the URL's path and query on a
// request built in-process.
func Target(r *http.Request) string {
	if r.RequestURI != "" {
		return r.RequestURI
	}
	return r.URL.RequestURI()
}

// Verifier checks signed requests and remembers their nonces (an LRU of
// NonceCap entries, each kept NonceTTL) so none is accepted twice. One per
// daemon; safe for concurrent use.
type Verifier struct {
	mu    sync.Mutex
	seen  map[string]*list.Element
	order *list.List // front = oldest; values are seenNonce
}

type seenNonce struct {
	nonce string
	at    time.Time
}

// NewVerifier returns an empty Verifier.
func NewVerifier() *Verifier {
	return &Verifier{seen: make(map[string]*list.Element), order: list.New()}
}

// VerifyRequest checks r's Swarmery-HMAC signature under key at now, and on
// success returns the request's nonce (the one its answer is signed over).
// The body is read (at most MaxBody) to hash it and put back, so the handler
// reads it as sent. The nonce is remembered only once the signature verified,
// so unauthenticated traffic cannot fill the cache.
func (v *Verifier) VerifyRequest(r *http.Request, key string, now time.Time) (string, error) {
	p, err := ParseAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		return "", err
	}
	skew := now.Sub(time.Unix(p.TS, 0))
	if skew > MaxSkew || skew < -MaxSkew {
		return "", ErrSkew
	}
	body, err := readBody(r)
	if err != nil {
		return "", err
	}
	if key == "" || !equalHex(Sign(key, r.Method, Target(r), p.TS, p.Nonce, BodyHash(body)), p.Sig) {
		return "", ErrBadSignature
	}
	if !v.remember(p.Nonce, now) {
		return "", ErrReplay
	}
	return p.Nonce, nil
}

// readBody reads r.Body (bounded by MaxBody) and replaces it with a reader of
// the same bytes.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	_ = r.Body.Close()
	if err != nil {
		return nil, ErrMalformed
	}
	if len(body) > MaxBody {
		return nil, ErrBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// remember records nonce at now; false when it was already seen inside NonceTTL.
func (v *Verifier) remember(nonce string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for front := v.order.Front(); front != nil; front = v.order.Front() {
		s, _ := front.Value.(seenNonce)
		if now.Sub(s.at) < NonceTTL {
			break
		}
		v.order.Remove(front)
		delete(v.seen, s.nonce)
	}
	if _, ok := v.seen[nonce]; ok {
		return false
	}
	v.seen[nonce] = v.order.PushBack(seenNonce{nonce: nonce, at: now})
	for v.order.Len() > NonceCap {
		oldest := v.order.Front()
		s, _ := oldest.Value.(seenNonce)
		v.order.Remove(oldest)
		delete(v.seen, s.nonce)
	}
	return true
}

// Len is how many nonces are remembered (tests).
func (v *Verifier) Len() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.order.Len()
}
