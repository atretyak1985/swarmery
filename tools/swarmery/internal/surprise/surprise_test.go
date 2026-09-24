package surprise

import (
	"math"
	"strings"
	"testing"
)

func fptr(v float64) *float64 { return &v }
func sptr(v string) *string   { return &v }
func i64(v int64) *int64      { return &v }
func iptr(v int) *int         { return &v }

// asForecast is a forecast the matching actual below fulfils on every axis.
func asForecast() Forecast {
	return Forecast{
		Kind: "posterior", Areas: []string{"internal/store", "internal/api"},
		SizeBand: "M", DurationBand: "30-90m", Outcome: "done", Confidence: fptr(0.8),
	}
}

func asActual() Actual {
	return Actual{
		Files:     []string{"internal/store/a.go", "internal/api/b.go"},
		Areas:     []string{"internal/api", "internal/store"},
		AreaDepth: 2, SizeBand: sptr("M"), DurationS: i64(45 * 60),
		Outcome: "completed", TestFailuresUnexpected: iptr(0),
	}
}

func comp(t *testing.T, r Result, name string) float64 {
	t.Helper()
	v := r.Components[name]
	if v == nil {
		t.Fatalf("component %s is unmeasured, want a value", name)
	}
	return *v
}

func TestARunThatWentAsForecastScoresZero(t *testing.T) {
	r, ok := Compute(asForecast(), asActual(), DefaultWeights())
	if !ok {
		t.Fatal("a fully measured run was not scored")
	}
	if r.Index != 0 || r.Top != "" {
		t.Errorf("index %v top %q, want 0 and no top component", r.Index, r.Top)
	}
	for _, c := range Components {
		if got := comp(t, r, c); got != 0 {
			t.Errorf("%s = %v, want 0", c, got)
		}
	}
	if !strings.Contains(r.Summary, "went as forecast") {
		t.Errorf("summary %q does not say the run went as forecast", r.Summary)
	}
}

// One fixture per component: move exactly that axis and nothing else.
func TestEachComponentFiresOnItsOwnAxis(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Forecast, *Actual)
		want   float64
		detail func(t *testing.T, r Result)
	}{
		{CompUnexpectedAreas, func(_ *Forecast, a *Actual) {
			a.Files = append(a.Files, "web/src/x.ts")
			a.Areas = append(a.Areas, "web/src")
		}, 1.0 / 3.0, func(t *testing.T, r Result) {
			if len(r.Detail.UnexpectedAreas) != 1 || r.Detail.UnexpectedAreas[0] != "web/src" {
				t.Errorf("unexpected areas = %v, want [web/src]", r.Detail.UnexpectedAreas)
			}
		}},
		{CompMissedAreas, func(f *Forecast, _ *Actual) {
			f.Areas = append(f.Areas, "internal/cost")
		}, 1.0 / 3.0, func(t *testing.T, r Result) {
			if len(r.Detail.MissedAreas) != 1 || r.Detail.MissedAreas[0] != "internal/cost" {
				t.Errorf("missed areas = %v, want [internal/cost]", r.Detail.MissedAreas)
			}
		}},
		{CompSizeMiss, func(_ *Forecast, a *Actual) { a.SizeBand = sptr("L") }, 0.5, nil},
		{CompDurationMiss, func(_ *Forecast, a *Actual) { a.DurationS = i64(5 * 3600) }, 1, nil},
		{CompOutcomeMiss, func(_ *Forecast, a *Actual) { a.Outcome = "partial" }, 1, nil},
		{CompTestSurprise, func(_ *Forecast, a *Actual) { a.TestFailuresUnexpected = iptr(2) }, 1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, a := asForecast(), asActual()
			f.Confidence = nil // keep overconfidence out of the picture
			tc.mutate(&f, &a)
			r, ok := Compute(f, a, DefaultWeights())
			if !ok {
				t.Fatal("not scored")
			}
			if got := comp(t, r, tc.name); math.Abs(got-round4(tc.want)) > 1e-9 {
				t.Errorf("%s = %v, want %v", tc.name, got, round4(tc.want))
			}
			for _, other := range Components {
				if other == tc.name || other == CompOverconfidence {
					continue
				}
				if v := r.Components[other]; v != nil && *v != 0 {
					t.Errorf("moving %s also moved %s to %v", tc.name, other, *v)
				}
			}
			if r.Top != tc.name {
				t.Errorf("top component = %q, want %q", r.Top, tc.name)
			}
			if r.Index <= 0 {
				t.Errorf("index %v, want > 0", r.Index)
			}
			if tc.detail != nil {
				tc.detail(t, r)
			}
		})
	}
}

