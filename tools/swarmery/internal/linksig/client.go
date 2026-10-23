package linksig

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxKeyFile caps a key file read: a minted token is 64 hex characters and a
// newline, so anything larger is not a key the daemon wrote.
const MaxKeyFile = 256

// KeyPath is the installation's link key file, <home>/.swarmery/link-token
// (the file internal/link mints; link.TokenPath returns the same path).
func KeyPath(home string) string {
	return filepath.Join(home, ".swarmery", "link-token")
}

// DefaultKeyPath is KeyPath of the current user's home, "" when the home
// directory cannot be resolved.
func DefaultKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return KeyPath(home)
}

// ReadKeyFile returns the trimmed key file content, or "" when path is empty,
// the file is missing or unreadable, is a symlink or any other non-regular
// file, or holds more than MaxKeyFile bytes. The file is Lstat-ed before the
// open and the opened file must be that same file, so a swap to a symlink in
// between is refused too. A client never mints or chmods the file: that is
// the daemon's job (link.EnsureToken).
func ReadKeyFile(path string) string {
	if path == "" {
		return ""
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxKeyFile+1))
	if err != nil || len(b) > MaxKeyFile {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// NewNonce is 16 random bytes, hex-encoded.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SignRequest sets r's Authorization to a Swarmery-HMAC signature under key
// at now and returns the nonce the daemon's answer is signed over. The body
// is read through r.GetBody when set; otherwise it is read and put back, so
// the request still sends it.
func SignRequest(r *http.Request, key string, now time.Time) (string, error) {
	body, err := requestBody(r)
	if err != nil {
		return "", err
	}
	nonce, err := NewNonce()
	if err != nil {
		return "", err
	}
	r.Header.Set("Authorization", Authorization(key, r.Method, r.URL.RequestURI(), now.Unix(), nonce, body))
	return nonce, nil
}

func requestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	if r.GetBody != nil {
		rc, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, MaxBody+1))
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return body, nil
}

// Transport is an http.RoundTripper that signs every request it carries with
// the key in KeyPath, read on each request so a rotation needs no restart.
// With no key on disk the request goes out unsigned and the daemon answers
// 401 — the caller sees the refusal rather than a silent skip. It never sends
// the key itself.
type Transport struct {
	KeyPath string            // the key file; "" = DefaultKeyPath()
	Base    http.RoundTripper // nil = http.DefaultTransport
	Now     func() time.Time  // nil = time.Now
}

// RoundTrip signs a clone of req (the original is not modified, per the
// RoundTripper contract) and sends it through Base.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	path := t.KeyPath
	if path == "" {
		path = DefaultKeyPath()
	}
	key := ReadKeyFile(path)
	if key == "" {
		return base.RoundTrip(req)
	}
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	signed := req.Clone(req.Context())
	if _, err := SignRequest(signed, key, now()); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return base.RoundTrip(signed)
}

// NewClient is an *http.Client whose requests are signed with the key in
// keyPath ("" = the current user's), with the given overall timeout (0 = none).
func NewClient(keyPath string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &Transport{KeyPath: keyPath}}
}

// Header is the Authorization header for one request built outside net/http
// (a WebSocket dial's handshake): method and target as on the request line,
// body as sent. It returns nil when there is no key on disk.
func Header(keyPath, method, target string, body []byte) (http.Header, error) {
	if keyPath == "" {
		keyPath = DefaultKeyPath()
	}
	key := ReadKeyFile(keyPath)
	if key == "" {
		return nil, nil
	}
	nonce, err := NewNonce()
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("Authorization", Authorization(key, method, target, time.Now().Unix(), nonce, body))
	return h, nil
}
