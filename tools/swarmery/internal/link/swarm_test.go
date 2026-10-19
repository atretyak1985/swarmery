package link

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

const (
	swA = "5a000000-0000-4000-8000-00000000000a"
	swB = "5a000000-0000-4000-8000-00000000000b"
	swC = "5a000000-0000-4000-8000-00000000000c"
)

// swarmEnv mints sessions A and B in project /work/p1 and C in /work/p2.
func swarmEnv(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	mustIngest(t, db, nil, []Event{
		{SessionID: swA, Cwd: "/work/p1", Kind: KindSessionStart, At: "2026-10-14T09:00:00Z"},
		{SessionID: swB, Cwd: "/work/p1", Kind: KindSessionStart, At: "2026-10-14T09:00:00Z"},
		{SessionID: swC, Cwd: "/work/p2", Kind: KindSessionStart, At: "2026-10-14T09:00:00Z"},
	})
	return db
}

func sendEv(from, to, text, at string, extra string) Event {
	p := fmt.Sprintf(`{"to":%q,"text":%q,"truncated":false,"isDelivered":true,"origin":"model"%s}`, to, text, extra)
	return Event{SessionID: from, Kind: KindSessionSend, At: at, Payload: json.RawMessage(p)}
}

func recvEv(to, text, at string) Event {
	p := fmt.Sprintf(`{"text":%q,"truncated":false,"originKind":"peer","fromPlugin":"swarmery-link"}`, text)
	return Event{SessionID: to, Kind: KindSessionReceive, At: at, Payload: json.RawMessage(p)}
}

type msgRow struct {
	id                              int64
	kind                            string
	from, to, addr, origin, reason  sql.NullString
	body                            string
	truncated                       int
	delivered, commandID, matchedID sql.NullInt64
}

func messages(t *testing.T, db *sql.DB) []msgRow {
	t.Helper()
	rows, err := db.Query(`SELECT id, kind, from_session, to_session, to_address, origin, reason, body, truncated,
	                              delivered, command_id, matched_id FROM link_messages ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []msgRow
	for rows.Next() {
		var m msgRow
		if err := rows.Scan(&m.id, &m.kind, &m.from, &m.to, &m.addr, &m.origin, &m.reason, &m.body, &m.truncated,
			&m.delivered, &m.commandID, &m.matchedID); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func sessionIDOf(t *testing.T, db *sql.DB, uuid string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM sessions WHERE session_uuid = ?`, uuid).Scan(&id); err != nil {
		t.Fatalf("session %s: %v", uuid, err)
	}
	return id
}

// A send then its receive: one row each, paired — the receive learns the
// sender, the send becomes delivered — and one swarm_message note per row,
// published after the session notes.
func TestSwarmSendThenReceiveCorrelates(t *testing.T) {
	db := swarmEnv(t)
	rec := &recorder{}
	res := mustIngest(t, db, rec.publish, []Event{sendEv(swA, swB, "please rebase", "2026-10-14T09:01:00Z", "")})
	if res.Accepted != 1 {
		t.Fatalf("send: %+v", res)
	}
	res = mustIngest(t, db, rec.publish, []Event{recvEv(swB, "please rebase", "2026-10-14T09:01:01Z")})
	if res.Accepted != 1 {
		t.Fatalf("receive: %+v", res)
	}
	ms := messages(t, db)
	if len(ms) != 2 {
		t.Fatalf("rows = %d, want 2", len(ms))
	}
	send, recv := ms[0], ms[1]
	if send.kind != MsgSend || send.from.String != swA || send.to.String != swB || send.addr.String != swB ||
		send.origin.String != "model" || send.delivered.Int64 != 1 || send.matchedID.Int64 != recv.id {
		t.Errorf("send row = %+v", send)
	}
	if recv.kind != MsgReceive || recv.from.String != swA || recv.to.String != swB || recv.addr.Valid ||
		recv.origin.String != "peer" || recv.matchedID.Int64 != send.id {
		t.Errorf("receive row = %+v", recv)
	}
	var swarmNotes []ingest.Notification
	for _, n := range rec.notes {
		if n.Type == ingest.NoteSwarmMessage {
			swarmNotes = append(swarmNotes, n)
		}
	}
	if len(swarmNotes) != 2 || swarmNotes[0].SessionID != sessionIDOf(t, db, swA) || swarmNotes[1].SessionID != sessionIDOf(t, db, swB) ||
		swarmNotes[0].ProjectID == 0 {
		t.Errorf("swarm notes = %+v", swarmNotes)
	}
	if last := rec.notes[len(rec.notes)-1]; last.Type != ingest.NoteSwarmMessage {
		t.Errorf("last note = %+v, want swarm_message after the session note", last)
	}
	// Both kinds also land in link_events, the dedup ledger: a resend is a duplicate.
	if res := mustIngest(t, db, rec.publish, []Event{recvEv(swB, "please rebase", "2026-10-14T09:01:01Z")}); res.Duplicates != 1 {
		t.Errorf("resent receive: %+v, want a duplicate", res)
	}
	if got := len(messages(t, db)); got != 2 {
		t.Errorf("rows after resend = %d, want 2", got)
	}
}

