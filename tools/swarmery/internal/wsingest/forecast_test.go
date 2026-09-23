package wsingest

import (
	"reflect"
	"testing"
)

// priorBlock / posteriorBlock are the format exactly as plan-format.md documents
// it, so a change to the reference example that this parser cannot read fails
// here rather than in a plan nobody reruns.
const priorBlock = "## Forecast\n\n" +
	"```yaml\n" +
	"kind: prior\n" +
	"written_at: 2026-09-23T10:12:00Z\n" +
	"areas: [internal/ingest, internal/cost]\n" +
	"files: [internal/ingest/record.go, config/pricing.json]\n" +
	"size_band: M\n" +
	"duration_band: 30-90m\n" +
	"outcome: done\n" +
	"risks: [\"migration touches turns table\", \"recost path diverges\"]\n" +
	"confidence: 0.7\n" +
	"```\n"

const posteriorBlock = "```yaml\n" +
	"kind: posterior\n" +
	"written_at: 2026-09-23T13:40:00Z\n" +
	"areas: [internal/ingest]\n" +
	"size_band: L\n" +
	"duration_band: 90m-4h\n" +
	"outcome: partial\n" +
	"confidence: 0.4\n" +
	"```\n"

func fl(v float64) *float64 { return &v }

func TestParseForecasts(t *testing.T) {
	t.Run("no forecast section", func(t *testing.T) {
		doc := "# Phase 1\n\n## Acceptance criteria\n- [ ] a\n\n## Completion Report\n"
		if got := ParseForecasts(doc, false); got != nil {
			t.Errorf("ParseForecasts = %+v, want nil for a doc with no section", got)
		}
	})

	t.Run("prior only", func(t *testing.T) {
		doc := "# Phase 1\n\n" + priorBlock + "\n## Completion Report\n"
		got := ParseForecasts(doc, false)
		if len(got) != 1 {
			t.Fatalf("forecasts = %d, want 1", len(got))
		}
		want := Forecast{
			Kind:         "prior",
			WrittenAt:    "2026-09-23T10:12:00Z",
			Areas:        []string{"internal/ingest", "internal/cost"},
			Files:        []string{"internal/ingest/record.go", "config/pricing.json"},
			SizeBand:     "M",
			DurationBand: "30-90m",
			Outcome:      "done",
			Risks:        []string{"migration touches turns table", "recost path diverges"},
			Confidence:   fl(0.7),
		}
		if !reflect.DeepEqual(got[0], want) {
			t.Errorf("forecast =\n%+v\nwant\n%+v", got[0], want)
		}
	})

	t.Run("prior and posterior", func(t *testing.T) {
		doc := "# Phase 1\n\n" + priorBlock + "\n" + posteriorBlock + "\n## Completion Report\n\nshipped it.\n"
		got := ParseForecasts(doc, true)
		if len(got) != 2 {
			t.Fatalf("forecasts = %d, want 2", len(got))
		}
		if got[0].Kind != ForecastPrior || got[1].Kind != ForecastPosterior {
			t.Errorf("kinds = %q, %q — want document order prior, posterior", got[0].Kind, got[1].Kind)
		}
		// The prior lives in a doc that already reports the work done, so it cannot
		// have been a prediction; the posterior is post hoc by definition and says
		// so in Kind, never in this flag.
		if !got[0].PostHoc {
			t.Error("prior in a doc with a filled Completion Report: postHoc = false, want true")
		}
		if got[1].PostHoc {
			t.Error("posterior: postHoc = true, want false — kind already says it")
		}
	})

	// The whole reason this parser exists in the shape it does: a `## Forecast`
	// section quoted INSIDE a fence is someone else's phase (a template in an agent
	// prompt, an example in plan-format.md), and reading it would attribute a
	// forecast to a doc whose author never made one.
	t.Run("a forecast quoted inside a fence is not this doc's", func(t *testing.T) {
		doc := "# Phase 1\n\n## Copy-paste agent prompt\n\n" +
			"````markdown\n" + priorBlock + "````\n\n## Completion Report\n"
		if got := ParseForecasts(doc, false); got != nil {
			t.Errorf("ParseForecasts = %+v, want nil — the block is a quoted example", got)
		}
	})

	// The complement: the doc's OWN section ends at its next real heading, so a
	// yaml block further down the document is not swept into the forecast.
	t.Run("section ends at the next heading", func(t *testing.T) {
		doc := "# Phase 1\n\n" + priorBlock + "\n## Notes\n\n```yaml\nkind: posterior\nareas: [x]\n```\n"
		got := ParseForecasts(doc, false)
		if len(got) != 1 {
			t.Fatalf("forecasts = %d, want 1 — the Notes block is not a forecast", len(got))
		}
	})

	t.Run("tolerant shapes", func(t *testing.T) {
		doc := "# Phase 1\n\n## Forecast\n\n```yaml\n" +
			"kind: Prior\n" + // case
			"areas: internal/ingest\n" + // bare scalar instead of a list
			"size_band: '**M**'\n" + // markdown decoration an author leaves in
			"```\n"
		got := ParseForecasts(doc, false)
		if len(got) != 1 {
			t.Fatalf("forecasts = %d, want 1", len(got))
		}
		if got[0].Kind != ForecastPrior {
			t.Errorf("kind = %q, want %q", got[0].Kind, ForecastPrior)
		}
		if !reflect.DeepEqual(got[0].Areas, []string{"internal/ingest"}) {
			t.Errorf("areas = %+v, want a one-element list", got[0].Areas)
		}
		if got[0].SizeBand != "M" {
			t.Errorf("size_band = %q, want %q", got[0].SizeBand, "M")
		}
	})

	// A bad value costs that value, never the block: decoding straight into
	// *float64 would fail the whole Unmarshal and throw away six good keys.
	t.Run("unreadable confidence costs only the confidence", func(t *testing.T) {
		doc := "# Phase 1\n\n## Forecast\n\n```yaml\nkind: prior\nareas: [a]\nconfidence: high\n```\n"
		got := ParseForecasts(doc, false)
		if len(got) != 1 {
			t.Fatalf("forecasts = %d, want 1", len(got))
		}
		if got[0].Confidence != nil {
			t.Errorf("confidence = %v, want nil", *got[0].Confidence)
		}
		if got[0].Kind != ForecastPrior || len(got[0].Areas) != 1 {
			t.Errorf("the rest of the block was lost: %+v", got[0])
		}
	})

	// A block of prose is still a forecast the author TRIED to write — dropping it
	// would make "broken forecast" and "no forecast" the same observation.
	t.Run("garbage block still yields a forecast to lint", func(t *testing.T) {
		doc := "# Phase 1\n\n## Forecast\n\n```yaml\nthis is not: [yaml: at all\n```\n"
		got := ParseForecasts(doc, false)
		if len(got) != 1 {
			t.Fatalf("forecasts = %d, want 1 empty forecast", len(got))
		}
		if lints := LintForecasts(got); len(lints) == 0 {
			t.Error("a garbage block raised no lint")
		}
	})

	t.Run("a non-yaml fence in the section is skipped", func(t *testing.T) {
		doc := "# Phase 1\n\n## Forecast\n\n```sh\nmake test\n```\n"
		if got := ParseForecasts(doc, false); got != nil {
			t.Errorf("ParseForecasts = %+v, want nil", got)
		}
	})
}

