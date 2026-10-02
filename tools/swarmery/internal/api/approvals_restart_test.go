package api

// Daemon restart under a pending approval (docs/hooks-protocol.md,
// amendment 3). A pending row is a DB row PLUS an in-memory long-poll waiter;
// a restart keeps the first and loses the second. These tests seed the row
// straight into the DB — exactly the state a fresh daemon finds at boot — so
// no long poll is opened here (and none needs cancelling, see
// approvals_leak_test.go).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/approvals"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// seedOrphanRequest inserts a project, a session in waiting_approval and ONE
// pending permission request requested at `at`, with no waiter attached to
// any service. Returns the request id and the session id.
func seedOrphanRequest(t *testing.T, db *sql.DB, uuid, tool, requestJSON string, at time.Time) (reqID, sessionID int64) {
	t.Helper()
	const ts = "2006-01-02T15:04:05.000Z"
	requested := at.UTC().Format(ts)
	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen) VALUES ('/work/orphan', 'orphan', 'Orphan', ?)
		 ON CONFLICT(path) DO NOTHING`, requested); err != nil {
		t.Fatal(err)
	}
	var projectID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE path = '/work/orphan'`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, source)
		 VALUES (?, ?, '/work/orphan', 'waiting_approval', ?, 'hook')`, projectID, uuid, requested)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, _ = res.LastInsertId()
	res, err = db.Exec(
		`INSERT INTO permission_requests (session_id, tool_name, request_json, status, requested_at, expires_at)
		 VALUES (?, ?, ?, 'pending', ?, ?)`,
		sessionID, tool, requestJSON, requested, at.Add(approvals.DefaultTimeout).UTC().Format(ts))
	if err != nil {
		t.Fatal(err)
	}
	reqID, _ = res.LastInsertId()
	return reqID, sessionID
}

func requestStatusAPI(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM permission_requests WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestResolveDeadRowGone: POST /api/approvals/{id} on a pending row nobody is
// polling on answers 410 for every action — never a 200 that reaches no one —
// and leaves the row pending (and listed) for the sweeper.
func TestResolveDeadRowGone(t *testing.T) {
	srv, db, _ := approvalsTestServer(t, approvals.Options{})
	past := time.Now().Add(-time.Minute)
	bashID, _ := seedOrphanRequest(t, db, "dead-bash", "Bash",
		`{"session_id":"dead-bash","tool_name":"Bash","tool_input":{"command":"ls"}}`, past)
	askID, _ := seedOrphanRequest(t, db, "dead-ask", "AskUserQuestion",
		`{"session_id":"dead-ask","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Deploy?","options":[{"label":"yes"}],"multiSelect":false}]}}`, past)

	for _, action := range []string{"approve", "deny", "terminal"} {
		resp := resolveVia(t, srv, float64(bashID), action, "because")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusGone {
			t.Errorf("%s on a dead row: status = %d, want 410; body %s", action, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "no live hook waiter") {
			t.Errorf("%s on a dead row: body = %s, want the no-waiter explanation", action, body)
		}
	}

	// answer: the waiter guard fires before any answer validation (a valid
	// answer for a dead AskUserQuestion is still undeliverable).
	resp, err := http.Post(fmt.Sprintf("%s/api/approvals/%d", srv.URL, askID),
		"application/json", strings.NewReader(`{"action":"answer","answers":{"Deploy?":"yes"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("answer on a dead row: status = %d, want 410", resp.StatusCode)
	}

	// Nothing was recorded: both rows still pending and still listed — a 410
	// is a verdict, not a write (the row may belong to another daemon).
	for _, id := range []int64{bashID, askID} {
		if got := requestStatusAPI(t, db, id); got != "pending" {
			t.Errorf("request %d status = %q after refused decisions, want pending", id, got)
		}
	}
	var list []map[string]any
	getJSON(t, srv.URL+"/api/approvals", &list)
	if len(list) != 2 {
		t.Errorf("pending list has %d rows after refused decisions, want 2", len(list))
	}
	var resolved int
	db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'permission_resolved'`).Scan(&resolved)
	if resolved != 0 {
		t.Errorf("%d permission_resolved events written by refused decisions, want 0", resolved)
	}
}

// TestHealStaleDropsOrphanFromPendingList: the boot heal takes a pre-boot
// pending row out of GET /api/approvals, parks it in history as expired via
// 'restart', pushes the permission_resolved frame an open dashboard needs to
// drop the card, and turns a later decision on it into a 409 — never a 200.
func TestHealStaleDropsOrphanFromPendingList(t *testing.T) {
	srv, db, svc, bus := approvalsTestServerWithBus(t, approvals.Options{})
	bootAt := time.Now()
	orphanID, sessionID := seedOrphanRequest(t, db, "orphan-uuid", "Bash",
		`{"session_id":"orphan-uuid","tool_name":"Bash","tool_input":{"command":"git push"}}`,
		bootAt.Add(-time.Minute))

	var before []map[string]any
	getJSON(t, srv.URL+"/api/approvals", &before)
	if len(before) != 1 || before[0]["status"] != "pending" {
		t.Fatalf("pending list before the heal = %v, want the orphan", before)
	}

	// An open dashboard: dial /api/ws and prove the subscription is live
	// (the handler subscribes after the handshake, so the first frame is a
	// poll-published session_updated).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/ws"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	readFrame := newFrameReader(t, ctx, c, func() {
		bus.Publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: sessionID})
	})
	assertEnvelope(t, readFrame(), "session_updated")

	healed, err := svc.HealStale(bootAt)
	if err != nil || healed != 1 {
		t.Fatalf("HealStale = %d, %v; want 1, nil", healed, err)
	}

	// Gone from the pending list, parked in history with the restart stamp.
	var after []map[string]any
	getJSON(t, srv.URL+"/api/approvals", &after)
	if len(after) != 0 {
		t.Errorf("pending list after the heal = %v, want empty", after)
	}
	var history []map[string]any
	getJSON(t, srv.URL+"/api/approvals?status=expired", &history)
	if len(history) != 1 || history[0]["resolvedVia"] != approvals.ViaRestart {
		t.Fatalf("expired list after the heal = %v, want the orphan via restart", history)
	}
	if reason, _ := history[0]["reason"].(string); reason != approvals.RestartReason {
		t.Errorf("reason = %q, want %q", reason, approvals.RestartReason)
	}

	// The WS frame: permission_resolved carrying the resolved DTO (the heal's
	// session_updated is skipped by the reader as the poll type; the
	// event_appended frame precedes it).
	var frame map[string]json.RawMessage
	for {
		frame = readFrame()
		var typ string
		json.Unmarshal(frame["type"], &typ)
		if typ == "permission_resolved" {
			break
		}
		if typ != "event_appended" {
			t.Fatalf("unexpected frame %s before permission_resolved", typ)
		}
	}
	assertPayloadKeys(t, frame, permissionRequestKeys)
	var dto struct {
		ID          int64   `json:"id"`
		Status      string  `json:"status"`
		ResolvedVia *string `json:"resolvedVia"`
	}
	if err := json.Unmarshal(frame["payload"], &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ID != orphanID || dto.Status != "expired" || dto.ResolvedVia == nil || *dto.ResolvedVia != approvals.ViaRestart {
		t.Errorf("permission_resolved payload = %+v, want the orphan expired via restart", dto)
	}

	// Deciding on it now is a 409 (terminal row), not the old silent 200.
	resp := resolveVia(t, srv, float64(orphanID), "approve", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("approve after the heal: status = %d, want 409", resp.StatusCode)
	}
}
