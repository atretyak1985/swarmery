package cost

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/config"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func i64(v int64) *int64 { return &v }

func testTable(t *testing.T) *Table {
	t.Helper()
	tbl, err := Load([]byte(`{
		"models": {
			"m-basic": {"input": 1, "output": 2, "cache_read": 0.1, "cache_write": 1.25},
			"m-basic-pro": {"input": 4, "output": 8, "cache_read": 0.4, "cache_write": 5}
		},
		"fallback_prefixes": {
			"m-basic-": "m-basic",
			"m-basic-pro-": "m-basic-pro"
		}
	}`))
	if err != nil {
		t.Fatalf("load test table: %v", err)
	}
	return tbl
}

func TestEnrichTurn(t *testing.T) {
	tbl := testTable(t)

	cases := []struct {
		name string
		turn Turn
		want *float64 // nil = expect NULL
	}{
		{
			name: "all four token kinds",
			turn: Turn{Model: "m-basic", TokensIn: i64(1_000_000), TokensOut: i64(1_000_000),
				TokensCacheRead: i64(1_000_000), TokensCacheWrite: i64(1_000_000)},
			want: f64(1 + 2 + 0.1 + 1.25),
		},
		{
			name: "cache tokens dominate (realistic shape)",
			turn: Turn{Model: "m-basic", TokensIn: i64(12), TokensOut: i64(80),
				TokensCacheRead: i64(9_000), TokensCacheWrite: i64(150)},
			// 12/1e6*1 + 80/1e6*2 + 9000/1e6*0.1 + 150/1e6*1.25
			want: f64(0.000012 + 0.00016 + 0.0009 + 0.0001875),
		},
		{
			name: "unknown model → NULL, never 0",
			turn: Turn{Model: "m-nonexistent", TokensIn: i64(1000), TokensOut: i64(1000)},
			want: nil,
		},
		{
			name: "empty model → NULL",
			turn: Turn{Model: "", TokensIn: i64(1000)},
			want: nil,
		},
		{
			name: "zero usage with known model → 0.0 (priced, not NULL)",
			turn: Turn{Model: "m-basic", TokensIn: i64(0), TokensOut: i64(0),
				TokensCacheRead: i64(0), TokensCacheWrite: i64(0)},
			want: f64(0),
		},
		{
			name: "no usage at all (user turn) → NULL",
			turn: Turn{Model: "m-basic"},
			want: nil,
		},
		{
			name: "partial usage (only output) still priced",
			turn: Turn{Model: "m-basic", TokensOut: i64(500_000)},
			want: f64(1.0),
		},
		{
			name: "versioned id resolves via prefix fallback",
			turn: Turn{Model: "m-basic-20260101", TokensIn: i64(1_000_000)},
			want: f64(1),
		},
		{
			name: "longest prefix wins",
			turn: Turn{Model: "m-basic-pro-20260101", TokensIn: i64(1_000_000)},
			want: f64(4),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tbl.EnrichTurn(tc.turn)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("EnrichTurn = %v, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("EnrichTurn = nil, want %v", *tc.want)
			case tc.want != nil && math.Abs(*got-*tc.want) > 1e-12:
				t.Fatalf("EnrichTurn = %.12f, want %.12f", *got, *tc.want)
			}
		})
	}
}

func TestLoadValidation(t *testing.T) {
	if _, err := Load([]byte(`{"models": {}}`)); err == nil {
		t.Error("empty models must fail")
	}
	if _, err := Load([]byte(`{"models": {"a": {"input": 1}}, "fallback_prefixes": {"b-": "b"}}`)); err == nil {
		t.Error("dangling fallback target must fail")
	}
	if _, err := Load([]byte(`not json`)); err == nil {
		t.Error("malformed JSON must fail")
	}
}

