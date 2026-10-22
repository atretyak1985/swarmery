package guards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The embedded bytes and the file on disk are the same document — go:embed
// guarantees it at build time; this pins that Defaults() decodes it faithfully.
func TestDefaultsEqualConfigFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "guards.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fromDisk Config
	if err := json.Unmarshal(raw, &fromDisk); err != nil {
		t.Fatal(err)
	}
	d, err := Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if !reflect.DeepEqual(d, fromDisk) {
		t.Fatalf("Defaults() = %+v\nconfig/guards.json = %+v", d, fromDisk)
	}
	// The documented D35 values, so a silent edit of the shipped file shows up here.
	if d.StallMinutes != 20 || d.PlaybookOverrides["long-tests"] != 60 ||
		d.CacheColdMinutes != (CacheCold{Subscription: 60, API: 5}) ||
		d.SkillSimilarityThreshold != 0.8 || d.SessionSkillLoadP95 != 12 || len(d.WorkaroundWords) == 0 {
		t.Errorf("defaults drifted from the plan: %+v", d)
	}
}

func TestLoadWithoutOverrideIsDefaults(t *testing.T) {
	t.Setenv(EnvOverride, "")
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	d, _ := Defaults()
	if !reflect.DeepEqual(got, d) {
		t.Fatalf("Load() = %+v, want the defaults", got)
	}
}

// fullOverride differs from the defaults in EVERY key, so a key the loader
// silently took from the defaults would show up as an equal field.
const fullOverride = `{
  "workaroundWords": ["kludge"],
  "stallMinutes": 7,
  "playbookOverrides": {"slow-e2e": 90},
  "cacheColdMinutes": {"subscription": 30, "api": 3},
  "skillSimilarityThreshold": 0.65,
  "sessionSkillLoadP95": 20
}`

func writeOverride(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "guards.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOverrideReplacesEveryKey(t *testing.T) {
	t.Setenv(EnvOverride, writeOverride(t, fullOverride))
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		WorkaroundWords:          []string{"kludge"},
		StallMinutes:             7,
		PlaybookOverrides:        map[string]int{"slow-e2e": 90},
		CacheColdMinutes:         CacheCold{Subscription: 30, API: 3},
		SkillSimilarityThreshold: 0.65,
		SessionSkillLoadP95:      20,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
	// Every field — walked by reflection so a new key is covered without
	// editing this test — differs from its default.
	d, _ := Defaults()
	gv, dv := reflect.ValueOf(got), reflect.ValueOf(d)
	for i := 0; i < gv.NumField(); i++ {
		if reflect.DeepEqual(gv.Field(i).Interface(), dv.Field(i).Interface()) {
			t.Errorf("field %s equals its default — the override did not replace it", gv.Type().Field(i).Name)
		}
	}
	if got.StallMinutesFor("slow-e2e") != 90 || got.StallMinutesFor("long-tests") != 7 {
		t.Errorf("StallMinutesFor: the override's playbook map must replace the default one")
	}
}

func TestPartialOverrideRejectedNamingTheKey(t *testing.T) {
	for _, key := range requiredKeys {
		t.Run(key, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal([]byte(fullOverride), &doc); err != nil {
				t.Fatal(err)
			}
			delete(doc, key)
			b, _ := json.Marshal(doc)
			t.Setenv(EnvOverride, writeOverride(t, string(b)))
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "missing key "+key) {
				t.Fatalf("Load() error = %v, want one naming the missing key %q", err, key)
			}
		})
	}
	t.Run("cacheColdMinutes.api", func(t *testing.T) {
		body := strings.Replace(fullOverride, `"subscription": 30, "api": 3`, `"subscription": 30`, 1)
		t.Setenv(EnvOverride, writeOverride(t, body))
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "missing key cacheColdMinutes.api") {
			t.Fatalf("Load() error = %v, want the nested key named", err)
		}
	})
}

func TestOverrideErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key":       strings.Replace(fullOverride, `"stallMinutes": 7`, `"stallMinutes": 7, "stalMinutes": 8`, 1),
		"null value":        strings.Replace(fullOverride, `"stallMinutes": 7`, `"stallMinutes": null`, 1),
		"out of range":      strings.Replace(fullOverride, `0.65`, `1.5`, 1),
		"zero stall":        strings.Replace(fullOverride, `"stallMinutes": 7`, `"stallMinutes": 0`, 1),
		"blank word":        strings.Replace(fullOverride, `["kludge"]`, `[" "]`, 1),
		"not json":          "{",
		"wrong value type":  strings.Replace(fullOverride, `"stallMinutes": 7`, `"stallMinutes": "7"`, 1),
		"bad playbook":      strings.Replace(fullOverride, `"slow-e2e": 90`, `"slow-e2e": -1`, 1),
		"cache not object":  strings.Replace(fullOverride, `{"subscription": 30, "api": 3}`, `5`, 1),
		"empty word list":   strings.Replace(fullOverride, `["kludge"]`, `[]`, 1),
		"zero cache minute": strings.Replace(fullOverride, `"api": 3`, `"api": 0`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(EnvOverride, writeOverride(t, body))
			if c, err := Load(); err == nil {
				t.Fatalf("Load() = %+v, nil; want an error", c)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		t.Setenv(EnvOverride, filepath.Join(t.TempDir(), "nope.json"))
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvOverride) {
			t.Fatalf("Load() error = %v, want one naming %s", err, EnvOverride)
		}
	})
}
