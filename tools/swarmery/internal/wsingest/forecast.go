// Phase-doc `## Forecast` blocks — the predictive half of the learning loop.
//
// A phase doc may carry a small, machine-checkable forecast: what the author (a
// PRIOR, written by the planner) or the executor (a POSTERIOR, written after the
// work) expected the phase to touch, how big it would be, how long it would take
// and how it would end. Later phases measure the run and score the difference.
//
// A FORECAST IS DATA, NEVER A FENCE. Nothing in this daemon may gate on one: a
// run is not refused for diverging from its forecast, a phase is not incomplete
// for lacking one, and a block this parser cannot read is a LINT on a plan that
// still ingests. That is the same tolerance contract parseCard, parseRetroDoc and
// parsePlan hold — the scan runs on a debounce over every plan on the machine and
// must not be stoppable by one author's typo — and it is stricter here, because a
// forecast is by construction a guess.
//
// THE FENCE TENSION. Every other phase-doc reader in this package walks
// mdfence.ForEachLine, which SKIPS fenced content: a checklist quoted inside a
// ``` block is an illustration, not the doc's own text, and counting it left a
// shipped phase stuck at 7/11 for ever. A forecast is the exact opposite case —
// the payload IS the fenced yaml. This parser therefore uses BOTH sides of the
// one fence definition and never a second one:
//
//   - the `## Forecast` heading and the `## ` heading that ends its section are
//     found with mdfence.ForEachLine, so a `## Forecast` quoted inside an agent
//     prompt or a ````markdown example is invisible — it describes someone else's
//     phase, exactly as a quoted `**Model:**` line does;
//   - the yaml inside that section is read with mdfence.Blocks, the complement of
//     ForEachLine added for this reader.
//
// Both ask the same marker() where a fence closes, so the two halves cannot
// disagree about where the section's own text ends and an example begins.

package wsingest

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
)

// Forecast kinds. A PRIOR is written before the work (the planner's prediction);
// a POSTERIOR is written after it (the executor's account). One doc may carry
// both — two fenced blocks, told apart by this key.
const (
	ForecastPrior     = "prior"
	ForecastPosterior = "posterior"
)

// ForecastSizeBands / ForecastDurationBands / ForecastOutcomes are the closed
// vocabularies a forecast draws on. Declared here, next to the parser, because
// the lint is the only thing that judges them and it must judge exactly what
// plan-format.md documents.
var (
	ForecastSizeBands     = []string{"XS", "S", "M", "L", "XL"}
	ForecastDurationBands = []string{"<30m", "30-90m", "90m-4h", ">4h"}
	ForecastOutcomes      = []string{"done", "partial", "blocked"}
)

// Forecast is one parsed `## Forecast` yaml block.
//
// Every text field is stored VERBATIM (trimmed only), NOT normalized to a known
// band and NOT rejected — the same decision ParseModel documents at length. The
// scan reports what the author wrote; LintForecasts judges it; the operator sees
// the offending text and can fix it. Folding an unknown band to "" here would
// make a typo indistinguishable from an absent key, and the learning loop would
// silently score a forecast nobody made.
type Forecast struct {
	Kind         string   // prior | posterior, verbatim; "" when the block declares none
	WrittenAt    string   // RFC3339 as written; "" when absent
	Areas        []string // directories or module names
	Files        []string // optional, globs allowed
	SizeBand     string   // XS | S | M | L | XL, verbatim
	DurationBand string   // <30m | 30-90m | 90m-4h | >4h, verbatim
	Outcome      string   // done | partial | blocked, verbatim
	Risks        []string
	// Confidence is 0..1, nil when the key is absent OR unreadable as a number.
	// Nil is not 0: "the author did not say" and "the author is certain this is
	// wrong" are opposite statements, and a REAL column that cannot be null would
	// have to pick one of them.
	Confidence *float64
	// PostHoc marks a PRIOR that cannot have been a prediction: the doc it lives
	// in already carries a filled `## Completion Report`, so the work was already
	// reported done when the forecast was written. Derived by the scan rather than
	// declared by the author, because an author backfilling a prior is precisely
	// the person who would not tick the box. Always false for a posterior, which
	// is post hoc by definition and says so in Kind.
	PostHoc bool
}

// ForecastLint is one thing wrong with a phase's forecasts. Code is a stable
// slug for the UI; Message is the operator's sentence, quoting what was written.
type ForecastLint struct {
	Kind    string `json:"kind"`    // the forecast the lint is about: prior | posterior | ""
	Code    string `json:"code"`    // unknown-kind | unknown-size-band | …
	Message string `json:"message"` // human sentence, quotes the offending text
}

