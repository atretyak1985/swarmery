package route

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
)

// EnvPolicy names an optional JSON policy file. Unset or blank means the
// in-code DefaultPolicy; a set path that cannot be read or parsed is an error —
// an operator who pointed at a file meant that file, and silently routing on
// defaults would hide the typo behind plausible-looking picks.
const EnvPolicy = "SWARMERY_ROUTE_POLICY"

// Mode is how a surface uses the router.
type Mode string

const (
	// ModeOff: the router is not consulted at all.
	ModeOff Mode = "off"
	// ModeShadow: the router decides and the decision is recorded, but the run
	// keeps the pick it would have made without it. The default.
	ModeShadow Mode = "shadow"
	// ModeActive: the decision is applied where the caller has no explicit pick.
	ModeActive Mode = "active"
)

// ModeFromEnv reads a surface's mode knob (e.g. SWARMERY_ROUTE_DISPATCH).
// Unset means shadow. An unrecognised value also means shadow, with a logged
// warning: an operator's typo must neither disable recording (off) nor start
// changing picks (active).
func ModeFromEnv(name string) Mode {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return ModeShadow
	}
	switch m := Mode(strings.ToLower(raw)); m {
	case ModeOff, ModeShadow, ModeActive:
		return m
	}
	log.Printf("warning: route: ignoring invalid %s=%q; using %s (valid: %s, %s, %s)",
		name, raw, ModeShadow, ModeOff, ModeShadow, ModeActive)
	return ModeShadow
}

// Band awards Points to a value ≤ Max. A Ladder's bands are checked in order,
// so they must be ascending by Max.
type Band struct {
	Max    int `json:"max"`
	Points int `json:"points"`
}

// Ladder scores one integer signal: a negative value is "unknown" and earns
// Unknown; otherwise the first band whose Max covers the value wins; a value
// above every band earns Else.
type Ladder struct {
	Unknown int    `json:"unknown"`
	Bands   []Band `json:"bands"`
	Else    int    `json:"else"`
}

// SizePoints is the ForecastSize row: points per size_band of a phase prior.
type SizePoints struct {
	XS int `json:"XS"`
	S  int `json:"S"`
	M  int `json:"M"`
	L  int `json:"L"`
	XL int `json:"XL"`
}

// Weights are the per-signal points. Every field is non-negative, so a score
// only ever grows with the signals and only the top clamp can cut it.
type Weights struct {
	PromptBytes  Ladder     `json:"prompt_bytes"`
	ForecastSize SizePoints `json:"forecast_size"`
	FileScope    Ladder     `json:"file_scope"`
	Areas        Ladder     `json:"areas"`
	RiskPaths    int        `json:"risk_paths"`
	Deps         int        `json:"deps"`
	// HistFail is awarded when HistFailRate ≥ HistFailRateMin over at least
	// HistMinSamples runs; below that sample count history is ignored.
	HistFail        int     `json:"hist_fail"`
	HistFailRateMin float64 `json:"hist_fail_rate_min"`
	HistMinSamples  int     `json:"hist_min_samples"`
}

// Cutoffs are the lowest scores of tiers M, L and XL; anything below M is S.
type Cutoffs struct {
	M  int `json:"M"`
	L  int `json:"L"`
	XL int `json:"XL"`
}

// Pick is what one tier runs with. Playbook applies to the dispatch surface only.
type Pick struct {
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Playbook string `json:"playbook"`
}

// Tiers is the per-tier pick table.
type Tiers struct {
	S  Pick `json:"S"`
	M  Pick `json:"M"`
	L  Pick `json:"L"`
	XL Pick `json:"XL"`
}

// Policy is the router's whole configuration: weights, tier cut-offs and the
// per-tier pick table.
//
// A policy file overrides it PARTIALLY: it is decoded on top of DefaultPolicy,
// so any key the file names replaces that value and every key it omits keeps
// its default. Objects merge key by key (naming only tiers.S.model keeps S's
// effort and playbook); a ladder's "bands" list is replaced as a whole, since a
// half-merged list of cut-offs has no sensible meaning.
type Policy struct {
	Weights Weights `json:"weights"`
	Cutoffs Cutoffs `json:"cutoffs"`
	Tiers   Tiers   `json:"tiers"`
}