// overconfidence is the forecast's confidence when a MAJOR miss happened, and 0
// when every miss was minor.
func TestOverconfidenceIsConfidenceTimesAMajorMiss(t *testing.T) {
	f, a := asForecast(), asActual()
	a.SizeBand = sptr("L") // one band: a minor miss
	r, _ := Compute(f, a, DefaultWeights())
	if got := comp(t, r, CompOverconfidence); got != 0 {
		t.Errorf("overconfidence on a minor miss = %v, want 0", got)
	}

	a.Outcome = "failed" // a major miss
	r, _ = Compute(f, a, DefaultWeights())
	if got := comp(t, r, CompOverconfidence); got != 0.8 {
		t.Errorf("overconfidence = %v, want the forecast's 0.8", got)
	}
	if !r.Detail.MajorMiss {
		t.Error("an outcome miss did not register as major")
	}

	f.Confidence = nil
	r, _ = Compute(f, a, DefaultWeights())
	if r.Components[CompOverconfidence] != nil {
		t.Error("overconfidence measured without a confidence — want unmeasured (nil), not 0")
	}
}

// The reverse outcome miss: the forecast expected to stop short, and the run
// completed.
func TestOutcomeMissInReverse(t *testing.T) {
	f, a := asForecast(), asActual()
	f.Outcome = "blocked"
	r, _ := Compute(f, a, DefaultWeights())
	if got := comp(t, r, CompOutcomeMiss); got != 1 {
		t.Errorf("forecast blocked, run completed: outcome_miss = %v, want 1", got)
	}
	f.Outcome = "partial"
	a.Outcome = "noop"
	r, _ = Compute(f, a, DefaultWeights())
	if got := comp(t, r, CompOutcomeMiss); got != 0 {
		t.Errorf("forecast partial, run noop: outcome_miss = %v, want 0 (both not done)", got)
	}
	a.Outcome = "running"
	r, _ = Compute(f, a, DefaultWeights())
	if r.Components[CompOutcomeMiss] != nil {
		t.Error("a running outcome was scored; want unmeasured")
	}
}

func TestSizeBandDistanceSaturatesAtTwo(t *testing.T) {
	f, a := asForecast(), asActual()
	for band, want := range map[string]float64{"M": 0, "S": 0.5, "L": 0.5, "XS": 1, "XL": 1} {
		a.SizeBand = sptr(band)
		r, _ := Compute(f, a, DefaultWeights())
		if got := comp(t, r, CompSizeMiss); got != want {
			t.Errorf("M vs %s: size_miss = %v, want %v", band, got, want)
		}
	}
	f.SizeBand = "ENORMOUS" // linted, unusable
	r, _ := Compute(f, a, DefaultWeights())
	if r.Components[CompSizeMiss] != nil {
		t.Error("an unknown forecast band was scored; want unmeasured")
	}
}

func TestDurationBands(t *testing.T) {
	for s, want := range map[int64]string{0: "<30m", 29 * 60: "<30m", 30 * 60: "30-90m", 90 * 60: "90m-4h", 4 * 3600: ">4h"} {
		if got := DurationBand(s); got != want {
			t.Errorf("DurationBand(%d) = %q, want %q", s, got, want)
		}
	}
}

// Index monotonicity: with non-negative weights, raising any one component —
// others fixed — never lowers the index.
func TestIndexIsMonotoneInEveryComponent(t *testing.T) {
	w := DefaultWeights()
	steps := []float64{0, 0.25, 0.5, 0.75, 1}
	bases := []float64{0, 0.3, 1}
	for _, target := range Components {
		for _, base := range bases {
			prev := -1.0
			for _, v := range steps {
				comps := map[string]*float64{}
				for _, c := range Components {
					comps[c] = fptr(base)
				}
				comps[target] = fptr(v)
				got, _ := index(comps, w)
				if got < prev {
					t.Fatalf("%s: index fell from %v to %v as it rose to %v (others at %v)", target, prev, got, v, base)
				}
				prev = got
			}
		}
	}
	// Everything at 1 saturates at exactly 1 with the default weights.
	all := map[string]*float64{}
	for _, c := range Components {
		all[c] = fptr(1)
	}
	if got, _ := index(all, w); got != 1 {
		t.Errorf("all components at 1: index %v, want 1", got)
	}
	// And the index is clipped when custom weights overshoot.
	heavy := map[string]float64{CompOutcomeMiss: 5}
	if got, _ := index(all, heavy); got != 1 {
		t.Errorf("overweighted index = %v, want clipped to 1", got)
	}
}

