package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// Frozen contract (web/src/api/types.ts): exact JSON key sets of the WSMessage
// payloads. Any drift between the Go DTOs and types.ts fails this test.
var (
	sessionKeys = []string{
		"id", "projectId", "projectSlug", "sessionUuid", "model", "gitBranch",
		"cwd", "status", "startedAt", "endedAt", "title", "source",
		// parity wave: additive per-session aggregates (nullable).
		"tokens", "costUsd",
	}
	eventKeys = []string{
		"id", "turnId", "ts", "type", "toolName", "parentEventId",
		"status", "durationMs", "payload",
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

	// Wait for the subscription to be registered before publishing: the
	// handler subscribes after the upgrade completes, so poll-publish.
	published := false
	readFrame := func() map[string]json.RawMessage {
		t.Helper()
		for {
			if !published {
				bus.Publish(ingest.Notification{Type: ingest.NoteSessionStarted, SessionID: 1})
			}
			readCtx, readCancel := context.WithTimeout(ctx, 300*time.Millisecond)
			typ, data, err := c.Read(readCtx)
			readCancel()
			if err != nil {
				if ctx.Err() != nil {
					t.Fatalf("read: %v", err)
				}
				continue // handler not subscribed yet — republish and retry
			}
			published = true
			if typ != websocket.MessageText {
				t.Fatalf("frame type = %v, want text", typ)
			}
			var frame map[string]json.RawMessage
			if err := json.Unmarshal(data, &frame); err != nil {
				t.Fatalf("frame is not a JSON object: %v\n%s", err, data)
			}
			return frame
		}
	}

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