// DefaultPolicy is the in-code policy. The numbers are a starting guess, meant
// to be tuned from shadow data — they live in this one literal so that a tuning
// change is a one-hunk diff. Returns a fresh value each call.
func DefaultPolicy() Policy {
	return Policy{
		Weights: Weights{
			PromptBytes:     Ladder{Bands: []Band{{Max: 399, Points: 0}, {Max: 1499, Points: 10}, {Max: 3999, Points: 20}}, Else: 30},
			ForecastSize:    SizePoints{XS: 0, S: 10, M: 25, L: 40, XL: 55},
			FileScope:       Ladder{Unknown: 10, Bands: []Band{{Max: 1, Points: 0}, {Max: 5, Points: 10}, {Max: 15, Points: 20}}, Else: 30},
			Areas:           Ladder{Unknown: 0, Bands: []Band{{Max: 1, Points: 0}, {Max: 3, Points: 10}}, Else: 20},
			RiskPaths:       15,
			Deps:            10,
			HistFail:        15,
			HistFailRateMin: 0.3,
			HistMinSamples:  5,
		},
		Cutoffs: Cutoffs{M: 20, L: 45, XL: 70},
		Tiers: Tiers{
			S:  Pick{Model: "haiku", Effort: "low", Playbook: PlaybookStandard},
			M:  Pick{Model: "sonnet", Effort: "medium", Playbook: PlaybookStandard},
			L:  Pick{Model: "opus", Effort: "high", Playbook: PlaybookPlanFirst},
			XL: Pick{Model: "opus", Effort: "xhigh", Playbook: PlaybookPlanFirst},
		},
	}
}

