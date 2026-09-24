package ingest

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestCacheWriteSplitParsing covers the decode in isolation: the TTL breakdown
// when cache_creation is present, and the legacy attribution when it is not.
func TestCacheWriteSplitParsing(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		want5m       int64
		want1h       int64
		wantSpeed    string
		wantFlatTotl int64
	}{
		{
			name:         "1h-only write (docs/jsonl-format.md §6 shape)",
			raw:          `{"input_tokens":17538,"cache_creation_input_tokens":6935,"cache_read_input_tokens":15457,"output_tokens":621,"cache_creation":{"ephemeral_1h_input_tokens":6935,"ephemeral_5m_input_tokens":0},"speed":"standard"}`,
			want5m:       0,
			want1h:       6935,
			wantSpeed:    "standard",
			wantFlatTotl: 6935,
		},
		{
			name:         "mixed 5m/1h write",
			raw:          `{"input_tokens":10,"cache_creation_input_tokens":1000,"output_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":400,"ephemeral_1h_input_tokens":600},"speed":"fast"}`,
			want5m:       400,
			want1h:       600,
			wantSpeed:    "fast",
			wantFlatTotl: 1000,
		},
		{
			name:         "legacy turn: no cache_creation object → all 5m",
			raw:          `{"input_tokens":10,"cache_creation_input_tokens":5146,"cache_read_input_tokens":99,"output_tokens":5}`,
			want5m:       5146,
			want1h:       0,
			wantSpeed:    "",
			wantFlatTotl: 5146,
		},
		{
			// A TTL bucket we do not parse yet must not fall out of the bill.
			// The unaccounted remainder is attributed to 5m (cheapest write
			// rate) so the estimate stays conservative instead of billing $0.
			name:         "unknown TTL bucket → remainder attributed to 5m",
			raw:          `{"input_tokens":10,"cache_creation_input_tokens":10000,"output_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0,"ephemeral_1d_input_tokens":10000}}`,
			want5m:       10000,
			want1h:       0,
			wantSpeed:    "",
			wantFlatTotl: 10000,
		},
		{
			name:         "no cache writes at all",
			raw:          `{"input_tokens":10,"cache_creation_input_tokens":0,"output_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}`,
			want5m:       0,
			want1h:       0,
			wantSpeed:    "",
			wantFlatTotl: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u usage
			if err := json.Unmarshal([]byte(tc.raw), &u); err != nil {
				t.Fatalf("decode usage: %v", err)
			}
			got5m, got1h := u.cacheWriteSplit()
			if got5m != tc.want5m || got1h != tc.want1h {
				t.Errorf("cacheWriteSplit = (%d, %d), want (%d, %d)", got5m, got1h, tc.want5m, tc.want1h)
			}
			if u.Speed != tc.wantSpeed {
				t.Errorf("speed = %q, want %q", u.Speed, tc.wantSpeed)
			}
			if u.CacheCreationInputTokens != tc.wantFlatTotl {
				t.Errorf("flat total = %d, want %d", u.CacheCreationInputTokens, tc.wantFlatTotl)
			}
			// The split must never invent or lose tokens against the flat total.
			if got5m+got1h != tc.wantFlatTotl {
				t.Errorf("5m+1h = %d, want the flat total %d", got5m+got1h, tc.wantFlatTotl)
			}
		})
	}
}

// ttlSessionFixture is a three-turn session on Opus 5.5: a 1h-only write, a
// fast-mode turn, and a legacy turn with no cache_creation object.
func ttlSessionFixture() string {
	asst := func(uuid, msgID, ts, usage string) string {
		return `{"type":"assistant","uuid":"` + uuid +
			`","message":{"model":"claude-opus-5-5","id":"` + msgID +
			`","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":` + usage + `}` +
			`,"timestamp":"` + ts + `","sessionId":"44444444-0000-4000-8000-000000000004"` +
			`,"cwd":"/tmp/ttlproj","version":"2.1.170","gitBranch":"main"}` + "\n"
	}
	return asst("ttl-a1", "msg_TTL01", "2026-09-23T10:00:01.000Z",
		`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":1000000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":1000000},"speed":"standard"}`) +
		asst("ttl-a2", "msg_TTL02", "2026-09-23T10:00:02.000Z",
			`{"input_tokens":1000000,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"speed":"fast"}`) +
		asst("ttl-a3", "msg_TTL03", "2026-09-23T10:00:03.000Z",
			`{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":1000000}`)
}

