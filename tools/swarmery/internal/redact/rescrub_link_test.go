package redact

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// Mod events stored before the link ingest masked them are rescrubbed too.
func TestRescrubCoversLinkEvents(t *testing.T) {
	if _, ok := Targets()["link_events"]; !ok {
		t.Fatalf("Targets() = %v, want link_events covered", Targets())
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "link.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, err := json.Marshal(map[string]any{"tool": "Bash", "note": redacttest.Joined()})
	if err != nil {
		t.Fatal(err)
	}
	clean := `{"tool":"Bash","inputHash":"abc"}`
	for i, p := range []string{string(raw), clean} {
		if _, err := db.Exec(`INSERT INTO link_events (session_id, kind, at, payload) VALUES (1, 'tool.call', ?, ?)`,
			fmt.Sprintf("2026-10-22T00:00:0%d.000Z", i), p); err != nil {
			t.Fatal(err)
		}
	}
	before, err := CountStored(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if before.Columns["link_events.payload"]["github_token"] != 1 {
		t.Errorf("count before = %v, want 1 github_token in link_events.payload", before.Columns["link_events.payload"])
	}
	stats, err := Rescrub(context.Background(), db, fastCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Done["link_events"] || stats.Changed["link_events"] != 1 {
		t.Errorf("link_events: done=%v changed=%d, want done and 1 row rewritten",
			stats.Done["link_events"], stats.Changed["link_events"])
	}
	for _, l := range leaks(t, db) {
		t.Error(l)
	}
	var got string
	if err := db.QueryRow(`SELECT payload FROM link_events ORDER BY id DESC LIMIT 1`).Scan(&got); err != nil || got != clean {
		t.Errorf("clean link event = %q (%v), want its exact bytes", got, err)
	}
	if after, err := CountStored(context.Background(), db); err != nil || after.Total() != 0 {
		t.Errorf("count after = %v (%v), want 0", after.Lines(), err)
	}
}