// LoadPolicy returns DefaultPolicy for an empty path (the env knob unset) and
// otherwise the file at path decoded on top of it. Unknown keys, a bad effort,
// a model the daemon cannot run, a playbook outside the auto-selectable set,
// or inconsistent numbers are all errors naming what is wrong.
func LoadPolicy(path string) (Policy, error) {
	p := DefaultPolicy()
	if strings.TrimSpace(path) == "" {
		return p, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("route policy %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields() // the error names the key: `json: unknown field "x"`
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("route policy %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("route policy %s: trailing data after the JSON object", path)
	}
	if err := p.normalize(); err != nil {
		return Policy{}, fmt.Errorf("route policy %s: %w", path, err)
	}
	return p, nil
}

// normalize validates the policy and canonicalizes the tier picks in place.
func (p *Policy) normalize() error {
	w := p.Weights
	// Slices, not maps: the first problem reported must be the same every run.
	for _, l := range []struct {
		name   string
		ladder Ladder
	}{{"prompt_bytes", w.PromptBytes}, {"file_scope", w.FileScope}, {"areas", w.Areas}} {
		if err := l.ladder.validate(); err != nil {
			return fmt.Errorf("weights.%s: %w", l.name, err)
		}
	}
	fs := w.ForecastSize
	for _, n := range []struct {
		name string
		v    int
	}{
		{"forecast_size", min(fs.XS, fs.S, fs.M, fs.L, fs.XL)},
		{"risk_paths", w.RiskPaths},
		{"deps", w.Deps},
		{"hist_fail", w.HistFail},
		{"hist_min_samples", w.HistMinSamples},
	} {
		if n.v < 0 {
			return fmt.Errorf("weights.%s: must be ≥ 0, got %d", n.name, n.v)
		}
	}
	if !(w.HistFailRateMin >= 0 && w.HistFailRateMin <= 1) {
		return fmt.Errorf("weights.hist_fail_rate_min: must be in 0..1, got %v", w.HistFailRateMin)
	}
	c := p.Cutoffs
	if !(0 < c.M && c.M < c.L && c.L < c.XL && c.XL <= maxScore) {
		return fmt.Errorf("cutoffs: want 0 < M < L < XL ≤ %d, got M=%d L=%d XL=%d", maxScore, c.M, c.L, c.XL)
	}
	for _, t := range []struct {
		name string
		pick *Pick
	}{{TierS, &p.Tiers.S}, {TierM, &p.Tiers.M}, {TierL, &p.Tiers.L}, {TierXL, &p.Tiers.XL}} {
		if err := t.pick.normalize(); err != nil {
			return fmt.Errorf("tiers.%s: %w", t.name, err)
		}
	}
	return nil
}

// validate checks a ladder's points are non-negative and its bands ascending.
func (l Ladder) validate() error {
	if l.Unknown < 0 || l.Else < 0 {
		return fmt.Errorf("points must be ≥ 0 (unknown=%d else=%d)", l.Unknown, l.Else)
	}
	for i, b := range l.Bands {
		if b.Points < 0 {
			return fmt.Errorf("bands[%d].points must be ≥ 0, got %d", i, b.Points)
		}
		if i > 0 && b.Max <= l.Bands[i-1].Max {
			return fmt.Errorf("bands must be strictly ascending by max (bands[%d].max=%d ≤ %d)", i, b.Max, l.Bands[i-1].Max)
		}
	}
	return nil
}

// modelAliasHaiku is accepted on top of planning.ResolveModel: the tier-S
// default picks it, and `claude --model haiku` is a valid CLI alias, but
// planning.Models is the planner's closed set and has no haiku in it.
const modelAliasHaiku = "haiku"

// ModelHaiku is the full ID the haiku alias spawns with. A full ID, not the
// alias, for the reason every engine pins one: an alias re-resolves over time,
// so the same route decision would silently change model between releases.
const ModelHaiku = "claude-haiku-4-5-20251001"

// ModelID maps a pick's model alias onto the full ID a spawn is handed.
//
// This package owns the map rather than planning.Models because haiku must
// stay OUT of that set: planning.Models is the planner's model picker, and a
// router tier that runs on haiku is no reason to offer haiku for writing plans.
// Every other alias (and a full ID already) resolves exactly as the planner
// resolves it, so a route pick of "opus" and a request for "opus" can never
// name two different models.
func ModelID(alias string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(alias))
	if a == "" {
		// planning.ResolveModel would answer "" with its DefaultModel; a pick
		// with no model is a broken pick, never a request for the default.
		return "", fmt.Errorf("%w: empty pick", planning.ErrUnknownModel)
	}
	if a == modelAliasHaiku || a == ModelHaiku {
		return ModelHaiku, nil
	}
	return planning.ResolveModel(a) // already wraps planning.ErrUnknownModel
}

// normalize canonicalizes one tier pick: a known effort (never "off" — a tier
// that omits --effort would silently inherit the CLI's xhigh), a model the
// daemon can run, and an auto-selectable playbook (never review-heavy).
func (k *Pick) normalize() error {
	effort, ok := claudeflags.NormalizeEffort(k.Effort)
	if !ok || effort == "" {
		return fmt.Errorf("effort %q: want one of %s", k.Effort, strings.Join(claudeflags.ValidEfforts(), ", "))
	}
	k.Effort = effort

	model := strings.ToLower(strings.TrimSpace(k.Model))
	if model == "" {
		return fmt.Errorf("model: required")
	}
	// The same map active mode spawns through, so a policy that loads is a
	// policy whose every tier can actually run.
	if _, err := ModelID(model); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	k.Model = model

	pb := strings.ToLower(strings.TrimSpace(k.Playbook))
	if pb != PlaybookStandard && pb != PlaybookPlanFirst {
		return fmt.Errorf("playbook %q: want %s or %s (review-heavy is never auto-selected)",
			k.Playbook, PlaybookStandard, PlaybookPlanFirst)
	}
	k.Playbook = pb
	return nil
}
