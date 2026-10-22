package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

func sigNonce(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

type signedReply struct {
	status int
	body   []byte
	sig    string
	nonce  string
}

// verified reports whether the answer carries the daemon's signature under key.
func (s signedReply) verified(key string) bool {
	return linksig.VerifyResponse(key, s.nonce, s.status, s.body, s.sig)
}

// signedDo sends one Swarmery-HMAC request signed with key at ts.
func signedDo(t *testing.T, base, method, target, key, body string, ts time.Time, nonce string) signedReply {
	t.Helper()
	req, err := http.NewRequest(method, base+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", linksig.Authorization(key, method, target, ts.Unix(), nonce, []byte(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return signedReply{status: resp.StatusCode, body: b, sig: resp.Header.Get(linksig.ResponseHeader), nonce: nonce}
}

func TestLinkSignedRequestIsServedAndItsAnswerSigned(t *testing.T) {
	e := newLinkTestEnv(t)
	r := signedDo(t, e.srv.URL, http.MethodPost, "/api/link/events", e.token, linkBatch("sig-uuid", "/work/app", 1), time.Now(), sigNonce(t))
	if r.status != http.StatusAccepted {
		t.Fatalf("signed POST = %d %s, want 202", r.status, r.body)
	}
	if !r.verified(e.token) {
		t.Fatalf("the 202 carries no valid %s (got %q)", linksig.ResponseHeader, r.sig)
	}
	if decodeLinkResult(t, string(r.body)).Accepted != 1 {
		t.Errorf("body = %s, want one accepted event", r.body)
	}
}

func TestLinkSignedQueryIsCovered(t *testing.T) {
	e := newLinkTestEnv(t)
	r := signedDo(t, e.srv.URL, http.MethodGet, "/api/link/health?probe=1", e.token, "", time.Now(), sigNonce(t))
	if r.status != http.StatusOK || !r.verified(e.token) {
		t.Fatalf("signed GET with a query = %d (sig verified %v), want 200 signed", r.status, r.verified(e.token))
	}
}

func TestLinkSignedRefusals(t *testing.T) {
	e := newLinkTestEnv(t)
	other := strings.Repeat("0", 64)
	cases := map[string]signedReply{
		"wrong key":   signedDo(t, e.srv.URL, http.MethodGet, "/api/link/health", other, "", time.Now(), sigNonce(t)),
		"stale ts":    signedDo(t, e.srv.URL, http.MethodGet, "/api/link/health", e.token, "", time.Now().Add(-2*time.Minute), sigNonce(t)),
		"future ts":   signedDo(t, e.srv.URL, http.MethodGet, "/api/link/health", e.token, "", time.Now().Add(2*time.Minute), sigNonce(t)),
		"short nonce": signedDo(t, e.srv.URL, http.MethodGet, "/api/link/health", e.token, "", time.Now(), "abc"),
	}
	for name, r := range cases {
		if r.status != http.StatusUnauthorized || strings.TrimSpace(string(r.body)) != `{"error":"link token required"}` {
			t.Errorf("%s: %d %s, want 401 link token required", name, r.status, r.body)
		}
		if r.sig != "" {
			t.Errorf("%s: a refusal carries a signature", name)
		}
	}
}

func TestLinkSignedReplayIsRefused(t *testing.T) {
	e := newLinkTestEnv(t)
	nonce, now := sigNonce(t), time.Now()
	if r := signedDo(t, e.srv.URL, http.MethodGet, "/api/link/health", e.token, "", now, nonce); r.status != http.StatusOK {
		t.Fatalf("first = %d, want 200", r.status)
	}
	if r := signedDo(t, e.srv.URL, http.MethodGet, "/api/link/health", e.token, "", now, nonce); r.status != http.StatusUnauthorized {
		t.Fatalf("replay = %d, want 401", r.status)
	}
}

func TestLinkLegacyBearerStillAccepted(t *testing.T) {
	e := newLinkTestEnv(t)
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/api/link/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer = %d, want 200 during the transition", resp.StatusCode)
	}
	if resp.Header.Get(linksig.ResponseHeader) != "" {
		t.Error("an unsigned request got a signed answer")
	}
}

func TestLinkSignedHookPermissionDecisionIsSigned(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	key := hookToken()
	if key == "" {
		t.Fatal("approvalsTestServer must attach a link token")
	}
	nonce := sigNonce(t)
	body := hookBody("sig-uuid", "Bash", "ls")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/hooks/permission-request", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", linksig.Authorization(key, http.MethodPost, "/api/hooks/permission-request", time.Now().Unix(), nonce, []byte(body)))
	done := make(chan signedReply, 1)
	go func() {
		// the long-poll holds until the approve below; a transport failure lands as a zero reply
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- signedReply{}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		done <- signedReply{status: resp.StatusCode, body: b, sig: resp.Header.Get(linksig.ResponseHeader), nonce: nonce}
	}()
	pending := waitPending(t, srv)
	resp, err := http.Post(fmt.Sprintf("%s/api/approvals/%d", srv.URL, int64(pending["id"].(float64))),
		"application/json", strings.NewReader(`{"action":"approve"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case r := <-done:
		if r.status != http.StatusOK || !strings.Contains(string(r.body), `"allow"`) {
			t.Fatalf("signed long-poll = %d %s, want 200 allow", r.status, r.body)
		}
		if !r.verified(key) {
			t.Fatalf("the decision carries no valid %s", linksig.ResponseHeader)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long-poll did not return after approve")
	}
}
