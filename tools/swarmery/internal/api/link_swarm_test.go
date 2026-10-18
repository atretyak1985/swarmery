package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

const (
	swarmA = "5b000000-0000-4000-8000-00000000000a"
	swarmB = "5b000000-0000-4000-8000-00000000000b"
	swarmC = "5b000000-0000-4000-8000-00000000000c" // another project
)

// swarmPost posts raw events JSON (the inside of "events":[…]) with the token.
func (e *cmdTestEnv) swarmPost(t *testing.T, events ...string) (int, string) {
	t.Helper()
	return e.do(t, http.MethodPost, "/api/link/events", e.token, `{"events":[`+strings.Join(events, ",")+`]}`)
}

func swarmEvent(session, cwd, kind, at, payload string) string {
	return fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"kind":%q,"at":%q,"payload":%s}`, session, cwd, kind, at, payload)
}

// The five swarm kinds go through POST /api/link/events like any other kind; a
// malformed one is a per-event reject inside the 202.
func TestLinkEventsAcceptsSwarmKinds(t *testing.T) {
	e := newCmdTestEnv(t)
	code, body := e.swarmPost(t,
		swarmEvent(swarmA, "/work/sw", "session.send", "2026-10-14T09:00:00Z", fmt.Sprintf(`{"to":%q,"text":"hi","truncated":false,"isDelivered":true,"origin":"model"}`, swarmB)),
		swarmEvent(swarmB, "/work/sw", "session.receive", "2026-10-14T09:00:01Z", `{"text":"hi","truncated":false,"originKind":"peer"}`),
		swarmEvent(swarmA, "/work/sw", "claim", "2026-10-14T09:00:02Z", `{"path":"/work/sw/a.go"}`),
		swarmEvent(swarmA, "/work/sw", "release", "2026-10-14T09:00:03Z", `{"paths":["/work/sw/a.go"]}`),
		swarmEvent(swarmA, "/work/sw", "conflict", "2026-10-14T09:00:04Z", fmt.Sprintf(`{"path":"/work/sw/a.go","otherSession":%q}`, swarmB)),
		swarmEvent(swarmA, "/work/sw", "claim", "2026-10-14T09:00:05Z", `{"path":"relative.go"}`),
	)
	if code != http.StatusAccepted {
		t.Fatalf("POST: %d %s", code, body)
	}
	res := decodeLinkResult(t, body)
	if res.Accepted != 5 || res.Rejected != 1 || len(res.Rejects) != 1 || res.Rejects[0].Index != 5 ||
		!strings.HasPrefix(res.Rejects[0].Reason, "claim payload: ") {
		t.Fatalf("result = %+v", res)
	}
	var paired int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM link_messages WHERE matched_id IS NOT NULL`).Scan(&paired); err != nil || paired != 2 {
		t.Errorf("paired rows = %d (%v), want 2", paired, err)
	}
}

