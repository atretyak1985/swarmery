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
	"strings"
	"sync"

	"github.com/atretyak1985/swarmery/tools/swarmery/config"
)

// ModelPrice is USD per 1M tokens for one model.
// cache_write is the 5-minute-TTL write rate (see config/pricing.json _meta).
type ModelPrice struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
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
func (t *Table) PriceFor(model string) (ModelPrice, bool) {
	if p, ok := t.Models[model]; ok {
		return p, true
	}
	best := ""
	for prefix := range t.FallbackPrefixes {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best != "" {
		return t.Models[t.FallbackPrefixes[best]], true
	}
	return ModelPrice{}, false
}

// Turn is the minimal usage view of one turns row needed for pricing.
// Nil token pointers mirror SQL NULLs (user turns carry no usage).
type Turn struct {
	Model            string
	TokensIn         *int64
	TokensOut        *int64
	TokensCacheRead  *int64
	TokensCacheWrite *int64
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
		turn.TokensCacheRead == nil && turn.TokensCacheWrite == nil {
		return nil
	}
	p, ok := t.PriceFor(turn.Model)
	if !ok {
		warnUnknownModel(turn.Model)
		return nil
	}
	c := float64(deref(turn.TokensIn))/1e6*p.Input +
		float64(deref(turn.TokensOut))/1e6*p.Output +
		float64(deref(turn.TokensCacheRead))/1e6*p.CacheRead +
		float64(deref(turn.TokensCacheWrite))/1e6*p.CacheWrite
	return &c
}

// EnrichTurn prices a turn against the Default() table. This is the single
// integration point called from ingest (see "// metrics hook").
func EnrichTurn(turn Turn) *float64 {
	return Default().EnrichTurn(turn)
}

var warnedModels sync.Map

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
