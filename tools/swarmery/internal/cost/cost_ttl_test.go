package cost

import (
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/config"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// ttlTable carries what the base testTable does not: a distinct 1h cache-write
// rate and a fast SKU, so the two can be told apart from the 5m/standard ones
// by the NUMBER alone (a test where 5m and 1h price the same cannot fail).
func ttlTable(t *testing.T) *Table {
	t.Helper()
	tbl, err := Load([]byte(`{
		"models": {
			"m-basic":      {"input": 1, "output": 2, "cache_read": 0.1, "cache_write": 1.25, "cache_write_1h": 2},
			"m-basic-fast": {"input": 2, "output": 4, "cache_read": 0.2, "cache_write": 2.5,  "cache_write_1h": 4},
			"m-solo":       {"input": 1, "output": 2, "cache_read": 0.1, "cache_write": 1.25, "cache_write_1h": 2}
		},
		"fallback_prefixes": {
			"m-basic-": "m-basic",
			"m-solo-":  "m-solo"
		}
	}`))
	if err != nil {
		t.Fatalf("load ttl table: %v", err)
	}
	return tbl
}

// TestEnrichTurnCacheWriteTTL pins the three shapes a cache write arrives in:
// split, 1h-only, and the legacy flat total from a transcript that had no
// cache_creation object at all.
func TestEnrichTurnCacheWriteTTL(t *testing.T) {
	tbl := ttlTable(t)

	cases := []struct {
		name string
		turn Turn
		want float64
	}{
		{
			name: "1h-only write bills at the 1h rate",
			turn: Turn{Model: "m-basic",
				TokensCacheWrite5m: i64(0), TokensCacheWrite1h: i64(1_000_000)},
			want: 2.0,
		},
		{
			name: "5m-only write bills at the 5m rate",
			turn: Turn{Model: "m-basic",
				TokensCacheWrite5m: i64(1_000_000), TokensCacheWrite1h: i64(0)},
			want: 1.25,
		},
		{
			name: "mixed 5m/1h write bills each bucket at its own rate",
			turn: Turn{Model: "m-basic",
				TokensCacheWrite5m: i64(400_000), TokensCacheWrite1h: i64(600_000)},
			// 0.4*1.25 + 0.6*2 = 0.5 + 1.2
			want: 1.7,
		},
		{
			name: "legacy turn (no split stored) bills the flat total at the 5m rate",
			turn: Turn{Model: "m-basic", TokensCacheWrite: i64(1_000_000)},
			want: 1.25,
		},
		{
			name: "split present wins over the flat total (no double-count)",
			turn: Turn{Model: "m-basic", TokensCacheWrite: i64(1_000_000),
				TokensCacheWrite5m: i64(0), TokensCacheWrite1h: i64(1_000_000)},
			want: 2.0,
		},
		{
			name: "a turn whose only usage is a 1h write is still priced",
			turn: Turn{Model: "m-basic", TokensCacheWrite1h: i64(500_000)},
			want: 1.0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tbl.EnrichTurn(tc.turn)
			if got == nil {
				t.Fatalf("EnrichTurn = nil, want %v", tc.want)
			}
			if math.Abs(*got-tc.want) > 1e-12 {
				t.Fatalf("EnrichTurn = %.12f, want %.12f", *got, tc.want)
			}
		})
	}
}

