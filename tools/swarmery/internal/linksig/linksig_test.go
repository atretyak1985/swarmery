package linksig

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testKey is built at run time: a sample key, never a literal credential.
func testKey(c string) string { return strings.Repeat(c, 64) }

var t0 = time.Unix(1_791_920_751, 0)

const testNonce = "0123456789abcdef0123456789abcdef"

func signedRequest(t *testing.T, key, method, target, body string, ts time.Time, nonce string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Authorization", Authorization(key, method, target, ts.Unix(), nonce, []byte(body)))
	return r
}

func TestSigVerifyRequestValid(t *testing.T) {
	v := NewVerifier()
	key := testKey("a")
	r := signedRequest(t, key, http.MethodPost, "/api/link/permission", `{"toolName":"Bash"}`, t0, testNonce)
	nonce, err := v.VerifyRequest(r, key, t0.Add(5*time.Second))
	if err != nil || nonce != testNonce {
		t.Fatalf("VerifyRequest = %q, %v; want the nonce, nil", nonce, err)
	}
	// the handler still reads the body as sent
	got, _ := io.ReadAll(r.Body)
	if string(got) != `{"toolName":"Bash"}` {
		t.Errorf("body after verify = %q", got)
	}
}

func TestSigVerifyRequestCoversQueryMethodAndBody(t *testing.T) {
	key := testKey("a")
	cases := map[string]func(*http.Request){
		"query":  func(r *http.Request) { r.RequestURI = "/api/link/permission/7?session=other&wait=25" },
		"method": func(r *http.Request) { r.Method = http.MethodDelete },
		"body":   func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"x":2}`)) },
	}
	for name, tamper := range cases {
		r := signedRequest(t, key, http.MethodGet, "/api/link/permission/7?session=s1&wait=25", `{"x":1}`, t0, testNonce)
		tamper(r)
		if _, err := NewVerifier().VerifyRequest(r, key, t0); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s tampered: err = %v, want ErrBadSignature", name, err)
		}
	}
}

func TestSigVerifyRequestSkew(t *testing.T) {
	key := testKey("a")
	for _, d := range []time.Duration{61 * time.Second, -61 * time.Second} {
		r := signedRequest(t, key, http.MethodGet, "/api/link/commands?session=s", "", t0, testNonce)
		if _, err := NewVerifier().VerifyRequest(r, key, t0.Add(d)); !errors.Is(err, ErrSkew) {
			t.Errorf("now = ts%+v: err = %v, want ErrSkew", d, err)
		}
	}
	for _, d := range []time.Duration{60 * time.Second, -60 * time.Second} {
		r := signedRequest(t, key, http.MethodGet, "/api/link/commands?session=s", "", t0, testNonce)
		if _, err := NewVerifier().VerifyRequest(r, key, t0.Add(d)); err != nil {
			t.Errorf("now = ts%+v (at the bound): err = %v, want nil", d, err)
		}
	}
}

func TestSigVerifyRequestReplay(t *testing.T) {
	v := NewVerifier()
	key := testKey("a")
	first := signedRequest(t, key, http.MethodPost, "/api/link/events", `{"events":[]}`, t0, testNonce)
	if _, err := v.VerifyRequest(first, key, t0); err != nil {
		t.Fatalf("first: %v", err)
	}
	again := signedRequest(t, key, http.MethodPost, "/api/link/events", `{"events":[]}`, t0, testNonce)
	if _, err := v.VerifyRequest(again, key, t0.Add(30*time.Second)); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay: err = %v, want ErrReplay", err)
	}
}

func TestSigVerifyRequestWrongKey(t *testing.T) {
	v := NewVerifier()
	r := signedRequest(t, testKey("a"), http.MethodPost, "/api/link/events", `{}`, t0, testNonce)
	if _, err := v.VerifyRequest(r, testKey("b"), t0); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong key: err = %v, want ErrBadSignature", err)
	}
	if v.Len() != 0 {
		t.Errorf("a request that did not verify left %d nonces remembered", v.Len())
	}
	r = signedRequest(t, testKey("a"), http.MethodPost, "/api/link/events", `{}`, t0, testNonce)
	if _, err := v.VerifyRequest(r, "", t0); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("empty key: err = %v, want ErrBadSignature", err)
	}
}

func TestSigParseAuthorization(t *testing.T) {
	sig := strings.Repeat("0", 64)
	bad := []string{
		"",
		"Bearer abc",
		"Swarmery-HMAC ts=1,nonce=short,sig=" + sig,
		"Swarmery-HMAC ts=x,nonce=" + testNonce + ",sig=" + sig,
		"Swarmery-HMAC nonce=" + testNonce + ",sig=" + sig,
		"Swarmery-HMAC ts=1,nonce=" + testNonce + ",sig=abc",
		"Swarmery-HMAC ts=1,nonce=" + testNonce + "!,sig=" + sig,
		"Swarmery-HMAC ts=1;nonce=" + testNonce,
	}
	for _, h := range bad {
		if _, err := ParseAuthorization(h); err == nil {
			t.Errorf("ParseAuthorization(%q) = nil error", h)
		}
	}
	p, err := ParseAuthorization("swarmery-hmac ts=12, nonce=" + testNonce + ", sig=" + sig)
	if err != nil || p.TS != 12 || p.Nonce != testNonce || p.Sig != sig {
		t.Errorf("case-insensitive scheme, spaced params: %+v, %v", p, err)
	}
}

func TestSigBodyTooLarge(t *testing.T) {
	key := testKey("a")
	big := strings.Repeat("x", MaxBody+1)
	r := signedRequest(t, key, http.MethodPost, "/api/link/events", big, t0, testNonce)
	if _, err := NewVerifier().VerifyRequest(r, key, t0); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestSigNonceCacheBounds(t *testing.T) {
	v := NewVerifier()
	for i := 0; i < NonceCap+10; i++ {
		if !v.remember(fmt.Sprintf("nonce-%026d", i), t0) {
			t.Fatalf("nonce %d refused", i)
		}
	}
	if v.Len() != NonceCap {
		t.Fatalf("Len = %d, want the cap %d", v.Len(), NonceCap)
	}
	// past the TTL every entry has expired
	if !v.remember("nonce-late-0000000000000000", t0.Add(NonceTTL)) || v.Len() != 1 {
		t.Errorf("after the TTL: Len = %d, want 1", v.Len())
	}
}

func TestSigResponseSignature(t *testing.T) {
	key := testKey("a")
	body := []byte(`{"decision":"allow"}`)
	sig := SignResponse(key, testNonce, 200, body)
	if !VerifyResponse(key, testNonce, 200, body, sig) {
		t.Fatal("own signature does not verify")
	}
	if !VerifyResponse(key, testNonce, 200, body, strings.ToUpper(sig)) {
		t.Error("upper-case hex does not verify")
	}
	for name, ok := range map[string]bool{
		"other key":    VerifyResponse(testKey("b"), testNonce, 200, body, sig),
		"other nonce":  VerifyResponse(key, strings.Repeat("f", 32), 200, body, sig),
		"other status": VerifyResponse(key, testNonce, 202, body, sig),
		"other body":   VerifyResponse(key, testNonce, 200, []byte(`{"decision":"deny"}`), sig),
		"no signature": VerifyResponse(key, testNonce, 200, body, ""),
		"empty key":    VerifyResponse("", testNonce, 200, body, sig),
		"not hex":      VerifyResponse(key, testNonce, 200, body, "zz"),
	} {
		if ok {
			t.Errorf("%s: verified", name)
		}
	}
}

// TestSigCrossLanguageVector pins the exact strings and signatures the mod's
// lib/sig.ts must produce (plugins/swarmery-link/hooks/sig.test.ts holds the
// same vector): a drift on either side fails one of the two.
func TestSigCrossLanguageVector(t *testing.T) {
	key := testKey("k")
	const target = "/api/link/permission/7?session=s1&wait=25"
	if got, want := BodyHash(nil), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"; got != want {
		t.Fatalf("BodyHash(empty) = %s", got)
	}
	got := Sign(key, "get", target, 1_791_920_751, testNonce, BodyHash(nil))
	if got != crossVectorRequestSig {
		t.Errorf("request sig = %s, want %s", got, crossVectorRequestSig)
	}
	resp := SignResponse(key, testNonce, 200, []byte(`{"decision":"allow"}`))
	if resp != crossVectorResponseSig {
		t.Errorf("response sig = %s, want %s", resp, crossVectorResponseSig)
	}
}

const (
	// computed independently (Python hmac/hashlib) from the same strings
	crossVectorRequestSig  = "182f66fc97d1e1100ad18eab89718f01571529d8e904efaec47c41c0208ebbff"
	crossVectorResponseSig = "e9b45a06929b2cf77c1cb31a157fb41cd083027126c29e4f28f3d1cd04056fcd"
)
