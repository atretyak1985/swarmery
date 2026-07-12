// Package config embeds runtime configuration files shipped with the binary.
package config

import _ "embed"

// PricingJSON is the embedded default model pricing table (see pricing.json).
// Override at runtime with the SWARMERY_PRICING env var (path to a JSON file
// of the same shape) — no rebuild needed after a price change.
//
//go:embed pricing.json
var PricingJSON []byte
