package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// approvalsTestServer wires a full daemon-shaped httptest server: store,
// bus, approvals service (short timeout for the 204 tests), API routes.
func approvalsTestServer(t *testing.T, opt approvals.Options) (*httptest.Server, *sql.DB, *approvals.Service) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	bus := ingest.NewBus()
	AttachBus(bus)
	svc := approvals.New(db, bus, opt)
	AttachApprovals(svc)
	t.Cleanup(func() { AttachBus(nil); AttachApprovals(nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, svc
}

func hookBody(uuid, tool, command string) string {
	return fmt.Sprintf(
		`{"session_id":%q,"transcript_path":"/x.jsonl","cwd":"/tmp/proj","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":%q,"tool_input":{"command":%q},"permission_suggestions":[]}`,
		uuid, tool, command)
}

// postHook fires the long-poll in a goroutine and returns a channel with the
// final response (status, body).
type hookResult struct {
	status int
	body   []byte
	err    error
}

func postHook(srv *httptest.Server, ctx context.Context, body string) <-chan hookResult {
	out := make(chan hookResult, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
			srv.URL+"/api/hooks/permission-request", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- hookResult{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		out <- hookResult{status: resp.StatusCode, body: b}
	}()
	return out
}

// waitPending polls GET /api/approvals until a pending request shows up.
func waitPending(t *testing.T, srv *httptest.Server) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var list []map[string]any
		getJSON(t, srv.URL+"/api/approvals", &list)
		if len(list) > 0 {
			return list[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pending approval appeared")
	return nil
}

func resolveVia(t *testing.T, srv *httptest.Server, id float64, action, reason string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": action, "reason": reason})
	resp, err := http.Post(fmt.Sprintf("%s/api/approvals/%d", srv.URL, int64(id)),
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLongPollApprove(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	res := postHook(srv, context.Background(), hookBody("lp-approve", "Bash", "ls -la"))

	pending := waitPending(t, srv)
	if pending["status"] != "pending" || pending["toolName"] != "Bash" {
		t.Fatalf("pending = %v", pending)
	}
	resp := resolveVia(t, srv, pending["id"].(float64), "approve", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("resolve status = %d", resp.StatusCode)
	}
	var dto map[string]any
	json.NewDecoder(resp.Body).Decode(&dto)
	if dto["status"] != "approved" || dto["resolvedVia"] != "dashboard" {
		t.Errorf("resolved DTO = %v", dto)
	}

	r := <-res
	if r.err != nil || r.status != 200 {
		t.Fatalf("long-poll result: %d %v", r.status, r.err)
	}
	var d struct {
		Decision string `json:"decision"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(r.body, &d); err != nil || d.Decision != "allow" {
		t.Fatalf("long-poll body = %s (%v)", r.body, err)
	}
	if strings.Contains(string(r.body), `"message"`) {
		t.Error("allow must not carry a message key")
	}
}

func TestLongPollDenyWithReason(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	res := postHook(srv, context.Background(), hookBody("lp-deny", "Bash", "rm -rf /"))

	pending := waitPending(t, srv)
	resp := resolveVia(t, srv, pending["id"].(float64), "deny", "not on prod")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("resolve status = %d", resp.StatusCode)
	}

	r := <-res
	if r.status != 200 {
		t.Fatalf("long-poll status = %d", r.status)
	}
	var d struct {
		Decision string `json:"decision"`
		Message  string `json:"message"`
	}
	json.Unmarshal(r.body, &d)
	if d.Decision != "deny" || d.Message != "not on prod" {
		t.Fatalf("deny body = %s", r.body)
	}
}

// TestLongPollTimeout204: sweeper expiry ends the poll with 204, no body.
func TestLongPollTimeout204(t *testing.T) {
	srv, db, svc := approvalsTestServer(t, approvals.Options{Timeout: 100 * time.Millisecond})
	res := postHook(srv, context.Background(), hookBody("lp-timeout", "Bash", "ls"))
	waitPending(t, srv)

	time.Sleep(150 * time.Millisecond)
	svc.Sweep()

	r := <-res
	if r.err != nil || r.status != 204 || len(r.body) != 0 {
		t.Fatalf("timeout result: status=%d body=%q err=%v", r.status, r.body, r.err)
	}
	var status string
	db.QueryRow(`SELECT status FROM permission_requests`).Scan(&status)
	if status != "expired" {
		t.Errorf("row status = %q, want expired", status)
	}
}

// TestLongPollClientDisconnect: cancelling the hook request (terminal
// Esc/Ctrl-C killing the shim) resolves the row as resolved_elsewhere.
func TestLongPollClientDisconnect(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	res := postHook(srv, ctx, hookBody("lp-disc", "Bash", "ls"))
	waitPending(t, srv)

	cancel()
	if r := <-res; r.err == nil {
		t.Fatalf("expected cancelled request error, got status %d", r.status)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		var via *string
		db.QueryRow(`SELECT status, resolved_via FROM permission_requests`).Scan(&status, &via)
		if status == "resolved_elsewhere" {
			if via == nil || *via != "terminal" {
				t.Fatalf("resolved_via = %v, want terminal", via)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("row never became resolved_elsewhere after client disconnect")
}

// TestDedupFanOutHTTP: two concurrent identical hook calls share one pending
// row; one dashboard decision answers both long-polls.
func TestDedupFanOutHTTP(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{})
	body := hookBody("lp-dedup", "Bash", "curl example.com")
	res1 := postHook(srv, context.Background(), body)
	res2 := postHook(srv, context.Background(), body)

	pending := waitPending(t, srv)
	// Give the second caller time to attach (it must NOT create a row).
	time.Sleep(100 * time.Millisecond)
	var rows int
	db.QueryRow(`SELECT COUNT(*) FROM permission_requests`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("permission_requests rows = %d, want 1", rows)
	}

	resolveVia(t, srv, pending["id"].(float64), "approve", "").Body.Close()
	for i, res := range []<-chan hookResult{res1, res2} {
		select {
		case r := <-res:
			if r.status != 200 || !strings.Contains(string(r.body), `"allow"`) {
				t.Errorf("caller %d: status=%d body=%s", i, r.status, r.body)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("caller %d never woke", i)
		}
	}
}

func TestResolveConflictAndNotFound(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	res := postHook(srv, context.Background(), hookBody("lp-409", "Bash", "ls"))
	pending := waitPending(t, srv)
	id := pending["id"].(float64)

	resolveVia(t, srv, id, "approve", "").Body.Close()
	<-res

	resp := resolveVia(t, srv, id, "deny", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("second resolve status = %d, want 409", resp.StatusCode)
	}
	resp = resolveVia(t, srv, 424242, "approve", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404", resp.StatusCode)
	}

	// Invalid action → 400.
	bad, _ := http.Post(fmt.Sprintf("%s/api/approvals/%d", srv.URL, int64(id)),
		"application/json", strings.NewReader(`{"action":"maybe"}`))
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid action status = %d, want 400", bad.StatusCode)
	}
}

// TestOriginMiddleware: foreign browser Origins are rejected on ALL write
// endpoints; localhost Origins and no-Origin (curl/shim) pass (D4).
func TestOriginMiddleware(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	endpoints := []string{"/api/hooks/stop", "/api/approvals/1"}

	for _, ep := range endpoints {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+ep, strings.NewReader(`{}`))
		req.Header.Set("Origin", "https://evil.example.com")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s with foreign Origin: status = %d, want 403", ep, resp.StatusCode)
		}
	}
	// Foreign origin on the long-poll endpoint too (no body needed — reject fires first).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/hooks/permission-request", strings.NewReader(`{}`))
	req.Header.Set("Origin", "http://attacker.local:7777")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("permission-request foreign Origin status = %d, want 403", resp.StatusCode)
	}

	// Local origins pass through to the handler (which then 202s / 404s).
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:7777"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/hooks/stop", strings.NewReader(`{}`))
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("stop with Origin %s: status = %d, want 202", origin, resp.StatusCode)
		}
	}
	// No Origin (the shim, curl) passes.
	resp, err := http.Post(srv.URL+"/api/hooks/stop", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("stop without Origin: status = %d, want 202", resp.StatusCode)
	}
}

// TestStopHeartbeatAndHealth: /api/hooks/* refresh hooks_last_seen; the field
// is absent until the first check-in (frozen HealthResponse, additive).
func TestStopHeartbeatAndHealth(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})

	var before map[string]any
	getJSON(t, srv.URL+"/api/health", &before)
	if _, ok := before["hooks_last_seen"]; ok {
		t.Error("hooks_last_seen must be absent before any hook call")
	}

	resp, err := http.Post(srv.URL+"/api/hooks/stop", "application/json",
		strings.NewReader(`{"session_id":"x","hook_event_name":"Stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status = %d, want 202", resp.StatusCode)
	}

	var after map[string]any
	getJSON(t, srv.URL+"/api/health", &after)
	ts, ok := after["hooks_last_seen"].(string)
	if !ok || ts == "" {
		t.Fatalf("hooks_last_seen = %v, want ISO timestamp", after["hooks_last_seen"])
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("hooks_last_seen %q is not RFC3339: %v", ts, err)
	}
}

// TestListApprovals: newest first, pending default, resolved/all/limit filters.
func TestListApprovals(t *testing.T) {
	srv, _, svc := approvalsTestServer(t, approvals.Options{})
	ctxs := []string{"one", "two", "three"}
	var results []<-chan hookResult
	for _, c := range ctxs {
		results = append(results, postHook(srv, context.Background(), hookBody("lp-list", "Bash", c)))
		time.Sleep(5 * time.Millisecond) // distinct requested_at ordering
	}
	deadline := time.Now().Add(5 * time.Second)
	var list []map[string]any
	for time.Now().Before(deadline) {
		getJSON(t, srv.URL+"/api/approvals", &list)
		if len(list) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(list) != 3 {
		t.Fatalf("pending = %d, want 3", len(list))
	}
	if list[0]["id"].(float64) < list[2]["id"].(float64) {
		t.Error("list must be newest first")
	}

	// Resolve one → it leaves the pending default, shows under resolved.
	if err := svc.Resolve(int64(list[2]["id"].(float64)), approvals.StatusDenied, "dashboard", "no"); err != nil {
		t.Fatal(err)
	}
	<-results[0]

	getJSON(t, srv.URL+"/api/approvals", &list)
	if len(list) != 2 {
		t.Errorf("pending after resolve = %d, want 2", len(list))
	}
	getJSON(t, srv.URL+"/api/approvals?status=resolved", &list)
	if len(list) != 1 || list[0]["status"] != "denied" {
		t.Errorf("resolved list = %v", list)
	}
	getJSON(t, srv.URL+"/api/approvals?status=all&limit=2", &list)
	if len(list) != 2 {
		t.Errorf("all&limit=2 = %d rows, want 2", len(list))
	}

	// Cleanup: expire the rest so goroutines finish.
	for _, p := range list {
		svc.Resolve(int64(p["id"].(float64)), approvals.StatusApproved, "dashboard", "")
	}
	svc.Sweep()
}

// TestHookEndpointsWithoutService: 503 when approvals is not attached.
func TestHookEndpointsWithoutService(t *testing.T) {
	AttachApprovals(nil)
	srv := testServer(t)
	resp, err := http.Post(srv.URL+"/api/hooks/permission-request", "application/json",
		strings.NewReader(hookBody("x", "Bash", "ls")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// TestHookBadPayload: malformed stdin → 400 (the shim fails open on it).
func TestHookBadPayload(t *testing.T) {
	srv, _, _ := approvalsTestServer(t, approvals.Options{})
	resp, err := http.Post(srv.URL+"/api/hooks/permission-request", "application/json",
		strings.NewReader(`this is not json`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHookExcludedCwd204: a hook from an excluded cwd is answered 204
// immediately (no decision -> the shim fails open to the native dialog) and
// persists nothing.
func TestHookExcludedCwd204(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{
		Exclude: ingest.ParseExcludeList(ingest.DefaultExclude),
	})
	r := <-postHook(srv, context.Background(), hookBody("excluded-cwd", "Bash", "ls"))
	if r.err != nil || r.status != http.StatusNoContent {
		t.Fatalf("excluded hook: status = %d, err = %v; want 204", r.status, r.err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("sessions = %d after excluded hook, want 0", n)
	}
}

// ── AskUserQuestion answers (hooks-protocol amendment 1, spike E12) ──────────

// askHookBody is an AskUserQuestion PermissionRequest hook stdin (E12 shape):
// one single-select, one multiSelect question.
func askHookBody(uuid string) string {
	return fmt.Sprintf(
		`{"session_id":%q,"transcript_path":"/x.jsonl","cwd":"/tmp/proj","hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Pick a color","header":"Color","options":[{"label":"Red","description":"warm"},{"label":"Blue","description":"cool"}],"multiSelect":false},{"question":"Pick fruits","header":"Fruits","options":[{"label":"Apple","description":""},{"label":"Banana","description":""}],"multiSelect":true}]}}`,
		uuid)
}

// postAction POSTs a raw JSON body to /api/approvals/{id}.
func postAction(t *testing.T, srv *httptest.Server, id float64, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(fmt.Sprintf("%s/api/approvals/%d", srv.URL, int64(id)),
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestAnswerLongPollUpdatedInput: {action:"answer"} approves the row with the
// summary reason; the long-poll 200 carries {"decision":"allow","updatedInput":
// {questions, answers}} in the default updated-input delivery mode.
func TestAnswerLongPollUpdatedInput(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{})
	res := postHook(srv, context.Background(), askHookBody("lp-answer"))

	pending := waitPending(t, srv)
	if pending["toolName"] != "AskUserQuestion" {
		t.Fatalf("pending = %v", pending)
	}
	resp := postAction(t, srv, pending["id"].(float64),
		`{"action":"answer","answers":{"Pick a color":"Red","Pick fruits":["Apple","Banana"]}}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("answer status = %d", resp.StatusCode)
	}
	var dto map[string]any
	json.NewDecoder(resp.Body).Decode(&dto)
	if dto["status"] != "approved" || dto["resolvedVia"] != "dashboard" {
		t.Errorf("resolved DTO = %v", dto)
	}
	if dto["reason"] != "«Pick a color» → Red · «Pick fruits» → Apple, Banana" {
		t.Errorf("reason = %v", dto["reason"])
	}

	r := <-res
	if r.err != nil || r.status != 200 {
		t.Fatalf("long-poll result: %d %v", r.status, r.err)
	}
	var d struct {
		Decision     string                     `json:"decision"`
		UpdatedInput map[string]json.RawMessage `json:"updatedInput"`
	}
	if err := json.Unmarshal(r.body, &d); err != nil {
		t.Fatalf("long-poll body %s: %v", r.body, err)
	}
	if d.Decision != "allow" || d.UpdatedInput == nil {
		t.Fatalf("long-poll body = %s, want allow + updatedInput", r.body)
	}
	if !strings.Contains(string(d.UpdatedInput["questions"]), `"Pick a color"`) {
		t.Errorf("questions not echoed: %s", d.UpdatedInput["questions"])
	}
	var answers map[string]json.RawMessage
	json.Unmarshal(d.UpdatedInput["answers"], &answers)
	if string(answers["Pick fruits"]) != `["Apple","Banana"]` {
		t.Errorf("multiSelect answer = %s, want the array form (E12c)", answers["Pick fruits"])
	}

	var status string
	db.QueryRow(`SELECT status FROM permission_requests`).Scan(&status)
	if status != "approved" {
		t.Errorf("row status = %q, want approved", status)
	}
}

// TestAnswerHTTPStatusMatrix: 400 wrong tool + failed validation (with the
// specific reason in the body), 404 unknown id, 409 already resolved.
func TestAnswerHTTPStatusMatrix(t *testing.T) {
	srv, _, svc := approvalsTestServer(t, approvals.Options{})

	// A Bash row can never take {action:"answer"} → 400, row stays pending.
	resBash := postHook(srv, context.Background(), hookBody("lp-answer-400", "Bash", "ls"))
	pendingBash := waitPending(t, srv)
	resp := postAction(t, srv, pendingBash["id"].(float64), `{"action":"answer","answers":{"x":"y"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong-tool answer status = %d, want 400", resp.StatusCode)
	}
	var e map[string]string
	json.NewDecoder(resp.Body).Decode(&e)
	resp.Body.Close()
	if !strings.Contains(e["error"], "AskUserQuestion") {
		t.Errorf("wrong-tool error = %q, want the specific reason", e["error"])
	}
	// Still pending → a normal approve succeeds (frees the long-poll).
	if err := svc.Resolve(int64(pendingBash["id"].(float64)), approvals.StatusApproved, "dashboard", ""); err != nil {
		t.Fatalf("failed answer consumed the row: %v", err)
	}
	<-resBash

	// AskUserQuestion row: missing answer → 400; valid → 200; repeat → 409.
	resAsk := postHook(srv, context.Background(), askHookBody("lp-answer-409"))
	pendingAsk := waitPending(t, srv)
	askID := pendingAsk["id"].(float64)

	resp = postAction(t, srv, askID, `{"action":"answer","answers":{"Pick a color":"Red"}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing-answer status = %d, want 400", resp.StatusCode)
	}
	resp = postAction(t, srv, askID, `{"action":"answer","answers":{"Pick a color":"Red","Pick fruits":["Apple"]}}`)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("valid answer status = %d, want 200", resp.StatusCode)
	}
	<-resAsk
	resp = postAction(t, srv, askID, `{"action":"answer","answers":{"Pick a color":"Red","Pick fruits":["Apple"]}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("second answer status = %d, want 409", resp.StatusCode)
	}

	resp = postAction(t, srv, 424242, `{"action":"answer","answers":{"x":"y"}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404", resp.StatusCode)
	}
}

// TestAnswerDeliveryDenyMessage: --answer-delivery=deny-message flips ONLY the
// wire form — the row stays approved with the same reason; the long-poll
// answers deny + "User answered via dashboard: …" (deny messages reach Claude
// verbatim as the tool result, E3).
func TestAnswerDeliveryDenyMessage(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{AnswerDelivery: approvals.DeliveryDenyMessage})
	res := postHook(srv, context.Background(), askHookBody("lp-answer-fallback"))
	pending := waitPending(t, srv)

	postAction(t, srv, pending["id"].(float64),
		`{"action":"answer","answers":{"Pick a color":"Blue","Pick fruits":["Banana"]}}`).Body.Close()

	r := <-res
	if r.status != 200 {
		t.Fatalf("long-poll status = %d", r.status)
	}
	var d struct {
		Decision     string          `json:"decision"`
		Message      string          `json:"message"`
		UpdatedInput json.RawMessage `json:"updatedInput"`
	}
	json.Unmarshal(r.body, &d)
	if d.Decision != "deny" ||
		d.Message != "User answered via dashboard: «Pick a color» → Blue · «Pick fruits» → Banana" {
		t.Fatalf("fallback body = %s", r.body)
	}
	if len(d.UpdatedInput) > 0 {
		t.Errorf("fallback must not carry updatedInput: %s", r.body)
	}

	// Audit stays honest: the human genuinely answered — the row is approved.
	var status, reason string
	db.QueryRow(`SELECT status, reason FROM permission_requests`).Scan(&status, &reason)
	if status != "approved" || !strings.Contains(reason, "«Pick a color» → Blue") {
		t.Errorf("row = %s/%q, want approved with the summary reason", status, reason)
	}
}

// TestTerminalHandoffNoDecision: {action:"terminal"} releases the long-poll
// with NO decision — 204, shim fails open, the native selector renders
// (E12d: a plain allow would resolve the questions unanswered; E12e: fail-open
// renders the dialog). The row records resolved_elsewhere via dashboard.
func TestTerminalHandoffNoDecision(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{})
	res := postHook(srv, context.Background(), askHookBody("lp-terminal"))
	pending := waitPending(t, srv)

	resp := postAction(t, srv, pending["id"].(float64), `{"action":"terminal"}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("terminal action status = %d", resp.StatusCode)
	}
	var dto map[string]any
	json.NewDecoder(resp.Body).Decode(&dto)
	if dto["status"] != "resolved_elsewhere" || dto["resolvedVia"] != "dashboard" ||
		dto["reason"] != "handed off to terminal" {
		t.Errorf("terminal DTO = %v", dto)
	}

	r := <-res
	if r.err != nil || r.status != 204 || len(r.body) != 0 {
		t.Fatalf("long-poll after terminal handoff: status=%d body=%q err=%v; want bare 204",
			r.status, r.body, r.err)
	}
	var status string
	db.QueryRow(`SELECT status FROM permission_requests`).Scan(&status)
	if status != "resolved_elsewhere" {
		t.Errorf("row status = %q, want resolved_elsewhere", status)
	}
}

// TestLongPollAutoApprovedByRule: with a matching rule in place the hook
// long-poll returns allow without any dashboard action, and the row is
// audit-visible as resolved_via='rule'.
func TestLongPollAutoApprovedByRule(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{})
	if _, err := db.Exec(
		`INSERT INTO approval_rules (project_id, tool_pattern, created_at)
		 VALUES (NULL, 'Bash(ls*)', '2026-07-16T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}

	r := <-postHook(srv, context.Background(), hookBody("lp-rule", "Bash", "ls -la"))
	if r.err != nil || r.status != 200 {
		t.Fatalf("long-poll result: %d %v", r.status, r.err)
	}
	var d struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(r.body, &d); err != nil || d.Decision != "allow" {
		t.Fatalf("long-poll body = %s (%v)", r.body, err)
	}

	var status, via string
	if err := db.QueryRow(
		`SELECT status, resolved_via FROM permission_requests ORDER BY id DESC LIMIT 1`).
		Scan(&status, &via); err != nil {
		t.Fatal(err)
	}
	if status != "approved" || via != "rule" {
		t.Errorf("audit row = (%s, %s), want (approved, rule)", status, via)
	}
}
