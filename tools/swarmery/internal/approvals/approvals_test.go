package approvals

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedSession inserts a project + active session and returns the session id.
func seedSession(t *testing.T, db *sql.DB, uuid string) int64 {
	t.Helper()
	now := time.Now().UTC().Format(tsFormat)
	res, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen) VALUES (?, ?, ?, ?)
		 ON CONFLICT(path) DO NOTHING`,
		"/tmp/proj", "-tmp-proj", "proj", now)
	if err != nil {
		t.Fatal(err)
	}
	var projectID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE path = '/tmp/proj'`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	res, err = db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, source)
		 VALUES (?, ?, '/tmp/proj', 'active', ?, 'jsonl')`,
		projectID, uuid, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func hookInput(t *testing.T, uuid, tool, command string) HookInput {
	t.Helper()
	raw := fmt.Sprintf(
		`{"session_id":%q,"transcript_path":"/x.jsonl","cwd":"/tmp/proj","permission_mode":"default","hook_event_name":"PermissionRequest","tool_name":%q,"tool_input":{"command":%q,"description":"d"},"permission_suggestions":[]}`,
		uuid, tool, command)
	in, err := ParseHookStdin([]byte(raw))
	if err != nil {
		t.Fatalf("parse hook stdin: %v", err)
	}
	return in
}

func sessionStatus(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func requestStatus(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM permission_requests WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCanonicalJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{`{"z":{"y":1,"x":[3,2,1]},"a":"s"}`, `{"a":"s","z":{"x":[3,2,1],"y":1}}`},
		{`{ "a" : 1 }`, `{"a":1}`},
		{`[2,1]`, `[2,1]`}, // arrays keep order
		{``, `null`},
	}
	for _, c := range cases {
		got, err := CanonicalJSON(json.RawMessage(c.in))
		if err != nil {
			t.Fatalf("CanonicalJSON(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("CanonicalJSON(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The frozen rule: whitespace/key order never changes the hash; values do.
	h1, _ := DedupHash("s", "Bash", json.RawMessage(`{"b":1,"a":2}`))
	h2, _ := DedupHash("s", "Bash", json.RawMessage(`{ "a": 2, "b": 1 }`))
	h3, _ := DedupHash("s", "Bash", json.RawMessage(`{"a":2,"b":3}`))
	if h1 != h2 {
		t.Error("identical canonical inputs must hash equal")
	}
	if h1 == h3 {
		t.Error("different inputs must hash differently")
	}
}

// TestDedupFanOut: two concurrent identical requests → ONE row, ONE
// permission_request event; the single decision wakes BOTH waiters (D6/E11).
func TestDedupFanOut(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-dedup")
	svc := New(db, nil, Options{})
	in := hookInput(t, "uuid-dedup", "Bash", "curl -sI https://example.com | head -1")

	type opened struct {
		id int64
		ch chan Decision
	}
	results := make(chan opened, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, ch, _, err := svc.Open(in)
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			results <- opened{id, ch}
		}()
	}
	wg.Wait()
	close(results)

	var ids []int64
	var chans []chan Decision
	for r := range results {
		ids = append(ids, r.id)
		chans = append(chans, r.ch)
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("both callers must share one request id, got %v", ids)
	}

	var rows, events int
	db.QueryRow(`SELECT COUNT(*) FROM permission_requests`).Scan(&rows)
	db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'permission_request'`).Scan(&events)
	if rows != 1 || events != 1 {
		t.Fatalf("rows = %d, permission_request events = %d, want 1/1", rows, events)
	}
	if got := sessionStatus(t, db, sid); got != "waiting_approval" {
		t.Fatalf("session status = %q, want waiting_approval", got)
	}

	if err := svc.Resolve(ids[0], StatusApproved, "dashboard", ""); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for i, ch := range chans {
		select {
		case d := <-ch:
			if d.Status != StatusApproved {
				t.Errorf("waiter %d got %q, want approved", i, d.Status)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d never woke", i)
		}
	}
	if got := sessionStatus(t, db, sid); got == "waiting_approval" {
		t.Error("session stuck in waiting_approval after resolution")
	}
	var resolvedEvents int
	db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'permission_resolved'`).Scan(&resolvedEvents)
	if resolvedEvents != 1 {
		t.Errorf("permission_resolved events = %d, want 1 (one decision, one audit row)", resolvedEvents)
	}
}

// TestRepeatAfterResolveOpensFreshRow: dedup scope is pending-only — a repeat
// of a previously-denied call opens a NEW row.
func TestRepeatAfterResolveOpensFreshRow(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-repeat")
	svc := New(db, nil, Options{})
	in := hookInput(t, "uuid-repeat", "Bash", "rm -rf /tmp/x")

	id1, _, isNew, err := svc.Open(in)
	if err != nil || !isNew {
		t.Fatalf("first Open: id=%d isNew=%v err=%v", id1, isNew, err)
	}
	if err := svc.Resolve(id1, StatusDenied, "dashboard", "nope"); err != nil {
		t.Fatal(err)
	}
	id2, _, isNew, err := svc.Open(in)
	if err != nil || !isNew {
		t.Fatalf("second Open: isNew=%v err=%v", isNew, err)
	}
	if id1 == id2 {
		t.Error("resolved rows must never absorb a new request")
	}
}

func TestResolveErrors(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-err")
	svc := New(db, nil, Options{})

	if err := svc.Resolve(999, StatusApproved, "dashboard", ""); err != ErrNotFound {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
	id, _, _, err := svc.Open(hookInput(t, "uuid-err", "Bash", "ls"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Resolve(id, StatusApproved, "dashboard", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Resolve(id, StatusDenied, "dashboard", ""); err != ErrAlreadyResolved {
		t.Errorf("second resolve: err = %v, want ErrAlreadyResolved", err)
	}
}

// TestExpirySweeper: overdue pending rows expire (fail-open), waiters wake
// with 'expired', the session leaves waiting_approval.
func TestExpirySweeper(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-expire")
	now := time.Now()
	svc := New(db, nil, Options{
		Timeout: 50 * time.Millisecond,
		Now:     func() time.Time { return now },
	})
	id, ch, _, err := svc.Open(hookInput(t, "uuid-expire", "Bash", "ls"))
	if err != nil {
		t.Fatal(err)
	}

	svc.Sweep() // not yet overdue
	if got := requestStatus(t, db, id); got != StatusPending {
		t.Fatalf("premature expiry: status = %q", got)
	}

	now = now.Add(time.Second) // jump past expires_at
	svc.Sweep()
	if got := requestStatus(t, db, id); got != StatusExpired {
		t.Fatalf("status = %q, want expired", got)
	}
	select {
	case d := <-ch:
		if d.Status != StatusExpired {
			t.Errorf("waiter got %q, want expired", d.Status)
		}
	default:
		t.Fatal("waiter not woken by expiry")
	}
	if got := sessionStatus(t, db, sid); got == "waiting_approval" {
		t.Error("session stuck in waiting_approval after expiry")
	}
}

// TestSweeperHealsStuckSession: waiting_approval with no pending rows left
// (e.g. crash between resolve and recompute) self-heals on the next sweep.
func TestSweeperHealsStuckSession(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-stuck")
	if _, err := db.Exec(`UPDATE sessions SET status = 'waiting_approval' WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	svc := New(db, nil, Options{})
	svc.Sweep()
	if got := sessionStatus(t, db, sid); got == "waiting_approval" {
		t.Errorf("session not healed, status = %q", got)
	}
}

