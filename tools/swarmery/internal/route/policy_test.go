package route

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
)

func writePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPolicyDefaults(t *testing.T) {
	for _, path := range []string{"", "   "} {
		p, err := LoadPolicy(path)
		if err != nil {
			t.Fatalf("LoadPolicy(%q): %v", path, err)
		}
		if !reflect.DeepEqual(p, DefaultPolicy()) {
			t.Errorf("LoadPolicy(%q) = %+v, want DefaultPolicy", path, p)
		}
	}

	// The defaults pass their own validation — haiku included — and survive it unchanged.
	p := DefaultPolicy()
	if err := p.normalize(); err != nil {
		t.Fatalf("DefaultPolicy invalid: %v", err)
	}
	if !reflect.DeepEqual(p, DefaultPolicy()) {
		t.Errorf("normalize changed the defaults: %+v", p)
	}

	// An empty object file is the defaults too.
	got, err := LoadPolicy(writePolicy(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, DefaultPolicy()) {
		t.Errorf("empty file = %+v, want DefaultPolicy", got)
	}

	// DefaultPolicy returns a fresh value: mutating one must not leak into the next.
	a := DefaultPolicy()
	a.Weights.PromptBytes.Bands[0].Points = 99
	if DefaultPolicy().Weights.PromptBytes.Bands[0].Points != 0 {
		t.Error("DefaultPolicy shares its bands slice between calls")
	}
}

func TestPolicyPartialOverride(t *testing.T) {
	path := writePolicy(t, `{
		"weights": {"deps": 25, "file_scope": {"bands": [{"max": 3, "points": 5}]}, "forecast_size": {"XL": 60}},
		"cutoffs": {"M": 25},
		"tiers":   {"S": {"model": "Sonnet"}, "XL": {"effort": "MAX"}}
	}`)
	got, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}

	want := DefaultPolicy()
	want.Weights.Deps = 25
	want.Weights.FileScope.Bands = []Band{{Max: 3, Points: 5}} // a list is replaced whole
	want.Weights.ForecastSize.XL = 60                          // sibling sizes keep defaults
	want.Cutoffs.M = 25                                        // L and XL keep defaults
	want.Tiers.S.Model = "sonnet"                              // canonicalized; S keeps effort + playbook
	want.Tiers.XL.Effort = "max"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("partial override:\n got  %+v\n want %+v", got, want)
	}

	// The loaded policy drives Decide: file_scope=0 now earns 5 (the replaced
	// band), so 5 + 19 = 24 is tier S under the raised M cut-off, and S runs on sonnet.
	got.Weights.Deps = 19
	if d := Decide(Signals{Deps: 1}, got); d.Score != 24 || d.Tier != TierS || d.Model != "sonnet" || d.Effort != "low" {
		t.Errorf("Decide on loaded policy = %+v", d)
	}
}

func TestPolicyAcceptsFullModelID(t *testing.T) {
	got, err := LoadPolicy(writePolicy(t, `{"tiers": {"M": {"model": "claude-sonnet-5"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Tiers.M.Model != "claude-sonnet-5" {
		t.Errorf("model = %q", got.Tiers.M.Model)
	}
}

func TestPolicyErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // substring the error must carry
	}{
		{"unknown top-level key", `{"weight": {}}`, `"weight"`},
		{"unknown nested key", `{"weights": {"bogus": 1}}`, `"bogus"`},
		{"unknown tier", `{"tiers": {"XXL": {}}}`, `"XXL"`},
		{"malformed json", `{"weights": `, "route policy"},
		{"wrong type", `{"weights": {"deps": "ten"}}`, "deps"},
		{"trailing data", `{} {}`, "trailing data"},

		{"bad effort", `{"tiers": {"M": {"effort": "extreme"}}}`, `tiers.M: effort "extreme"`},
		{"off effort", `{"tiers": {"L": {"effort": "off"}}}`, `tiers.L: effort "off"`},
		{"bad model", `{"tiers": {"L": {"model": "gpt-9"}}}`, "tiers.L: model"},
		{"empty model", `{"tiers": {"S": {"model": " "}}}`, "tiers.S: model: required"},
		{"review-heavy playbook", `{"tiers": {"XL": {"playbook": "review-heavy"}}}`, "review-heavy is never auto-selected"},
		{"unknown playbook", `{"tiers": {"S": {"playbook": "yolo"}}}`, `tiers.S: playbook "yolo"`},

		{"cutoffs out of order", `{"cutoffs": {"M": 50}}`, "cutoffs: want 0 < M < L < XL"},
		{"cutoff zero", `{"cutoffs": {"M": 0}}`, "cutoffs"},
		{"cutoff above 100", `{"cutoffs": {"XL": 101}}`, "cutoffs"},
		{"bands not ascending", `{"weights": {"areas": {"bands": [{"max": 3, "points": 1}, {"max": 3, "points": 2}]}}}`, "weights.areas: bands must be strictly ascending"},
		{"negative band points", `{"weights": {"prompt_bytes": {"bands": [{"max": 3, "points": -1}]}}}`, "weights.prompt_bytes: bands[0].points"},
		{"negative ladder else", `{"weights": {"file_scope": {"else": -1}}}`, "weights.file_scope: points must be ≥ 0"},
		{"negative size points", `{"weights": {"forecast_size": {"S": -5}}}`, "weights.forecast_size: must be ≥ 0"},
		{"negative deps", `{"weights": {"deps": -1}}`, "weights.deps: must be ≥ 0"},
		{"negative min samples", `{"weights": {"hist_min_samples": -1}}`, "weights.hist_min_samples"},
		{"rate above 1", `{"weights": {"hist_fail_rate_min": 1.5}}`, "weights.hist_fail_rate_min"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadPolicy(writePolicy(t, c.body))
			if err == nil {
				t.Fatalf("LoadPolicy(%s) = nil error, want one containing %q", c.body, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

func TestPolicyBadModelWrapsSentinel(t *testing.T) {
	_, err := LoadPolicy(writePolicy(t, `{"tiers": {"M": {"model": "gpt-9"}}}`))
	if !errors.Is(err, planning.ErrUnknownModel) {
		t.Errorf("err = %v, want it to wrap planning.ErrUnknownModel", err)
	}
}

func TestPolicyMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")
	_, err := LoadPolicy(path)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path", err)
	}
}

func TestModeFromEnv(t *testing.T) {
	const name = "SWARMERY_ROUTE_TEST_MODE"
	cases := []struct {
		raw  string
		set  bool
		want Mode
	}{
		{"", false, ModeShadow},
		{"", true, ModeShadow},
		{"   ", true, ModeShadow},
		{"off", true, ModeOff},
		{"shadow", true, ModeShadow},
		{"active", true, ModeActive},
		{" Active ", true, ModeActive},
		{"OFF", true, ModeOff},
		{"actve", true, ModeShadow}, // a typo neither disables recording nor goes active
		{"on", true, ModeShadow},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			if c.set {
				t.Setenv(name, c.raw)
			} else {
				t.Setenv(name, "")
				os.Unsetenv(name)
			}
			if got := ModeFromEnv(name); got != c.want {
				t.Errorf("ModeFromEnv(%q=%q) = %q, want %q", name, c.raw, got, c.want)
			}
		})
	}
}