// The receiver's mod may flush first: a send arriving after its receive pairs
// the same way.
func TestSwarmReceiveThenSendCorrelates(t *testing.T) {
	db := swarmEnv(t)
	mustIngest(t, db, nil, []Event{recvEv(swB, "hello", "2026-10-14T09:02:00Z")})
	ms := messages(t, db)
	if ms[0].from.Valid {
		t.Fatalf("lone receive has from_session %v, want NULL", ms[0].from)
	}
	mustIngest(t, db, nil, []Event{sendEv(swA, swB, "hello", "2026-10-14T09:02:00.500Z", "")})
	ms = messages(t, db)
	if ms[0].from.String != swA || ms[0].matchedID.Int64 != ms[1].id || ms[1].matchedID.Int64 != ms[0].id || ms[1].delivered.Int64 != 1 {
		t.Errorf("rows = %+v", ms)
	}
}

// No pairing for a different text, a send outside the 120 s window, a send
// addressed elsewhere, or a send already paired.
func TestSwarmCorrelationMisses(t *testing.T) {
	db := swarmEnv(t)
	mustIngest(t, db, nil, []Event{
		sendEv(swA, swB, "text one", "2026-10-14T09:00:00Z", ""),
		sendEv(swA, swC, "same text", "2026-10-14T09:10:00Z", ""),
		sendEv(swA, swB, "late", "2026-10-14T09:20:00Z", ""),
	})
	mustIngest(t, db, nil, []Event{
		recvEv(swB, "text two", "2026-10-14T09:00:01Z"),  // different text
		recvEv(swB, "same text", "2026-10-14T09:10:01Z"), // addressed to C, not B
		recvEv(swB, "late", "2026-10-14T09:22:00.001Z"),  // 120.001 s after the send
	})
	for _, m := range messages(t, db) {
		if m.matchedID.Valid || (m.kind == MsgReceive && m.from.Valid) {
			t.Errorf("unexpected pairing: %+v", m)
		}
	}

	// Exactly 120 s is inside the window; a second identical receive finds the
	// send already paired.
	mustIngest(t, db, nil, []Event{sendEv(swA, swB, "edge", "2026-10-14T09:30:00Z", "")})
	mustIngest(t, db, nil, []Event{recvEv(swB, "edge", "2026-10-14T09:32:00Z"), recvEv(swB, "edge", "2026-10-14T09:32:01Z")})
	ms := messages(t, db)
	edge1, edge2 := ms[len(ms)-2], ms[len(ms)-1]
	if edge1.from.String != swA || edge2.from.Valid {
		t.Errorf("edge receives = %+v / %+v, want the first paired and the second not", edge1, edge2)
	}
}

// swD is a fourth session, in A and B's project /work/p1.
const swD = "5a000000-0000-4000-8000-00000000000d"

func addSessionD(t *testing.T, db *sql.DB) {
	t.Helper()
	mustIngest(t, db, nil, []Event{{SessionID: swD, Cwd: "/work/p1", Kind: KindSessionStart, At: "2026-10-14T09:00:00Z"}})
}

// The most recent unpaired send wins; a send addressed by name (no session
// with that uuid) pairs only when no uuid-addressed one matches.
func TestSwarmCorrelationPrefersRecentAndUUID(t *testing.T) {
	db := swarmEnv(t)
	addSessionD(t, db)
	mustIngest(t, db, nil, []Event{
		sendEv(swA, swB, "ping", "2026-10-14T09:00:00Z", ""),
		sendEv(swD, "reviewer", "ping", "2026-10-14T09:00:30Z", ""),
		sendEv(swA, swB, "ping", "2026-10-14T09:00:20Z", ""),
	})
	mustIngest(t, db, nil, []Event{recvEv(swB, "ping", "2026-10-14T09:00:40Z")})
	ms := messages(t, db)
	if !ms[2].matchedID.Valid || ms[0].matchedID.Valid || ms[1].matchedID.Valid {
		t.Fatalf("want the 09:00:20 uuid send paired: %+v", ms)
	}
	if ms[1].to.Valid {
		t.Errorf("name-addressed send resolved to_session %v", ms[1].to)
	}
	mustIngest(t, db, nil, []Event{recvEv(swB, "ping", "2026-10-14T09:00:41Z")})
	ms = messages(t, db)
	last := ms[len(ms)-1]
	if last.from.String != swA || ms[0].matchedID.Int64 != last.id {
		t.Errorf("second receive should take the older uuid send: %+v", ms)
	}
	mustIngest(t, db, nil, []Event{recvEv(swB, "ping", "2026-10-14T09:00:42Z")})
	ms = messages(t, db)
	last = ms[len(ms)-1]
	if last.from.String != swD || ms[1].to.String != swB {
		t.Errorf("third receive should fall back to the name-addressed send: %+v", ms)
	}
}