// TestIngestStoresCacheWriteTTLAndSpeed is the end-to-end case: a transcript
// goes in, the split and speed land in the new columns, and the cost written
// at ingest reflects both. The legacy turn is the control — it must still
// price at the 5m rate, so re-ingesting old history cannot move its cost.
func TestIngestStoresCacheWriteTTLAndSpeed(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "ttl-session.jsonl")
	if err := os.WriteFile(path, []byte(ttlSessionFixture()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	type turnRow struct {
		w5m, w1h, flat sql.NullInt64
		speed          sql.NullString
		cost           sql.NullFloat64
	}
	read := func(msgID string) turnRow {
		t.Helper()
		var r turnRow
		if err := db.QueryRow(`
			SELECT cache_write_5m_tokens, cache_write_1h_tokens, tokens_cache_write, speed, cost_usd
			  FROM turns WHERE message_id = ?`, msgID).
			Scan(&r.w5m, &r.w1h, &r.flat, &r.speed, &r.cost); err != nil {
			t.Fatalf("read turn %s: %v", msgID, err)
		}
		return r
	}

	// 1h-only: 1M tokens at the Opus 5.5 1h rate = $8.00, not the $5.00 the
	// flat 5m rate used to charge.
	one := read("msg_TTL01")
	if one.w5m.Int64 != 0 || one.w1h.Int64 != 1_000_000 {
		t.Errorf("1h turn split = (%d, %d), want (0, 1000000)", one.w5m.Int64, one.w1h.Int64)
	}
	if one.flat.Int64 != 1_000_000 {
		t.Errorf("1h turn flat total = %d, want 1000000 (the legacy column must still be written)", one.flat.Int64)
	}
	if !one.cost.Valid || math.Abs(one.cost.Float64-8.0) > 1e-9 {
		t.Errorf("1h turn cost = %v, want 8.00", one.cost)
	}

	// Fast: 1M input tokens at the claude-opus-5-5-fast rate ($8/M), not $4/M.
	fast := read("msg_TTL02")
	if fast.speed.String != "fast" {
		t.Errorf("fast turn speed = %q, want fast", fast.speed.String)
	}
	if !fast.cost.Valid || math.Abs(fast.cost.Float64-8.0) > 1e-9 {
		t.Errorf("fast turn cost = %v, want 8.00 (the -fast SKU is 2x standard)", fast.cost)
	}

	// Legacy: no cache_creation object → the whole total is 5m → $5.00.
	legacy := read("msg_TTL03")
	if legacy.w5m.Int64 != 1_000_000 || legacy.w1h.Int64 != 0 {
		t.Errorf("legacy turn split = (%d, %d), want (1000000, 0)", legacy.w5m.Int64, legacy.w1h.Int64)
	}
	if legacy.speed.Valid {
		t.Errorf("legacy turn speed = %q, want NULL (the field was absent)", legacy.speed.String)
	}
	if !legacy.cost.Valid || math.Abs(legacy.cost.Float64-5.0) > 1e-9 {
		t.Errorf("legacy turn cost = %v, want 5.00 (unchanged from before the split)", legacy.cost)
	}

	// Re-ingest must not move anything.
	if _, err := File(db, path); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if again := read("msg_TTL01"); again.cost.Float64 != one.cost.Float64 || again.w1h.Int64 != 1_000_000 {
		t.Errorf("re-ingest changed the 1h turn: %+v", again)
	}
}

// TestRebuildTextBackfillsCacheWriteSplit pins the only repair path for rows
// ingested before migration 0075: their split and speed are NULL and their
// lines are already consumed, so `backfill --rebuild-text` replays from byte 0
// and the matched turn must learn its split (which `swarmery recost` prices).
// A split that is already known is never overwritten by a replay.
func TestRebuildTextBackfillsCacheWriteSplit(t *testing.T) {
	db := testDB(t)
	root := t.TempDir()
	projDir := filepath.Join(root, "-tmp-ttlproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, "ttl-session.jsonl")
	if err := os.WriteFile(path, []byte(ttlSessionFixture()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Simulate pre-0075 rows for two turns; msg_TTL03 keeps a sentinel split
	// that the replay must leave alone.
	if _, err := db.Exec(`UPDATE turns SET cache_write_5m_tokens = NULL, cache_write_1h_tokens = NULL, speed = NULL
	                       WHERE message_id IN ('msg_TTL01', 'msg_TTL02')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE turns SET cache_write_5m_tokens = 7, cache_write_1h_tokens = 9
	                       WHERE message_id = 'msg_TTL03'`); err != nil {
		t.Fatal(err)
	}

	if stats := RebuildText(t.Context(), db, []string{root}); stats.Files != 1 || stats.Errors != 0 {
		t.Fatalf("rebuild stats = %+v, want 1 file / 0 errors", stats)
	}

	read := func(msgID string) (w5m, w1h sql.NullInt64, speed sql.NullString) {
		t.Helper()
		if err := db.QueryRow(`SELECT cache_write_5m_tokens, cache_write_1h_tokens, speed
		                         FROM turns WHERE message_id = ?`, msgID).Scan(&w5m, &w1h, &speed); err != nil {
			t.Fatalf("read turn %s: %v", msgID, err)
		}
		return
	}
	if w5m, w1h, speed := read("msg_TTL01"); w5m.Int64 != 0 || !w5m.Valid || w1h.Int64 != 1_000_000 || speed.String != "standard" {
		t.Errorf("1h turn after replay = (%v, %v, %v), want (0, 1000000, standard)", w5m, w1h, speed)
	}
	if _, _, speed := read("msg_TTL02"); speed.String != "fast" {
		t.Errorf("fast turn speed after replay = %v, want fast", speed)
	}
	if w5m, w1h, _ := read("msg_TTL03"); w5m.Int64 != 7 || w1h.Int64 != 9 {
		t.Errorf("known split was overwritten by the replay: (%v, %v), want (7, 9)", w5m, w1h)
	}
}