// Monotone through Compute as well: a wider size miss never scores lower.
func TestIndexMonotoneThroughCompute(t *testing.T) {
	f, a := asForecast(), asActual()
	f.SizeBand = "XS"
	prev := -1.0
	for _, band := range []string{"XS", "S", "M", "L", "XL"} {
		a.SizeBand = sptr(band)
		r, _ := Compute(f, a, DefaultWeights())
		if r.Index < prev {
			t.Fatalf("size %s: index fell from %v to %v", band, prev, r.Index)
		}
		prev = r.Index
	}
}

// Nothing measurable ⇒ no score at all, never a zero.
func TestNothingMeasurableIsNoScore(t *testing.T) {
	f := Forecast{Kind: "prior", Areas: []string{"x"}, SizeBand: "?", Outcome: "maybe"}
	a := Actual{Outcome: "running"} // no diff, no size, no duration, no tests
	if _, ok := Compute(f, a, DefaultWeights()); ok {
		t.Error("a run with nothing measurable was scored")
	}
}

// A forecast with no areas cannot say which areas were unexpected: both area
// components are unmeasured rather than "everything was unexpected".
func TestForecastWithoutAreasLeavesAreaComponentsUnmeasured(t *testing.T) {
	f, a := asForecast(), asActual()
	f.Areas = nil
	r, _ := Compute(f, a, DefaultWeights())
	if r.Components[CompUnexpectedAreas] != nil || r.Components[CompMissedAreas] != nil {
		t.Error("area components scored against a forecast that names no areas")
	}
}

func TestSummaryNamesTheMisses(t *testing.T) {
	f, a := asForecast(), asActual()
	a.Outcome = "partial"
	a.SizeBand = sptr("XL")
	a.Files = append(a.Files, "web/src/x.ts")
	a.Areas = append(a.Areas, "web/src")
	r, _ := Compute(f, a, DefaultWeights())
	for _, want := range []string{"top: outcome_miss", "unexpected areas web/src", "size M→XL", "forecast done, run partial", "confidence 0.80"} {
		if !strings.Contains(r.Summary, want) {
			t.Errorf("summary %q is missing %q", r.Summary, want)
		}
	}
	h := FocusHint(r)
	for _, want := range []string{"web/src", `"done"`, `"partial"`} {
		if !strings.Contains(h, want) {
			t.Errorf("focus hint %q is missing %q", h, want)
		}
	}
}

func TestRevisionComparesPriorWithPosterior(t *testing.T) {
	prior := Forecast{Kind: "prior", Areas: []string{"internal/store"}, SizeBand: "S",
		DurationBand: "<30m", Outcome: "done", Confidence: fptr(0.9)}
	post := Forecast{Kind: "posterior", Areas: []string{"tools/app/internal/store", "internal/api"},
		SizeBand: "L", DurationBand: "<30m", Outcome: "partial", Confidence: fptr(0.5)}
	rv, ok := ComputeRevision(prior, post)
	if !ok {
		t.Fatal("no revision")
	}
	if len(rv.AreasAdded) != 1 || rv.AreasAdded[0] != "internal/api" || len(rv.AreasDropped) != 0 {
		t.Errorf("areas added %v dropped %v, want [internal/api] and [] (module-relative spelling matches)", rv.AreasAdded, rv.AreasDropped)
	}
	if rv.SizeShift == nil || *rv.SizeShift != 2 || rv.DurationShift == nil || *rv.DurationShift != 0 {
		t.Errorf("shifts size %v duration %v, want +2 and 0", rv.SizeShift, rv.DurationShift)
	}
	if rv.ConfidenceDelta == nil || math.Abs(*rv.ConfidenceDelta+0.4) > 1e-9 {
		t.Errorf("confidence delta %v, want -0.4", rv.ConfidenceDelta)
	}
	// axes: areas 1/3, size 1, duration 0, outcome 1 → mean 0.5833
	if math.Abs(rv.Index-0.5833) > 1e-4 {
		t.Errorf("revision index %v, want 0.5833", rv.Index)
	}
	if _, ok := ComputeRevision(Forecast{}, Forecast{}); ok {
		t.Error("two empty forecasts produced a revision")
	}
}