// The name-addressed fallback never crosses projects, in either ingest order:
// C (project p2) sending "reviewer" a text B (p1) receives is not a pair; D
// (p1) sending the same is.
func TestSwarmCorrelationNameFallbackStaysInProject(t *testing.T) {
	t.Run("send first", func(t *testing.T) {
		db := swarmEnv(t)
		addSessionD(t, db)
		mustIngest(t, db, nil, []Event{sendEv(swC, "reviewer", "lint it", "2026-10-14T09:00:00Z", "")})
		mustIngest(t, db, nil, []Event{recvEv(swB, "lint it", "2026-10-14T09:00:01Z")})
		for _, m := range messages(t, db) {
			if m.matchedID.Valid || (m.kind == MsgReceive && m.from.Valid) {
				t.Fatalf("cross-project pairing: %+v", m)
			}
		}
		// a same-project send then pairs with the receive C's could not take
		mustIngest(t, db, nil, []Event{sendEv(swD, "reviewer", "lint it", "2026-10-14T09:00:02Z", "")})
		ms := messages(t, db)
		if ms[1].from.String != swD || ms[2].matchedID.Int64 != ms[1].id || ms[0].matchedID.Valid {
			t.Errorf("same-project send should pair with the receive: %+v", ms)
		}
	})
	t.Run("receive first", func(t *testing.T) {
		db := swarmEnv(t)
		addSessionD(t, db)
		mustIngest(t, db, nil, []Event{recvEv(swB, "lint it", "2026-10-14T09:00:00Z")})
		mustIngest(t, db, nil, []Event{sendEv(swC, "reviewer", "lint it", "2026-10-14T09:00:01Z", "")})
		for _, m := range messages(t, db) {
			if m.matchedID.Valid {
				t.Fatalf("cross-project pairing: %+v", m)
			}
		}
		mustIngest(t, db, nil, []Event{sendEv(swD, "reviewer", "lint it", "2026-10-14T09:00:02Z", "")})
		ms := messages(t, db)
		if ms[0].from.String != swD || ms[2].matchedID.Int64 != ms[0].id || ms[1].matchedID.Valid {
			t.Errorf("same-project send should pair with the receive: %+v", ms)
		}
	})
}

// A body over 4096 characters is cut and flagged; isDelivered false keeps
// the engine's reason.
func TestSwarmBodyTruncatedAndUndelivered(t *testing.T) {
	db := swarmEnv(t)
	long := strings.Repeat("é", MaxMessageBody+10)
	p := fmt.Sprintf(`{"to":"nobody","text":%q,"isDelivered":false,"reason":"no such peer","origin":"plugin"}`, long)
	mustIngest(t, db, nil, []Event{{SessionID: swA, Kind: KindSessionSend, At: "2026-10-14T09:00:00Z", Payload: json.RawMessage(p)}})
	m := messages(t, db)[0]
	if len([]rune(m.body)) != MaxMessageBody || m.truncated != 1 || m.delivered.Int64 != 0 || !m.delivered.Valid ||
		m.reason.String != "no such peer" || m.to.Valid {
		t.Errorf("row = runes %d %+v", len([]rune(m.body)), m)
	}
	// isDelivered true never stores a reason; absent isDelivered is "unknown".
	mustIngest(t, db, nil, []Event{
		{SessionID: swA, Kind: KindSessionSend, At: "2026-10-14T09:00:01Z", Payload: json.RawMessage(`{"to":"x","text":"a","isDelivered":true,"reason":"ignored"}`)},
		{SessionID: swA, Kind: KindSessionSend, At: "2026-10-14T09:00:02Z", Payload: json.RawMessage(`{"to":"x","text":"b"}`)},
	})
	ms := messages(t, db)
	if ms[1].reason.Valid || ms[1].delivered.Int64 != 1 || ms[2].delivered.Valid {
		t.Errorf("rows = %+v", ms[1:])
	}
}

