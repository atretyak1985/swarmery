// Package surprise scores how far a finished phase run landed from what was
// predicted for it (learning loop phase 13). The forecast (phase_forecasts,
// 0079/0080) is compared with the run's measured actuals (phase_actuals, 0081)
// and the difference becomes a SURPRISE VECTOR plus one headline index 0..1,
// stored in phase_surprise (0082).
//
// The idea is the predictive-processing one: when input matches prediction
// there is nothing to attend to, and a mismatch is exactly what deserves the
// operator's attention and what the learning loop learns from. So a surprise is
// ROUTED — a Plans chip, a notification above a threshold, an opt-in
// verification — and never ENFORCED.
//
// ADVISORY, END TO END. Nothing may block a merge, a run, or a phase's
// completion on a surprise score, the way internal/trajjudge never gates a
// merge. Every writer here logs and returns on failure.
//
// DETERMINISTIC, NO LLM. The same forecast and actuals always produce the same
// vector, index and summary.
//
// THE VECTOR. Each component is a number in 0..1, or nil when it cannot be
// measured (nil is NOT zero — "we could not tell" is not "it went as predicted"):
//
//	unexpected_areas  share of the actual areas no forecast area covers
//	missed_areas      share of the forecast areas the run never touched
//	size_miss         band distance, forecast size vs actual size: 0 → 0, 1 → 0.5, 2+ → 1
//	duration_miss     the same for the duration band
//	outcome_miss      1 when the forecast said done and the run did not complete, or the reverse
//	test_surprise     1 when a test failed outside what the forecast named (actuals' test_failures_unexpected > 0)
//	overconfidence    the forecast's confidence when any MAJOR miss happened, else 0
//
// A major miss is: unexpected_areas or missed_areas ≥ 0.5, a size or duration
// miss of 2+ bands, an outcome miss, or a test surprise.
//
// THE INDEX is the weighted sum of the measurable components, clipped to 0..1.
// Unmeasurable components contribute nothing, so thin evidence reads as a LOW
// surprise rather than an alarming one. With non-negative weights the index is
// monotone: raising any component never lowers it. The default weights sum to 1,
// so a run that misses on every axis scores 1.
//
// OUTCOME VOCABULARIES. A forecast says done | partial | blocked; a run's
// outcome is phasediag's completed | partial | noop | failed (| running | idle,
// which never reach a scored row). They are compared on the one axis both can
// state: did the phase get DONE?
//
//	forecast done              ↔ actual completed
//	forecast partial | blocked ↔ actual partial | noop | failed
//
// Anything else (an unknown forecast value, an actual outcome of running/idle)
// makes outcome_miss unmeasurable.
package surprise

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Component names — the keys of the vector, of the weights, and the label the
// UI puts on a chip. Stable: they are stored in phase_surprise.components_json.
const (
	CompUnexpectedAreas = "unexpected_areas"
	CompMissedAreas     = "missed_areas"
	CompSizeMiss        = "size_miss"
	CompDurationMiss    = "duration_miss"
	CompOutcomeMiss     = "outcome_miss"
	CompTestSurprise    = "test_surprise"
	CompOverconfidence  = "overconfidence"
)

// Components lists every component in its fixed order. The order is the
// tie-break when two components contribute equally to the index.
var Components = []string{
	CompUnexpectedAreas, CompMissedAreas, CompSizeMiss, CompDurationMiss,
	CompOutcomeMiss, CompTestSurprise, CompOverconfidence,
}

// majorAreaShare is the area share at and above which an area miss is major.
const majorAreaShare = 0.5

// Forecast is the part of a phase_forecasts row the scorer reads.
type Forecast struct {
	Kind         string
	Areas        []string
	Files        []string
	SizeBand     string
	DurationBand string
	Outcome      string
	Confidence   *float64 // nil = the author said nothing readable
	PostHoc      bool
	DocHash      string
}

// Actual is the part of a phase_actuals row the scorer reads. nil pointers and
// a nil Files slice mean UNMEASURED.
type Actual struct {
	// Files are the repo-relative paths the run changed. nil ⇒ the diff was not
	// measured (then every area component is unmeasurable); empty ⇒ measured, and
	// the run changed nothing.
	Files []string
	// Areas are the run's areas as actuals stored them (depth-truncated dirs).
	Areas     []string
	AreaDepth int
	SizeBand  *string
	DurationS *int64
	// Outcome is phasediag's derived outcome for the run.
	Outcome string
	// TestFailuresUnexpected is actuals' count of failing test runs outside the
	// forecast's areas/files/risks; nil when not measured.
	TestFailuresUnexpected *int
	Source                 string
}