// Forecast lint codes.
const (
	LintUnknownKind          = "unknown-kind"
	LintUnknownSizeBand      = "unknown-size-band"
	LintUnknownDurationBand  = "unknown-duration-band"
	LintUnknownOutcome       = "unknown-outcome"
	LintBadConfidence        = "bad-confidence"
	LintMissingAreas         = "missing-areas"
	LintPosteriorWithoutPrior = "posterior-without-prior"
)

// forecastHeadingRe-free by design: the heading scan runs inside ForEachLine, so
// it is a prefix test on an already-unfenced line rather than a (?m) regex over
// the whole document — a regex would match a heading inside a fence, which is the
// one thing this parser must not do.
func isForecastHeading(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "## ") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(t[3:]), "Forecast")
}

// forecastYAML is the lenient shape a block is decoded into. Every field is a
// yaml.Node rather than its final type so that ONE bad value (confidence: high)
// costs that value and not the whole block — decoding straight into *float64
// fails the entire Unmarshal and would throw away the six keys that were fine.
type forecastYAML struct {
	Kind         yaml.Node `yaml:"kind"`
	WrittenAt    yaml.Node `yaml:"written_at"`
	Areas        yaml.Node `yaml:"areas"`
	Files        yaml.Node `yaml:"files"`
	SizeBand     yaml.Node `yaml:"size_band"`
	DurationBand yaml.Node `yaml:"duration_band"`
	Outcome      yaml.Node `yaml:"outcome"`
	Risks        yaml.Node `yaml:"risks"`
	Confidence   yaml.Node `yaml:"confidence"`
}

// ParseForecasts extracts the phase doc's `## Forecast` blocks, in document
// order. nil when the doc has no such section or the section holds no fenced
// block — the overwhelmingly common case, and the one that keeps every plan
// written before this format existed behaving exactly as it did.
//
// hasReport is whether the doc's `## Completion Report` is already filled; it
// decides Forecast.PostHoc. Passed in rather than re-derived so the doc body is
// parsed once per scan, like every other extraction in parsePlan.
//
// A block that is not readable yaml still yields a Forecast — an empty one. That
// is deliberate: dropping it would make "the author wrote a broken forecast" and
// "the author wrote none" the same observation, and the first one is a lint the
// operator should see (it surfaces as unknown-kind + missing-areas).
//
// Pure; unit-tested.
func ParseForecasts(md string, hasReport bool) []Forecast {
	lines := strings.Split(md, "\n")
	start, end := -1, len(lines)
	mdfence.ForEachLine(md, func(i int, line string) {
		switch {
		case start < 0 && isForecastHeading(line):
			start = i
		case start >= 0 && end == len(lines) && i > start && strings.HasPrefix(strings.TrimSpace(line), "## "):
			end = i
		}
	})
	if start < 0 {
		return nil
	}
	section := strings.Join(lines[start+1:end], "\n")

	var out []Forecast
	for _, b := range mdfence.Blocks(section) {
		if info := strings.ToLower(b.Info); info != "" && info != "yaml" && info != "yml" {
			continue // a shell snippet or a quoted example beside the forecast
		}
		out = append(out, decodeForecast(b.Content, hasReport))
	}
	return out
}

// decodeForecast turns one fenced block's body into a Forecast. Never fails:
// an undecodable body yields the zero Forecast, which LintForecasts reports.
func decodeForecast(body string, hasReport bool) Forecast {
	var raw forecastYAML
	_ = yaml.Unmarshal([]byte(body), &raw) // tolerant by contract — see the package doc

	f := Forecast{
		Kind:         strings.ToLower(nodeScalar(&raw.Kind)),
		WrittenAt:    nodeScalar(&raw.WrittenAt),
		Areas:        nodeStrings(&raw.Areas),
		Files:        nodeStrings(&raw.Files),
		SizeBand:     nodeScalar(&raw.SizeBand),
		DurationBand: nodeScalar(&raw.DurationBand),
		Outcome:      nodeScalar(&raw.Outcome),
		Risks:        nodeStrings(&raw.Risks),
	}
	if s := nodeScalar(&raw.Confidence); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			f.Confidence = &v
		}
	}
	f.PostHoc = hasReport && f.Kind == ForecastPrior
	return f
}

// nodeScalar is a scalar node's text exactly as written, trimmed of whitespace
// and of the markdown decoration an author may wrap it in. "" for a missing,
// null or non-scalar node.
func nodeScalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return ""
	}
	return strings.Trim(strings.TrimSpace(n.Value), "`*_")
}

