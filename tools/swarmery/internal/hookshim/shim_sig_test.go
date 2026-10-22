package hookshim

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

// shimKey is the token the default stubs hold, built at run time.
var shimKey = strings.Repeat("5", 64)

// verifiedKey returns the candidate the request's Swarmery-HMAC signature
// verifies under ("" when none does) and the request's nonce.
func verifiedKey(r *http.Request, body []byte, candidates ...string) (string, string) {
	p, err := linksig.ParseAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		return "", ""
	}
	for _, k := range candidates {
		if linksig.Sign(k, r.Method, r.RequestURI, p.TS, p.Nonce, linksig.BodyHash(body)) == p.Sig {
			return k, p.Nonce
		}
	}
	return "", p.Nonce
}

// answerSigned writes status and body, signed over nonce with key as the
// daemon signs its answers; key "" leaves the answer unsigned.
func answerSigned(w http.ResponseWriter, key, nonce string, status int, body string) {
	if key != "" && nonce != "" {
		w.Header().Set(linksig.ResponseHeader, linksig.SignResponse(key, nonce, status, []byte(body)))
	}
	w.WriteHeader(status)
	if body != "" {
		w.Write([]byte(body))
	}
}

// listenerStub answers every request with 200 body, signed under signKey
// ("" = no signature at all): something on the daemon's port that is not the
// daemon. It records whether the request was signed with shimKey.
func listenerStub(t *testing.T, body, signKey string) (*httptest.Server, *[]string) {
	t.Helper()
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		auth = append(auth, r.Header.Get("Authorization"))
		_, nonce := verifiedKey(r, got, shimKey)
		answerSigned(w, signKey, nonce, http.StatusOK, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

// runKeyedShim runs one hook call holding shimKey in its token file.
func runKeyedShim(t *testing.T, event, baseURL, stdin string) (stdout, logged string) {
	t.Helper()
	dir := t.TempDir()
	tokenPath, logPath := filepath.Join(dir, "link-token"), filepath.Join(dir, "hook.log")
	writeToken(t, tokenPath, shimKey)
	var out bytes.Buffer
	Run(event, strings.NewReader(stdin), Config{BaseURL: baseURL, Stdout: &out, TokenPath: tokenPath, LogPath: logPath})
	b, _ := os.ReadFile(logPath)
	return out.String(), string(b)
}

// TestUnsignedAllowIsIgnored: an allow (or a deny, or session context) that
// does not carry the daemon's signature is not acted on: the shim fails open
// to the native dialog, and the request it sent carried no token.
func TestUnsignedAllowIsIgnored(t *testing.T) {
	for name, signKey := range map[string]string{"unsigned": "", "another key": strings.Repeat("6", 64)} {
		for _, body := range []string{`{"decision":"allow"}`, `{"decision":"deny","message":"x"}`} {
			srv, auth := listenerStub(t, body, signKey)
			stdout, logged := runKeyedShim(t, EventPermissionRequest, srv.URL, stdinFixture)
			if stdout != "" {
				t.Errorf("%s %s: stdout = %q, want silent fail-open", name, body, stdout)
			}
			if !strings.Contains(logged, "outcome=unsigned-http-200") {
				t.Errorf("%s %s: log = %q, want outcome=unsigned-http-200", name, body, logged)
			}
			if len(*auth) != 1 || !strings.HasPrefix((*auth)[0], linksig.Scheme+" ") || strings.Contains((*auth)[0], shimKey) {
				t.Errorf("%s: Authorization = %q, want one signed request without the token", name, *auth)
			}
		}
		srv, _ := listenerStub(t, `{"additionalContext":"injected"}`, signKey)
		if stdout, _ := runKeyedShim(t, EventSessionStart, srv.URL, `{"session_id":"s"}`); stdout != "" {
			t.Errorf("%s: session-start stdout = %q, want no context from an unsigned answer", name, stdout)
		}
	}
}

// TestSignedAllowIsHonoured: the daemon's signed allow is printed.
func TestSignedAllowIsHonoured(t *testing.T) {
	srv, _ := listenerStub(t, `{"decision":"allow"}`, shimKey)
	stdout, _ := runKeyedShim(t, EventPermissionRequest, srv.URL, stdinFixture)
	if !strings.Contains(stdout, `"behavior":"allow"`) {
		t.Errorf("stdout = %q, want the allow decision", stdout)
	}
}