// GET /api/swarm/messages: the exact key set, newest first, session refs,
// filters and parameter errors.
func TestSwarmMessagesEndpoint(t *testing.T) {
	e := newCmdTestEnv(t)
	e.swarmPost(t,
		swarmEvent(swarmA, "/work/sw", "session.send", "2026-10-14T09:00:00Z", fmt.Sprintf(`{"to":%q,"text":"one"}`, swarmB)),
		swarmEvent(swarmB, "/work/sw", "session.receive", "2026-10-14T09:00:01Z", `{"text":"one","originKind":"peer"}`),
		swarmEvent(swarmC, "/work/other", "session.send", "2026-10-14T09:00:02Z", `{"text":"two","to":"reviewer"}`),
	)
	code, body := e.do(t, http.MethodGet, "/api/swarm/messages", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET: %d %s", code, body)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil || len(raw) != 3 {
		t.Fatalf("decode %s: %v", body, err)
	}
	want := []string{"at", "body", "delivered", "fromSession", "id", "kind", "origin", "reason", "toAddress", "toSession", "truncated"}
	if got := keysOf(raw[0]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v", got, want)
	}
	var msgs []struct {
		Kind        string
		Body        string
		FromSession *struct {
			ID    int64   `json:"id"`
			UUID  string  `json:"uuid"`
			Title *string `json:"title"`
		} `json:"fromSession"`
		Delivered *bool `json:"delivered"`
	}
	if err := json.Unmarshal([]byte(body), &msgs); err != nil {
		t.Fatal(err)
	}
	if msgs[0].Body != "two" || msgs[1].Kind != "receive" || msgs[1].FromSession == nil || msgs[1].FromSession.UUID != swarmA ||
		msgs[2].Delivered == nil || !*msgs[2].Delivered || msgs[0].Delivered != nil {
		t.Errorf("messages = %s", body)
	}

	var pid int64
	if err := e.db.QueryRow(`SELECT project_id FROM sessions WHERE session_uuid = ?`, swarmC).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	_, body = e.do(t, http.MethodGet, fmt.Sprintf("/api/swarm/messages?projectId=%d", pid), "", "")
	if err := json.Unmarshal([]byte(body), &msgs); err != nil || len(msgs) != 1 || msgs[0].Body != "two" {
		t.Errorf("project filter: %s", body)
	}
	_, body = e.do(t, http.MethodGet, "/api/swarm/messages?since=2026-10-14T11:00:01%2B02:00&limit=900", "", "")
	if err := json.Unmarshal([]byte(body), &msgs); err != nil || len(msgs) != 2 {
		t.Errorf("since (offset form) + clamped limit: %s", body)
	}
	_, body = e.do(t, http.MethodGet, "/api/swarm/messages?limit=1", "", "")
	if err := json.Unmarshal([]byte(body), &msgs); err != nil || len(msgs) != 1 {
		t.Errorf("limit=1: %s", body)
	}
	_, body = e.do(t, http.MethodGet, "/api/swarm/messages?projectId=424242", "", "")
	if strings.TrimSpace(body) != "[]" {
		t.Errorf("empty list = %s, want []", body)
	}
	for _, q := range []string{"projectId=x", "projectId=-1", "since=yesterday", "limit=0", "limit=many"} {
		if code, _ := e.do(t, http.MethodGet, "/api/swarm/messages?"+q, "", ""); code != http.StatusBadRequest {
			t.Errorf("?%s: %d, want 400", q, code)
		}
	}
}

