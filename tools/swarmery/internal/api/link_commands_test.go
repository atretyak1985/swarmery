package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
)

// cmdTestEnv is a linkTestEnv with the command queue attached.
type cmdTestEnv struct {
	*linkTestEnv
	cmds *link.Commands
}

func newCmdTestEnv(t *testing.T) *cmdTestEnv {
	t.Helper()
	e := newLinkTestEnv(t)
	c := link.NewCommands(e.db)
	AttachLinkCommands(c)
	t.Cleanup(func() { AttachLinkCommands(nil) })
	return &cmdTestEnv{linkTestEnv: e, cmds: c}
}

// session mints a mod session through POST /api/link/events and returns its
// sessions.id.
func (e *cmdTestEnv) session(t *testing.T, uuid string) int64 {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"sessionId":%q,"cwd":"/work/ctl","kind":"session.start","at":"2026-10-14T09:00:00Z"}]}`, uuid)
	if code, b := e.do(t, http.MethodPost, "/api/link/events", e.token, body); code != http.StatusAccepted {
		t.Fatalf("mint %s: %d %s", uuid, code, b)
	}
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM sessions WHERE session_uuid = ?`, uuid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// poll is the mod's GET /api/link/commands with the given wait.
func (e *cmdTestEnv) poll(t *testing.T, uuid string, wait int) (int, string) {
	t.Helper()
	return e.do(t, http.MethodGet, fmt.Sprintf("/api/link/commands?session=%s&wait=%d", uuid, wait), e.token, "")
}

