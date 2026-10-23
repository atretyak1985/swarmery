package link

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
)

// A mod event's payload is stored with its secret-shaped values masked, the
// same way ingest masks a transcript event; the rest of the payload — and a
// clean payload's exact bytes — are kept.
func TestIngestStoresPayloadMasked(t *testing.T) {
	db := testDB(t)
	github := redacttest.Samples()[1].Value
	pw := "hunter" + "2link"
	payload, err := json.Marshal(map[string]any{
		"tool": "Bash", "inputHash": "abc", "note": "used " + github,
		"env": map[string]any{"pass" + "word": pw},
	})
	if err != nil {
		t.Fatal(err)
	}
	clean := `{"reason":"prompt_input_exit"}`
	res := mustIngest(t, db, nil, []Event{
		{SessionID: "u-redact", Cwd: "/tmp/redact", Kind: KindToolCall, At: "2026-10-13T20:00:01Z", Payload: payload},
		{SessionID: "u-redact", Cwd: "/tmp/redact", Kind: KindSessionEnd, At: "2026-10-13T20:00:02Z", Payload: json.RawMessage(clean)},
	})
	if res.Accepted != 2 {
		t.Fatalf("Ingest = %+v, want 2 accepted", res)
	}
	var stored string
	if err := db.QueryRow(`SELECT payload FROM link_events WHERE kind = 'tool.call'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, github) || strings.Contains(stored, pw) {
		t.Errorf("link_events.payload kept a secret: %s", stored)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stored), &got); err != nil {
		t.Fatalf("stored payload is not JSON: %v (%s)", err, stored)
	}
	if got["tool"] != "Bash" || got["inputHash"] != "abc" || got["note"] != "used "+redact.Mark {
		t.Errorf("stored payload = %v, want the fields kept and the token masked", got)
	}
	if env, _ := got["env"].(map[string]any); env["pass"+"word"] != redact.Mark {
		t.Errorf("env.password = %v, want %q", got["env"], redact.Mark)
	}
	if err := db.QueryRow(`SELECT payload FROM link_events WHERE kind = 'session.end'`).Scan(&stored); err != nil || stored != clean {
		t.Errorf("clean payload = %q (%v), want its exact bytes", stored, err)
	}
}
