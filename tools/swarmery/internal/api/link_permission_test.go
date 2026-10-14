package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// permClock is a settable approvals.Options.Now for the window/lease tests.
type permClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *permClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *permClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// linkPermEnv is a daemon-shaped server with BOTH the approvals service and
// the link attached.
type linkPermEnv struct {
	srv   *httptest.Server
	db    *sql.DB
	svc   *approvals.Service
	token string
}

func newLinkPermEnv(t *testing.T, opt approvals.Options) *linkPermEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "perm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	bus := ingest.NewBus()
	AttachBus(bus)
	svc := approvals.New(db, bus, opt)
	AttachApprovals(svc)
	home := t.TempDir()
	tok, err := link.EnsureToken(home)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	AttachLink(link.NewHolder(link.TokenPath(home), tok), ingest.ParseExcludeList(ingest.DefaultExclude))
	t.Cleanup(func() { AttachBus(nil); AttachApprovals(nil); AttachLink(nil, nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &linkPermEnv{srv: srv, db: db, svc: svc, token: tok}
}

// req sends one request; token "" omits Authorization.
func (e *linkPermEnv) req(t *testing.T, method, path, token, body string) (int, string) {
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

func (e *linkPermEnv) ask(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	code, raw := e.req(t, http.MethodPost, "/api/link/permission", e.token, body)
	return code, decodeObj(t, raw)
}

func (e *linkPermEnv) poll(t *testing.T, id int64, session string, wait int) (int, map[string]any) {
	t.Helper()
	code, raw := e.req(t, http.MethodGet,
		fmt.Sprintf("/api/link/permission/%d?session=%s&wait=%d", id, session, wait), e.token, "")
	return code, decodeObj(t, raw)
}

// decide POSTs a dashboard action (no Origin, like the shim tests).
func (e *linkPermEnv) decide(t *testing.T, id int64, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(fmt.Sprintf("%s/api/approvals/%d", e.srv.URL, id), "application/json",
		bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func decodeObj(t *testing.T, raw string) map[string]any {
	t.Helper()
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return m
}

func permBody(session, tool, toolUseID, toolInput string) string {
	return fmt.Sprintf(`{"sessionId":%q,"cwd":"/work/proj","toolName":%q,"toolInput":%s,"toolUseId":%q}`,
		session, tool, toolInput, toolUseID)
}

func idOf(t *testing.T, m map[string]any) int64 {
	t.Helper()
	f, ok := m["id"].(float64)
	if !ok {
		t.Fatalf("no id in %v", m)
	}
	return int64(f)
}

type permRow struct {
	tool, status, risk, hash, channel, via, requestJSON string
}

func readPermRow(t *testing.T, db *sql.DB, id int64) permRow {
	t.Helper()
	var r permRow
	var via sql.NullString
	if err := db.QueryRow(
		`SELECT tool_name, status, risk_class, dedup_hash, channel, resolved_via, request_json
		 FROM permission_requests WHERE id = ?`, id).
		Scan(&r.tool, &r.status, &r.risk, &r.hash, &r.channel, &via, &r.requestJSON); err != nil {
		t.Fatal(err)
	}
	r.via = via.String
	return r
}

func countPermRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM permission_requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestLinkPermissionParityWithShim: the same call asked by the shim and by
// the mod (fresh DBs) lands as the same row: tool, status, risk class and
// dedup hash; only the channel differs. The mod row's request_json is the
// hook-stdin shape.
func TestLinkPermissionParityWithShim(t *testing.T) {
	shimEnv := newLinkPermEnv(t, approvals.Options{})
	res := postHook(shimEnv.srv, hookCtx(t), hookBody("parity-uuid", "Bash", "curl -sI https://example.com"))
	pending := waitPending(t, shimEnv.srv)
	shim := readPermRow(t, shimEnv.db, int64(pending["id"].(float64)))
	if code, _ := shimEnv.decide(t, int64(pending["id"].(float64)), `{"action":"deny"}`); code != http.StatusOK {
		t.Fatalf("deny shim row: %d", code)
	}
	<-res

	modEnv := newLinkPermEnv(t, approvals.Options{})
	code, body := modEnv.ask(t, permBody("parity-uuid", "Bash", "toolu_p1", `{"command":"curl -sI https://example.com"}`))
	if code != http.StatusAccepted {
		t.Fatalf("mod ask: %d %v", code, body)
	}
	mod := readPermRow(t, modEnv.db, idOf(t, body))

	if shim.tool != mod.tool || shim.risk != mod.risk || shim.hash != mod.hash {
		t.Errorf("parity: shim %+v vs mod %+v", shim, mod)
	}
	if shim.status != "pending" || mod.status != "pending" {
		t.Errorf("status: shim %s, mod %s, want pending both", shim.status, mod.status)
	}
	if shim.channel != "shim" || mod.channel != "mod" {
		t.Errorf("channel: shim %q, mod %q", shim.channel, mod.channel)
	}
	var rj map[string]any
	if err := json.Unmarshal([]byte(mod.requestJSON), &rj); err != nil {
		t.Fatalf("request_json %s: %v", mod.requestJSON, err)
	}
	if rj["session_id"] != "parity-uuid" || rj["tool_name"] != "Bash" || rj["tool_use_id"] != "toolu_p1" ||
		rj["hook_event_name"] != "tool.check" || rj["cwd"] != "/work/proj" {
		t.Errorf("request_json = %s", mod.requestJSON)
	}
	if ti, _ := rj["tool_input"].(map[string]any); ti["command"] != "curl -sI https://example.com" {
		t.Errorf("tool_input = %v", rj["tool_input"])
	}
	if _, has := rj["agent_id"]; has {
		t.Error("agent_id present without an agentId")
	}

	// The DTO carries the channel.
	var list []map[string]any
	getJSON(t, modEnv.srv.URL+"/api/approvals", &list)
	if len(list) != 1 || list[0]["channel"] != "mod" {
		t.Errorf("GET /api/approvals = %v, want one row with channel mod", list)
	}
}

// TestLinkPermissionDashboardApproveBetweenPolls: with no GET in flight the
// dashboard approve still lands (the lease is a waiter: 200, not 410) and the
// next GET returns allow.
func TestLinkPermissionDashboardApproveBetweenPolls(t *testing.T) {
	e := newLinkPermEnv(t, approvals.Options{})
	code, body := e.ask(t, permBody("between-uuid", "Bash", "toolu_b1", `{"command":"make build"}`))
	if code != http.StatusAccepted || body["status"] != "pending" || body["pollMs"] != float64(25000) {
		t.Fatalf("ask: %d %v", code, body)
	}
	if exp, _ := body["expiresAt"].(string); !strings.HasSuffix(exp, "Z") || len(exp) != len("2006-01-02T15:04:05.000Z") {
		t.Errorf("expiresAt = %v, want RFC3339 ms", body["expiresAt"])
	}
	id := idOf(t, body)

	if code, raw := e.poll(t, id, "between-uuid", 0); code != http.StatusNoContent {
		t.Fatalf("poll while pending: %d %v", code, raw)
	}
	if code, raw := e.decide(t, id, `{"action":"approve"}`); code != http.StatusOK {
		t.Fatalf("dashboard approve between polls: %d %s", code, raw)
	}
	code, got := e.poll(t, id, "between-uuid", 0)
	if code != http.StatusOK || got["decision"] != "allow" || len(got) != 1 {
		t.Fatalf("poll after approve: %d %v, want exactly {decision:allow}", code, got)
	}
	if r := readPermRow(t, e.db, id); r.via != "dashboard" || r.channel != "mod" || r.status != "approved" {
		t.Errorf("row = %+v, want approved via dashboard on channel mod", r)
	}
}

// TestLinkPermissionPollWakesAndDenies: a GET in flight is woken by a deny
// and carries the reason.
func TestLinkPermissionPollWakesAndDenies(t *testing.T) {
	e := newLinkPermEnv(t, approvals.Options{})
	_, body := e.ask(t, permBody("deny-uuid", "Bash", "toolu_d1", `{"command":"rm -rf /"}`))
	id := idOf(t, body)

	type out struct {
		code int
		body string
		err  error
	}
	done := make(chan out, 1)
	ctx := hookCtx(t)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s/api/link/permission/%d?session=deny-uuid&wait=10", e.srv.URL, id), nil)
		req.Header.Set("Authorization", "Bearer "+e.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- out{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		done <- out{code: resp.StatusCode, body: string(b)}
	}()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	if code, raw := e.decide(t, id, `{"action":"deny","reason":"too dangerous"}`); code != http.StatusOK {
		t.Fatalf("deny: %d %s", code, raw)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		got := decodeObj(t, r.body)
		if r.code != http.StatusOK || got["decision"] != "deny" || got["message"] != "too dangerous" {
			t.Fatalf("poll = %d %v", r.code, got)
		}
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("poll answered %s after the deny", took)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("poll not answered")
	}
	// Re-POST of the decided call (same toolUseId) answers the same decision.
	code, again := e.ask(t, permBody("deny-uuid", "Bash", "toolu_d1", `{"command":"rm -rf /"}`))
	if code != http.StatusOK || again["decision"] != "deny" || again["message"] != "too dangerous" || idOf(t, again) != id {
		t.Errorf("re-POST after deny: %d %v", code, again)
	}
}

// TestLinkPermissionPollTimesOut: a GET with a short wait answers 204 and
// leaves the row pending.
func TestLinkPermissionPollTimesOut(t *testing.T) {
	e := newLinkPermEnv(t, approvals.Options{})
	_, body := e.ask(t, permBody("slow-uuid", "Bash", "toolu_s1", `{"command":"ls"}`))
	id := idOf(t, body)
	start := time.Now()
	if code, raw := e.poll(t, id, "slow-uuid", 1); code != http.StatusNoContent {
		t.Fatalf("poll: %d %v", code, raw)
	}
	if took := time.Since(start); took < 900*time.Millisecond {
		t.Errorf("wait=1 returned after %s", took)
	}
	if r := readPermRow(t, e.db, id); r.status != "pending" {
		t.Errorf("status after a timed-out poll = %s", r.status)
	}
}

// TestLinkPermissionDedup: the same toolUseId twice is one row; a shim ask
// for a call the mod row still holds waits on that row and receives the
// dashboard decision.
func TestLinkPermissionDedup(t *testing.T) {
	e := newLinkPermEnv(t, approvals.Options{})
	modBody := permBody("dedup-uuid", "Bash", "toolu_x1", `{"command":"go test ./..."}`)
	_, first := e.ask(t, modBody)
	code, second := e.ask(t, modBody)
	if code != http.StatusAccepted || idOf(t, second) != idOf(t, first) {
		t.Fatalf("second ask: %d %v (first %v)", code, second, first)
	}
	id := idOf(t, first)

	res := postHook(e.srv, hookCtx(t), hookBody("dedup-uuid", "Bash", "go test ./..."))
	// The shim handler heartbeats before it opens; give the attach a beat.
	deadline := time.Now().Add(3 * time.Second)
	for _, seen := e.svc.LastSeen(); !seen && time.Now().Before(deadline); _, seen = e.svc.LastSeen() {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	// The shim attached rather than creating a row.
	if n := countPermRows(t, e.db); n != 1 {
		t.Fatalf("rows after the shim ask = %d, want 1", n)
	}
	if code, raw := e.decide(t, id, `{"action":"approve"}`); code != http.StatusOK {
		t.Fatalf("approve: %d %s", code, raw)
	}
	select {
	case r := <-res:
		if r.err != nil || r.status != http.StatusOK || !strings.Contains(string(r.body), `"allow"`) {
			t.Fatalf("shim got %d %s %v", r.status, r.body, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shim not woken by the decision on the mod row")
	}
	if code, got := e.poll(t, id, "dedup-uuid", 0); code != http.StatusOK || got["decision"] != "allow" {
		t.Errorf("mod poll: %d %v", code, got)
	}
}

// TestLinkPermissionShimAfterModWindow: within 5 s of the mod row being
// handed back (expired / resolved_elsewhere) a shim ask for the same call
// answers 204 with no new row; after the window it opens its own row.
func TestLinkPermissionShimAfterModWindow(t *testing.T) {
	for _, handBack := range []string{"terminal", "expire"} {
		t.Run(handBack, func(t *testing.T) {
			clock := &permClock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
			e := newLinkPermEnv(t, approvals.Options{Now: clock.Now})
			_, body := e.ask(t, permBody("window-uuid", "Bash", "toolu_w1", `{"command":"npm publish"}`))
			id := idOf(t, body)
			if handBack == "terminal" {
				if code, raw := e.decide(t, id, `{"action":"terminal"}`); code != http.StatusOK {
					t.Fatalf("terminal hand-off: %d %s", code, raw)
				}
			} else if err := e.svc.Expire(id); err != nil {
				t.Fatal(err)
			}
			if code, got := e.poll(t, id, "window-uuid", 0); code != http.StatusOK || got["decision"] != "pass" {
				t.Fatalf("mod poll after hand-back: %d %v", code, got)
			}

			clock.Advance(3 * time.Second)
			select {
			case r := <-postHook(e.srv, hookCtx(t), hookBody("window-uuid", "Bash", "npm publish")):
				if r.err != nil || r.status != http.StatusNoContent {
					t.Fatalf("shim within window: %d %s %v", r.status, r.body, r.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shim within window still polling, want an immediate 204")
			}
			if n := countPermRows(t, e.db); n != 1 {
				t.Fatalf("rows within window = %d, want 1", n)
			}

			clock.Advance(3 * time.Second) // 6 s after the hand-back
			postHook(e.srv, hookCtx(t), hookBody("window-uuid", "Bash", "npm publish"))
			deadline := time.Now().Add(3 * time.Second)
			for countPermRows(t, e.db) != 2 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			var channel string
			if err := e.db.QueryRow(`SELECT channel FROM permission_requests WHERE id != ?`, id).Scan(&channel); err != nil {
				t.Fatalf("no shim row after the window: %v", err)
			}
			if channel != "shim" {
				t.Errorf("new row channel = %q, want shim", channel)
			}
		})
	}
}

// TestLinkPermissionProdDeployIsLocal: a prod deploy answers {"decision":
// "local"} at once and the dashboard cannot approve it.
func TestLinkPermissionProdDeployIsLocal(t *testing.T) {
	e := newLinkPermEnv(t, prodDeployOptions(t))
	code, body := e.ask(t, permBody("prod-uuid", "Bash", "toolu_pd", `{"command":"acme-cli deploy --prod"}`))
	if code != http.StatusOK || body["decision"] != "local" || len(body) != 2 {
		t.Fatalf("prod ask: %d %v, want {id, decision:local}", code, body)
	}
	id := idOf(t, body)
	if r := readPermRow(t, e.db, id); r.risk != approvals.RiskProdDeploy || r.via != approvals.ViaLocalOnly || r.channel != "mod" {
		t.Errorf("row = %+v", r)
	}
	code, raw := e.decide(t, id, `{"action":"approve"}`)
	if code != http.StatusForbidden || !strings.Contains(raw, "prod_deploy_local_only") {
		t.Errorf("dashboard approve of prod row: %d %s", code, raw)
	}
	// A re-POST is still "local"; a GET reads the hand-off as a pass.
	if code, again := e.ask(t, permBody("prod-uuid", "Bash", "toolu_pd", `{"command":"acme-cli deploy --prod"}`)); code != http.StatusOK || again["decision"] != "local" {
		t.Errorf("re-POST: %d %v", code, again)
	}
	if code, got := e.poll(t, id, "prod-uuid", 0); code != http.StatusOK || got["decision"] != "pass" || got["status"] != "resolved_elsewhere" {
		t.Errorf("poll: %d %v", code, got)
	}
}

// TestLinkPermissionRuleAutoApprove: a matching rule answers allow via rule.
func TestLinkPermissionRuleAutoApprove(t *testing.T) {
	e := newLinkPermEnv(t, approvals.Options{})
	if _, err := e.db.Exec(`INSERT INTO approval_rules (project_id, tool_pattern, created_at)
		VALUES (NULL, 'Bash(git *)', '2026-10-14T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	code, body := e.ask(t, permBody("rule-uuid", "Bash", "toolu_r1", `{"command":"git status"}`))
	if code != http.StatusOK || body["decision"] != "allow" || body["via"] != "rule" {
		t.Fatalf("rule ask: %d %v", code, body)
	}
	if code, got := e.poll(t, idOf(t, body), "rule-uuid", 0); code != http.StatusOK || got["decision"] != "allow" {
		t.Errorf("poll: %d %v", code, got)
	}
}

// TestLinkPermissionAnswerRoundTrip: an AskUserQuestion answered from the
// dashboard reaches the mod as {decision:allow, answer:{questions, answers}}
// — also under --answer-delivery=deny-message.
func TestLinkPermissionAnswerRoundTrip(t *testing.T) {
	for _, delivery := range []string{approvals.DeliveryUpdatedInput, approvals.DeliveryDenyMessage} {
		t.Run(delivery, func(t *testing.T) {
			e := newLinkPermEnv(t, approvals.Options{AnswerDelivery: delivery})
			questions := `{"questions":[{"question":"Which DB?","header":"DB","multiSelect":false,"options":[{"label":"SQLite","description":"s"},{"label":"Postgres","description":"p"}]}]}`
			code, body := e.ask(t, permBody("ask-uuid", "AskUserQuestion", "toolu_q1", questions))
			if code != http.StatusAccepted {
				t.Fatalf("ask: %d %v", code, body)
			}
			id := idOf(t, body)

			// The synthesized request_json reads like a shim row for needs-you.
			var resp needsYouResponse
			getJSON(t, e.srv.URL+"/api/needs-you", &resp)
			if len(resp.Items) != 1 || resp.Items[0].Kind != needsYouQuestion ||
				resp.Items[0].Channel != "mod" || !strings.Contains(resp.Items[0].Preview, "Which DB?") {
				t.Errorf("needs-you = %+v", resp.Items)
			}

			if code, raw := e.decide(t, id, `{"action":"answer","answers":{"Which DB?":"Postgres"}}`); code != http.StatusOK {
				t.Fatalf("answer: %d %s", code, raw)
			}
			code, got := e.poll(t, id, "ask-uuid", 0)
			if code != http.StatusOK || got["decision"] != "allow" {
				t.Fatalf("poll: %d %v", code, got)
			}
			answer, _ := got["answer"].(map[string]any)
			qs, _ := answer["questions"].([]any)
			as, _ := answer["answers"].(map[string]any)
			if len(qs) != 1 || as["Which DB?"] != "Postgres" {
				t.Errorf("answer = %v", got["answer"])
			}
			if _, has := got["message"]; has {
				t.Errorf("answer poll carries a message: %v", got)
			}
		})
	}
}

// TestLinkPermissionLeaseLapse: with the lease lapsed the dashboard gets 410,
// and the sweeper resolves the row resolved_elsewhere via terminal.
func TestLinkPermissionLeaseLapse(t *testing.T) {
	clock := &permClock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	e := newLinkPermEnv(t, approvals.Options{Now: clock.Now})
	_, body := e.ask(t, permBody("lapse-uuid", "Bash", "toolu_l1", `{"command":"ls"}`))
	id := idOf(t, body)

	clock.Advance(approvals.ModLeaseTTL + time.Second)
	if code, raw := e.decide(t, id, `{"action":"approve"}`); code != http.StatusGone {
		t.Fatalf("approve after lapse: %d %s, want 410", code, raw)
	}
	e.svc.Sweep()
	if r := readPermRow(t, e.db, id); r.status != "resolved_elsewhere" || r.via != "terminal" {
		t.Fatalf("row after sweep = %+v", r)
	}
	if code, got := e.poll(t, id, "lapse-uuid", 0); code != http.StatusOK || got["decision"] != "pass" || got["status"] != "resolved_elsewhere" {
		t.Errorf("poll after lapse: %d %v", code, got)
	}
}

// TestLinkPermissionErrors: auth, availability, and the 400/404/204/429 paths.
func TestLinkPermissionErrors(t *testing.T) {
	e := newLinkPermEnv(t, approvals.Options{Exclude: ingest.ParseExcludeList(ingest.DefaultExclude)})
	good := permBody("err-uuid", "Bash", "toolu_e1", `{"command":"ls"}`)

	for _, tok := range []string{"", "wrong"} {
		if code, _ := e.req(t, http.MethodPost, "/api/link/permission", tok, good); code != http.StatusUnauthorized {
			t.Errorf("POST token %q: %d, want 401", tok, code)
		}
		if code, _ := e.req(t, http.MethodGet, "/api/link/permission/1?session=err-uuid", tok, ""); code != http.StatusUnauthorized {
			t.Errorf("GET token %q: %d, want 401", tok, code)
		}
	}

	for _, bad := range []string{`not json`, `{"sessionId":"s"}`, `{"toolName":"Bash"}`} {
		if code, _ := e.req(t, http.MethodPost, "/api/link/permission", e.token, bad); code != http.StatusBadRequest {
			t.Errorf("POST %s: %d, want 400", bad, code)
		}
	}

	// Excluded cwd, no session row: 204, nothing stored.
	excluded := `{"sessionId":"tmp-uuid","cwd":"/tmp/scratch","toolName":"Bash","toolInput":{"command":"ls"},"toolUseId":"toolu_t"}`
	if code, raw := e.req(t, http.MethodPost, "/api/link/permission", e.token, excluded); code != http.StatusNoContent {
		t.Errorf("excluded: %d %s, want 204", code, raw)
	}
	if n := countPermRows(t, e.db); n != 0 {
		t.Errorf("rows after excluded ask = %d", n)
	}

	_, body := e.ask(t, good)
	id := idOf(t, body)
	for path, want := range map[string]int{
		fmt.Sprintf("/api/link/permission/%d?session=other-uuid&wait=0", id):  http.StatusNotFound,
		fmt.Sprintf("/api/link/permission/%d?session=err-uuid&wait=0", id+99): http.StatusNotFound,
		"/api/link/permission/abc?session=err-uuid":                           http.StatusBadRequest,
		fmt.Sprintf("/api/link/permission/%d", id):                            http.StatusBadRequest,
		fmt.Sprintf("/api/link/permission/%d?session=err-uuid&wait=x", id):    http.StatusBadRequest,
		fmt.Sprintf("/api/link/permission/%d?session=err-uuid&wait=-5", id):   http.StatusNoContent,
	} {
		if code, raw := e.req(t, http.MethodGet, path, e.token, ""); code != want {
			t.Errorf("GET %s: %d %s, want %d", path, code, raw, want)
		}
	}

	// The per-session pending cap is the shim's: 20 pending, the 21st is 429.
	for i := 2; i <= 20; i++ {
		if code, raw := e.req(t, http.MethodPost, "/api/link/permission", e.token,
			permBody("err-uuid", "Bash", fmt.Sprintf("toolu_e%d", i), fmt.Sprintf(`{"command":"cmd-%d"}`, i))); code != http.StatusAccepted {
			t.Fatalf("ask %d: %d %s", i, code, raw)
		}
	}
	if code, _ := e.req(t, http.MethodPost, "/api/link/permission", e.token,
		permBody("err-uuid", "Bash", "toolu_e21", `{"command":"cmd-21"}`)); code != http.StatusTooManyRequests {
		t.Errorf("21st pending: %d, want 429", code)
	}
}

// TestLinkPermissionUnavailable: 503 with approvals detached, and with the
// link detached.
func TestLinkPermissionUnavailable(t *testing.T) {
	e := newLinkPermEnv(t, approvals.Options{})
	AttachApprovals(nil)
	if code, _ := e.req(t, http.MethodPost, "/api/link/permission", e.token, permBody("u", "Bash", "t", `{}`)); code != http.StatusServiceUnavailable {
		t.Errorf("POST without approvals: %d, want 503", code)
	}
	if code, _ := e.req(t, http.MethodGet, "/api/link/permission/1?session=u", e.token, ""); code != http.StatusServiceUnavailable {
		t.Errorf("GET without approvals: %d, want 503", code)
	}
	AttachApprovals(e.svc)
	AttachLink(nil, nil)
	if code, _ := e.req(t, http.MethodPost, "/api/link/permission", e.token, permBody("u", "Bash", "t", `{}`)); code != http.StatusServiceUnavailable {
		t.Errorf("POST without link: %d, want 503", code)
	}
}