// GET /api/swarm/claims: live claims and conflicts, project filter, errors.
func TestSwarmClaimsEndpoint(t *testing.T) {
	e := newCmdTestEnv(t)
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	e.swarmPost(t,
		swarmEvent(swarmA, "/work/sw", "claim", at(-time.Minute), `{"path":"/work/sw/shared.go"}`),
		swarmEvent(swarmB, "/work/sw", "claim", at(-30*time.Second), `{"path":"/work/sw/shared.go"}`),
		swarmEvent(swarmB, "/work/sw", "claim", at(-31*time.Minute), `{"path":"/work/sw/stale.go"}`),
		swarmEvent(swarmC, "/work/other", "claim", at(-time.Minute), `{"path":"/work/other/x.go"}`),
	)
	code, body := e.do(t, http.MethodGet, "/api/swarm/claims", "", "")
	if code != http.StatusOK {
		t.Fatalf("GET: %d %s", code, body)
	}
	var dto struct {
		Claims []struct {
			SessionID   int64  `json:"sessionId"`
			SessionUUID string `json:"sessionUuid"`
			Path        string `json:"path"`
			ClaimedAt   string `json:"claimedAt"`
		} `json:"claims"`
		Conflicts []struct {
			Path     string `json:"path"`
			Sessions []struct {
				SessionID   int64  `json:"sessionId"`
				SessionUUID string `json:"sessionUuid"`
			} `json:"sessions"`
		} `json:"conflicts"`
	}
	if err := json.Unmarshal([]byte(body), &dto); err != nil {
		t.Fatal(err)
	}
	if len(dto.Claims) != 3 || len(dto.Conflicts) != 1 || dto.Conflicts[0].Path != "/work/sw/shared.go" ||
		len(dto.Conflicts[0].Sessions) != 2 || dto.Conflicts[0].Sessions[0].SessionID == 0 {
		t.Fatalf("claims = %s", body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	var claimKeys []map[string]json.RawMessage
	if err := json.Unmarshal(raw["claims"], &claimKeys); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(keysOf(claimKeys[0]), ","); got != "claimedAt,path,sessionId,sessionUuid" {
		t.Errorf("claim keys = %s", got)
	}

	var pid int64
	if err := e.db.QueryRow(`SELECT project_id FROM sessions WHERE session_uuid = ?`, swarmC).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	_, body = e.do(t, http.MethodGet, fmt.Sprintf("/api/swarm/claims?projectId=%d", pid), "", "")
	if err := json.Unmarshal([]byte(body), &dto); err != nil || len(dto.Claims) != 1 || len(dto.Conflicts) != 0 {
		t.Errorf("project filter: %s", body)
	}
	_, body = e.do(t, http.MethodGet, "/api/swarm/claims?projectId=424242", "", "")
	if strings.TrimSpace(body) != `{"claims":[],"conflicts":[]}` {
		t.Errorf("empty = %s", body)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/swarm/claims?projectId=x", "", ""); code != http.StatusBadRequest {
		t.Errorf("bad projectId: %d, want 400", code)
	}
}

// POST /api/sessions/{id}/link/message: refusals, then a relay the sender's
// mod picks up and acks done — the dashboard row reads delivered.
func TestLinkSessionMessage(t *testing.T) {
	e := newCmdTestEnv(t)
	sender := e.session(t, swarmA)
	recipient := e.session(t, swarmB)
	path := fmt.Sprintf("/api/sessions/%d/link/message", sender)
	body := func(to any, text string) string {
		b, _ := json.Marshal(map[string]any{"to": to, "text": text})
		return string(b)
	}

	if code, b := e.spa(t, path, "", body(recipient, "hi")); code != http.StatusConflict || !strings.Contains(b, "session has no link") {
		t.Errorf("not controllable: %d %s, want 409", code, b)
	}
	e.poll(t, swarmA, 0)
	for name, tc := range map[string]struct {
		path, body string
		want       int
	}{
		"empty text":        {path, body(recipient, " "), http.StatusBadRequest},
		"long text":         {path, body(recipient, strings.Repeat("x", 4097)), http.StatusBadRequest},
		"unknown recipient": {path, body(987654, "hi"), http.StatusBadRequest},
		"unknown uuid":      {path, body("nope", "hi"), http.StatusBadRequest},
		"missing to":        {path, `{"text":"hi"}`, http.StatusBadRequest},
		"to is sender":      {path, body(swarmA, "hi"), http.StatusBadRequest},
		"malformed":         {path, `not json`, http.StatusBadRequest},
		"bad id":            {"/api/sessions/x/link/message", body(recipient, "hi"), http.StatusBadRequest},
		"unknown sender":    {"/api/sessions/987654/link/message", body(recipient, "hi"), http.StatusNotFound},
	} {
		if code, b := e.spa(t, tc.path, "", tc.body); code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, code, b, tc.want)
		}
	}
	if code, _ := e.spa(t, path, "https://evil.example", body(recipient, "hi")); code != http.StatusForbidden {
		t.Errorf("foreign origin: %d, want 403", code)
	}
	var n int
	if err := e.db.QueryRow(`SELECT (SELECT COUNT(*) FROM link_commands) + (SELECT COUNT(*) FROM link_messages)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after refusals = %d (%v), want 0", n, err)
	}

	// The recipient by db id and by uuid string both resolve.
	for _, to := range []any{recipient, swarmB, fmt.Sprint(recipient)} {
		code, b := e.spa(t, path, "", body(to, "rebase please"))
		if code != http.StatusAccepted {
			t.Fatalf("to=%v: %d %s", to, code, b)
		}
		var q struct {
			CommandID int64 `json:"commandId"`
			MessageID int64 `json:"messageId"`
		}
		if err := json.Unmarshal([]byte(b), &q); err != nil || q.CommandID == 0 || q.MessageID == 0 {
			t.Fatalf("202 body %s", b)
		}
		code, b = e.poll(t, swarmA, 0)
		want := fmt.Sprintf(`"kind":"message","payload":{"to":%q,"text":"rebase please"}`, swarmB)
		if code != http.StatusOK || !strings.Contains(b, want) {
			t.Fatalf("poll: %d %s, want %s", code, b, want)
		}
		if code, b := e.do(t, http.MethodPost, fmt.Sprintf("/api/link/commands/%d/ack", q.CommandID), e.token, `{"result":"done"}`); code != http.StatusNoContent {
			t.Fatalf("ack: %d %s", code, b)
		}
	}
	_, b := e.do(t, http.MethodGet, "/api/swarm/messages", "", "")
	var msgs []struct {
		Kind      string `json:"kind"`
		Delivered *bool  `json:"delivered"`
		ToSession *struct {
			ID int64 `json:"id"`
		} `json:"toSession"`
	}
	if err := json.Unmarshal([]byte(b), &msgs); err != nil || len(msgs) != 3 {
		t.Fatalf("messages: %s", b)
	}
	for _, m := range msgs {
		if m.Kind != "dashboard" || m.Delivered == nil || !*m.Delivered || m.ToSession == nil || m.ToSession.ID != recipient {
			t.Errorf("dashboard message = %+v", m)
		}
	}

	// A failed ack is recorded with its reason.
	_, b = e.spa(t, path, "", body(recipient, "again"))
	id := decodeCommandID(t, b)
	e.poll(t, swarmA, 0)
	e.do(t, http.MethodPost, fmt.Sprintf("/api/link/commands/%d/ack", id), e.token, `{"result":"failed","message":"undeliverable: peer gone"}`)
	var reason string
	if err := e.db.QueryRow(`SELECT reason FROM link_messages WHERE command_id = ? AND delivered = 0`, id).Scan(&reason); err != nil ||
		reason != "undeliverable: peer gone" {
		t.Errorf("failed reason = %q (%v)", reason, err)
	}
}

func TestLinkSessionMessageDisabled(t *testing.T) {
	e := newLinkTestEnv(t)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/sessions/1/link/message", strings.NewReader(`{"to":2,"text":"hi"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("detached queue: %d, want 503", resp.StatusCode)
	}
}

// swarm_message / swarm_claim reach the WS bus after the batch commits and
// render as the contract's thin frames.
func TestSwarmPublishesWSFrames(t *testing.T) {
	e := newCmdTestEnv(t)
	bus := ingest.NewBus()
	AttachBus(bus)
	t.Cleanup(func() { AttachBus(nil) })
	ch, cancel := bus.Subscribe(64)
	defer cancel()

	e.swarmPost(t,
		swarmEvent(swarmA, "/work/sw", "session.send", "2026-10-14T09:00:00Z", `{"to":"x","text":"hi"}`),
		swarmEvent(swarmA, "/work/sw", "claim", "2026-10-14T09:00:01Z", `{"path":"/work/sw/a.go"}`),
	)
	var sid, pid int64
	if err := e.db.QueryRow(`SELECT id, project_id FROM sessions WHERE session_uuid = ?`, swarmA).Scan(&sid, &pid); err != nil {
		t.Fatal(err)
	}
	h := &Handler{DB: e.db}
	want := map[string]string{
		ingest.NoteSwarmMessage: fmt.Sprintf(`{"type":"swarm_message","payload":{"sessionId":%d,"projectId":%d}}`, sid, pid),
		ingest.NoteSwarmClaim:   fmt.Sprintf(`{"type":"swarm_claim","payload":{"projectId":%d}}`, pid),
	}
	deadline := time.After(2 * time.Second)
	for len(want) > 0 {
		select {
		case n := <-ch:
			w, ok := want[n.Type]
			if !ok {
				continue
			}
			frame, err := h.buildWSMessage(n)
			if err != nil {
				t.Fatal(err)
			}
			if string(frame) != w {
				t.Errorf("frame = %s\nwant    %s", frame, w)
			}
			delete(want, n.Type)
		case <-deadline:
			t.Fatalf("frames never published: %v", want)
		}
	}
}
