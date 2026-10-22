package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/cachecold"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
)

const coldSessionUUID = "55555555-0000-4000-8000-000000000005"

func coldAsst(uuid, msgID, ts, usage string) string {
	return `{"type":"assistant","uuid":"` + uuid +
		`","message":{"model":"claude-opus-5-5","id":"` + msgID +
		`","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":` + usage + `}` +
		`,"timestamp":"` + ts + `","sessionId":"` + coldSessionUUID + `"` +
		`,"cwd":"/tmp/coldproj","version":"2.1.170","gitBranch":"main"}` + "\n"
}

func coldUser(uuid, ts string) string {
	return `{"type":"user","uuid":"` + uuid + `","message":{"role":"user","content":"carry on"}` +
		`,"timestamp":"` + ts + `","sessionId":"` + coldSessionUUID + `","cwd":"/tmp/coldproj"}` + "\n"
}

func coldMark(t *testing.T, db *sql.DB, msgID string) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT cold_restart FROM turns WHERE message_id = ?`, msgID).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", msgID, err)
	}
	return v
}

// TestTailMarksColdRestart: the tail transaction marks the turn that paid for a
// cold cache after a 2 h pause, leaves the warm continuation alone, and closes
// the session's open «cache cold» prediction when the new prompt lands.
func TestTailMarksColdRestart(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(t.TempDir(), coldSessionUUID+".jsonl")

	// Batch 1: a fat first turn and a warm continuation a minute later.
	batch1 := coldAsst("c-a1", "msg_C01", "2026-10-22T10:00:00.000Z",
		`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":200000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200000}}`) +
		coldUser("c-u1", "2026-10-22T10:00:30.000Z") +
		coldAsst("c-a2", "msg_C02", "2026-10-22T10:01:00.000Z",
			`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":200000,"cache_creation_input_tokens":1500,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":1500}}`)
	if err := os.WriteFile(path, []byte(batch1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TailFile(db, path, "", Thresholds{}); err != nil {
		t.Fatalf("tail 1: %v", err)
	}
	if coldMark(t, db, "msg_C01") != 0 || coldMark(t, db, "msg_C02") != 0 {
		t.Fatal("warm turns marked")
	}

	// The daemon tick flagged the session while it sat idle.
	if _, err := db.Exec(`INSERT INTO session_cache_cold (session_uuid, since, kind, footprint)
		VALUES (?, '2026-10-22T11:01:00Z', 'subscription', 201510)`, coldSessionUUID); err != nil {
		t.Fatal(err)
	}

	// Batch 2, two hours later: the prompt, then a turn that read only the
	// shared prefix and re-wrote the rest.
	batch2 := coldUser("c-u2", "2026-10-22T12:01:00.000Z") +
		coldAsst("c-a3", "msg_C03", "2026-10-22T12:01:05.000Z",
			`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":25000,"cache_creation_input_tokens":178000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":178000}}`)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(batch2); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := TailFile(db, path, "", Thresholds{}); err != nil {
		t.Fatalf("tail 2: %v", err)
	}
	if coldMark(t, db, "msg_C03") != 1 {
		t.Error("cold restart after a 2 h pause not marked")
	}
	var cleared sql.NullString
	if err := db.QueryRow(`SELECT cleared_at FROM session_cache_cold WHERE session_uuid = ?`, coldSessionUUID).Scan(&cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.String != "2026-10-22T12:01:00.000Z" {
		t.Errorf("prediction cleared_at = %q, want the new prompt's timestamp", cleared.String)
	}

	// A replay of the whole file changes nothing.
	if _, err := File(db, path); err != nil {
		t.Fatal(err)
	}
	if coldMark(t, db, "msg_C03") != 1 || coldMark(t, db, "msg_C02") != 0 {
		t.Error("replay moved the marks")
	}
}

// TestTailColdRestartUsesAccountKind: with the daemon's resolver installed, an
// API-key account's 5 min TTL marks a re-write after a 6 min pause that a
// subscription would still have had cached.
func TestTailColdRestartUsesAccountKind(t *testing.T) {
	t.Cleanup(func() { cacheColdCfg.Store(nil) })
	db := testDB(t)
	transcript := coldAsst("k-a1", "msg_K01", "2026-10-22T10:00:00.000Z",
		`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":200000}`) +
		coldUser("k-u1", "2026-10-22T10:05:55.000Z") +
		coldAsst("k-a2", "msg_K02", "2026-10-22T10:06:00.000Z",
			`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":200100}`)
	path := filepath.Join(t.TempDir(), coldSessionUUID+".jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	SetCacheCold(cachecold.Config{
		ColdMinutes: guards.CacheCold{Subscription: 60, API: 5},
		KindOf:      func(string) cachecold.Kind { return cachecold.KindAPI },
	})
	if _, err := TailFile(db, path, "", Thresholds{}); err != nil {
		t.Fatal(err)
	}
	if coldMark(t, db, "msg_K02") != 1 {
		t.Error("api account: a 6 min pause past the 5 min TTL not marked")
	}
}

// TestTailColdRestartDefaultIsSubscription: with nothing injected, the same
// 6 min pause is inside the subscription TTL and stays unmarked.
func TestTailColdRestartDefaultIsSubscription(t *testing.T) {
	cacheColdCfg.Store(nil)
	db := testDB(t)
	transcript := coldAsst("d-a1", "msg_D01", "2026-10-22T10:00:00.000Z",
		`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":200000}`) +
		coldAsst("d-a2", "msg_D02", "2026-10-22T10:06:00.000Z",
			`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":200100}`)
	path := filepath.Join(t.TempDir(), coldSessionUUID+".jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TailFile(db, path, "", Thresholds{}); err != nil {
		t.Fatal(err)
	}
	if coldMark(t, db, "msg_D02") != 0 {
		t.Error("default config marked a 6 min pause")
	}
}

// TestTailColdRestartSkipsSyntheticStub: a zero-usage `<synthetic>` assistant
// message (an API error, an interrupt) refreshes no cache, so the pause and the
// footprint are measured from the real turn before it.
func TestTailColdRestartSkipsSyntheticStub(t *testing.T) {
	cacheColdCfg.Store(nil)
	db := testDB(t)
	synthetic := `{"type":"assistant","uuid":"s-syn","message":{"model":"<synthetic>","id":"msg_SYN","role":"assistant",` +
		`"content":[{"type":"text","text":"API Error"}],"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}` +
		`,"timestamp":"2026-10-22T11:30:00.000Z","sessionId":"` + coldSessionUUID + `","cwd":"/tmp/coldproj"}` + "\n"
	transcript := coldAsst("s-a1", "msg_S01", "2026-10-22T10:00:00.000Z",
		`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":190000,"cache_creation_input_tokens":10000}`) +
		synthetic +
		coldUser("s-u1", "2026-10-22T12:00:00.000Z") +
		coldAsst("s-a2", "msg_S02", "2026-10-22T12:00:05.000Z",
			`{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":26000,"cache_creation_input_tokens":176000}`)
	path := filepath.Join(t.TempDir(), coldSessionUUID+".jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TailFile(db, path, "", Thresholds{}); err != nil {
		t.Fatal(err)
	}
	if coldMark(t, db, "msg_S02") != 1 {
		t.Error("cold restart behind a synthetic stub not marked")
	}
}