// nodeStrings flattens a sequence node into its scalar entries, and accepts a
// bare scalar as a one-element list (`areas: internal/ingest` is what an author
// writes when there is only one). Empty entries are dropped; nil when nothing
// usable is there.
func nodeStrings(n *yaml.Node) []string {
	if n == nil || n.Tag == "!!null" {
		return nil
	}
	var out []string
	switch n.Kind {
	case yaml.ScalarNode:
		if s := nodeScalar(n); s != "" {
			out = append(out, s)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if s := nodeScalar(c); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// LintForecasts reports everything wrong with ONE phase's forecasts.
//
// It is the Covers lint's channel, not a gate: `unknownRefs` on the spec rollup
// is computed the same way — from already-stored rows, in the read path, purely
// for display — and nothing refuses a plan over it. The same holds here by
// design. A forecast that fails every rule below still ingests, still renders,
// and still lets its phase run.
//
// The rules are the four plan-format.md names, plus the two vocabulary checks
// that are the same rule applied to `kind` and `outcome`:
//
//   - an unrecognized kind / size_band / duration_band / outcome;
//   - a confidence that is not a number in 0..1;
//   - a forecast with no `areas` (the one required key — a forecast that names no
//     surface cannot be scored against what a run actually touched);
//   - a posterior with no prior beside it (nothing to score the prediction against,
//     which is the whole point of the pair).
//
// Order is stable: document order of the forecasts, rule order within each, then
// the phase-level rule last. Pure; unit-tested.
func LintForecasts(fs []Forecast) []ForecastLint {
	out := []ForecastLint{}
	var havePrior, havePosterior bool
	for _, f := range fs {
		switch f.Kind {
		case ForecastPrior:
			havePrior = true
		case ForecastPosterior:
			havePosterior = true
		default:
			out = append(out, ForecastLint{Kind: f.Kind, Code: LintUnknownKind,
				Message: fmt.Sprintf("forecast kind %q is not %s or %s", f.Kind, ForecastPrior, ForecastPosterior)})
		}
		if f.SizeBand != "" && !knownBand(f.SizeBand, ForecastSizeBands) {
			out = append(out, ForecastLint{Kind: f.Kind, Code: LintUnknownSizeBand,
				Message: fmt.Sprintf("size_band %q is not one of %s", f.SizeBand, strings.Join(ForecastSizeBands, ", "))})
		}
		if f.DurationBand != "" && !knownBand(f.DurationBand, ForecastDurationBands) {
			out = append(out, ForecastLint{Kind: f.Kind, Code: LintUnknownDurationBand,
				Message: fmt.Sprintf("duration_band %q is not one of %s", f.DurationBand, strings.Join(ForecastDurationBands, ", "))})
		}
		if f.Outcome != "" && !knownBand(f.Outcome, ForecastOutcomes) {
			out = append(out, ForecastLint{Kind: f.Kind, Code: LintUnknownOutcome,
				Message: fmt.Sprintf("outcome %q is not one of %s", f.Outcome, strings.Join(ForecastOutcomes, ", "))})
		}
		if f.Confidence != nil && (*f.Confidence < 0 || *f.Confidence > 1) {
			out = append(out, ForecastLint{Kind: f.Kind, Code: LintBadConfidence,
				Message: fmt.Sprintf("confidence %g is outside 0..1", *f.Confidence)})
		}
		if len(f.Areas) == 0 {
			out = append(out, ForecastLint{Kind: f.Kind, Code: LintMissingAreas,
				Message: "forecast declares no areas — nothing to score a run against"})
		}
	}
	if havePosterior && !havePrior {
		out = append(out, ForecastLint{Kind: ForecastPosterior, Code: LintPosteriorWithoutPrior,
			Message: "posterior forecast with no prior in the same doc — there is no prediction to score"})
	}
	return out
}

// knownBand matches a verbatim value against a closed vocabulary, case- and
// dash-insensitively: an author writing `30–90m` with an en dash, or `xs`, means
// the band and should not be linted for typography.
func knownBand(v string, vocab []string) bool {
	n := normBand(v)
	for _, w := range vocab {
		if normBand(w) == n {
			return true
		}
	}
	return false
}

var bandDashes = strings.NewReplacer("–", "-", "—", "-", " ", "")

func normBand(v string) string { return bandDashes.Replace(strings.ToLower(strings.TrimSpace(v))) }
