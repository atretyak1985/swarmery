// Package lessons turns an understood surprise into a reviewable lesson
// candidate (Opus 5.5 / learning-loop phase 14).
//
// The loop: a phase run is scored against its forecast (internal/surprise). When
// the score reaches the attention threshold AND the executor explained the gap in
// its Completion Report ("Where reality diverged"), a cheap headless model reads
// the forecast, the measured actuals, that paragraph and the diff stat, and may
// return 0–2 small lessons — one imperative sentence of guidance, the area globs
// it applies to, and evidence ids copied from its input. Every lesson is
// validated like a retro analysis: an id that was not offered is a fabrication,
// and the lesson is rejected.
//
// A candidate is ADVISORY until an operator accepts it. Accept (review.go) is
// the only code path that sets status = 'active'; activeguard_test.go proves it
// over the whole module. Generation is best-effort: it runs in the background
// after scoring, logs and drops every failure, and never changes how a run is
// reported.
package lessons

import (
	"fmt"
	"regexp"
	"strings"
)

// Environment knobs.
const (
	// EnvEnabled switches candidate generation. Default ON: generation spends
	// tokens only for a run that is BOTH above the surprise attention threshold
	// and explained by a divergence paragraph, so no LLM call ever happens
	// without a surprise. "off" (or 0/false/no) disables it.
	EnvEnabled = "SWARMERY_LESSONS"
	// EnvModel overrides DefaultModel for the generation run.
	EnvModel = "SWARMERY_LESSON_MODEL"
	// EnvEffort overrides DefaultEffort (resolved by internal/claudeflags, which
	// also honours the cross-site SWARMERY_EFFORT and the "off" escape hatch).
	EnvEffort = "SWARMERY_LESSON_EFFORT"
)

// DefaultModel / DefaultEffort pin the generation run: a mechanical pass over
// material the prompt already carries, so the cheap tier at low depth.
const (
	DefaultModel  = "claude-sonnet-5"
	DefaultEffort = "low"
)

// MaxLessons caps one generation. A run that taught three things taught none
// of them well enough to write down.
const MaxLessons = 2

// Lesson statuses. StatusActive is written by exactly one function (Accept).
const (
	StatusCandidate = "candidate"
	StatusActive    = "active"
	StatusRetired   = "retired"
	StatusDismissed = "dismissed"
	StatusMerged    = "merged"
)

// Config is the generator's configuration.
type Config struct {
	Enabled bool
	// Threshold is the surprise index at and above which a run is eligible. It
	// is the surprise attention threshold (SWARMERY_SURPRISE_NOTIFY): a run that
	// does not deserve the operator's attention does not deserve a lesson
	// either. nil ⇒ no run is eligible.
	Threshold *float64
	// Model is the resolved --model value (EnvModel, else DefaultModel).
	Model string
}

// ConfigFromEnv reads the knobs. notifyAt is surprise.Config.NotifyAt.
func ConfigFromEnv(getenv func(string) string, notifyAt *float64) (Config, []string) {
	cfg := Config{Enabled: true, Threshold: notifyAt, Model: DefaultModel}
	var warn []string
	switch v := strings.ToLower(strings.TrimSpace(getenv(EnvEnabled))); v {
	case "", "on", "1", "true", "yes":
	case "off", "0", "false", "no":
		cfg.Enabled = false
	default:
		warn = append(warn, fmt.Sprintf("%s: ignoring %q (want on or off)", EnvEnabled, v))
	}
	if m := strings.TrimSpace(getenv(EnvModel)); m != "" {
		cfg.Model = m
	}
	return cfg, warn
}

// String renders the configuration for the startup log.
func (c Config) String() string {
	th := "off"
	if c.Threshold != nil {
		th = fmt.Sprintf("%.2f", *c.Threshold)
	}
	return fmt.Sprintf("enabled=%t threshold=%s model=%s", c.Enabled, th, c.Model)
}

// maxDivergenceRunes bounds the paragraph handed to the model and stored on the
// candidate.
const maxDivergenceRunes = 2000

var (
	divergenceAt    = regexp.MustCompile(`(?i)where reality diverged`)
	divergenceLabel = regexp.MustCompile(`(?i)\*{0,2}where reality diverged\*{0,2}\s*[:.—-]?\s*\*{0,2}`)
	headingPrefix   = regexp.MustCompile(`^#+\s*`)
)

// ExtractDivergence returns the Completion Report's "Where reality diverged"
// paragraph: the label line (with any text after the label) plus the lines up
// to the next blank line or heading. "" when absent.
//
// The Go twin of web/src/pages/Plans.tsx extractDivergence — keep them in
// lockstep, so the paragraph the operator reads on the "Forecast vs actual" tab
// is the paragraph a lesson was generated from.
func ExtractDivergence(report string) string {
	lines := strings.Split(report, "\n")
	at := -1
	for i, l := range lines {
		if divergenceAt.MatchString(l) {
			at = i
			break
		}
	}
	if at < 0 {
		return ""
	}
	var out []string
	first := headingPrefix.ReplaceAllString(lines[at], "")
	if loc := divergenceLabel.FindStringIndex(first); loc != nil {
		first = first[:loc[0]] + first[loc[1]:]
	}
	if first = strings.TrimSpace(first); first != "" {
		out = append(out, first)
	}
	for _, line := range lines[at+1:] {
		if strings.HasPrefix(line, "#") {
			break
		}
		if strings.TrimSpace(line) == "" {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, line)
	}
	return capRunes(strings.TrimSpace(strings.Join(out, "\n")), maxDivergenceRunes)
}

func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
