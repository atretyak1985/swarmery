// Package cost computes per-turn USD cost from token usage and the model
// pricing table in config/pricing.json.
//
// Honesty rule: an unknown model yields a nil cost (stored as SQL NULL),
// never 0 — a zero would silently corrupt aggregate sums.
package cost

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/atretyak1985/swarmery/tools/swarmery/config"
)

// ModelPrice is USD per 1M tokens for one model.
// CacheWrite is the 5-minute-TTL write rate, CacheWrite1h the 1-hour-TTL rate
// (see config/pricing.json _meta). The two are different SKUs — 1.25x input
// against 2x input — and Claude Code writes both.
type ModelPrice struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite   float64 `json:"cache_write"`
	CacheWrite1h float64 `json:"cache_write_1h"`
}

// Table is a loaded pricing table. Immutable after Load.
type Table struct {
	Models           map[string]ModelPrice `json:"models"`
	FallbackPrefixes map[string]string     `json:"fallback_prefixes"`
}

// Load parses a pricing table from JSON and validates its internal references.
func Load(raw []byte) (*Table, error) {
	var t Table
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parse pricing table: %w", err)
	}
	if len(t.Models) == 0 {
		return nil, fmt.Errorf("pricing table has no models")
	}
	for prefix, target := range t.FallbackPrefixes {
		if _, ok := t.Models[target]; !ok {
			return nil, fmt.Errorf("pricing table: fallback_prefixes[%q] points at unknown model %q", prefix, target)
		}
	}
	// A model row with no cache_write_1h would price every 1h write at $0 —
	// silently CHEAPER than the truth, which is the one direction a cost table
	// must never fail in. Fall the bucket back to the 5m rate (under-bills by
	// the 5m/1h spread, but stays the same order of magnitude) and name the
	// rows so the gap gets closed in the table rather than in the arithmetic.
	var missing []string
	for id, p := range t.Models {
		if p.CacheWrite1h == 0 && p.CacheWrite > 0 {
			p.CacheWrite1h = p.CacheWrite
			t.Models[id] = p
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		log.Printf("warn: cost: no cache_write_1h for %s — 1h cache writes billed at the 5m rate (add cache_write_1h to config/pricing.json)",
			strings.Join(missing, ", "))
	}
	return &t, nil
}

// LoadFile loads a pricing table from a JSON file on disk.
func LoadFile(path string) (*Table, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pricing file: %w", err)
	}
	return Load(raw)
}

var (
	defaultOnce  sync.Once
	defaultTable *Table
)

// Default returns the process-wide pricing table, loaded once at first use:
// the file named by SWARMERY_PRICING if set, otherwise the embedded
// config/pricing.json. No hot reload — restart (or `swarmery recost`) after
// editing prices.
func Default() *Table {
	defaultOnce.Do(func() {
		if path := os.Getenv("SWARMERY_PRICING"); path != "" {
			t, err := LoadFile(path)
			if err == nil {
				log.Printf("cost: pricing loaded from %s (%d models)", path, len(t.Models))
				defaultTable = t
				return
			}
			log.Printf("warn: cost: SWARMERY_PRICING=%s: %v — falling back to embedded pricing", path, err)
		}
		t, err := Load(config.PricingJSON)
		if err != nil {
			// Embedded table broken = programmer error; stay honest: an empty
			// table prices nothing, so every cost becomes NULL, never a lie.
			log.Printf("error: cost: embedded pricing.json invalid (%v) — all costs will be NULL", err)
			t = &Table{Models: map[string]ModelPrice{}}
		}
		defaultTable = t
	})
	return defaultTable
}

// PriceFor resolves a model id to its price: exact key match first, then the
// longest matching entry in fallback_prefixes (for date-suffixed ids like
// claude-haiku-4-5-20251001).
//
// A trailing context-window marker is stripped before either lookup. Claude
// Code stamps the selected window onto the id it writes to the transcript
// ("claude-opus-5-5[1m]"), but the window carries no price of its own — the
// full 1M context is standard-priced, per the note in config/pricing.json — so
// the marker is identity noise here. Stripping it is what keeps the
// longest-prefix fallback honest: "claude-opus-5-5[1m]" is NOT matched by the
// "claude-opus-5-5-" prefix (no dash before the bracket) but IS matched by the
// shorter "claude-opus-5-", which would silently bill an Opus 5.5 session at
// Opus 5 rates. Without the strip the id is simply unknown and goes unpriced;
// with a sibling model whose id is a prefix of it, unknown turns into wrong.
func (t *Table) PriceFor(model string) (ModelPrice, bool) {
	return t.PriceForSpeed(model, "")
}

