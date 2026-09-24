package surprise

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Environment knobs. All optional; every one has a documented default.
const (
	// EnvWeights overrides component weights, comma-separated name=value pairs:
	// SWARMERY_SURPRISE_WEIGHTS="outcome_miss=0.4,size_miss=0.1". Components not
	// named keep their default weight. Negative or unparseable values are ignored
	// (with a warning) — a negative weight would make the index non-monotone.
	EnvWeights = "SWARMERY_SURPRISE_WEIGHTS"
	// EnvNotify is the index at and above which a scored run raises an attention
	// event (a WS frame for the notch and the dashboard, and a `phase_surprise`
	// webhook when that event is enabled). Default DefaultNotifyAt. "off" disables.
	EnvNotify = "SWARMERY_SURPRISE_NOTIFY"
	// EnvAutoVerifyAt is the index at and above which a finished phase run is
	// verified automatically, with the surprise summary as the verifier's focus
	// hint. OFF unless set: unset, "", "off" all mean no auto-verification.
	EnvAutoVerifyAt = "SWARMERY_SURPRISE_AUTOVERIFY_AT"
)

// DefaultNotifyAt is the attention threshold when EnvNotify is unset.
const DefaultNotifyAt = 0.6

// DefaultWeights are the component weights when EnvWeights is unset. They sum
// to 1, so a run that misses on every axis scores exactly 1 before clipping.
//
// The outcome carries the most weight because it is the miss an operator acts
// on (a phase predicted done that is not); areas come next because touching
// code nobody expected is the classic source of an unreviewed regression;
// size, the other areas share, duration, tests and overconfidence share the rest.
func DefaultWeights() map[string]float64 {
	return map[string]float64{
		CompUnexpectedAreas: 0.20,
		CompMissedAreas:     0.10,
		CompSizeMiss:        0.15,
		CompDurationMiss:    0.10,
		CompOutcomeMiss:     0.25,
		CompTestSurprise:    0.10,
		CompOverconfidence:  0.10,
	}
}

// Config is the scorer's configuration.
type Config struct {
	Weights map[string]float64
	// NotifyAt is the attention threshold; nil disables attention events.
	NotifyAt *float64
	// AutoVerifyAt is the auto-verification threshold; nil (the default) means
	// auto-verification is OFF.
	AutoVerifyAt *float64
}

// DefaultConfig is the configuration with no environment set: default weights,
// attention at DefaultNotifyAt, auto-verification off.
func DefaultConfig() Config {
	n := DefaultNotifyAt
	return Config{Weights: DefaultWeights(), NotifyAt: &n}
}

// ConfigFromEnv reads the knobs through getenv (os.Getenv in the daemon; a map
// in tests). Bad values never fail startup: each is ignored with a warning and
// the default stands, because a typo in an advisory knob must not take the
// daemon down.
func ConfigFromEnv(getenv func(string) string) (Config, []string) {
	cfg := DefaultConfig()
	var warn []string

	if raw := strings.TrimSpace(getenv(EnvWeights)); raw != "" {
		known := map[string]bool{}
		for _, c := range Components {
			known[c] = true
		}
		for _, pair := range strings.Split(raw, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			name, val, ok := strings.Cut(pair, "=")
			name = strings.TrimSpace(name)
			v, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
			switch {
			case !ok || !known[name]:
				warn = append(warn, fmt.Sprintf("%s: ignoring %q (want <component>=<weight>, component one of %s)",
					EnvWeights, pair, strings.Join(Components, ", ")))
			case err != nil || !(v >= 0) || math.IsInf(v, 1): // NaN and +Inf fail too
				warn = append(warn, fmt.Sprintf("%s: ignoring %q (weight must be a number ≥ 0)", EnvWeights, pair))
			default:
				cfg.Weights[name] = v
			}
		}
	}

	if v, ok, w := threshold(EnvNotify, getenv(EnvNotify)); w != "" {
		warn = append(warn, w)
	} else if ok {
		cfg.NotifyAt = v // nil when "off"
	}
	if v, ok, w := threshold(EnvAutoVerifyAt, getenv(EnvAutoVerifyAt)); w != "" {
		warn = append(warn, w)
	} else if ok {
		cfg.AutoVerifyAt = v
	}
	return cfg, warn
}

// threshold parses one 0..1 threshold. ok=false with no warning means "unset —
// keep the default"; ok=true with a nil value means "off".
func threshold(name, raw string) (v *float64, ok bool, warn string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, ""
	}
	if strings.EqualFold(raw, "off") {
		return nil, true, ""
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || !(f >= 0 && f <= 1) { // written so NaN fails too
		return nil, false, fmt.Sprintf("%s: ignoring %q (want a number in 0..1, or off)", name, raw)
	}
	return &f, true, ""
}

// String renders the effective configuration for the startup log.
func (c Config) String() string {
	names := make([]string, 0, len(c.Weights))
	for n := range c.Weights {
		names = append(names, n)
	}
	sort.Strings(names)
	ws := make([]string, 0, len(names))
	for _, n := range names {
		ws = append(ws, fmt.Sprintf("%s=%.2f", n, c.Weights[n]))
	}
	return fmt.Sprintf("weights[%s] notify=%s autoverify=%s",
		strings.Join(ws, " "), fmtThreshold(c.NotifyAt), fmtThreshold(c.AutoVerifyAt))
}

func fmtThreshold(v *float64) string {
	if v == nil {
		return "off"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
