// Package config embeds runtime configuration files shipped with the binary.
package config

import _ "embed"

// PricingJSON is the embedded default model pricing table (see pricing.json).
// Override at runtime with the SWARMERY_PRICING env var (path to a JSON file
// of the same shape) — no rebuild needed after a price change.
//
//go:embed pricing.json
var PricingJSON []byte

// GuardsJSON is the embedded default guard configuration (see guards.json):
// workaround words, stall and cache-cold thresholds, skill-load limits. Override
// at runtime with the SWARMERY_GUARDS env var (path to a JSON file of the same
// shape). The override REPLACES the whole document — internal/guards rejects a
// file missing any key instead of filling it from these defaults.
//
//go:embed guards.json
var GuardsJSON []byte