func TestSwarmValidation(t *testing.T) {
	db := swarmEnv(t)
	at := "2026-10-14T09:00:00Z"
	bad := []Event{
		{SessionID: swA, Kind: KindSessionSend, At: at},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`[]`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"text":"x"}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b"}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","origin":"user"}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","commandId":0}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","commandId":-3}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","commandId":1.5}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","commandId":"0"}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","commandId":"+7"}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","commandId":"7a"}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","commandId":""}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","commandId":true}`)},
		{SessionID: swA, Kind: KindSessionSend, At: at, Payload: json.RawMessage(`{"to":"b","text":"x","isDelivered":"yes"}`)},
		{SessionID: swA, Kind: KindSessionReceive, At: at, Payload: json.RawMessage(`{"originKind":"peer"}`)},
		{SessionID: swA, Kind: KindSessionReceive, At: at, Payload: json.RawMessage(`{"text":"x","fromPid":0}`)},
		{SessionID: swA, Kind: KindSessionReceive, At: at, Payload: json.RawMessage(`{"text":"x","fromPid":-4}`)},
		{SessionID: swA, Kind: KindSessionReceive, At: at, Payload: json.RawMessage(`{"text":"x","fromPid":"41881"}`)},
		{SessionID: swA, Kind: KindSessionReceive, At: at, Payload: json.RawMessage(`{"text":"x","fromPid":1.5}`)},
		{SessionID: swA, Kind: KindClaim, At: at, Payload: json.RawMessage(`{"path":"rel/file.go"}`)},
		{SessionID: swA, Kind: KindClaim, At: at, Payload: json.RawMessage(`{}`)},
		{SessionID: swA, Kind: KindRelease, At: at, Payload: json.RawMessage(`{}`)},
		{SessionID: swA, Kind: KindRelease, At: at, Payload: json.RawMessage(`{"paths":["/ok","rel"]}`)},
		{SessionID: swA, Kind: KindConflict, At: at, Payload: json.RawMessage(`{"path":"/a"}`)},
		{SessionID: swA, Kind: KindConflict, At: at, Payload: json.RawMessage(`{"otherSession":"x"}`)},
	}
	res := mustIngest(t, db, nil, bad)
	if res.Rejected != len(bad) || res.Accepted != 0 {
		t.Fatalf("result = %+v, want all %d rejected", res, len(bad))
	}
	for _, rj := range res.Rejects {
		if !strings.Contains(rj.Reason, " payload: ") {
			t.Errorf("reject %d reason %q", rj.Index, rj.Reason)
		}
	}
	// Unknown fields are ignored; a conflict is kept in link_events only.
	ok := mustIngest(t, db, nil, []Event{
		{SessionID: swA, Kind: KindConflict, At: at, Payload: json.RawMessage(`{"path":"/a","otherSession":"x","extra":1}`)},
	})
	if ok.Accepted != 1 {
		t.Fatalf("conflict: %+v", ok)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM link_events WHERE kind = 'conflict'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("conflict link_events = %d (%v), want 1", n, err)
	}
	if len(messages(t, db)) != 0 {
		t.Error("a rejected or conflict event stored a message")
	}
}

type claimRow struct {
	claimedAt  string
	releasedAt sql.NullString
}

func claimOf(t *testing.T, db *sql.DB, uuid, path string) (claimRow, bool) {
	t.Helper()
	var r claimRow
	err := db.QueryRow(`SELECT claimed_at, released_at FROM link_claims WHERE session_id = ? AND path = ?`, uuid, path).
		Scan(&r.claimedAt, &r.releasedAt)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

func claimEv(uuid, path, at string) Event {
	return Event{SessionID: uuid, Kind: KindClaim, At: at, Payload: json.RawMessage(fmt.Sprintf(`{"path":%q}`, path))}
}

func releaseEv(uuid, payload, at string) Event {
	return Event{SessionID: uuid, Kind: KindRelease, At: at, Payload: json.RawMessage(payload)}
}

func countType(notes []ingest.Notification, typ string) int {
	n := 0
	for _, x := range notes {
		if x.Type == typ {
			n++
		}
	}
	return n
}

// Claim, release, re-claim: the row is reused, a re-claim clears released_at
// and refreshes claimed_at; an older claim never re-opens a newer release.
func TestSwarmClaimLifecycle(t *testing.T) {
	db := swarmEnv(t)
	rec := &recorder{}
	mustIngest(t, db, rec.publish, []Event{
		claimEv(swA, "/repo/a.go", "2026-10-14T09:00:00Z"),
		claimEv(swA, "/repo/./b.go", "2026-10-14T09:00:01Z"),
	})
	if got := countType(rec.notes, ingest.NoteSwarmClaim); got != 1 {
		t.Errorf("swarm_claim notes for one project = %d, want 1", got)
	}
	if _, ok := claimOf(t, db, swA, "/repo/b.go"); !ok {
		t.Fatal("claimed path not cleaned")
	}
	mustIngest(t, db, nil, []Event{releaseEv(swA, `{"paths":["/repo/a.go"]}`, "2026-10-14T09:01:00Z")})
	if r, _ := claimOf(t, db, swA, "/repo/a.go"); r.releasedAt.String != "2026-10-14T09:01:00.000Z" {
		t.Errorf("a.go after release = %+v", r)
	}
	if r, _ := claimOf(t, db, swA, "/repo/b.go"); r.releasedAt.Valid {
		t.Errorf("b.go released by a paths release that did not name it: %+v", r)
	}
	// A late claim stamped before the release does not re-open it.
	rec.notes = nil
	mustIngest(t, db, rec.publish, []Event{claimEv(swA, "/repo/a.go", "2026-10-14T09:00:30Z")})
	if r, _ := claimOf(t, db, swA, "/repo/a.go"); !r.releasedAt.Valid {
		t.Errorf("an older claim re-opened a newer release: %+v", r)
	}
	if countType(rec.notes, ingest.NoteSwarmClaim) != 0 {
		t.Error("a no-op claim published swarm_claim")
	}
	mustIngest(t, db, nil, []Event{claimEv(swA, "/repo/a.go", "2026-10-14T09:02:00Z")})
	if r, _ := claimOf(t, db, swA, "/repo/a.go"); r.releasedAt.Valid || r.claimedAt != "2026-10-14T09:02:00.000Z" {
		t.Errorf("re-claim = %+v, want live and refreshed", r)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM link_claims`).Scan(&n); err != nil || n != 2 {
		t.Errorf("claim rows = %d, want 2", n)
	}
	// release all ends every live claim claimed at or before it.
	mustIngest(t, db, nil, []Event{releaseEv(swA, `{"all":true}`, "2026-10-14T09:03:00Z")})
	if err := db.QueryRow(`SELECT COUNT(*) FROM link_claims WHERE released_at IS NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("live after release all = %d, want 0", n)
	}
}

// Two sessions on one live path conflict; a stale claim (> 30 min) or a
// released one does not; the project filter keeps a conflict any of whose
// holders is in the project.
func TestSwarmConflicts(t *testing.T) {
	db := swarmEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	mustIngest(t, db, nil, []Event{
		claimEv(swA, "/repo/shared.go", "2026-10-14T09:50:00Z"),
		claimEv(swB, "/repo/shared.go", "2026-10-14T09:55:00Z"),
		claimEv(swA, "/repo/stale.go", "2026-10-14T09:59:00Z"),
		claimEv(swB, "/repo/stale.go", "2026-10-14T09:29:59Z"), // > 30 min old
		claimEv(swA, "/repo/released.go", "2026-10-14T09:58:00Z"),
		claimEv(swB, "/repo/released.go", "2026-10-14T09:58:00Z"),
		claimEv(swC, "/repo/cross.go", "2026-10-14T09:58:00Z"),
		claimEv(swA, "/repo/cross.go", "2026-10-14T09:58:30Z"),
	})
	mustIngest(t, db, nil, []Event{releaseEv(swB, `{"paths":["/repo/released.go"]}`, "2026-10-14T09:59:00Z")})

	all, err := Conflicts(ctx, db, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Path != "/repo/cross.go" || all[1].Path != "/repo/shared.go" {
		t.Fatalf("conflicts = %+v", all)
	}
	if s := all[1].Sessions; len(s) != 2 || s[0].SessionUUID != swA || s[1].SessionUUID != swB || s[0].SessionID == 0 {
		t.Errorf("shared.go holders = %+v", s)
	}

	var p2 int64
	if err := db.QueryRow(`SELECT project_id FROM sessions WHERE session_uuid = ?`, swC).Scan(&p2); err != nil {
		t.Fatal(err)
	}
	only, err := Conflicts(ctx, db, p2, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Path != "/repo/cross.go" || len(only[0].Sessions) != 2 {
		t.Errorf("project %d conflicts = %+v", p2, only)
	}

	live, err := LiveClaims(ctx, db, p2, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].SessionUUID != swC || live[0].Path != "/repo/cross.go" || live[0].ClaimedAt != "2026-10-14T09:58:00.000Z" {
		t.Errorf("project %d live claims = %+v", p2, live)
	}
	everyone, err := LiveClaims(ctx, db, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(everyone) != 6 { // shared ×2, stale (A only), released (A only), cross ×2
		t.Errorf("all live claims = %d, want 6: %+v", len(everyone), everyone)
	}
	none, err := Conflicts(ctx, db, 0, now.Add(time.Hour))
	if err != nil || len(none) != 0 {
		t.Errorf("an hour later: %+v %v, want no conflicts (all stale)", none, err)
	}
}

// ListMessages: newest first, project filter on either side, since bound,
// limit, session refs with the list title.
func TestSwarmListMessages(t *testing.T) {
	db := swarmEnv(t)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE sessions SET custom_title = 'Builder' WHERE session_uuid = ?`, swA); err != nil {
		t.Fatal(err)
	}
	mustIngest(t, db, nil, []Event{
		sendEv(swA, swB, "one", "2026-10-14T09:00:00Z", ""),
		sendEv(swA, swC, "two", "2026-10-14T09:01:00Z", ""),
		sendEv(swA, "someone", "three", "2026-10-14T09:02:00Z", ""),
	})
	mustIngest(t, db, nil, []Event{recvEv(swC, "orphan", "2026-10-14T09:03:00Z")})

	all, err := ListMessages(ctx, db, 0, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].Body != "orphan" || all[3].Body != "one" {
		t.Fatalf("all = %+v", all)
	}
	if f := all[3].FromSession; f == nil || f.UUID != swA || f.Title == nil || *f.Title != "Builder" || f.ID != sessionIDOf(t, db, swA) {
		t.Errorf("fromSession = %+v", f)
	}
	if all[0].FromSession != nil || all[0].ToSession == nil || all[0].ToSession.UUID != swC || all[0].ToAddress != nil {
		t.Errorf("orphan receive = %+v", all[0])
	}
	if all[1].ToSession != nil || all[1].ToAddress == nil || *all[1].ToAddress != "someone" {
		t.Errorf("name-addressed = %+v", all[1])
	}
	if all[3].Delivered == nil || !*all[3].Delivered || all[3].Origin == nil || *all[3].Origin != "model" {
		t.Errorf("delivered/origin = %+v", all[3])
	}

	var p2 int64
	if err := db.QueryRow(`SELECT project_id FROM sessions WHERE session_uuid = ?`, swC).Scan(&p2); err != nil {
		t.Fatal(err)
	}
	inP2, err := ListMessages(ctx, db, p2, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(inP2) != 2 || inP2[0].Body != "orphan" || inP2[1].Body != "two" {
		t.Errorf("project %d = %+v", p2, inP2)
	}
	since, err := ListMessages(ctx, db, 0, "2026-10-14T09:01:00.000Z", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 2 || since[0].Body != "orphan" || since[1].Body != "three" {
		t.Errorf("since+limit = %+v", since)
	}
	empty, err := ListMessages(ctx, db, 999, "", 10)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("unknown project = %#v %v, want an empty non-nil slice", empty, err)
	}
}

