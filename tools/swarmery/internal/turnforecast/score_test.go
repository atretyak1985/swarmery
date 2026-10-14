package turnforecast

import (
	"encoding/json"
	"strings"
	"testing"
)

func bp(v bool) *bool       { return &v }
func ip(v int) *int         { return &v }
func fp(v float64) *float64 { return &v }
func i64p(v int64) *int64   { return &v }
func val(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func mustCompute(t *testing.T, f Forecast, a Actual) Result {
	t.Helper()
	r, ok := Compute(f, a, nil)
	if !ok {
		t.Fatalf("Compute(%+v, %+v) not measurable", f, a)
	}
	return r
}

// Same inputs twice → the same index and byte-identical vector JSON.
func TestComputeDeterministic(t *testing.T) {
	f := Forecast{TurnSeq: 1, Files: []string{"b.go", "a.go", "c.go"}, TestsPass: bp(true), Steps: ip(4), Confidence: fp(0.8)}
	a := Actual{TurnSeq: 1, Files: []string{"a.go", "d.go", "e.go"}, TestsRan: true, TestsPass: bp(false), Steps: 9}
	r1, r2 := mustCompute(t, f, a), mustCompute(t, f, a)
	if r1.Index != r2.Index || r1.Top != r2.Top {
		t.Fatalf("index/top differ: %v/%s vs %v/%s", r1.Index, r1.Top, r2.Index, r2.Top)
	}
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	if string(b1) != string(b2) {
		t.Fatalf("vector JSON differs:\n%s\n%s", b1, b2)
	}
}

// Every component with known numbers, the weighted index and the top.
func TestComputeComponentsAndIndex(t *testing.T) {
	f := Forecast{Files: []string{"a.go", "b.go"}, TestsPass: bp(true), Steps: ip(4), Confidence: fp(0.8)}
	a := Actual{Files: []string{"a.go", "c.go", "d.go", "e.go"}, TestsRan: true, TestsPass: bp(false), Steps: 8}
	r := mustCompute(t, f, a)
	want := map[string]float64{
		CompUnexpectedFiles: 0.75, // c, d, e of 4
		CompMissedFiles:     0.5,  // b of 2
		CompTestsMiss:       1,
		CompStepsMiss:       0.5, // |4-8|/8
		CompOverconfidence:  0.8, // major miss → the confidence
	}
	for k, w := range want {
		if got := r.Components[k]; got == nil || *got != w {
			t.Errorf("%s = %v, want %v", k, val(got), w)
		}
	}
	// .3*.75 + .2*.5 + .25*1 + .15*.5 + .1*.8 = .225+.1+.25+.075+.08 = .73
	if r.Index != 0.73 {
		t.Errorf("index = %v, want 0.73", r.Index)
	}
	if r.Top != CompTestsMiss || !r.MajorMiss {
		t.Errorf("top = %q major = %v, want tests_miss, true", r.Top, r.MajorMiss)
	}
}

// A perfect forecast scores 0 with no top, and overconfidence is 0 (not nil).
func TestComputePerfectForecast(t *testing.T) {
	f := Forecast{Files: []string{"./src/a.go"}, TestsPass: bp(true), Steps: ip(3), Confidence: fp(0.9)}
	a := Actual{Files: []string{"src//a.go/"}, TestsRan: true, TestsPass: bp(true), Steps: 3}
	r := mustCompute(t, f, a)
	if r.Index != 0 || r.Top != "" || r.MajorMiss {
		t.Errorf("index=%v top=%q major=%v, want 0, \"\", false", r.Index, r.Top, r.MajorMiss)
	}
	for _, k := range Components {
		if v := r.Components[k]; v == nil || *v != 0 {
			t.Errorf("%s = %v, want 0", k, val(v))
		}
	}
}

// nil means "could not tell", never 0.
func TestComputeNilComponents(t *testing.T) {
	cases := []struct {
		name string
		f    Forecast
		a    Actual
		nils []string
	}{
		{"no actual files", Forecast{Files: []string{"a"}, Steps: ip(1)}, Actual{Steps: 1},
			[]string{CompUnexpectedFiles, CompTestsMiss, CompOverconfidence}},
		{"no forecast files", Forecast{Steps: ip(1)}, Actual{Files: []string{"a"}, Steps: 1},
			[]string{CompMissedFiles, CompTestsMiss, CompOverconfidence}},
		{"tests did not run", Forecast{TestsPass: bp(true), Steps: ip(1)}, Actual{TestsPass: bp(true), Steps: 1},
			[]string{CompTestsMiss}},
		{"tests outcome unobservable", Forecast{TestsPass: bp(true), Steps: ip(1)}, Actual{TestsRan: true, Steps: 1},
			[]string{CompTestsMiss}},
		{"no tests claim", Forecast{Steps: ip(1)}, Actual{TestsRan: true, TestsPass: bp(false), Steps: 1},
			[]string{CompTestsMiss}},
		{"no steps claim", Forecast{Files: []string{"a"}}, Actual{Files: []string{"a"}, Steps: 5},
			[]string{CompStepsMiss}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := mustCompute(t, c.f, c.a)
			for _, k := range c.nils {
				if r.Components[k] != nil {
					t.Errorf("%s = %v, want nil", k, *r.Components[k])
				}
			}
			if _, ok := r.Components[CompStepsMiss]; !ok {
				t.Error("vector must list every component, nil ones included")
			}
		})
	}
}

