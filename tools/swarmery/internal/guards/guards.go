// Package guards loads the run guards' tunables: the words that mark a
// workaround, how long a silent run may stall, when a prompt cache counts as
// cold, and the skill-load limits.
//
// The defaults are config/guards.json, embedded in the binary. SWARMERY_GUARDS
// names a JSON file of the same shape that REPLACES the whole document — the
// same channel as SWARMERY_PRICING (config.PricingJSON). Unlike pricing, a broken
// override is an error and never a silent fallback: these values decide when a
// run is stopped, and a key missing from an operator's file must not quietly
// take the shipped default the operator thought they had changed.
package guards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/config"
)

// EnvOverride is the env var naming an override file.
const EnvOverride = "SWARMERY_GUARDS"

// Config is config/guards.json.
type Config struct {
	// WorkaroundWords mark an executor's text as a workaround rather than a fix
	// (D23, en + uk in one array); internal/workaround matches each as a whole
	// word or phrase, case-insensitively, in assistant prose only.
	WorkaroundWords []string `json:"workaroundWords"`
	// StallMinutes is how long a run may produce nothing before it counts as
	// stalled.
	StallMinutes int `json:"stallMinutes"`
	// PlaybookOverrides replaces StallMinutes for a named playbook (e.g. a phase
	// whose tests legitimately run for an hour).
	PlaybookOverrides map[string]int `json:"playbookOverrides"`
	// CacheColdMinutes is when a resumed session's prompt cache counts as cold,
	// per billing channel.
	CacheColdMinutes CacheCold `json:"cacheColdMinutes"`
	// SkillSimilarityThreshold (0..1] is when two skills count as duplicates.
	SkillSimilarityThreshold float64 `json:"skillSimilarityThreshold"`
	// SessionSkillLoadP95 is the p95 of skills a session loads above which the
	// estate is flagged.
	SessionSkillLoadP95 int `json:"sessionSkillLoadP95"`
}

// CacheCold is the prompt-cache TTL, in minutes, per billing channel.
type CacheCold struct {
	Subscription int `json:"subscription"`
	API          int `json:"api"`
}

// requiredKeys are the top-level keys every guards document must carry, and
// requiredCacheKeys those of cacheColdMinutes. Derived from the struct tags so a
// new field cannot be forgotten here.
var (
	requiredKeys      = jsonKeys(Config{})
	requiredCacheKeys = jsonKeys(CacheCold{})
)

// Defaults returns the embedded config/guards.json. An error means the shipped
// file is broken — a build defect the package tests catch.
func Defaults() (Config, error) {
	c, err := Parse(config.GuardsJSON)
	if err != nil {
		return Config{}, fmt.Errorf("embedded config/guards.json: %w", err)
	}
	return c, nil
}

// Load returns the override named by SWARMERY_GUARDS when the variable is set,
// the embedded defaults otherwise. An unreadable or incomplete override is an
// error, not a fallback.
func Load() (Config, error) {
	path := strings.TrimSpace(os.Getenv(EnvOverride))
	if path == "" {
		return Defaults()
	}
	c, err := LoadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("%s=%s: %w", EnvOverride, path, err)
	}
	return c, nil
}

// LoadFile parses a guards document from disk.
func LoadFile(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return Parse(raw)
}

// Parse decodes a WHOLE guards document: every key must be present (a missing
// one is named in the error), no unknown key is accepted (a typo would otherwise
// leave the real key missing anyway, with a worse message), and every value must
// be in range.
func Parse(raw []byte) (Config, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return Config{}, fmt.Errorf("not a JSON object: %w", err)
	}
	if err := requireKeys("", top, requiredKeys); err != nil {
		return Config{}, err
	}
	var cache map[string]json.RawMessage
	if err := json.Unmarshal(top["cacheColdMinutes"], &cache); err != nil {
		return Config{}, fmt.Errorf("cacheColdMinutes: not a JSON object: %w", err)
	}
	if err := requireKeys("cacheColdMinutes.", cache, requiredCacheKeys); err != nil {
		return Config{}, err
	}

	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, err
	}
	return c, c.validate()
}

func (c Config) validate() error {
	var bad []string
	if len(c.WorkaroundWords) == 0 {
		bad = append(bad, "workaroundWords is empty")
	}
	for i, w := range c.WorkaroundWords {
		if strings.TrimSpace(w) == "" {
			bad = append(bad, fmt.Sprintf("workaroundWords[%d] is blank", i))
		}
	}
	if c.StallMinutes <= 0 {
		bad = append(bad, "stallMinutes must be > 0")
	}
	for name, m := range c.PlaybookOverrides {
		if m <= 0 {
			bad = append(bad, fmt.Sprintf("playbookOverrides.%s must be > 0", name))
		}
	}
	if c.CacheColdMinutes.Subscription <= 0 || c.CacheColdMinutes.API <= 0 {
		bad = append(bad, "cacheColdMinutes values must be > 0")
	}
	if c.SkillSimilarityThreshold <= 0 || c.SkillSimilarityThreshold > 1 {
		bad = append(bad, "skillSimilarityThreshold must be in (0, 1]")
	}
	if c.SessionSkillLoadP95 <= 0 {
		bad = append(bad, "sessionSkillLoadP95 must be > 0")
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("invalid guards: %s", strings.Join(bad, "; "))
	}
	return nil
}

// StallMinutesFor is the stall threshold for a playbook: its override when one
// is set, StallMinutes otherwise.
func (c Config) StallMinutesFor(playbook string) int {
	if m, ok := c.PlaybookOverrides[playbook]; ok {
		return m
	}
	return c.StallMinutes
}

func requireKeys(prefix string, got map[string]json.RawMessage, want []string) error {
	var missing []string
	for _, k := range want {
		if v, ok := got[k]; !ok || string(v) == "null" {
			missing = append(missing, prefix+k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing key %s — the override replaces the whole document; copy every key from config/guards.json",
			strings.Join(missing, ", "))
	}
	return nil
}

// jsonKeys lists a struct's json tag names in field order.
func jsonKeys(v any) []string {
	t := reflect.TypeOf(v)
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		keys = append(keys, strings.Split(t.Field(i).Tag.Get("json"), ",")[0])
	}
	return keys
}