// Detail is the human half of a score: what was compared with what. It is what
// the Plans "Forecast vs actual" tab renders, so every field says a FACT rather
// than a derived number.
type Detail struct {
	ForecastKind    string   `json:"forecastKind"`
	ForecastPostHoc bool     `json:"forecastPostHoc"`
	ForecastAreas   []string `json:"forecastAreas"`
	// ActualAreas is null when the diff was not measured.
	ActualAreas []string `json:"actualAreas"`
	// UnexpectedAreas are actual areas no forecast area covers; MissedAreas are
	// forecast areas the run never touched; MatchedAreas are forecast areas it did.
	UnexpectedAreas []string `json:"unexpectedAreas"`
	MissedAreas     []string `json:"missedAreas"`
	MatchedAreas    []string `json:"matchedAreas"`

	ForecastSize string `json:"forecastSize"`
	ActualSize   string `json:"actualSize"`
	// SizeDistance is |forecast band − actual band| in bands; null when either
	// band is unknown.
	SizeDistance *int `json:"sizeDistance"`

	ForecastDuration string `json:"forecastDuration"`
	ActualDuration   string `json:"actualDuration"`
	ActualDurationS  *int64 `json:"actualDurationS"`
	DurationDistance *int   `json:"durationDistance"`

	ForecastOutcome string `json:"forecastOutcome"`
	ActualOutcome   string `json:"actualOutcome"`

	TestFailuresUnexpected *int     `json:"testFailuresUnexpected"`
	Confidence             *float64 `json:"confidence"`
	MajorMiss              bool     `json:"majorMiss"`
}

// Revision is step 13.2: how much reading the code changed the expectation —
// the prior compared with the posterior on the same axes the run is scored on.
// It needs no actuals; it is stored beside the run's score because that is when
// both halves are known to be final.
type Revision struct {
	// Index is the mean of the measurable normalized revision axes (areas share,
	// size, duration, outcome), 0..1. It is its own scale, not the surprise index.
	Index        float64  `json:"index"`
	AreasAdded   []string `json:"areasAdded"`   // posterior areas no prior area covers
	AreasDropped []string `json:"areasDropped"` // prior areas the posterior no longer names
	// SizeShift / DurationShift are signed band moves, posterior − prior
	// (positive = the executor expects MORE). null when either band is unknown.
	SizeShift        *int     `json:"sizeShift"`
	DurationShift    *int     `json:"durationShift"`
	PriorOutcome     string   `json:"priorOutcome"`
	PosteriorOutcome string   `json:"posteriorOutcome"`
	ConfidenceDelta  *float64 `json:"confidenceDelta"`
}

// Result is one computed score.
type Result struct {
	Index float64 `json:"index"`
	// Top is the component with the largest weighted contribution; "" when the
	// index is 0 (nothing diverged).
	Top        string              `json:"top"`
	Components map[string]*float64 `json:"components"`
	Weights    map[string]float64  `json:"weights"`
	Detail     Detail              `json:"detail"`
	Revision   *Revision           `json:"revision"`
	Summary    string              `json:"summary"`
}