// Nothing measurable → no score at all.
func TestComputeNothingMeasurable(t *testing.T) {
	if _, ok := Compute(Forecast{Confidence: fp(0.9)}, Actual{Steps: 3}, nil); ok {
		t.Error("Compute with no measurable axis returned ok")
	}
}

// Overconfidence fires only on a major miss (any component ≥ 0.5).
func TestComputeOverconfidence(t *testing.T) {
	minor := mustCompute(t, Forecast{Steps: ip(10), Confidence: fp(0.9)}, Actual{Steps: 6}) // .4
	if v := minor.Components[CompOverconfidence]; v == nil || *v != 0 || minor.MajorMiss {
		t.Errorf("minor miss overconfidence = %v major=%v, want 0 false", val(v), minor.MajorMiss)
	}
	major := mustCompute(t, Forecast{Steps: ip(10), Confidence: fp(0.9)}, Actual{Steps: 5}) // .5
	if v := major.Components[CompOverconfidence]; v == nil || *v != 0.9 || !major.MajorMiss {
		t.Errorf("major miss overconfidence = %v major=%v, want 0.9 true", val(v), major.MajorMiss)
	}
}

// Steps: denominator floors at 1, so 0 vs 0 is 0 and 0 vs 2 is 1.
func TestComputeStepsMiss(t *testing.T) {
	if r := mustCompute(t, Forecast{Steps: ip(0)}, Actual{Steps: 0}); *r.Components[CompStepsMiss] != 0 {
		t.Errorf("0 vs 0 = %v", *r.Components[CompStepsMiss])
	}
	if r := mustCompute(t, Forecast{Steps: ip(0)}, Actual{Steps: 2}); *r.Components[CompStepsMiss] != 1 {
		t.Errorf("0 vs 2 = %v", *r.Components[CompStepsMiss])
	}
	if r := mustCompute(t, Forecast{Steps: ip(3)}, Actual{Steps: 0}); *r.Components[CompStepsMiss] != 1 {
		t.Errorf("3 vs 0 = %v", *r.Components[CompStepsMiss])
	}
}

// Custom weights drive the index; the index clips at 1.
func TestComputeWeightsAndClip(t *testing.T) {
	f := Forecast{Files: []string{"a"}, Steps: ip(1)}
	a := Actual{Files: []string{"b"}, Steps: 1}
	r, ok := Compute(f, a, map[string]float64{CompUnexpectedFiles: 1, CompMissedFiles: 1})
	if !ok || r.Index != 1 || r.Top != CompUnexpectedFiles {
		t.Errorf("index=%v top=%q ok=%v, want clipped 1 with unexpected_files on the tie", r.Index, r.Top, ok)
	}
	r, _ = Compute(f, a, map[string]float64{CompMissedFiles: 0.4})
	if r.Index != 0.4 || r.Top != CompMissedFiles {
		t.Errorf("index=%v top=%q, want 0.4 missed_files", r.Index, r.Top)
	}
	sum := 0.0
	for _, w := range DefaultWeights() {
		sum += w
	}
	if sum < 0.9999 || sum > 1.0001 {
		t.Errorf("default weights sum to %v, want 1", sum)
	}
}

func TestNormPath(t *testing.T) {
	for in, want := range map[string]string{
		"./a/b.go": "a/b.go", "a//b.go": "a/b.go", "a/b/": "a/b", " ./x ": "x",
		"": "", ".": "", "./": "", "/abs//p/": "/abs/p", "././a": "a",
	} {
		if got := NormPath(in); got != want {
			t.Errorf("NormPath(%q) = %q, want %q", in, got, want)
		}
	}
	// Duplicates after normalisation count once.
	r := mustCompute(t, Forecast{Files: []string{"a", "./a", "a/"}}, Actual{Files: []string{"a"}, Steps: 0})
	if *r.Components[CompMissedFiles] != 0 || *r.Components[CompUnexpectedFiles] != 0 {
		t.Errorf("normalised duplicates scored a miss: %+v", r.Components)
	}
}

