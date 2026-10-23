package link

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
)

// Swarm message text is stored masked in link_messages as well as in
// link_events, and a send and its receive carrying the same secret still
// pair. Nothing in the whole store keeps a sample.
func TestIngestSwarmMessagesStoredMasked(t *testing.T) {
	db := testDB(t)
	text := "deploy with " + redacttest.Joined()
	send, _ := json.Marshal(map[string]any{"to": "u-swarm-b", "text": text, "origin": "model", "isDelivered": false,
		"reason": "peer offline, retry with " + redacttest.Samples()[1].Value})
	recv, _ := json.Marshal(map[string]any{"text": text, "originKind": "peer"})
	res := mustIngest(t, db, nil, []Event{
		{SessionID: "u-swarm-b", Cwd: "/tmp/swarm", Kind: KindSessionStart, At: "2026-10-13T20:00:00Z"},
		{SessionID: "u-swarm-a", Cwd: "/tmp/swarm", Kind: KindSessionSend, At: "2026-10-13T20:00:01Z", Payload: send},
		{SessionID: "u-swarm-b", Cwd: "/tmp/swarm", Kind: KindSessionReceive, At: "2026-10-13T20:00:02Z", Payload: recv},
	})
	if res.Accepted != 3 {
		t.Fatalf("Ingest = %+v, want 3 accepted", res)
	}
	leaks, err := redacttest.Leaks(db, redacttest.Samples())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range leaks {
		t.Error(l)
	}
	var body string
	var reason sql.NullString
	if err := db.QueryRow(`SELECT body, reason FROM link_messages WHERE kind = 'send'`).Scan(&body, &reason); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "deploy with ") || !strings.Contains(body, redact.Mark) {
		t.Errorf("send body = %q, want the text kept and its secrets masked", body)
	}
	if !reason.Valid || !strings.Contains(reason.String, redact.Mark) {
		t.Errorf("send reason = %v, want it stored masked", reason)
	}
	var matched sql.NullInt64
	if err := db.QueryRow(`SELECT matched_id FROM link_messages WHERE kind = 'receive'`).Scan(&matched); err != nil {
		t.Fatal(err)
	}
	if !matched.Valid {
		t.Error("the receive did not pair with its send once both bodies were masked")
	}
}

// A drift report's reason is stored masked.
func TestRecordDriftStoresPayloadMasked(t *testing.T) {
	db := testDB(t)
	mustIngest(t, db, nil, []Event{{SessionID: "u-drift", Cwd: "/tmp/drift", Kind: KindSessionStart, At: "2026-10-13T20:00:00Z"}})
	github := redacttest.Samples()[1].Value
	stored, err := RecordDrift(context.Background(), db, "u-drift", time.Date(2026, 10, 13, 20, 0, 1, 0, time.UTC),
		map[string]string{"verdict": "off-phase", "reason": "pushed with " + github})
	if err != nil || !stored {
		t.Fatalf("RecordDrift = %v, %v", stored, err)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM link_events WHERE kind = ?`, KindDrift).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, github) {
		t.Errorf("drift payload kept a token: %s", payload)
	}
	var p map[string]string
	if err := json.Unmarshal([]byte(payload), &p); err != nil || p["verdict"] != "off-phase" || p["reason"] != "pushed with "+redact.Mark {
		t.Errorf("drift payload = %s (%v), want the verdict kept and the token masked", payload, err)
	}
}