// Compute scores one run. ok is false when NOTHING could be measured — then
// there is no score at all, never a zero one.
func Compute(f Forecast, a Actual, weights map[string]float64) (Result, bool) {
	comps := make(map[string]*float64, len(Components))
	d := Detail{
		ForecastKind: f.Kind, ForecastPostHoc: f.PostHoc,
		ForecastAreas: nonNil(f.Areas),
		ForecastSize:  strings.TrimSpace(f.SizeBand), ForecastDuration: strings.TrimSpace(f.DurationBand),
		ForecastOutcome: strings.TrimSpace(f.Outcome), ActualOutcome: a.Outcome,
		ActualDurationS: a.DurationS, TestFailuresUnexpected: a.TestFailuresUnexpected,
		UnexpectedAreas: []string{}, MissedAreas: []string{}, MatchedAreas: []string{},
	}
	if f.Confidence != nil {
		c := clip01(*f.Confidence)
		d.Confidence = &c
	}

	// Areas.
	if a.Files != nil {
		d.ActualAreas = nonNil(a.Areas)
		if len(d.ActualAreas) == 0 && len(a.Files) > 0 {
			d.ActualAreas = areasOf(a.Files, a.AreaDepth)
		}
		diff := diffAreas(f.Areas, d.ActualAreas, a.Files, a.AreaDepth)
		d.UnexpectedAreas, d.MissedAreas, d.MatchedAreas = diff.unexpected, diff.missed, diff.matched
		if diff.forecastN > 0 {
			comps[CompUnexpectedAreas] = ptr(share(len(diff.unexpected), diff.actualN))
			comps[CompMissedAreas] = ptr(share(len(diff.missed), diff.forecastN))
		}
	}

	// Size.
	if a.SizeBand != nil {
		d.ActualSize = *a.SizeBand
	}
	if dist, ok := bandDistance(wsingest.ForecastSizeBands, f.SizeBand, d.ActualSize); ok {
		d.SizeDistance = &dist
		comps[CompSizeMiss] = ptr(bandNorm(dist))
	}

	// Duration.
	if a.DurationS != nil {
		d.ActualDuration = DurationBand(*a.DurationS)
	}
	if dist, ok := bandDistance(wsingest.ForecastDurationBands, f.DurationBand, d.ActualDuration); ok {
		d.DurationDistance = &dist
		comps[CompDurationMiss] = ptr(bandNorm(dist))
	}

	// Outcome.
	fd, fok := forecastDone(f.Outcome)
	ad, aok := actualDone(a.Outcome)
	if fok && aok {
		comps[CompOutcomeMiss] = ptr(boolNum(fd != ad))
	}

	// Tests.
	if a.TestFailuresUnexpected != nil {
		comps[CompTestSurprise] = ptr(boolNum(*a.TestFailuresUnexpected > 0))
	}

	measured := len(comps) > 0
	if !measured {
		return Result{}, false
	}

	// Overconfidence rides on the others, so it is judged last and only when at
	// least one other axis was measurable.
	d.MajorMiss = majorMiss(comps, d)
	if d.Confidence != nil {
		v := 0.0
		if d.MajorMiss {
			v = *d.Confidence
		}
		comps[CompOverconfidence] = &v
	}

	r := Result{Components: comps, Weights: weights, Detail: d}
	r.Index, r.Top = index(comps, weights)
	r.Summary = summarize(r)
	return r, true
}

// majorMiss reports whether any axis missed by a margin the forecast's
// confidence should be held to.
func majorMiss(comps map[string]*float64, d Detail) bool {
	at := func(name string, min float64) bool {
		v := comps[name]
		return v != nil && *v >= min
	}
	return at(CompUnexpectedAreas, majorAreaShare) || at(CompMissedAreas, majorAreaShare) ||
		(d.SizeDistance != nil && *d.SizeDistance >= 2) ||
		(d.DurationDistance != nil && *d.DurationDistance >= 2) ||
		at(CompOutcomeMiss, 1) || at(CompTestSurprise, 1)
}

// index is the clipped weighted sum and the top contributor. A missing weight
// is 0; negative weights never reach here (Config refuses them), which is what
// keeps the index monotone in every component.
func index(comps map[string]*float64, weights map[string]float64) (float64, string) {
	sum, best, top := 0.0, 0.0, ""
	for _, name := range Components {
		v := comps[name]
		if v == nil {
			continue
		}
		c := weights[name] * *v
		sum += c
		if c > best {
			best, top = c, name
		}
	}
	return round4(clip01(sum)), top
}

// summarize renders the one sentence a notification and a verify focus hint
// carry. Deterministic: fixed component order, sorted area lists.
func summarize(r Result) string {
	d := r.Detail
	var parts []string
	if r.Top == "" {
		parts = append(parts, "the run went as forecast")
	} else {
		parts = append(parts, "top: "+r.Top)
	}
	if len(d.UnexpectedAreas) > 0 {
		parts = append(parts, "unexpected areas "+listCap(d.UnexpectedAreas))
	}
	if len(d.MissedAreas) > 0 {
		parts = append(parts, "forecast areas not touched "+listCap(d.MissedAreas))
	}
	if d.SizeDistance != nil && *d.SizeDistance > 0 {
		parts = append(parts, fmt.Sprintf("size %s→%s", d.ForecastSize, d.ActualSize))
	}
	if d.DurationDistance != nil && *d.DurationDistance > 0 {
		parts = append(parts, fmt.Sprintf("duration %s→%s", d.ForecastDuration, d.ActualDuration))
	}
	if v := r.Components[CompOutcomeMiss]; v != nil && *v > 0 {
		parts = append(parts, fmt.Sprintf("forecast %s, run %s", d.ForecastOutcome, d.ActualOutcome))
	}
	if v := r.Components[CompTestSurprise]; v != nil && *v > 0 {
		parts = append(parts, fmt.Sprintf("%d unexpected test failure(s)", *d.TestFailuresUnexpected))
	}
	if v := r.Components[CompOverconfidence]; v != nil && *v > 0 {
		parts = append(parts, fmt.Sprintf("forecast confidence %.2f", *d.Confidence))
	}
	return fmt.Sprintf("surprise %.2f — %s", r.Index, strings.Join(parts, "; "))
}

