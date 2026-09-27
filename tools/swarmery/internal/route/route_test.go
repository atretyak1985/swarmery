package route

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// signedPart is the "(+N)" / "(-N)" tail every reason line carries.
var signedPart = regexp.MustCompile(`\(([+-]\d+)\)$`)

// reasonSum adds up the signed tails of the reasons, failing on a line without one.
func reasonSum(t *testing.T, reasons []string) int {
	t.Helper()
	sum := 0
	for _, r := range reasons {
		m := signedPart.FindStringSubmatch(r)
		if m == nil {
			t.Fatalf("reason %q has no (+N)/(-N) tail", r)
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("reason %q: %v", r, err)
		}
		sum += n
	}
	return sum
}

func hasReason(reasons []string, substr string) bool {
	for _, r := range reasons {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

// TestDecideSignals pins every row of the weights table: the zero Signals
// value scores 0, and each case moves exactly one signal.
func TestDecideSignals(t *testing.T) {
	cases := []struct {
		name   string
		s      Signals
		want   int
		reason string
	}{
		{"zero value", Signals{}, 0, "prompt_bytes=0 (+0)"},

		{"prompt 399", Signals{PromptBytes: 399}, 0, "prompt_bytes=399 (+0)"},
		{"prompt 400", Signals{PromptBytes: 400}, 10, "prompt_bytes=400 (+10)"},
		{"prompt 1499", Signals{PromptBytes: 1499}, 10, "prompt_bytes=1499 (+10)"},
		{"prompt 1500", Signals{PromptBytes: 1500}, 20, "prompt_bytes=1500 (+20)"},
		{"prompt 3999", Signals{PromptBytes: 3999}, 20, "prompt_bytes=3999 (+20)"},
		{"prompt 4000", Signals{PromptBytes: 4000}, 30, "prompt_bytes=4000 (+30)"},

		{"forecast XS", Signals{ForecastSize: "XS"}, 0, "forecast_size=XS (+0)"},
		{"forecast S", Signals{ForecastSize: "S"}, 10, "forecast_size=S (+10)"},
		{"forecast M", Signals{ForecastSize: "M"}, 25, "forecast_size=M (+25)"},
		{"forecast L", Signals{ForecastSize: "L"}, 40, "forecast_size=L (+40)"},
		{"forecast XL", Signals{ForecastSize: "XL"}, 55, "forecast_size=XL (+55)"},
		{"forecast case-insensitive", Signals{ForecastSize: " m "}, 25, "forecast_size=M (+25)"},
		{"forecast replaces prompt", Signals{ForecastSize: "M", PromptBytes: 5000}, 25, "prompt_bytes=5000 ignored: forecast_size set (+0)"},
		{"forecast XS still replaces prompt", Signals{ForecastSize: "XS", PromptBytes: 5000}, 0, "ignored: forecast_size set"},
		{"unknown forecast falls back to prompt", Signals{ForecastSize: "XXL", PromptBytes: 5000}, 30, `forecast_size="XXL" unknown, ignored (+0)`},

		{"file_scope unknown", Signals{FileScope: -1}, 10, "file_scope=-1 unknown (+10)"},
		{"file_scope 0", Signals{FileScope: 0}, 0, "file_scope=0 (+0)"},
		{"file_scope 1", Signals{FileScope: 1}, 0, "file_scope=1 (+0)"},
		{"file_scope 2", Signals{FileScope: 2}, 10, "file_scope=2 (+10)"},
		{"file_scope 5", Signals{FileScope: 5}, 10, "file_scope=5 (+10)"},
		{"file_scope 6", Signals{FileScope: 6}, 20, "file_scope=6 (+20)"},
		{"file_scope 15", Signals{FileScope: 15}, 20, "file_scope=15 (+20)"},
		{"file_scope 16", Signals{FileScope: 16}, 30, "file_scope=16 (+30)"},

		{"areas unknown", Signals{Areas: -1}, 0, "areas=-1 unknown (+0)"},
		{"areas 1", Signals{Areas: 1}, 0, "areas=1 (+0)"},
		{"areas 2", Signals{Areas: 2}, 10, "areas=2 (+10)"},
		{"areas 3", Signals{Areas: 3}, 10, "areas=3 (+10)"},
		{"areas 4", Signals{Areas: 4}, 20, "areas=4 (+20)"},

		{"risk paths", Signals{RiskPaths: []string{"db/migrations/0001.sql", "go.sum"}}, 15, "risk_paths=db/migrations/0001.sql,go.sum (+15)"},
		{"no risk paths", Signals{}, 0, "risk_paths=none (+0)"},

		{"deps", Signals{Deps: 2}, 10, "deps=2 (+10)"},

		{"history at threshold", Signals{HistFailRate: 0.3, HistSamples: 5}, 15, "history: fail_rate=0.3 ≥ 0.3 (n=5) (+15)"},
		{"history below rate", Signals{HistFailRate: 0.29, HistSamples: 10}, 0, "history: fail_rate=0.29 < 0.3 (n=10) (+0)"},
		{"history too few samples", Signals{HistFailRate: 0.9, HistSamples: 4}, 0, "history: n<5, ignored (n=4) (+0)"},
	}
	p := DefaultPolicy()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(c.s, p)
			if d.Score != c.want {
				t.Errorf("score = %d, want %d; reasons %q", d.Score, c.want, d.Reasons)
			}
			if !hasReason(d.Reasons, c.reason) {
				t.Errorf("no reason containing %q in %q", c.reason, d.Reasons)
			}
			if got := reasonSum(t, d.Reasons); got != d.Score {
				t.Errorf("reasons sum to %d, score is %d: %q", got, d.Score, d.Reasons)
			}
		})
	}
}