// PriceForSpeed resolves a model id to its price at the given usage.speed
// ("standard" / "fast" / "" when the transcript predates the field).
//
// Fast mode is a separate SKU at 2x standard, carried by a sibling
// "<model>-fast" row. The lookup runs on the RESOLVED pricing key, not on the
// raw id: a date-suffixed id like "claude-opus-5-5-20260922" has no
// "...-20260922-fast" row, and appending -fast to it would fall back through
// the "claude-opus-5-5-" prefix straight onto STANDARD rates while looking
// like a successful fast lookup. Resolving first ("claude-opus-5-5") and then
// asking for "claude-opus-5-5-fast" either hits the fast SKU exactly or misses
// loudly.
func (t *Table) PriceForSpeed(model, speed string) (ModelPrice, bool) {
	key, ok := t.resolveKey(model)
	if !ok {
		return ModelPrice{}, false
	}
	if speed == "fast" && !strings.HasSuffix(key, "-fast") {
		if p, ok := t.Models[key+"-fast"]; ok {
			return p, true
		}
		warnMissingFastSKU(key)
	}
	return t.Models[key], true
}

// resolveKey maps a transcript model id onto a key in Models: exact match
// first, then the longest matching entry in fallback_prefixes. See PriceFor
// for why the trailing context-window marker is stripped before either lookup.
func (t *Table) resolveKey(model string) (string, bool) {
	if i := strings.IndexByte(model, '['); i > 0 {
		model = model[:i]
	}
	if _, ok := t.Models[model]; ok {
		return model, true
	}
	best := ""
	for prefix := range t.FallbackPrefixes {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best != "" {
		return t.FallbackPrefixes[best], true
	}
	return "", false
}

// Turn is the minimal usage view of one turns row needed for pricing.
// Nil token pointers mirror SQL NULLs (user turns carry no usage).
type Turn struct {
	Model           string
	Speed           string // usage.speed: "standard" / "fast" / "" (unknown)
	TokensIn        *int64
	TokensOut       *int64
	TokensCacheRead *int64

	// TokensCacheWrite is the flat cache-write total. It prices the turn only
	// when the TTL split below is absent (pre-0075 rows, transcripts with no
	// cache_creation object); otherwise the split is authoritative and this
	// field is carried for continuity of the legacy column.
	TokensCacheWrite   *int64
	TokensCacheWrite5m *int64
	TokensCacheWrite1h *int64
}

// cacheWriteBuckets returns the (5m, 1h) token counts to bill this turn on.
// With neither split field set the legacy total is billed entirely at the 5m
// rate — exactly how it was priced before the split existed, so re-pricing old
// rows never moves their cost.
func (turn Turn) cacheWriteBuckets() (fiveMin, oneHour int64) {
	if turn.TokensCacheWrite5m == nil && turn.TokensCacheWrite1h == nil {
		return deref(turn.TokensCacheWrite), 0
	}
	return deref(turn.TokensCacheWrite5m), deref(turn.TokensCacheWrite1h)
}

// EnrichTurn computes the USD cost of one turn, or nil when the turn cannot
// be priced honestly:
//   - all token fields nil (no usage — e.g. user turns) → nil
//   - unknown model → nil + one warn log per model (never 0)
//
// Zero usage with a known model prices to 0.0 — that is a real, priced cost.
// The full float is returned; round only at display time.
func (t *Table) EnrichTurn(turn Turn) *float64 {
	if turn.TokensIn == nil && turn.TokensOut == nil &&
		turn.TokensCacheRead == nil && turn.TokensCacheWrite == nil &&
		turn.TokensCacheWrite5m == nil && turn.TokensCacheWrite1h == nil {
		return nil
	}
	p, ok := t.PriceForSpeed(turn.Model, turn.Speed)
	if !ok {
		warnUnknownModel(turn.Model)
		return nil
	}
	write5m, write1h := turn.cacheWriteBuckets()
	c := float64(deref(turn.TokensIn))/1e6*p.Input +
		float64(deref(turn.TokensOut))/1e6*p.Output +
		float64(deref(turn.TokensCacheRead))/1e6*p.CacheRead +
		float64(write5m)/1e6*p.CacheWrite +
		float64(write1h)/1e6*p.CacheWrite1h
	return &c
}

// EnrichTurn prices a turn against the Default() table. This is the single
// integration point called from ingest (see "// metrics hook").
func EnrichTurn(turn Turn) *float64 {
	return Default().EnrichTurn(turn)
}

var (
	warnedModels  sync.Map
	warnedFastSKU sync.Map
)

// warnMissingFastSKU logs once per model per process when a fast-mode turn has
// no "<model>-fast" row. The turn is billed at standard rates, which UNDER-bills
// it by 2x — named here rather than left silent.
func warnMissingFastSKU(key string) {
	if _, seen := warnedFastSKU.LoadOrStore(key, true); !seen {
		log.Printf("warn: cost: speed=fast turn on %q has no %q row — billed at standard rates (2x under-billed; add the fast SKU to config/pricing.json and run `swarmery recost`)", key, key+"-fast")
	}
}

// warnUnknownModel logs once per unknown model per process (avoids log spam
// on transcripts with thousands of turns).
func warnUnknownModel(model string) {
	if _, seen := warnedModels.LoadOrStore(model, true); !seen {
		log.Printf("warn: cost: no pricing for model %q — cost_usd left NULL (add it to config/pricing.json and run `swarmery recost`)", model)
	}
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