// TestEnrichTurnSpeed pins fast-mode routing. The date-suffixed case is the
// sharp one: "-fast" is appended to the RESOLVED key, never to the raw id,
// because "m-basic-20260101-fast" would fall back through "m-basic-" onto
// STANDARD rates and look like a successful fast lookup.
func TestEnrichTurnSpeed(t *testing.T) {
	tbl := ttlTable(t)

	cases := []struct {
		name  string
		model string
		speed string
		want  float64 // cost of 1M input tokens
	}{
		{name: "standard speed uses the standard row", model: "m-basic", speed: "standard", want: 1},
		{name: "empty speed (pre-field transcript) uses the standard row", model: "m-basic", speed: "", want: 1},
		{name: "fast speed uses the -fast row", model: "m-basic", speed: "fast", want: 2},
		{name: "fast speed resolves through a date suffix", model: "m-basic-20260101", speed: "fast", want: 2},
		{name: "an id already on the fast SKU is not doubled", model: "m-basic-fast", speed: "fast", want: 2},
		{name: "no fast SKU falls back to standard, never to NULL", model: "m-solo", speed: "fast", want: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tbl.EnrichTurn(Turn{Model: tc.model, Speed: tc.speed, TokensIn: i64(1_000_000)})
			if got == nil {
				t.Fatalf("EnrichTurn = nil, want %v", tc.want)
			}
			if math.Abs(*got-tc.want) > 1e-12 {
				t.Fatalf("EnrichTurn = %.12f, want %.12f", *got, tc.want)
			}
		})
	}
}

// TestOpus55OneHourCacheWrite is the phase's headline number: a 1h-only cache
// write of 1,000,000 tokens on Opus 5.5 costs $8.00, not the $5.00 the flat
// 5m rate used to charge.
func TestOpus55OneHourCacheWrite(t *testing.T) {
	tbl, err := Load(config.PricingJSON)
	if err != nil {
		t.Fatalf("embedded pricing.json invalid: %v", err)
	}

	got := tbl.EnrichTurn(Turn{Model: "claude-opus-5-5",
		TokensCacheWrite5m: i64(0), TokensCacheWrite1h: i64(1_000_000)})
	if got == nil {
		t.Fatal("1h cache-write turn not priced")
	}
	if math.Abs(*got-8.0) > 1e-12 {
		t.Errorf("1M 1h cache-write tokens on Opus 5.5 = $%.6f, want $8.00", *got)
	}

	// The same tokens arriving as a legacy flat total must NOT move — old
	// transcripts keep the cost they already had until they are re-ingested.
	legacy := tbl.EnrichTurn(Turn{Model: "claude-opus-5-5", TokensCacheWrite: i64(1_000_000)})
	if legacy == nil || math.Abs(*legacy-5.0) > 1e-12 {
		t.Errorf("legacy flat 1M cache-write on Opus 5.5 = %v, want $5.00 (unchanged)", legacy)
	}

	// The real transcript shape from docs/jsonl-format.md §6: all 6935 write
	// tokens are 1h.
	real := tbl.EnrichTurn(Turn{Model: "claude-opus-5-5",
		TokensIn: i64(17538), TokensOut: i64(621), TokensCacheRead: i64(15457),
		TokensCacheWrite: i64(6935), TokensCacheWrite5m: i64(0), TokensCacheWrite1h: i64(6935)})
	want := 17538.0/1e6*4 + 621.0/1e6*20 + 15457.0/1e6*0.2 + 6935.0/1e6*8
	if real == nil || math.Abs(*real-want) > 1e-12 {
		t.Errorf("jsonl-format §6 turn = %v, want %.9f", real, want)
	}
}

// TestOpus55FastFromSpeedField pins the other half of the under-billing: a
// transcript id never contains "-fast", so only usage.speed can route a fast
// turn onto its 2x SKU.
func TestOpus55FastFromSpeedField(t *testing.T) {
	tbl, err := Load(config.PricingJSON)
	if err != nil {
		t.Fatalf("embedded pricing.json invalid: %v", err)
	}

	for _, id := range []string{"claude-opus-5-5", "claude-opus-5-5[1m]", "claude-opus-5-5-20260922"} {
		p, ok := tbl.PriceForSpeed(id, "fast")
		if !ok {
			t.Errorf("%s (fast): not priced", id)
			continue
		}
		if p.Input != 8 || p.Output != 40 || p.CacheRead != 0.4 || p.CacheWrite != 10 || p.CacheWrite1h != 16 {
			t.Errorf("%s (fast) = %+v, want {8 40 0.4 10 16}", id, p)
		}
		// Standard speed on the same id must stay on the cheap row.
		if p, _ := tbl.PriceForSpeed(id, "standard"); p.Input != 4 {
			t.Errorf("%s (standard) input = %v, want 4", id, p.Input)
		}
	}

	// Opus 5 keeps its own fast SKU rather than borrowing 5.5's.
	if p, ok := tbl.PriceForSpeed("claude-opus-5", "fast"); !ok || p.Input != 10 || p.Output != 50 {
		t.Errorf("claude-opus-5 (fast) = %+v (ok=%v), want {10 50 ...}", p, ok)
	}
}

