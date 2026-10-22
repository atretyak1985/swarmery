package linksig

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeKey(t *testing.T, key string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "link-token")
	if err := os.WriteFile(p, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeyPath(t *testing.T) {
	if got, want := KeyPath("/h"), filepath.Join("/h", ".swarmery", "link-token"); got != want {
		t.Errorf("KeyPath = %q, want %q", got, want)
	}
}

func TestReadKeyFile(t *testing.T) {
	key := testKey("a")
	p := writeKey(t, key)
	if got := ReadKeyFile(p); got != key {
		t.Errorf("ReadKeyFile = %q", got)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(big, []byte(strings.Repeat("a", MaxKeyFile+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"empty path": "", "missing": filepath.Join(t.TempDir(), "nope"), "symlink": link,
		"directory": t.TempDir(), "too large": big,
	} {
		if got := ReadKeyFile(path); got != "" {
			t.Errorf("%s: ReadKeyFile = %q, want \"\"", name, got)
		}
	}
}

// TestTransportSignsWhatTheVerifierAccepts: the client half and the daemon
// half agree, for bodies, queries and methods, and the key never travels.
func TestTransportSignsWhatTheVerifierAccepts(t *testing.T) {
	key := testKey("b")
	v := NewVerifier()
	var verified []bool
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := v.VerifyRequest(r, key, time.Now())
		b, _ := io.ReadAll(r.Body)
		verified = append(verified, err == nil)
		bodies = append(bodies, string(b))
		if strings.Contains(r.Header.Get("Authorization"), key) {
			t.Error("the key travelled in the Authorization header")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(writeKey(t, key), 5*time.Second)
	reqs := []func() (*http.Request, error){
		func() (*http.Request, error) {
			return http.NewRequest(http.MethodPost, srv.URL+"/api/approvals/4", strings.NewReader(`{"action":"approve"}`))
		},
		func() (*http.Request, error) {
			return http.NewRequest(http.MethodDelete, srv.URL+"/api/x/1?force=1", nil)
		},
		// a body with no GetBody (a plain io.Reader) is read and put back
		func() (*http.Request, error) {
			return http.NewRequest(http.MethodPatch, srv.URL+"/api/y", io.NopCloser(strings.NewReader(`{"p":1}`)))
		},
	}
	for _, mk := range reqs {
		req, err := mk()
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if req.Header.Get("Authorization") != "" {
			t.Error("the transport modified the caller's request")
		}
	}
	want := []string{`{"action":"approve"}`, "", `{"p":1}`}
	for i := range want {
		if !verified[i] {
			t.Errorf("request %d did not verify", i)
		}
		if bodies[i] != want[i] {
			t.Errorf("request %d body = %q, want %q", i, bodies[i], want[i])
		}
	}
}

func TestTransportWithoutKeySendsUnsigned(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	c := NewClient(filepath.Join(t.TempDir(), "missing"), time.Second)
	resp, err := c.Post(srv.URL+"/api/x", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if auth != "" {
		t.Errorf("Authorization = %q, want none", auth)
	}
}

func TestHeaderForAWebSocketDial(t *testing.T) {
	key := testKey("c")
	h, err := Header(writeKey(t, key), http.MethodGet, "/api/ws", nil)
	if err != nil || h == nil {
		t.Fatalf("Header = %v, %v", h, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	r.Header.Set("Authorization", h.Get("Authorization"))
	if _, err := NewVerifier().VerifyRequest(r, key, time.Now()); err != nil {
		t.Errorf("verify: %v", err)
	}
	if h, err := Header(filepath.Join(t.TempDir(), "missing"), http.MethodGet, "/api/ws", nil); h != nil || err != nil {
		t.Errorf("no key: Header = %v, %v, want nil, nil", h, err)
	}
}
