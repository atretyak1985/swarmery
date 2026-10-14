// Package turnforecast scores how far one Claude Code turn landed from what a
// cheap model predicted for it (mods-swarmery-link phase 10, the turn-forecast
// loop). The swarmery link mod sends two halves per main-loop turn through
// POST /api/link/events: a `turn.forecast` (files the turn will touch, whether
// tests will pass, how many tool calls, a confidence) and a `turn.actual` (what
// its own tool.call journal saw). This package validates both payloads, stores
// them as one turn_forecasts row (migration 0113) and, once both halves are
// present, computes a SURPRISE VECTOR plus one headline index 0..1.
//
// It mirrors internal/surprise (the phase-level scorer) without importing it:
// the same idea — a mismatch between prediction and outcome is what deserves
// attention — applied to the much smaller unit of one turn.
//
// ADVISORY. Nothing gates on a turn's surprise; Insights renders a calibration
// curve and the top surprises.
//
// DETERMINISTIC, NO LLM. The same forecast and actual always produce the same
// vector and index (score_test pins it). Maps marshal with sorted keys, so the
// stored vector JSON is byte-identical too.
//
// THE VECTOR. Each component is a number in 0..1, or nil when it cannot be
// measured (nil is NOT zero — "we could not tell" is not "it went as predicted"):
//
//	unexpected_files  share of the actual files the forecast did not name (nil when the actual has no files)
//	missed_files      share of the forecast files the turn never touched (nil when the forecast named none)
//	tests_miss        1 when the forecast's testsPass differs from the observed one, 0 when equal
//	                  (nil unless the forecast made a claim AND tests ran AND the outcome was observable)
//	steps_miss        |forecast steps − actual steps| / max(forecast, actual, 1) (nil when the forecast gave none)
//	overconfidence    the forecast's confidence when any other component is ≥ 0.5 (a "major miss"), else 0
//	                  (nil when the forecast gave no confidence)
//
// THE INDEX is the weighted sum of the measurable components, clipped to 0..1
// and rounded to 4 decimals. With the default weights summing to 1, a turn that
// misses on every axis scores 1. Top is the largest weighted contributor, ""
// when the index is 0.
package turnforecast

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
)

// Component names — the keys of the vector and of the weights. Stable: they are
// stored in turn_forecasts.vector.
const (
	CompUnexpectedFiles = "unexpected_files"
	CompMissedFiles     = "missed_files"
	CompTestsMiss       = "tests_miss"
	CompStepsMiss       = "steps_miss"
	CompOverconfidence  = "overconfidence"
)

// Components lists every component in its fixed order — the tie-break when two
// components contribute equally to the index.
var Components = []string{
	CompUnexpectedFiles, CompMissedFiles, CompTestsMiss, CompStepsMiss, CompOverconfidence,
}

// majorMissAt is the component value at and above which a miss is "major" and
// the forecast's confidence is held against it.
const majorMissAt = 0.5

// Payload limits (docs/mods-link-protocol.md, Forecast).
const (
	MaxForecastFiles = 50
	MaxPathLen       = 300
	MaxModelLen      = 80
	MaxRunIDLen      = 64
)

// runIDPattern is the shape a runId must have: 1..MaxRunIDLen characters of
// [A-Za-z0-9._:-]. The mod mints one each time it restarts its turn counter
// (session.start, /clear, an in-process resume), so (session, runId, turnSeq)
// names one turn even when a resumed session counts from 1 again.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// DefaultWeights returns a fresh copy of the default component weights. They
// sum to 1.
func DefaultWeights() map[string]float64 {
	return map[string]float64{
		CompUnexpectedFiles: 0.30,
		CompMissedFiles:     0.20,
		CompTestsMiss:       0.25,
		CompStepsMiss:       0.15,
		CompOverconfidence:  0.10,
	}
}