func TestClipBody(t *testing.T) {
	s, cut := clipBody(strings.Repeat("a", MaxMessageBody))
	if cut || len(s) != MaxMessageBody {
		t.Errorf("at the limit: cut=%t len=%d", cut, len(s))
	}
	s, cut = clipBody(strings.Repeat("ж", MaxMessageBody+1))
	if !cut || len([]rune(s)) != MaxMessageBody {
		t.Errorf("over the limit: cut=%t runes=%d", cut, len([]rune(s)))
	}
}

// envelope wraps body the way the engine (Claude Code 2.1.303) hands a peer's
// message to the receiver, byte for byte as the phase-6 live check stored it.
func envelope(pid int, name, mode, body string) string {
	return fmt.Sprintf("<cross-session-message from=\"uds:/tmp/cc-socks/%d.sock\" from-name=%q from-mode=%q>\n%s\n</cross-session-message>",
		pid, name, mode, body)
}

func TestUnwrapEnvelope(t *testing.T) {
	for name, tc := range map[string]struct {
		text      string
		truncated bool
		body      string
		from      string
		ok        bool
	}{
		"live shape": {text: envelope(41881, "proj-10", "bypass", "ping-from-A"), body: "ping-from-A",
			from: "uds:/tmp/cc-socks/41881.sock", ok: true},
		"attributes reordered, extra ones, single quotes": {
			text: "<cross-session-message from-mode='prompting' x-new=\"1\" from-name=\"proj-a3\"  from='uds:/tmp/cc-socks/98092.sock'>hi</cross-session-message>",
			body: "hi", from: "uds:/tmp/cc-socks/98092.sock", ok: true},
		"whitespace around the body and the tags": {
			text: "  \r\n<cross-session-message from=\"uds:/s/7.sock\" >\r\n\t multi\nline \r\n</cross-session-message >\n",
			body: "multi\nline", from: "uds:/s/7.sock", ok: true},
		"no attributes": {text: "<cross-session-message>x</cross-session-message>", body: "x", ok: true},
		"plain text":    {text: "  please rebase\n", body: "  please rebase\n"},
		"text before the envelope": {text: "fwd: " + envelope(1, "n", "m", "x"),
			body: "fwd: " + envelope(1, "n", "m", "x")},
		"another tag": {text: "<cross-session-messages from=\"a\">x</cross-session-messages>",
			body: "<cross-session-messages from=\"a\">x</cross-session-messages>"},
		"no closing tag": {text: "<cross-session-message from=\"a\">x", body: "<cross-session-message from=\"a\">x"},
		"no closing tag, truncated": {text: "<cross-session-message from=\"uds:/s/9.sock\">\nlong bo",
			truncated: true, body: "long bo", from: "uds:/s/9.sock", ok: true},
	} {
		body, from, ok := unwrapEnvelope(tc.text, tc.truncated)
		if body != tc.body || from != tc.from || ok != tc.ok {
			t.Errorf("%s: unwrapEnvelope = (%q, %q, %t), want (%q, %q, %t)", name, body, from, ok, tc.body, tc.from, tc.ok)
		}
	}
	for from, want := range map[string]int64{
		"uds:/tmp/cc-socks/41881.sock": 41881,
		"uds:/tmp/cc-socks/abc.sock":   0,
		"tcp://127.0.0.1:41881":        0,
		"uds:/tmp/cc-socks/0.sock":     0,
		"":                             0,
	} {
		if got := socketPID(from); got != want {
			t.Errorf("socketPID(%q) = %d, want %d", from, got, want)
		}
	}
}