// TestDetachLastWaiter: client disconnect with no remaining waiters resolves
// the row as resolved_elsewhere via 'terminal' (E4-interrupt semantics).
func TestDetachLastWaiter(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-detach")
	svc := New(db, nil, Options{})
	in := hookInput(t, "uuid-detach", "Bash", "ls")

	id, ch1, _, err := svc.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	_, ch2, _, err := svc.Open(in) // dedup attach
	if err != nil {
		t.Fatal(err)
	}

	svc.Detach(id, ch1) // one of two → still pending
	if got := requestStatus(t, db, id); got != StatusPending {
		t.Fatalf("after first detach: status = %q, want pending", got)
	}
	svc.Detach(id, ch2) // last waiter gone → resolved_elsewhere
	var status string
	var via *string
	if err := db.QueryRow(
		`SELECT status, resolved_via FROM permission_requests WHERE id = ?`, id).Scan(&status, &via); err != nil {
		t.Fatal(err)
	}
	if status != StatusResolvedElsewhere || via == nil || *via != "terminal" {
		t.Errorf("status/via = %q/%v, want resolved_elsewhere/terminal", status, via)
	}
}

// TestUnknownSessionCreatesHookRow: a hook for a not-yet-ingested transcript
// mints project (by cwd) + session (source='hook').
func TestUnknownSessionCreatesHookRow(t *testing.T) {
	db := testDB(t)
	svc := New(db, nil, Options{})
	if _, _, _, err := svc.Open(hookInput(t, "uuid-fresh", "Edit", "x")); err != nil {
		t.Fatal(err)
	}
	var source, status, cwd string
	if err := db.QueryRow(
		`SELECT source, status, cwd FROM sessions WHERE session_uuid = 'uuid-fresh'`).Scan(&source, &status, &cwd); err != nil {
		t.Fatal(err)
	}
	if source != "hook" || status != "waiting_approval" || cwd != "/tmp/proj" {
		t.Errorf("session = %s/%s/%s, want hook/waiting_approval//tmp/proj", source, status, cwd)
	}
	var path, slug, name string
	if err := db.QueryRow(`SELECT path, slug, name FROM projects LIMIT 1`).Scan(&path, &slug, &name); err != nil {
		t.Fatal(err)
	}
	if path != "/tmp/proj" {
		t.Errorf("project path = %q, want /tmp/proj", path)
	}
	// Attribution must use the SAME derivation as the JSONL ingest — a later
	// tail must find (not duplicate) this project.
	if slug != "-tmp-proj" || name != "proj" {
		t.Errorf("project slug/name = %q/%q, want -tmp-proj/proj", slug, name)
	}
	var startedAt string
	if err := db.QueryRow(
		`SELECT started_at FROM sessions WHERE session_uuid = 'uuid-fresh'`).Scan(&startedAt); err != nil {
		t.Fatal(err)
	}
	if startedAt == "" {
		t.Error("hook stub started_at empty — dashboards would show 'started —'")
	}
}