// TestEmbeddedPricingHasCacheWrite1h reads the raw JSON, not the loaded table:
// Load falls a missing cache_write_1h back to the 5m rate, so a struct-level
// check would pass on exactly the rows this test exists to catch.
func TestEmbeddedPricingHasCacheWrite1h(t *testing.T) {
	var raw struct {
		Models map[string]map[string]float64 `json:"models"`
	}
	if err := json.Unmarshal(config.PricingJSON, &raw); err != nil {
		t.Fatalf("embedded pricing.json invalid: %v", err)
	}
	if len(raw.Models) == 0 {
		t.Fatal("no models in embedded pricing.json")
	}
	for id, fields := range raw.Models {
		rate, ok := fields["cache_write_1h"]
		if !ok {
			t.Errorf("%s: no cache_write_1h — every 1h cache write on this model bills at the 5m rate", id)
			continue
		}
		if rate <= fields["cache_write"] {
			t.Errorf("%s: cache_write_1h %v must exceed cache_write %v (1h is 2x input, 5m is 1.25x)",
				id, rate, fields["cache_write"])
		}
	}
}

// TestRecostAppliesTTLSplitAndSpeed proves the repair path: a recost run reads
// the stored split and speed back out of the DB and re-prices from them, while
// a pre-0075 row (NULLs) keeps the cost it always had.
func TestRecostAppliesTTLSplitAndSpeed(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "recost-ttl.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', '-p', '2026-09-23T00:00:00Z')`)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at)
	          VALUES (1, 1, 'u1', 'm-basic', 'completed', '2026-09-23T00:00:00Z')`)
	mustExec(`INSERT INTO turns (id, session_id, seq, role, model, started_at,
	                             tokens_cache_write, cache_write_5m_tokens, cache_write_1h_tokens, speed, tokens_in)
	          VALUES
	            (1, 1, 0, 'assistant', 'm-basic', '2026-09-23T00:00:01Z', 1000000, 0,       1000000, NULL,     NULL),
	            (2, 1, 1, 'assistant', 'm-basic', '2026-09-23T00:00:02Z', 1000000, 1000000, 0,       NULL,     NULL),
	            (3, 1, 2, 'assistant', 'm-basic', '2026-09-23T00:00:03Z', 1000000, NULL,    NULL,    NULL,     NULL),
	            (4, 1, 3, 'assistant', 'm-basic', '2026-09-23T00:00:04Z', NULL,    NULL,    NULL,    'fast',   1000000)`)

	if _, err := Recost(db, ttlTable(t)); err != nil {
		t.Fatalf("recost: %v", err)
	}

	for _, tc := range []struct {
		id   int64
		want float64
		why  string
	}{
		{1, 2.00, "1h bucket must bill at the 1h rate"},
		{2, 1.25, "5m bucket must bill at the 5m rate"},
		{3, 1.25, "a pre-0075 row (NULL split) must keep its old cost"},
		{4, 2.00, "speed=fast must route to the -fast SKU"},
	} {
		var got float64
		if err := db.QueryRow(`SELECT cost_usd FROM turns WHERE id = ?`, tc.id).Scan(&got); err != nil {
			t.Fatalf("turn %d cost: %v", tc.id, err)
		}
		if math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("turn %d cost = %v, want %v (%s)", tc.id, got, tc.want, tc.why)
		}
	}
}