// The phase-6 live check: the receive arrives wrapped in the engine's
// envelope, the send carries the bare text. They pair in either ingest order,
// by uuid and by peer name, and the receive is stored without the envelope.
func TestSwarmEnvelopedReceiveCorrelates(t *testing.T) {
	for _, tc := range []struct {
		name, to     string
		receiveFirst bool
	}{
		{"by uuid, send first", swB, false},
		{"by uuid, receive first", swB, true},
		{"by name, send first", "proj-68", false},
		{"by name, receive first", "proj-68", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := swarmEnv(t)
			send := sendEv(swA, tc.to, "ping-from-A", "2026-10-19T06:55:19.639Z", "")
			recv := recvEv(swB, envelope(41881, "proj-10", "bypass", "ping-from-A"), "2026-10-19T06:55:19.488Z")
			first, second := send, recv
			if tc.receiveFirst {
				first, second = recv, send
			}
			mustIngest(t, db, nil, []Event{first})
			mustIngest(t, db, nil, []Event{second})
			var s, r msgRow
			for _, m := range messages(t, db) {
				if m.kind == MsgSend {
					s = m
				} else {
					r = m
				}
			}
			if r.body != "ping-from-A" {
				t.Errorf("receive body = %q, want the envelope stripped", r.body)
			}
			if s.from.String != swA || s.to.String != swB || s.addr.String != tc.to || s.delivered.Int64 != 1 || s.matchedID.Int64 != r.id {
				t.Errorf("send row = %+v", s)
			}
			if r.from.String != swA || r.to.String != swB || r.matchedID.Int64 != s.id {
				t.Errorf("receive row = %+v", r)
			}
		})
	}
}