// Forecast is a validated `turn.forecast` payload. Nil pointers mean "the model
// made no claim".
type Forecast struct {
	RunID          string   `json:"runId"`
	TurnSeq        int      `json:"turnSeq"`
	Files          []string `json:"files"`
	TestsPass      *bool    `json:"testsPass"`
	Steps          *int     `json:"steps"`
	Confidence     *float64 `json:"confidence"`
	Model          string   `json:"model"`
	ForecastTokens *int64   `json:"forecastTokens"`
}

// Actual is a validated `turn.actual` payload.
type Actual struct {
	RunID     string   `json:"runId"`
	TurnSeq   int      `json:"turnSeq"`
	Files     []string `json:"files"`
	TestsRan  bool     `json:"testsRan"`
	TestsPass *bool    `json:"testsPass"` // nil = not observable from the mod
	Steps     int      `json:"steps"`
}

// ErrNoPayload is returned for a turn event that carries no payload object.
var ErrNoPayload = errors.New("payload is required")

// ParseForecast validates a `turn.forecast` payload. Unknown fields are
// ignored (the contract is additive); every known field is type- and
// range-checked.
func ParseForecast(raw []byte) (Forecast, error) {
	var w struct {
		RunID          *string   `json:"runId"`
		TurnSeq        *int      `json:"turnSeq"`
		Files          *[]string `json:"files"`
		TestsPass      *bool     `json:"testsPass"`
		Steps          *int      `json:"steps"`
		Confidence     *float64  `json:"confidence"`
		Model          *string   `json:"model"`
		ForecastTokens *int64    `json:"forecastTokens"`
	}
	if err := decodeObject(raw, &w); err != nil {
		return Forecast{}, err
	}
	f := Forecast{TestsPass: w.TestsPass, Steps: w.Steps, Confidence: w.Confidence, ForecastTokens: w.ForecastTokens}
	var err error
	if f.RunID, err = runID(w.RunID); err != nil {
		return Forecast{}, err
	}
	if f.TurnSeq, err = turnSeq(w.TurnSeq); err != nil {
		return Forecast{}, err
	}
	if w.Files != nil {
		f.Files = *w.Files
	}
	if len(f.Files) > MaxForecastFiles {
		return Forecast{}, fmt.Errorf("files has %d entries, max %d", len(f.Files), MaxForecastFiles)
	}
	for _, p := range f.Files {
		if len(p) > MaxPathLen {
			return Forecast{}, fmt.Errorf("files entry longer than %d chars", MaxPathLen)
		}
	}
	if f.Steps != nil && *f.Steps < 0 {
		return Forecast{}, errors.New("steps must be ≥ 0")
	}
	if c := f.Confidence; c != nil && (math.IsNaN(*c) || *c < 0 || *c > 1) {
		return Forecast{}, errors.New("confidence must be within 0..1")
	}
	if w.Model != nil {
		f.Model = *w.Model
	}
	if len(f.Model) > MaxModelLen {
		return Forecast{}, fmt.Errorf("model longer than %d chars", MaxModelLen)
	}
	if t := f.ForecastTokens; t != nil && *t < 0 {
		return Forecast{}, errors.New("forecastTokens must be ≥ 0")
	}
	return f, nil
}

// ParseActual validates a `turn.actual` payload. runId, turnSeq, testsRan and
// steps are required; testsPass may be null.
func ParseActual(raw []byte) (Actual, error) {
	var w struct {
		RunID     *string   `json:"runId"`
		TurnSeq   *int      `json:"turnSeq"`
		Files     *[]string `json:"files"`
		TestsRan  *bool     `json:"testsRan"`
		TestsPass *bool     `json:"testsPass"`
		Steps     *int      `json:"steps"`
	}
	if err := decodeObject(raw, &w); err != nil {
		return Actual{}, err
	}
	a := Actual{TestsPass: w.TestsPass}
	var err error
	if a.RunID, err = runID(w.RunID); err != nil {
		return Actual{}, err
	}
	if a.TurnSeq, err = turnSeq(w.TurnSeq); err != nil {
		return Actual{}, err
	}
	if w.Files != nil {
		a.Files = *w.Files
	}
	if w.TestsRan == nil {
		return Actual{}, errors.New("testsRan is required")
	}
	a.TestsRan = *w.TestsRan
	if w.Steps == nil {
		return Actual{}, errors.New("steps is required")
	}
	if *w.Steps < 0 {
		return Actual{}, errors.New("steps must be ≥ 0")
	}
	a.Steps = *w.Steps
	return a, nil
}