// TestDecideTierBoundaries drives the score to each side of every cut-off
// through Decide itself: all other signals score 0, and the deps weight is set
// to the score wanted.
func TestDecideTierBoundaries(t *testing.T) {
	cases := []struct {
		score    int
		tier     string
		model    string
		effort   string
		playbook string
	}{
		{0, TierS, "haiku", "low", PlaybookStandard},
		{19, TierS, "haiku", "low", PlaybookStandard},
		{20, TierM, "sonnet", "medium", PlaybookStandard},
		{44, TierM, "sonnet", "medium", PlaybookStandard},
		{45, TierL, "opus", "high", PlaybookPlanFirst},
		{69, TierL, "opus", "high", PlaybookPlanFirst},
		{70, TierXL, "opus", "xhigh", PlaybookPlanFirst},
		{100, TierXL, "opus", "xhigh", PlaybookPlanFirst},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.score), func(t *testing.T) {
			p := DefaultPolicy()
			p.Weights.Deps = c.score
			d := Decide(Signals{Surface: SurfaceDispatch, Deps: 1}, p)
			want := Decision{Score: c.score, Tier: c.tier, Model: c.model, Effort: c.effort, Playbook: c.playbook, Reasons: d.Reasons}
			if !reflect.DeepEqual(d, want) {
				t.Errorf("Decide = %+v, want %+v", d, want)
			}
		})
	}
}

func TestDecideClamp(t *testing.T) {
	s := Signals{
		ForecastSize: "XL", FileScope: 16, Areas: 4, RiskPaths: []string{"auth/"},
		Deps: 1, HistFailRate: 1, HistSamples: 20,
	}
	d := Decide(s, DefaultPolicy())
	if d.Score != 100 || d.Tier != TierXL {
		t.Fatalf("score/tier = %d/%s, want 100/XL", d.Score, d.Tier)
	}
	if !hasReason(d.Reasons, "clamp: 145 → 100 (-45)") {
		t.Errorf("no clamp reason in %q", d.Reasons)
	}
	if got := reasonSum(t, d.Reasons); got != d.Score {
		t.Errorf("reasons sum to %d, score is %d", got, d.Score)
	}

	// A hand-built policy with a negative weight cannot drive the score below 0.
	p := DefaultPolicy()
	p.Weights.Deps = -10
	d = Decide(Signals{Deps: 1}, p)
	if d.Score != 0 || d.Tier != TierS {
		t.Fatalf("score/tier = %d/%s, want 0/S", d.Score, d.Tier)
	}
	if got := reasonSum(t, d.Reasons); got != 0 {
		t.Errorf("reasons sum to %d, want 0: %q", got, d.Reasons)
	}
}

func TestDecideNeverReviewHeavy(t *testing.T) {
	// A hand-built policy that names review-heavy everywhere still never yields it.
	p := DefaultPolicy()
	for _, k := range []*Pick{&p.Tiers.S, &p.Tiers.M, &p.Tiers.L, &p.Tiers.XL} {
		k.Playbook = "Review-Heavy"
	}
	for _, score := range []int{0, 20, 45, 70} {
		q := p
		q.Weights.Deps = score
		d := Decide(Signals{Surface: SurfaceDispatch, Deps: 1}, q)
		if d.Playbook != PlaybookPlanFirst {
			t.Errorf("score %d: playbook = %q, want %q", score, d.Playbook, PlaybookPlanFirst)
		}
	}

	// And the default policy only ever yields the two auto-selectable recipes.
	def := DefaultPolicy()
	for prompt := 0; prompt <= 6000; prompt += 250 {
		for _, fs := range []int{-1, 0, 3, 10, 20} {
			d := Decide(Signals{Surface: SurfaceDispatch, PromptBytes: prompt, FileScope: fs, Deps: fs & 1}, def)
			if d.Playbook != PlaybookStandard && d.Playbook != PlaybookPlanFirst {
				t.Fatalf("prompt=%d fs=%d: playbook %q", prompt, fs, d.Playbook)
			}
		}
	}
}

func TestDecidePhaseRunHasNoPlaybook(t *testing.T) {
	d := Decide(Signals{Surface: SurfacePhaseRun, ForecastSize: "L", FileScope: 8}, DefaultPolicy())
	if d.Playbook != "" {
		t.Errorf("phaserun playbook = %q, want empty", d.Playbook)
	}
	if d.Score != 60 || d.Tier != TierL || d.Model != "opus" || d.Effort != "high" {
		t.Errorf("Decide = %+v, want score 60 L opus high", d)
	}
}

func TestDecideDeterministic(t *testing.T) {
	s := Signals{Surface: SurfaceDispatch, PromptBytes: 2100, FileScope: 7, Areas: 3,
		RiskPaths: []string{"go.sum"}, Deps: 1, HistFailRate: 0.4, HistSamples: 9}
	p := DefaultPolicy()
	first := Decide(s, p)
	for range 20 {
		if again := Decide(s, p); !reflect.DeepEqual(first, again) {
			t.Fatalf("Decide not deterministic: %+v vs %+v", first, again)
		}
	}
	// 20 + 20 + 10 + 15 + 10 + 15 = 90
	if first.Score != 90 || first.Tier != TierXL {
		t.Errorf("score/tier = %d/%s, want 90/XL", first.Score, first.Tier)
	}
}