// spa sends a dashboard request, with an Origin header when origin != "".
func (e *cmdTestEnv) spa(t *testing.T, path, origin, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func decodeCommandID(t *testing.T, body string) int64 {
	t.Helper()
	var r struct {
		CommandID int64 `json:"commandId"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil || r.CommandID == 0 {
		t.Fatalf("decode %q: %v", body, err)
	}
	return r.CommandID
}

func TestLinkCommandRoutesRequireToken(t *testing.T) {
	e := newCmdTestEnv(t)
	for _, tc := range []struct{ name, method, path, body string }{
		{"poll", http.MethodGet, "/api/link/commands?session=s-1&wait=0", ""},
		{"ack", http.MethodPost, "/api/link/commands/1/ack", `{"result":"done"}`},
	} {
		for _, tok := range []string{"", strings.Repeat("0", 64)} {
			t.Run(fmt.Sprintf("%s token=%t", tc.name, tok != ""), func(t *testing.T) {
				code, body := e.do(t, tc.method, tc.path, tok, tc.body)
				if code != http.StatusUnauthorized || body != `{"error":"link token required"}` {
					t.Fatalf("%d %q, want 401", code, body)
				}
			})
		}
	}
	if e.cmds.Controllable("s-1") {
		t.Error("an unauthenticated poll made the session controllable")
	}
}

func TestLinkCommandRoutesDisabled(t *testing.T) {
	e := newLinkTestEnv(t) // token attached, queue not
	if code, _ := e.do(t, http.MethodGet, "/api/link/commands?session=s&wait=0", e.token, ""); code != http.StatusServiceUnavailable {
		t.Errorf("poll: %d, want 503", code)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/link/commands/1/ack", e.token, `{"result":"done"}`); code != http.StatusServiceUnavailable {
		t.Errorf("ack: %d, want 503", code)
	}
	for _, p := range []string{"/api/sessions/1/link/stop", "/api/sessions/1/link/prompt", "/api/sessions/link/bulk-prompt"} {
		if code, _ := e.do(t, http.MethodPost, p, "", `{}`); code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503", p, code)
		}
	}
	if linkControllable("anything") {
		t.Error("controllable with no queue attached")
	}
}

func TestParseLinkWait(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", 0, true},
		{"0", 0, true},
		{"25", 25 * time.Second, true},
		{"26", link.MaxWait, true},
		{"99999999999999999", link.MaxWait, true},
		{"-1", 0, false},
		{"2.5", 0, false},
		{"soon", 0, false},
	} {
		got, ok := parseLinkWait(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseLinkWait(%q) = %v, %t; want %v, %t", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLinkCommandsPollParams(t *testing.T) {
	e := newCmdTestEnv(t)
	for _, q := range []string{"wait=0", "session=s&wait=x", "session=s&wait=-3", "session=%20&wait=0"} {
		if code, body := e.do(t, http.MethodGet, "/api/link/commands?"+q, e.token, ""); code != http.StatusBadRequest {
			t.Errorf("?%s: %d %s, want 400", q, code, body)
		}
	}
}

func TestLinkCommandsPollEmptyIs204(t *testing.T) {
	e := newCmdTestEnv(t)
	uuid := "a0000000-0000-4000-8000-000000000001"
	e.session(t, uuid)
	if code, body := e.poll(t, uuid, 0); code != http.StatusNoContent || body != "" {
		t.Fatalf("wait=0: %d %q, want 204 with no body", code, body)
	}
	start := time.Now()
	if code, _ := e.poll(t, uuid, 1); code != http.StatusNoContent {
		t.Fatalf("wait=1: %d, want 204", code)
	}
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Errorf("wait=1 answered after %v: it did not wait", el)
	}
	// A poll for a session the daemon has no row for yet is legal too.
	if code, _ := e.poll(t, "not-minted-yet", 0); code != http.StatusNoContent {
		t.Errorf("unknown uuid: %d, want 204", code)
	}
	// A wait above the cap is clamped, not refused: with a command queued it
	// answers at once.
	sid := e.session(t, uuid)
	if code, _ := e.spa(t, fmt.Sprintf("/api/sessions/%d/link/stop", sid), "", ""); code != http.StatusAccepted {
		t.Fatalf("stop: %d", code)
	}
	if code, _ := e.poll(t, uuid, 300); code != http.StatusOK {
		t.Errorf("wait=300: %d, want 200 (clamped, not 400)", code)
	}
}

func TestLinkStopDeliveredAndAcked(t *testing.T) {
	e := newCmdTestEnv(t)
	uuid := "a0000000-0000-4000-8000-000000000002"
	sid := e.session(t, uuid)

	// Never polled → 409.
	stopPath := fmt.Sprintf("/api/sessions/%d/link/stop", sid)
	if code, body := e.spa(t, stopPath, "", ""); code != http.StatusConflict ||
		strings.TrimSpace(body) != `{"error":"session has no link"}` {
		t.Fatalf("unpolled: %d %s, want 409", code, body)
	}
	e.poll(t, uuid, 0)
	if !linkControllable(uuid) {
		t.Fatal("not controllable after a poll")
	}
	code, body := e.spa(t, stopPath, "", "")
	if code != http.StatusAccepted {
		t.Fatalf("stop: %d %s, want 202", code, body)
	}
	id := decodeCommandID(t, body)

	code, body = e.poll(t, uuid, 25)
	if code != http.StatusOK {
		t.Fatalf("poll: %d %s", code, body)
	}
	var cmd struct {
		ID      int64           `json:"id"`
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(body), &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ID != id || cmd.Kind != "stop" || string(cmd.Payload) != "{}" {
		t.Fatalf("poll body = %s", body)
	}
	var delivered *string
	if err := e.db.QueryRow(`SELECT delivered_at FROM link_commands WHERE id = ?`, id).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered == nil || *delivered == "" {
		t.Fatal("delivered_at not set by the 200")
	}

	ackPath := fmt.Sprintf("/api/link/commands/%d/ack", id)
	for i := range 2 { // the second ack is idempotent
		if code, body := e.do(t, http.MethodPost, ackPath, e.token, `{"result":"done"}`); code != http.StatusNoContent {
			t.Fatalf("ack #%d: %d %s, want 204", i+1, code, body)
		}
	}
	var state string
	if err := e.db.QueryRow(`SELECT state FROM link_commands WHERE id = ?`, id).Scan(&state); err != nil || state != "done" {
		t.Fatalf("state = %q, %v", state, err)
	}
}

func TestLinkCommandAckErrors(t *testing.T) {
	e := newCmdTestEnv(t)
	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"unknown id", "/api/link/commands/9999/ack", `{"result":"done"}`, http.StatusNotFound},
		{"bad id", "/api/link/commands/abc/ack", `{"result":"done"}`, http.StatusBadRequest},
		{"not json", "/api/link/commands/1/ack", `done`, http.StatusBadRequest},
		{"bad result", "/api/link/commands/1/ack", `{"result":"maybe"}`, http.StatusBadRequest},
		{"long message", "/api/link/commands/1/ack",
			`{"result":"failed","message":"` + strings.Repeat("x", 501) + `"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := e.do(t, http.MethodPost, tc.path, e.token, tc.body); code != tc.want {
				t.Fatalf("%d %s, want %d", code, body, tc.want)
			}
		})
	}
}