// decodeObject decodes a JSON object into dst; absent, null or a non-object
// payload is an error.
func decodeObject(raw []byte, dst any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ErrNoPayload
	}
	if raw[0] != '{' {
		return errors.New("payload must be an object")
	}
	return json.Unmarshal(raw, dst)
}

func runID(v *string) (string, error) {
	if v == nil {
		return "", errors.New("runId is required")
	}
	if !runIDPattern.MatchString(*v) {
		return "", fmt.Errorf("runId must be 1..%d characters of [A-Za-z0-9._:-]", MaxRunIDLen)
	}
	return *v, nil
}

func turnSeq(v *int) (int, error) {
	if v == nil {
		return 0, errors.New("turnSeq is required")
	}
	if *v < 1 {
		return 0, errors.New("turnSeq must be ≥ 1")
	}
	return *v, nil
}

// Result is one computed score — what turn_forecasts.vector stores.
type Result struct {
	Index      float64             `json:"index"`
	Top        string              `json:"top"`
	MajorMiss  bool                `json:"majorMiss"`
	Components map[string]*float64 `json:"components"`
	Weights    map[string]float64  `json:"weights"`
}

// Compute scores one turn. ok is false when NOTHING could be measured — then
// there is no score at all, never a zero one. A nil weights map means
// DefaultWeights.
func Compute(f Forecast, a Actual, weights map[string]float64) (Result, bool) {
	if weights == nil {
		weights = DefaultWeights()
	}
	comps := make(map[string]*float64, len(Components))

	fs, as := normSet(f.Files), normSet(a.Files)
	if len(as) > 0 {
		comps[CompUnexpectedFiles] = ptr(share(countMissing(as, fs), len(as)))
	}
	if len(fs) > 0 {
		comps[CompMissedFiles] = ptr(share(countMissing(fs, as), len(fs)))
	}
	if f.TestsPass != nil && a.TestsRan && a.TestsPass != nil {
		comps[CompTestsMiss] = ptr(boolNum(*f.TestsPass != *a.TestsPass))
	}
	if f.Steps != nil {
		denom := max(*f.Steps, a.Steps, 1)
		comps[CompStepsMiss] = ptr(round4(clip01(float64(absInt(*f.Steps-a.Steps)) / float64(denom))))
	}
	if len(comps) == 0 {
		return Result{}, false
	}

	// Overconfidence rides on the others, so it is judged last and only when at
	// least one other axis was measurable (the same rule as internal/surprise).
	major := false
	for _, v := range comps {
		if *v >= majorMissAt {
			major = true
		}
	}
	if f.Confidence != nil {
		v := 0.0
		if major {
			v = round4(clip01(*f.Confidence))
		}
		comps[CompOverconfidence] = &v
	}

	full := make(map[string]*float64, len(Components))
	for _, name := range Components {
		full[name] = comps[name] // absent → explicit null in the vector JSON
	}
	r := Result{Components: full, Weights: weights, MajorMiss: major}
	r.Index, r.Top = index(full, weights)
	return r, true
}

// index is the clipped weighted sum and the top contributor. A missing weight
// is 0.
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

// NormPath brings a path to its comparison form: surrounding space trimmed,
// "./" prefixes, duplicate separators and a trailing slash removed. "" for a
// path that names nothing ("", ".", "./").
func NormPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	return p
}

// normSet is the deduplicated set of normalized paths.
func normSet(paths []string) map[string]bool {
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		if n := NormPath(p); n != "" {
			out[n] = true
		}
	}
	return out
}

// countMissing counts the members of a that b lacks.
func countMissing(a, b map[string]bool) int {
	n := 0
	for k := range a {
		if !b[k] {
			n++
		}
	}
	return n
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