// TestEmbeddedPricing pins the embedded table to the JSONL model ids observed
// in docs/jsonl-format.md §6 and to the platform.claude.com prices verified
// at implementation time.
func TestEmbeddedPricing(t *testing.T) {
	tbl, err := Load(config.PricingJSON)
	if err != nil {
		t.Fatalf("embedded pricing.json invalid: %v", err)
	}

	// Exact id as it appears in main-chain assistant records.
	p, ok := tbl.PriceFor("claude-fable-5")
	if !ok {
		t.Fatal("claude-fable-5 missing from embedded pricing")
	}
	if p.Input != 10 || p.Output != 50 || p.CacheRead != 1 || p.CacheWrite != 12.5 {
		t.Errorf("claude-fable-5 = %+v, want {10 50 1 12.5}", p)
	}

	// Date-suffixed id (sidechain haiku subagent) resolves via prefix.
	p, ok = tbl.PriceFor("claude-haiku-4-5-20251001")
	if !ok {
		t.Fatal("claude-haiku-4-5-20251001 did not resolve via fallback_prefixes")
	}
	if p.Input != 1 || p.Output != 5 || p.CacheRead != 0.1 || p.CacheWrite != 1.25 {
		t.Errorf("claude-haiku-4-5-20251001 = %+v, want {1 5 0.1 1.25}", p)
	}

	// Realistic fable-5 turn (fixture usage shape).
	got := tbl.EnrichTurn(Turn{Model: "claude-fable-5",
		TokensIn: i64(12695), TokensOut: i64(467),
		TokensCacheRead: i64(15457), TokensCacheWrite: i64(5146)})
	if got == nil {
		t.Fatal("fable-5 turn not priced")
	}
	want := 12695.0/1e6*10 + 467.0/1e6*50 + 15457.0/1e6*1 + 5146.0/1e6*12.5
	if math.Abs(*got-want) > 1e-12 {
		t.Errorf("fable-5 turn cost = %.9f, want %.9f", *got, want)
	}
}

func TestRecostIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "recost.db"))
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
	mustExec(`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/p', '-p', '2026-07-12T00:00:00Z')`)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at)
	          VALUES (1, 1, 'u1', 'm-basic', 'completed', '2026-07-12T00:00:00Z'),
	                 (2, 1, 'u2', 'm-unknown', 'completed', '2026-07-12T00:00:00Z')`)
	mustExec(`INSERT INTO turns (id, session_id, seq, role, model, started_at, tokens_in, tokens_out, cost_usd)
	          VALUES (1, 1, 0, 'assistant', NULL,      '2026-07-12T00:00:01Z', 1000000, 1000000, 999.0),
	                 (2, 1, 1, 'user',      NULL,      '2026-07-12T00:00:02Z', NULL, NULL, 123.0),
	                 (3, 2, 0, 'assistant', NULL,      '2026-07-12T00:00:03Z', 500, 500, 0.0),
	                 (4, 2, 1, 'assistant', 'm-basic', '2026-07-12T00:00:04Z', 1000000, 0, NULL)`)

	tbl := testTable(t)
	for pass := 1; pass <= 2; pass++ { // second pass proves idempotency
		stats, err := Recost(db, tbl)
		if err != nil {
			t.Fatalf("recost pass %d: %v", pass, err)
		}
		if stats.Total != 4 || stats.Priced != 2 || stats.Unpriced != 1 || stats.NoUsage != 1 {
			t.Fatalf("pass %d stats = %+v, want Total:4 Priced:2 Unpriced:1 NoUsage:1", pass, stats)
		}
	}

	var c1 float64
	if err := db.QueryRow(`SELECT cost_usd FROM turns WHERE id = 1`).Scan(&c1); err != nil {
		t.Fatalf("turn 1 cost: %v", err)
	}
	if math.Abs(c1-3.0) > 1e-12 { // 1e6/1e6*1 + 1e6/1e6*2
		t.Errorf("turn 1 cost = %v, want 3.0 (stale 999.0 must be overwritten)", c1)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turns WHERE id IN (2,3) AND cost_usd IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("turns 2 (no usage) and 3 (unknown model) must both be NULL; got %d NULLs", n)
	}
	// turns.model beats the session's unknown model (migration 0002 semantics).
	var c4 float64
	if err := db.QueryRow(`SELECT cost_usd FROM turns WHERE id = 4`).Scan(&c4); err != nil {
		t.Fatalf("turn 4 cost: %v", err)
	}
	if math.Abs(c4-1.0) > 1e-12 { // 1e6/1e6 * m-basic input rate (1)
		t.Errorf("turn 4 cost = %v, want 1.0 (per-turn model must override session model)", c4)
	}
}

func f64(v float64) *float64 { return &v }