func TestLintForecasts(t *testing.T) {
	good := Forecast{Kind: ForecastPrior, Areas: []string{"a"}, SizeBand: "M",
		DurationBand: "30-90m", Outcome: "done", Confidence: fl(0.7)}

	codes := func(ls []ForecastLint) []string {
		out := []string{}
		for _, l := range ls {
			out = append(out, l.Code)
		}
		return out
	}

	for _, tc := range []struct {
		name string
		in   []Forecast
		want []string
	}{
		{"clean", []Forecast{good}, []string{}},
		{"no forecasts at all", nil, []string{}},
		{
			"typography is not a typo",
			[]Forecast{{Kind: "prior", Areas: []string{"a"}, SizeBand: "xs", DurationBand: "90m–4h"}},
			[]string{},
		},
		{
			"unknown size band",
			[]Forecast{{Kind: ForecastPrior, Areas: []string{"a"}, SizeBand: "HUGE"}},
			[]string{LintUnknownSizeBand},
		},
		{
			"unknown duration band",
			[]Forecast{{Kind: ForecastPrior, Areas: []string{"a"}, DurationBand: "a fortnight"}},
			[]string{LintUnknownDurationBand},
		},
		{
			"unknown outcome",
			[]Forecast{{Kind: ForecastPrior, Areas: []string{"a"}, Outcome: "shipped"}},
			[]string{LintUnknownOutcome},
		},
		{
			"unknown kind",
			[]Forecast{{Kind: "guess", Areas: []string{"a"}}},
			[]string{LintUnknownKind},
		},
		{
			"confidence above 1",
			[]Forecast{{Kind: ForecastPrior, Areas: []string{"a"}, Confidence: fl(3)}},
			[]string{LintBadConfidence},
		},
		{
			"confidence below 0",
			[]Forecast{{Kind: ForecastPrior, Areas: []string{"a"}, Confidence: fl(-0.1)}},
			[]string{LintBadConfidence},
		},
		{
			"confidence at the edges is fine",
			[]Forecast{{Kind: ForecastPrior, Areas: []string{"a"}, Confidence: fl(0)},
				{Kind: ForecastPosterior, Areas: []string{"a"}, Confidence: fl(1)}},
			[]string{},
		},
		{
			"missing areas",
			[]Forecast{{Kind: ForecastPrior}},
			[]string{LintMissingAreas},
		},
		{
			"posterior without a prior",
			[]Forecast{{Kind: ForecastPosterior, Areas: []string{"a"}}},
			[]string{LintPosteriorWithoutPrior},
		},
		{
			"posterior beside its prior is clean",
			[]Forecast{good, {Kind: ForecastPosterior, Areas: []string{"a"}}},
			[]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codes(LintForecasts(tc.in)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("lint codes = %v, want %v", got, tc.want)
			}
		})
	}
}