func TestParseForecast(t *testing.T) {
	f, err := ParseForecast([]byte(`{"runId":"r1","turnSeq":3,"files":["a.go"],"testsPass":null,"steps":4,"confidence":0.7,"model":"haiku","forecastTokens":210,"extra":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.RunID != "r1" || f.TurnSeq != 3 || len(f.Files) != 1 || f.TestsPass != nil || *f.Steps != 4 || *f.Confidence != 0.7 || f.Model != "haiku" || *f.ForecastTokens != 210 {
		t.Errorf("parsed %+v", f)
	}
	if f, err := ParseForecast([]byte(`{"runId":"r1","turnSeq":1}`)); err != nil || f.Files != nil || f.Steps != nil {
		t.Errorf("minimal forecast = %+v, %v", f, err)
	}
	legal := "1760436000000-a.b:c_D-9"
	legal += strings.Repeat("z", MaxRunIDLen-len(legal))
	if _, err := ParseForecast([]byte(`{"runId":"` + legal + `","turnSeq":1}`)); err != nil {
		t.Errorf("a %d-char runId of the legal alphabet was refused: %v", MaxRunIDLen, err)
	}
	long := strings.Repeat("x", MaxPathLen+1)
	many := `[` + strings.TrimSuffix(strings.Repeat(`"a",`, MaxForecastFiles+1), ",") + `]`
	for name, raw := range map[string]string{
		"absent":        ``,
		"null":          `null`,
		"array":         `[1]`,
		"no turnSeq":    `{"runId":"r1","files":[]}`,
		"turnSeq 0":     `{"runId":"r1","turnSeq":0}`,
		"turnSeq float": `{"runId":"r1","turnSeq":1.5}`,
		"files string":  `{"runId":"r1","turnSeq":1,"files":"a"}`,
		"too many":      `{"runId":"r1","turnSeq":1,"files":` + many + `}`,
		"long path":     `{"runId":"r1","turnSeq":1,"files":["` + long + `"]}`,
		"steps <0":      `{"runId":"r1","turnSeq":1,"steps":-1}`,
		"conf >1":       `{"runId":"r1","turnSeq":1,"confidence":1.2}`,
		"conf <0":       `{"runId":"r1","turnSeq":1,"confidence":-0.1}`,
		"model long":    `{"runId":"r1","turnSeq":1,"model":"` + strings.Repeat("m", MaxModelLen+1) + `"}`,
		"tokens <0":     `{"runId":"r1","turnSeq":1,"forecastTokens":-5}`,
		"testsPass str": `{"runId":"r1","turnSeq":1,"testsPass":"yes"}`,
		"no runId":      `{"turnSeq":1}`,
		"runId empty":   `{"runId":"","turnSeq":1}`,
		"runId spaces":  `{"runId":"a b","turnSeq":1}`,
		"runId slash":   `{"runId":"a/b","turnSeq":1}`,
		"runId number":  `{"runId":12,"turnSeq":1}`,
		"runId long":    `{"runId":"` + strings.Repeat("r", MaxRunIDLen+1) + `","turnSeq":1}`,
	} {
		if _, err := ParseForecast([]byte(raw)); err == nil {
			t.Errorf("%s: ParseForecast(%s) accepted", name, raw)
		}
	}
}

func TestParseActual(t *testing.T) {
	a, err := ParseActual([]byte(`{"runId":"r1","turnSeq":2,"files":["x"],"testsRan":true,"testsPass":false,"steps":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.RunID != "r1" || a.TurnSeq != 2 || len(a.Files) != 1 || !a.TestsRan || a.TestsPass == nil || *a.TestsPass || a.Steps != 7 {
		t.Errorf("parsed %+v", a)
	}
	for name, raw := range map[string]string{
		"no testsRan": `{"runId":"r1","turnSeq":1,"steps":1}`,
		"no steps":    `{"runId":"r1","turnSeq":1,"testsRan":false}`,
		"steps <0":    `{"runId":"r1","turnSeq":1,"testsRan":false,"steps":-1}`,
		"no turnSeq":  `{"runId":"r1","testsRan":false,"steps":1}`,
		"bad json":    `{"runId":"r1","turnSeq":`,
		"no runId":    `{"turnSeq":1,"testsRan":false,"steps":1}`,
		"runId bad":   `{"runId":"x;y","turnSeq":1,"testsRan":false,"steps":1}`,
	} {
		if _, err := ParseActual([]byte(raw)); err == nil {
			t.Errorf("%s: ParseActual(%s) accepted", name, raw)
		}
	}
}
