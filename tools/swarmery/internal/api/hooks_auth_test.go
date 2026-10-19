package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
)

// attachHookToken mints a link token in a temp home, attaches a Holder over it
// and returns the token. The /api/hooks/* shim routes require it (hookAuth),
// so every test that drives them over HTTP calls this first.
func attachHookToken(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	tok, err := link.EnsureToken(home)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	AttachLink(link.NewHolder(link.TokenPath(home), tok), ingest.ParseExcludeList(ingest.DefaultExclude))
	t.Cleanup(func() { AttachLink(nil, nil) })
	return tok
}

// hookToken is the token of the currently attached link holder, "" when the
// link is disabled. Read it on the test goroutine, before spawning a client.
func hookToken() string {
	if h := linkHolder; h != nil {
		return h.Get()
	}
	return ""
}

// setHookAuth adds the Authorization header the shim sends; "" sends none.
func setHookAuth(req *http.Request, tok string) {
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// hookPost POSTs body to path with the given token ("" = no header) and
// returns status and the trimmed body.
func hookPost(t *testing.T, base, path, tok, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	setHookAuth(req, tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// hookAuthRoutes are the three shim routes behind hookAuth, each with a body
// that a valid token gets past the handler's own checks with: stop answers
// 202, session-start 204 (undecodable body path), permission-request 400
// (malformed payload — the handler ran, so auth passed).
var hookAuthRoutes = []struct {
	path   string
	body   string
	authed int
}{
	{"/api/hooks/permission-request", `not json`, http.StatusBadRequest},
	{"/api/hooks/stop", `{"session_id":"x","hook_event_name":"Stop"}`, http.StatusAccepted},
	{"/api/hooks/session-start", `not json`, http.StatusNoContent},
}

// TestHookAuthRejectsMissingToken: no Authorization header → 401 with the
// exact link body on every shim route.
func TestHookAuthRejectsMissingToken(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	for _, r := range hookAuthRoutes {
		code, body := hookPost(t, srv.URL, r.path, "", r.body)
		if code != http.StatusUnauthorized || body != `{"error":"link token required"}` {
			t.Errorf("POST %s without token: %d %s, want 401 link token required", r.path, code, body)
		}
	}
}

// TestHookAuthRejectsWrongToken: a well-formed but wrong token → 401.
func TestHookAuthRejectsWrongToken(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	wrong := strings.Repeat("0", 64)
	for _, r := range hookAuthRoutes {
		if code, body := hookPost(t, srv.URL, r.path, wrong, r.body); code != http.StatusUnauthorized {
			t.Errorf("POST %s with a wrong token: %d %s, want 401", r.path, code, body)
		}
	}
}

// TestHookAuthAcceptsToken: the right token reaches each handler.
func TestHookAuthAcceptsToken(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	tok := hookToken()
	if tok == "" {
		t.Fatal("approvalsTestServer must attach a link token")
	}
	for _, r := range hookAuthRoutes {
		if code, body := hookPost(t, srv.URL, r.path, tok, r.body); code != r.authed {
			t.Errorf("POST %s with the token: %d %s, want %d", r.path, code, body, r.authed)
		}
	}
}

// TestHookAuthPermissionRequestDecision: with the token the long-poll opens a
// row, and a dashboard approve answers it 200 allow — the full shim channel.
func TestHookAuthPermissionRequestDecision(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	res := postHook(srv, hookCtx(t), hookBody("auth-uuid", "Bash", "ls"))
	pending := waitPending(t, srv)
	resp, err := http.Post(fmt.Sprintf("%s/api/approvals/%d", srv.URL, int64(pending["id"].(float64))),
		"application/json", strings.NewReader(`{"action":"approve"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case r := <-res:
		if r.err != nil || r.status != http.StatusOK || !strings.Contains(string(r.body), `"allow"`) {
			t.Fatalf("long-poll = %d %s (err %v), want 200 allow", r.status, r.body, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long-poll did not return after approve")
	}
}

// TestHookAuthLinkDisabled: no holder attached → 503 link disabled, even with
// a token, and no permission row is opened.
func TestHookAuthLinkDisabled(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{})
	tok := hookToken()
	AttachLink(nil, nil)
	for _, r := range hookAuthRoutes {
		code, body := hookPost(t, srv.URL, r.path, tok, r.body)
		if code != http.StatusServiceUnavailable || body != `{"error":"link disabled"}` {
			t.Errorf("POST %s with the link disabled: %d %s, want 503 link disabled", r.path, code, body)
		}
	}
	if n := countPermRows(t, db); n != 0 {
		t.Errorf("permission_requests rows = %d, want 0", n)
	}
}

// TestHookAuthWrongTokenOpensNoRow: a rejected permission-request never
// reaches the approvals service.
func TestHookAuthWrongTokenOpensNoRow(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{})
	code, _ := hookPost(t, srv.URL, "/api/hooks/permission-request", strings.Repeat("f", 64),
		hookBody("no-row-uuid", "Write", "x"))
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
	if n := countPermRows(t, db); n != 0 {
		t.Errorf("permission_requests rows = %d, want 0", n)
	}
}

// TestHookAuthRoutineWebhookUnaffected: /api/hooks/routine/{id}/{token} keeps
// its own per-routine token — no Authorization header needed, whether the
// link is attached or disabled.
func TestHookAuthRoutineWebhookUnaffected(t *testing.T) {
	srv, _, _ := serverWithRoutines(t)
	resp, data := doRoutineReq(t, http.MethodPost, srv.URL+"/api/routines", map[string]any{
		"name":    "hooked",
		"webhook": true,
		"steps":   []map[string]any{{"type": "command", "name": "c", "command": "true"}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	var made map[string]any
	if err := json.Unmarshal(data, &made); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/hooks/routine/" + made["id"].(string) + "/" + made["webhookToken"].(string)

	attachHookToken(t)
	if resp, _ := doRoutineReq(t, http.MethodPost, url, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("routine webhook with the link attached = %d, want 202", resp.StatusCode)
	}
	AttachLink(nil, nil)
	if resp, _ := doRoutineReq(t, http.MethodPost, url, nil); resp.StatusCode != http.StatusAccepted {
		t.Errorf("routine webhook with the link disabled = %d, want 202", resp.StatusCode)
	}
}
