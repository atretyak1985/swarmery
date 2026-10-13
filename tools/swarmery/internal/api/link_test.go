package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// linkTestEnv is a daemon-shaped server with the link attached: a temp HOME
// holding a minted token, a Holder over it, the default exclude list.
type linkTestEnv struct {
	srv   *httptest.Server
	db    *sql.DB
	home  string
	token string
}

func newLinkTestEnv(t *testing.T) *linkTestEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "link.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	home := t.TempDir()
	tok, err := link.EnsureToken(home)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	AttachLink(link.NewHolder(link.TokenPath(home), tok), ingest.ParseExcludeList(ingest.DefaultExclude))
	t.Cleanup(func() { AttachLink(nil, nil) })

	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &linkTestEnv{srv: srv, db: db, home: home, token: tok}
}

// do sends one request; token "" omits the Authorization header.
func (e *linkTestEnv) do(t *testing.T, method, path, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// linkBatch renders {"events":[…]} with n turn.start events of one session,
// one second apart, in cwd.
func linkBatch(uuid, cwd string, n int) string {
	evs := make([]string, n)
	for i := range n {
		evs[i] = fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"kind":"turn.start","at":"2026-10-13T10:00:%02d.000Z"}`,
			uuid, cwd, i%60)
		if i >= 60 {
			evs[i] = fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"kind":"tool.call","at":"2026-10-13T10:%02d:%02d.000Z","payload":{"tool":"Bash","inputHash":"h%d"}}`,
				uuid, cwd, 1+i/60, i%60, i)
		}
	}
	return `{"events":[` + strings.Join(evs, ",") + `]}`
}

func decodeLinkResult(t *testing.T, body string) link.Result {
	t.Helper()
	var r link.Result
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return r
}

func TestLinkEventsRequiresToken(t *testing.T) {
	e := newLinkTestEnv(t)
	for _, tc := range []struct {
		name, token string
	}{
		{"no token", ""},
		{"wrong token", strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := e.do(t, http.MethodPost, "/api/link/events", tc.token, linkBatch("s-auth", "/work/proj", 1))
			if code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", code)
			}
			if body != `{"error":"link token required"}` {
				t.Errorf("body = %q", body)
			}
		})
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM link_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("link_events = %d after unauthenticated posts, want 0", n)
	}
}

func TestLinkEventsAcceptsAndDedups(t *testing.T) {
	e := newLinkTestEnv(t)
	batch := linkBatch("s-dedup", "/work/proj", 3)

	code, body := e.do(t, http.MethodPost, "/api/link/events", e.token, batch)
	if code != http.StatusAccepted {
		t.Fatalf("first post: status = %d body %s, want 202", code, body)
	}
	if r := decodeLinkResult(t, body); r.Accepted != 3 || r.Duplicates != 0 || r.Rejected != 0 {
		t.Errorf("first post = %+v, want accepted 3", r)
	}

	code, body = e.do(t, http.MethodPost, "/api/link/events", e.token, batch)
	if code != http.StatusAccepted {
		t.Fatalf("resend: status = %d, want 202", code)
	}
	if r := decodeLinkResult(t, body); r.Accepted != 0 || r.Duplicates != 3 {
		t.Errorf("resend = %+v, want duplicates 3", r)
	}

	var source, status string
	if err := e.db.QueryRow(`SELECT source, link_status FROM sessions WHERE session_uuid = 's-dedup'`).
		Scan(&source, &status); err != nil {
		t.Fatal(err)
	}
	if source != "mod" || status != link.StatusWorking {
		t.Errorf("session source/link_status = %q/%q, want mod/working", source, status)
	}
}

func TestLinkEventsEmptyBatch(t *testing.T) {
	e := newLinkTestEnv(t)
	code, body := e.do(t, http.MethodPost, "/api/link/events", e.token, `{"events":[]}`)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if r := decodeLinkResult(t, body); r.Accepted+r.Duplicates+r.Rejected != 0 || len(r.Rejects) != 0 {
		t.Errorf("result = %+v, want all zero", r)
	}
}

func TestLinkEventsBadRequests(t *testing.T) {
	e := newLinkTestEnv(t)
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"broken JSON", `{"events":[`, http.StatusBadRequest},
		{"bare array", `[{"sessionId":"s","kind":"turn.start","at":"2026-10-13T10:00:00Z"}]`, http.StatusBadRequest},
		{"no events key", `{}`, http.StatusBadRequest},
		{"oversize body", `{"events":[],"pad":"` + strings.Repeat("x", maxLinkBody) + `"}`, http.StatusRequestEntityTooLarge},
		{"too many events", linkBatch("s-many", "/work/proj", maxLinkEvents+1), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := e.do(t, http.MethodPost, "/api/link/events", e.token, tc.body)
			if code != tc.want {
				t.Fatalf("status = %d body %s, want %d", code, body, tc.want)
			}
			var eb struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &eb); err != nil || eb.Error == "" {
				t.Errorf("body = %q, want {\"error\":…}", body)
			}
		})
	}
	// Exactly the cap is fine.
	code, body := e.do(t, http.MethodPost, "/api/link/events", e.token, linkBatch("s-cap", "/work/proj", maxLinkEvents))
	if code != http.StatusAccepted {
		t.Fatalf("batch of %d: status = %d body %s, want 202", maxLinkEvents, code, body)
	}
}