func TestLinkSessionRoutesRefuseForeignOrigin(t *testing.T) {
	e := newCmdTestEnv(t)
	uuid := "a0000000-0000-4000-8000-000000000003"
	sid := e.session(t, uuid)
	e.poll(t, uuid, 0)
	for _, tc := range []struct{ path, body string }{
		{fmt.Sprintf("/api/sessions/%d/link/stop", sid), ""},
		{fmt.Sprintf("/api/sessions/%d/link/prompt", sid), `{"text":"hi"}`},
		{"/api/sessions/link/bulk-prompt", fmt.Sprintf(`{"sessionIds":[%d],"text":"hi"}`, sid)},
	} {
		if code, _ := e.spa(t, tc.path, "https://evil.example", tc.body); code != http.StatusForbidden {
			t.Errorf("%s with a foreign Origin: %d, want 403", tc.path, code)
		}
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM link_commands`).Scan(&n); err != nil || n != 0 {
		t.Errorf("link_commands = %d after refused requests, want 0", n)
	}
}

func TestLinkSessionRoutesNotFoundAndBadID(t *testing.T) {
	e := newCmdTestEnv(t)
	if code, _ := e.spa(t, "/api/sessions/987654/link/stop", "", ""); code != http.StatusNotFound {
		t.Errorf("unknown session stop: %d, want 404", code)
	}
	if code, _ := e.spa(t, "/api/sessions/987654/link/prompt", "", `{"text":"hi"}`); code != http.StatusNotFound {
		t.Errorf("unknown session prompt: %d, want 404", code)
	}
	if code, _ := e.spa(t, "/api/sessions/x/link/stop", "", ""); code != http.StatusBadRequest {
		t.Errorf("non-numeric id: %d, want 400", code)
	}
}

func TestLinkSessionPrompt(t *testing.T) {
	e := newCmdTestEnv(t)
	uuid := "a0000000-0000-4000-8000-000000000004"
	sid := e.session(t, uuid)
	e.poll(t, uuid, 0)
	path := fmt.Sprintf("/api/sessions/%d/link/prompt", sid)
	for _, body := range []string{
		`{"text":""}`, `{"text":"  \n\t"}`, `{}`, `not json`,
		`{"text":"` + strings.Repeat("a", 16<<10+1) + `"}`,
	} {
		if code, _ := e.spa(t, path, "", body); code != http.StatusBadRequest {
			t.Errorf("body %.40q: %d, want 400", body, code)
		}
	}
	code, body := e.spa(t, path, "", `{"text":"continue","asUser":true}`)
	if code != http.StatusAccepted {
		t.Fatalf("prompt: %d %s", code, body)
	}
	id := decodeCommandID(t, body)
	code, body = e.poll(t, uuid, 0)
	if code != http.StatusOK || !strings.Contains(body, fmt.Sprintf(`"id":%d`, id)) ||
		!strings.Contains(body, `"payload":{"text":"continue","asUser":true}`) {
		t.Fatalf("poll: %d %s", code, body)
	}
}

func TestLinkBulkPromptMixedSelection(t *testing.T) {
	e := newCmdTestEnv(t)
	linked := "a0000000-0000-4000-8000-000000000005"
	unlinked := "a0000000-0000-4000-8000-000000000006"
	a := e.session(t, linked)
	b := e.session(t, unlinked)
	e.poll(t, linked, 0)

	body := fmt.Sprintf(`{"sessionIds":[%d,%d,424242,%d],"text":"next phase"}`, a, b, a)
	code, resp := e.spa(t, "/api/sessions/link/bulk-prompt", "", body)
	if code != http.StatusOK {
		t.Fatalf("bulk: %d %s", code, resp)
	}
	var got bulkPromptResponse
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Queued) != 1 || got.Queued[0].ID != a || got.Queued[0].CommandID == 0 {
		t.Errorf("queued = %+v, want only session %d (once)", got.Queued, a)
	}
	wantSkipped := []bulkSkipped{{ID: b, Reason: "session has no link"}, {ID: 424242, Reason: "not found"}}
	if fmt.Sprint(got.Skipped) != fmt.Sprint(wantSkipped) {
		t.Errorf("skipped = %+v, want %+v", got.Skipped, wantSkipped)
	}
	code, cmd := e.poll(t, linked, 0)
	if code != http.StatusOK || !strings.Contains(cmd, `"payload":{"text":"next phase","asUser":false}`) {
		t.Errorf("poll: %d %s", code, cmd)
	}

	// Nothing deliverable for anyone: both lists render as [], not null.
	code, resp = e.spa(t, "/api/sessions/link/bulk-prompt", "", fmt.Sprintf(`{"sessionIds":[%d],"text":"x"}`, b))
	if code != http.StatusOK || !strings.Contains(resp, `"queued":[]`) {
		t.Errorf("all skipped: %d %s", code, resp)
	}

	ids := make([]string, 51)
	for i := range ids {
		ids[i] = fmt.Sprint(i + 1)
	}
	for _, bad := range []string{
		`{"sessionIds":[1],"text":"   "}`,
		`{"sessionIds":[],"text":"x"}`,
		`{"text":"x"}`,
		`{"sessionIds":[` + strings.Join(ids, ",") + `],"text":"x"}`,
		`[1,2]`,
	} {
		if code, _ := e.spa(t, "/api/sessions/link/bulk-prompt", "", bad); code != http.StatusBadRequest {
			t.Errorf("%.60s: %d, want 400", bad, code)
		}
	}
}

// Every transition reaches the WS bus as a link_command note, and the WS
// layer renders it as the contract's frame.
func TestLinkCommandPublishesWSNote(t *testing.T) {
	e := newCmdTestEnv(t)
	bus := ingest.NewBus()
	AttachBus(bus)
	t.Cleanup(func() { AttachBus(nil) })
	ch, cancel := bus.Subscribe(64)
	defer cancel()

	uuid := "a0000000-0000-4000-8000-000000000007"
	sid := e.session(t, uuid)
	e.poll(t, uuid, 0)
	code, body := e.spa(t, fmt.Sprintf("/api/sessions/%d/link/stop", sid), "", "")
	if code != http.StatusAccepted {
		t.Fatalf("stop: %d %s", code, body)
	}
	id := decodeCommandID(t, body)

	h := &Handler{DB: e.db}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-ch:
			if n.Type != ingest.NoteLinkCommand {
				continue // session_started / session_updated from the mint
			}
			frame, err := h.buildWSMessage(n)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf(`{"type":"link_command","payload":{"sessionId":%d,"commandId":%d,"kind":"stop","state":"queued"}}`, sid, id)
			if string(frame) != want {
				t.Fatalf("frame = %s\nwant    %s", frame, want)
			}
			return
		case <-deadline:
			t.Fatal("no link_command note on the bus after a 202")
		}
	}
}

// A prompt bounced on "turn in progress" is held; the session's next
// turn.complete on POST /api/link/events releases it and wakes the waiting poll.
func TestLinkTurnCompleteReleasesHeldPrompt(t *testing.T) {
	e := newCmdTestEnv(t)
	uuid := "a0000000-0000-4000-8000-000000000008"
	sid := e.session(t, uuid)
	e.poll(t, uuid, 0)
	_, body := e.spa(t, fmt.Sprintf("/api/sessions/%d/link/prompt", sid), "", `{"text":"go on"}`)
	id := decodeCommandID(t, body)
	if code, _ := e.poll(t, uuid, 0); code != http.StatusOK {
		t.Fatalf("first delivery: %d", code)
	}
	if code, b := e.do(t, http.MethodPost, fmt.Sprintf("/api/link/commands/%d/ack", id), e.token,
		`{"result":"failed","message":"turn in progress"}`); code != http.StatusNoContent {
		t.Fatalf("ack: %d %s", code, b)
	}
	if code, _ := e.poll(t, uuid, 0); code != http.StatusNoContent {
		t.Fatalf("held prompt delivered before turn.complete: %d", code)
	}

	got := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/link/commands?wait=10&session="+uuid, nil)
		req.Header.Set("Authorization", "Bearer "+e.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- -1
			return
		}
		resp.Body.Close()
		got <- resp.StatusCode
	}()
	time.Sleep(100 * time.Millisecond)
	ev := fmt.Sprintf(`{"events":[{"sessionId":%q,"kind":"turn.complete","at":"2026-10-14T09:05:00Z"}]}`, uuid)
	if code, b := e.do(t, http.MethodPost, "/api/link/events", e.token, ev); code != http.StatusAccepted {
		t.Fatalf("turn.complete: %d %s", code, b)
	}
	select {
	case code := <-got:
		if code != http.StatusOK {
			t.Fatalf("woken poll: %d, want 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn.complete did not wake the waiting poll")
	}
}

// TestSessionLinkControllable pins sessionDTO.link.controllable: false until
// the session's mod polls the command queue, true right after a poll — on the
// detail and the list read paths alike.
func TestSessionLinkControllable(t *testing.T) {
	e := newCmdTestEnv(t)
	uuid := "a0000000-0000-4000-8000-0000000000c1"
	sid := e.session(t, uuid)

	controllable := func(t *testing.T) (detail, list bool) {
		t.Helper()
		var d linkProbe
		getJSON(t, fmt.Sprintf("%s/api/sessions/%d", e.srv.URL, sid), &d)
		if got := decodeLink(t, d.Link); got != nil {
			detail = got.Controllable
		}
		var page struct {
			Sessions []linkProbe `json:"sessions"`
		}
		getJSON(t, e.srv.URL+"/api/sessions", &page)
		for _, s := range page.Sessions {
			if s.ID == sid {
				if got := decodeLink(t, s.Link); got != nil {
					list = got.Controllable
				}
			}
		}
		return detail, list
	}

	if d, l := controllable(t); d || l {
		t.Fatalf("before any poll: detail=%t list=%t, want false", d, l)
	}
	if code, _ := e.poll(t, uuid, 0); code != http.StatusNoContent {
		t.Fatalf("poll: %d", code)
	}
	if d, l := controllable(t); !d || !l {
		t.Fatalf("after a poll: detail=%t list=%t, want true", d, l)
	}
	AttachLinkCommands(nil)
	if d, l := controllable(t); d || l {
		t.Fatalf("queue detached: detail=%t list=%t, want false", d, l)
	}
}