// TestHookWithoutCwdFallsBackToUnknown: only a genuinely absent cwd may mint
// the '(unknown)' placeholder (healed later by the ingest upsert).
func TestHookWithoutCwdFallsBackToUnknown(t *testing.T) {
	db := testDB(t)
	svc := New(db, nil, Options{})
	raw := `{"session_id":"uuid-nocwd","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`
	in, err := ParseHookStdin([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Open(in); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := db.QueryRow(
		`SELECT p.path FROM projects p JOIN sessions s ON s.project_id = p.id
		 WHERE s.session_uuid = 'uuid-nocwd'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != ingest.UnknownProjectPath {
		t.Errorf("project path = %q, want the '(unknown)' placeholder", path)
	}
}

// TestPendingLimit: >20 pending per session fails fast (429 upstream).
func TestPendingLimit(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-limit")
	svc := New(db, nil, Options{})
	for i := 0; i < maxPendingPerSession; i++ {
		if _, _, _, err := svc.Open(hookInput(t, "uuid-limit", "Bash", fmt.Sprintf("cmd-%d", i))); err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
	}
	_, _, _, err := svc.Open(hookInput(t, "uuid-limit", "Bash", "one-too-many"))
	if err != ErrTooManyPending {
		t.Errorf("err = %v, want ErrTooManyPending", err)
	}
}

// TestBusNotifications: a full open→resolve cycle publishes the frozen WS
// note sequence with request ids attached.
func TestBusNotifications(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-bus")
	bus := ingest.NewBus()
	ch, cancel := bus.Subscribe(32)
	defer cancel()
	svc := New(db, bus, Options{})

	id, _, _, err := svc.Open(hookInput(t, "uuid-bus", "Bash", "ls"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Resolve(id, StatusApproved, "dashboard", ""); err != nil {
		t.Fatal(err)
	}

	var types []string
	var reqIDs []int64
	deadline := time.After(2 * time.Second)
	for len(types) < 6 {
		select {
		case n := <-ch:
			types = append(types, n.Type)
			reqIDs = append(reqIDs, n.RequestID)
		case <-deadline:
			t.Fatalf("bus notes so far: %v", types)
		}
	}
	// Session messages precede their event_appended batch (docs/ws-protocol.md).
	want := []string{
		ingest.NoteSessionUpdated, ingest.NoteEventAppended, ingest.NotePermissionRequested,
		ingest.NoteSessionUpdated, ingest.NoteEventAppended, ingest.NotePermissionResolved,
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("note[%d] = %s, want %s (all: %v)", i, types[i], want[i], types)
		}
	}
	if reqIDs[2] != id || reqIDs[5] != id {
		t.Errorf("permission_* notes must carry the request id %d, got %v", id, reqIDs)
	}
}

// TestParseHookStdinRejectsGarbage covers the 400 path.
func TestParseHookStdinRejectsGarbage(t *testing.T) {
	for _, raw := range []string{``, `not json`, `{}`, `{"session_id":"x"}`} {
		if _, err := ParseHookStdin([]byte(raw)); err == nil {
			t.Errorf("ParseHookStdin(%q) accepted garbage", raw)
		}
	}
}