// listCap renders at most three items and says how many more there were.
func listCap(items []string) string {
	const max = 3
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(items[:max], ", "), len(items)-max)
}

// ComputeRevision compares a prior with a posterior (step 13.2). ok is false
// when no axis is comparable.
func ComputeRevision(prior, posterior Forecast) (Revision, bool) {
	rv := Revision{
		PriorOutcome:     strings.TrimSpace(prior.Outcome),
		PosteriorOutcome: strings.TrimSpace(posterior.Outcome),
		AreasAdded:       []string{},
		AreasDropped:     []string{},
	}
	var axes []float64

	pa, qa := normAreas(prior.Areas), normAreas(posterior.Areas)
	if len(pa)+len(qa) > 0 {
		for _, q := range qa {
			if !anyOverlap(q, pa) {
				rv.AreasAdded = append(rv.AreasAdded, q)
			}
		}
		for _, p := range pa {
			if !anyOverlap(p, qa) {
				rv.AreasDropped = append(rv.AreasDropped, p)
			}
		}
		axes = append(axes, share(len(rv.AreasAdded)+len(rv.AreasDropped), len(pa)+len(qa)))
	}
	if s, ok := bandShift(wsingest.ForecastSizeBands, prior.SizeBand, posterior.SizeBand); ok {
		rv.SizeShift = &s
		axes = append(axes, bandNorm(absInt(s)))
	}
	if s, ok := bandShift(wsingest.ForecastDurationBands, prior.DurationBand, posterior.DurationBand); ok {
		rv.DurationShift = &s
		axes = append(axes, bandNorm(absInt(s)))
	}
	if pd, ok1 := forecastDone(prior.Outcome); ok1 {
		if qd, ok2 := forecastDone(posterior.Outcome); ok2 {
			axes = append(axes, boolNum(pd != qd))
		}
	}
	if prior.Confidence != nil && posterior.Confidence != nil {
		v := round4(clip01(*posterior.Confidence) - clip01(*prior.Confidence))
		rv.ConfidenceDelta = &v
	}
	if len(axes) == 0 {
		return Revision{}, false
	}
	sum := 0.0
	for _, v := range axes {
		sum += v
	}
	rv.Index = round4(sum / float64(len(axes)))
	return rv, true
}

// ── bands ──

// DurationBand maps a run's wall-clock seconds onto the forecast format's
// duration vocabulary (wsingest.ForecastDurationBands): <30m | 30-90m | 90m-4h | >4h.
func DurationBand(seconds int64) string {
	switch {
	case seconds < 30*60:
		return "<30m"
	case seconds < 90*60:
		return "30-90m"
	case seconds < 4*60*60:
		return "90m-4h"
	default:
		return ">4h"
	}
}

// bandIndex finds a value in an ordered band vocabulary, case-insensitively and
// ignoring surrounding space. -1 when the value is not a band (the forecast lint
// already tells the author; the scorer just cannot use it).
func bandIndex(bands []string, v string) int {
	v = strings.TrimSpace(v)
	for i, b := range bands {
		if strings.EqualFold(b, v) {
			return i
		}
	}
	return -1
}

func bandDistance(bands []string, forecast, actual string) (int, bool) {
	s, ok := bandShift(bands, forecast, actual)
	return absInt(s), ok
}

// bandShift is to − from in bands.
func bandShift(bands []string, from, to string) (int, bool) {
	i, j := bandIndex(bands, from), bandIndex(bands, to)
	if i < 0 || j < 0 {
		return 0, false
	}
	return j - i, true
}

// bandNorm maps a band distance onto 0..1: 0 → 0, 1 → 0.5, 2+ → 1.
func bandNorm(dist int) float64 {
	switch {
	case dist <= 0:
		return 0
	case dist == 1:
		return 0.5
	default:
		return 1
	}
}

// ── outcomes ──

// forecastDone reads a forecast outcome as "will the phase be done?".
func forecastDone(o string) (done, ok bool) {
	switch strings.ToLower(strings.TrimSpace(o)) {
	case "done":
		return true, true
	case "partial", "blocked":
		return false, true
	}
	return false, false
}

// actualDone reads phasediag's outcome as "did the phase get done?".
func actualDone(o string) (done, ok bool) {
	switch o {
	case "completed":
		return true, true
	case "partial", "noop", "failed":
		return false, true
	}
	return false, false
}

// ── numbers ──

func share(n, of int) float64 {
	if of <= 0 {
		return 0
	}
	return round4(float64(n) / float64(of))
}

func clip01(v float64) float64 {
	switch {
	case math.IsNaN(v) || v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

func boolNum(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func ptr(v float64) *float64 { return &v }

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	out := append([]string(nil), v...)
	sort.Strings(out)
	return out
}