// A receive stored with its envelope (before receives were unwrapped) still
// pairs with a send arriving later.
func TestSwarmLegacyEnvelopedRowPairs(t *testing.T) {
	db := swarmEnv(t)
	at := "2026-10-19T06:55:19.488Z"
	if _, err := db.Exec(`INSERT INTO link_messages (kind, to_session, body, truncated, origin, at) VALUES ('receive', ?, ?, 0, 'peer', ?)`,
		swB, envelope(41881, "proj-10", "bypass", "ping-from-A"), at); err != nil {
		t.Fatal(err)
	}
	mustIngest(t, db, nil, []Event{sendEv(swA, "proj-68", "ping-from-A", "2026-10-19T06:55:19.639Z", "")})
	ms := messages(t, db)
	if ms[0].from.String != swA || ms[0].matchedID.Int64 != ms[1].id || ms[1].to.String != swB {
		t.Errorf("rows = %+v", ms)
	}
}

func setPID(t *testing.T, db *sql.DB, uuid string, pid int64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE sessions SET pid = ?, pid_source = 'hook' WHERE session_uuid = ?`, pid, uuid); err != nil {
		t.Fatal(err)
	}
}

// The envelope names the sending process: a receive no send matches still
// learns its sender from a session running as that pid, and a send from that
// session arriving later pairs with it — one from another session does not.
func TestSwarmReceiveSenderByPID(t *testing.T) {
	db := swarmEnv(t)
	addSessionD(t, db)
	setPID(t, db, swA, 41881)
	setPID(t, db, swD, 50000)
	mustIngest(t, db, nil, []Event{recvEv(swB, envelope(41881, "proj-10", "bypass", "hi"), "2026-10-19T07:00:00Z")})
	r := messages(t, db)[0]
	if r.from.String != swA || r.matchedID.Valid {
		t.Fatalf("receive = %+v, want from_session A unpaired", r)
	}
	mustIngest(t, db, nil, []Event{sendEv(swD, "proj-68", "hi", "2026-10-19T07:00:01Z", "")})
	if ms := messages(t, db); ms[0].matchedID.Valid || ms[0].from.String != swA || ms[1].matchedID.Valid {
		t.Fatalf("D's send paired with A's receive: %+v", ms)
	}
	mustIngest(t, db, nil, []Event{sendEv(swA, "proj-68", "hi", "2026-10-19T07:00:02Z", "")})
	ms := messages(t, db)
	if ms[0].from.String != swA || ms[0].matchedID.Int64 != ms[2].id || ms[2].to.String != swB || ms[2].delivered.Int64 != 1 {
		t.Errorf("A's send should pair: %+v", ms)
	}

	// A dead process's session, or an unknown pid, names no sender.
	if _, err := db.Exec(`UPDATE sessions SET proc_state = 'dead' WHERE session_uuid = ?`, swD); err != nil {
		t.Fatal(err)
	}
	mustIngest(t, db, nil, []Event{
		recvEv(swB, envelope(50000, "proj-d", "bypass", "x"), "2026-10-19T07:10:00Z"),
		recvEv(swB, envelope(77777, "proj-z", "bypass", "y"), "2026-10-19T07:10:01Z"),
	})
	ms = messages(t, db)
	for _, m := range ms[len(ms)-2:] {
		if m.from.Valid {
			t.Errorf("receive %q from_session = %v, want NULL", m.body, m.from)
		}
	}
}

// Two sends of the same text could be the receive's: the one from the
// process the envelope names wins over the more recent one, and a send from a
// session known to run as another process is never taken. The mod's fromPid
// (the engine's verifiedPeerPid) wins over the envelope's socket.
func TestSwarmReceivePrefersSenderPID(t *testing.T) {
	db := swarmEnv(t)
	addSessionD(t, db)
	setPID(t, db, swA, 41881)
	setPID(t, db, swD, 50000)
	mustIngest(t, db, nil, []Event{
		sendEv(swA, "proj-68", "same", "2026-10-19T07:00:00Z", ""),
		sendEv(swD, "proj-68", "same", "2026-10-19T07:00:01Z", ""),
	})
	mustIngest(t, db, nil, []Event{recvEv(swB, envelope(41881, "proj-10", "bypass", "same"), "2026-10-19T07:00:02Z")})
	ms := messages(t, db)
	if ms[2].from.String != swA || ms[0].matchedID.Int64 != ms[2].id || ms[1].matchedID.Valid {
		t.Errorf("want A's older send paired: %+v", ms)
	}
	// Only D's send is left: a receive from pid 41881 does not take it.
	mustIngest(t, db, nil, []Event{recvEv(swB, envelope(41881, "proj-10", "bypass", "same"), "2026-10-19T07:00:03Z")})
	ms = messages(t, db)
	if ms[1].matchedID.Valid || ms[3].from.String != swA || ms[3].matchedID.Valid {
		t.Errorf("D's send taken by a receive from A's process: %+v", ms)
	}
	// fromPid 50000 overrides the envelope's 41881: D's send pairs.
	p := fmt.Sprintf(`{"text":%q,"originKind":"peer","fromPid":50000}`, envelope(41881, "proj-10", "bypass", "same"))
	mustIngest(t, db, nil, []Event{{SessionID: swB, Kind: KindSessionReceive, At: "2026-10-19T07:00:04Z", Payload: json.RawMessage(p)}})
	ms = messages(t, db)
	if ms[4].from.String != swD || ms[1].matchedID.Int64 != ms[4].id || ms[4].body != "same" {
		t.Errorf("fromPid should name D: %+v", ms)
	}
}
