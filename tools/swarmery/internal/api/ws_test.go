package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// Frozen contract (web/src/api/types.ts): exact JSON key sets of the WSMessage
// payloads. Any drift between the Go DTOs and types.ts fails this test.
var (
	sessionKeys = []string{
		"id", "projectId", "projectSlug", "projectName", "sessionUuid", "model", "gitBranch",
		"cwd", "status", "startedAt", "endedAt", "title", "source",
		// parity wave: additive per-session aggregates (nullable).
		"tokens", "costUsd",
		// phase 3.5: workspaces — additive best-task-link fields (nullable).
		"taskId", "taskExternalId", "taskLinkSource", "taskConfidence",
		// phase 4 step-07+: process liveness (nullable).
		"procState", "procPid",
		// canvas wave: one-line intent from the first user turn (omitempty —
		// present here because the fixture session has a user prompt).
		"why",
		// session composer: dashboard-initiated resume run flag (in-memory).
		"resumeInFlight",
	}
	eventKeys = []string{
		"id", "turnId", "ts", "type", "toolName", "parentEventId",
		"status", "durationMs", "payload",
	}
	// phase 2 — approvals (frozen at gate 2.2): PermissionRequest DTO keys.
	permissionRequestKeys = []string{
		"id", "sessionId", "toolName", "requestJson", "status",
		"requestedAt", "resolvedAt", "resolvedVia", "reason", "expiresAt",
	}
)

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestWSMessageShape: dial /api/ws for real, publish one notification of each
// type, and verify the frames match the frozen WSMessage contract exactly.
func TestWSMessageShape(t *testing.T) {
	bus := ingest.NewBus()
	AttachBus(bus)
	t.Cleanup(func() { AttachBus(nil) })

	srv := testServer(t) // ingests the subagent fixture → session id 1 exists

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	readFrame := newFrameReader(t, ctx, c, func() {
		bus.Publish(ingest.Notification{Type: ingest.NoteSessionStarted, SessionID: 1})
	})

	// session_started (poll-published above).
	frame := readFrame()
	assertEnvelope(t, frame, "session_started")
	assertPayloadKeys(t, frame, sessionKeys)
	var sess struct {
		ID          int64  `json:"id"`
		SessionUUID string `json:"sessionUuid"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(frame["payload"], &sess); err != nil {
		t.Fatal(err)
	}
	if sess.ID != 1 || sess.SessionUUID == "" {
		t.Errorf("session payload = %+v, want hydrated session row 1", sess)
	}
	if sess.Status != "active" && sess.Status != "idle" && sess.Status != "completed" {
		t.Errorf("session status %q outside the MVP vocabulary", sess.Status)
	}

	// session_updated — same payload shape.
	bus.Publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: 1})
	frame = readFrame()
	assertEnvelope(t, frame, "session_updated")
	assertPayloadKeys(t, frame, sessionKeys)

	// event_appended — {sessionId, event: Event DTO} payload (step-10 contract).
	bus.Publish(ingest.Notification{Type: ingest.NoteEventAppended, SessionID: 1, EventID: 1})
	frame = readFrame()
	assertEnvelope(t, frame, "event_appended")
	assertPayloadKeys(t, frame, []string{"sessionId", "event"})
	var wrapper struct {
		SessionID int64           `json:"sessionId"`
		Event     json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(frame["payload"], &wrapper); err != nil {
		t.Fatal(err)
	}
	if wrapper.SessionID != 1 {
		t.Errorf("event_appended sessionId = %d, want 1", wrapper.SessionID)
	}
	var eventObj map[string]json.RawMessage
	if err := json.Unmarshal(wrapper.Event, &eventObj); err != nil {
		t.Fatalf("event is not a JSON object: %v", err)
	}
	if got, want := keysOf(eventObj), sortedCopy(eventKeys); len(got) != len(want) {
		t.Fatalf("event keys = %v, want %v", got, want)
	} else {
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("event keys = %v, want %v", got, want)
			}
		}
	}
	var ev struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
		TS   string `json:"ts"`
	}
	if err := json.Unmarshal(wrapper.Event, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != 1 || ev.Type == "" || ev.TS == "" {
		t.Errorf("event payload = %+v, want hydrated event row 1", ev)
	}
}

// TestWSPermissionMessageShape: golden-key check for the two phase-2 frames —
// permission_requested / permission_resolved carry the full frozen
// PermissionRequest DTO (docs/ws-protocol.md).
func TestWSPermissionMessageShape(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	bus := ingest.NewBus()
	AttachBus(bus)
	svc := approvals.New(db, bus, approvals.Options{})
	AttachApprovals(svc)
	t.Cleanup(func() { AttachBus(nil); AttachApprovals(nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Mint a real permission_requests row through the service.
	in, err := approvals.ParseHookStdin([]byte(hookBody("ws-shape", "Bash", "ls")))
	if err != nil {
		t.Fatal(err)
	}
	reqID, ch, _, err := svc.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		svc.Resolve(reqID, approvals.StatusApproved, "dashboard", "")
		select {
		case <-ch:
		default:
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	readFrame := newFrameReader(t, ctx, c, func() {
		bus.Publish(ingest.Notification{Type: ingest.NotePermissionRequested, SessionID: 1, RequestID: reqID})
	})

	frame := readFrame()
	assertEnvelope(t, frame, "permission_requested")
	assertPayloadKeys(t, frame, permissionRequestKeys)
	var pr struct {
		ID          int64  `json:"id"`
		SessionID   int64  `json:"sessionId"`
		ToolName    string `json:"toolName"`
		RequestJSON string `json:"requestJson"`
		Status      string `json:"status"`
		ExpiresAt   string `json:"expiresAt"`
	}
	if err := json.Unmarshal(frame["payload"], &pr); err != nil {
		t.Fatal(err)
	}
	if pr.ID != reqID || pr.ToolName != "Bash" || pr.Status != "pending" ||
		pr.SessionID == 0 || pr.ExpiresAt == "" {
		t.Errorf("permission_requested payload = %+v, want hydrated pending row %d", pr, reqID)
	}
	// requestJson is the raw hook stdin as a JSON STRING (frozen contract).
	var echoed map[string]any
	if err := json.Unmarshal([]byte(pr.RequestJSON), &echoed); err != nil {
		t.Fatalf("requestJson is not a JSON string of the stdin: %v", err)
	}
	if echoed["session_id"] != "ws-shape" {
		t.Errorf("requestJson lost the verbatim stdin: %v", echoed)
	}

	// Resolve → permission_resolved carries the same DTO with resolution fields.
	if err := svc.Resolve(reqID, approvals.StatusDenied, "dashboard", "nope"); err != nil {
		t.Fatal(err)
	}
	<-ch
	frame = readFrame()
	for {
		var typ string
		json.Unmarshal(frame["type"], &typ)
		if typ == "permission_resolved" {
			break
		}
		// Skip the session_updated/event_appended frames the resolve produced.
		frame = readFrame()
	}
	assertPayloadKeys(t, frame, permissionRequestKeys)
	var resolved struct {
		Status      string  `json:"status"`
		ResolvedVia *string `json:"resolvedVia"`
		Reason      *string `json:"reason"`
		ResolvedAt  *string `json:"resolvedAt"`
	}
	if err := json.Unmarshal(frame["payload"], &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "denied" || resolved.ResolvedVia == nil || *resolved.ResolvedVia != "dashboard" ||
		resolved.Reason == nil || *resolved.Reason != "nope" || resolved.ResolvedAt == nil {
		t.Errorf("permission_resolved payload = %+v", resolved)
	}
}

// newFrameReader wires the poll-publish handshake: the /api/ws handler
// subscribes to the bus only after the upgrade returns, so poll is republished
// on an interval until its first frame arrives. Reads carry the test's full
// deadline — in coder/websocket an expired Read context closes the ENTIRE
// connection (setupReadTimeout → c.close()), so a short per-read timeout is
// unrecoverable once the first read misses (the CI-only 10 s hang this
// replaces). Republishing can deliver the polled frame more than once; after
// the first frame is returned, later duplicates of its type are skipped.
func newFrameReader(t *testing.T, ctx context.Context, c *websocket.Conn, poll func()) func() map[string]json.RawMessage {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			poll()
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	subscribed := false
	t.Cleanup(func() {
		if !subscribed {
			close(stop)
		}
		<-done
	})
	var pollType string
	return func() map[string]json.RawMessage {
		t.Helper()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if typ != websocket.MessageText {
				t.Fatalf("frame type = %v, want text", typ)
			}
			var frame map[string]json.RawMessage
			if err := json.Unmarshal(data, &frame); err != nil {
				t.Fatalf("frame is not a JSON object: %v\n%s", err, data)
			}
			var frameType string
			if err := json.Unmarshal(frame["type"], &frameType); err != nil {
				t.Fatalf("frame type is not a JSON string: %v\n%s", err, data)
			}
			if !subscribed {
				subscribed = true
				pollType = frameType
				close(stop)
				<-done
				return frame
			}
			if frameType == pollType {
				continue // duplicate of the poll-published note
			}
			return frame
		}
	}
}

func assertEnvelope(t *testing.T, frame map[string]json.RawMessage, wantType string) {
	t.Helper()
	if got := keysOf(frame); len(got) != 2 || got[0] != "payload" || got[1] != "type" {
		t.Fatalf("envelope keys = %v, want exactly [payload type]", got)
	}
	var typ string
	if err := json.Unmarshal(frame["type"], &typ); err != nil || typ != wantType {
		t.Fatalf("type = %q (%v), want %q", typ, err, wantType)
	}
}

func assertPayloadKeys(t *testing.T, frame map[string]json.RawMessage, want []string) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(frame["payload"], &payload); err != nil {
		t.Fatalf("payload is not a JSON object: %v", err)
	}
	got := keysOf(payload)
	wantSorted := sortedCopy(want)
	if len(got) != len(wantSorted) {
		t.Fatalf("payload keys = %v, want %v", got, wantSorted)
	}
	for i := range got {
		if got[i] != wantSorted[i] {
			t.Fatalf("payload keys = %v, want %v", got, wantSorted)
		}
	}
}

// TestWSWithoutBus: without an attached bus the endpoint degrades to 503.
func TestWSWithoutBus(t *testing.T) {
	AttachBus(nil)
	srv := testServer(t)
	resp, err := srv.Client().Get(srv.URL + "/api/ws")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}