func TestLinkEventsExcludedProjectRejected(t *testing.T) {
	e := newLinkTestEnv(t)
	body := `{"events":[
		{"sessionId":"s-tmp","cwd":"/tmp/scratch","kind":"turn.start","at":"2026-10-13T10:00:00Z"},
		{"sessionId":"s-ok","cwd":"/work/proj","kind":"bogus.kind","at":"2026-10-13T10:00:01Z"},
		{"sessionId":"s-ok","cwd":"/work/proj","kind":"turn.start","at":"2026-10-13T10:00:02Z"}]}`
	code, resp := e.do(t, http.MethodPost, "/api/link/events", e.token, body)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d body %s, want 202", code, resp)
	}
	r := decodeLinkResult(t, resp)
	if r.Accepted != 1 || r.Rejected != 2 {
		t.Fatalf("result = %+v, want accepted 1 rejected 2", r)
	}
	reasons := map[int]string{}
	for _, rj := range r.Rejects {
		reasons[rj.Index] = rj.Reason
	}
	if reasons[0] != rejectExcluded {
		t.Errorf("reject[0] = %q, want %q", reasons[0], rejectExcluded)
	}
	// The ingest reject is reported at the index the mod sent, not its index
	// in the filtered sub-batch.
	if !strings.Contains(reasons[1], "unknown kind") {
		t.Errorf("reject[1] = %q, want unknown kind", reasons[1])
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_uuid = 's-tmp'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("excluded cwd minted %d session rows, want 0", n)
	}
}

func TestLinkHealth(t *testing.T) {
	e := newLinkTestEnv(t)
	code, body := e.do(t, http.MethodGet, "/api/link/health", e.token, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	var h struct {
		OK     bool   `json:"ok"`
		Daemon string `json:"daemon"`
	}
	if err := json.Unmarshal([]byte(body), &h); err != nil || !h.OK || h.Daemon == "" {
		t.Errorf("body = %q, want ok:true + daemon version", body)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/link/health", "", ""); code != http.StatusUnauthorized {
		t.Errorf("health without token = %d, want 401", code)
	}
}

// TestLinkTokenRotation: `swarmery link token --rotate` rewrites the file; the
// running daemon's Holder picks the new value up on the first request that
// does not match the cached one (one file re-read, ≤ 1/s) — the new token
// works without a restart, and from then on the old one is 401.
//
// The reload is demand-driven: until some client presents the new token, the
// cached old value still authenticates (docs/mods-link-protocol.md, Rotation).
func TestLinkTokenRotation(t *testing.T) {
	e := newLinkTestEnv(t)
	if code, _ := e.do(t, http.MethodGet, "/api/link/health", e.token, ""); code != http.StatusOK {
		t.Fatalf("pre-rotation health = %d, want 200", code)
	}
	newTok, err := link.Rotate(e.home)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if newTok == e.token {
		t.Fatal("Rotate returned the old token")
	}
	onDisk, err := link.EnsureToken(e.home)
	if err != nil || onDisk != newTok {
		t.Fatalf("token file after Rotate does not hold the new token (err %v)", err)
	}

	code, body := e.do(t, http.MethodPost, "/api/link/events", newTok, linkBatch("s-rot", "/work/proj", 1))
	if code != http.StatusAccepted {
		t.Fatalf("new token after rotation: status = %d body %s, want 202", code, body)
	}
	if r := decodeLinkResult(t, body); r.Accepted != 1 {
		t.Errorf("result = %+v, want accepted 1", r)
	}
	code, body = e.do(t, http.MethodPost, "/api/link/events", e.token, linkBatch("s-rot", "/work/proj", 1))
	if code != http.StatusUnauthorized || body != `{"error":"link token required"}` {
		t.Fatalf("old token after rotation: status = %d body %s, want 401", code, body)
	}
}

func TestLinkDisabledWithoutHolder(t *testing.T) {
	e := newLinkTestEnv(t)
	AttachLink(nil, nil)
	code, body := e.do(t, http.MethodGet, "/api/link/health", e.token, "")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "link disabled") {
		t.Errorf("status/body = %d %q, want 503 link disabled", code, body)
	}
}

// TestLinkHostFenceStillApplies: the link routes skip the origin fence, not the
// DNS-rebinding Host fence that wraps the whole API.
func TestLinkHostFenceStillApplies(t *testing.T) {
	e := newLinkTestEnv(t)
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/api/link/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "attacker.example"
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: status = %d, want 403", resp.StatusCode)
	}
}

// TestLinkIgnoresForeignOrigin: no requireLocalOrigin on /api/link/* — a
// request carrying a token is served whatever its Origin says (the token, not
// the Origin, is the credential).
func TestLinkIgnoresForeignOrigin(t *testing.T) {
	e := newLinkTestEnv(t)
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/api/link/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://elsewhere.example")
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("foreign Origin with token: status = %d, want 200", resp.StatusCode)
	}
}
