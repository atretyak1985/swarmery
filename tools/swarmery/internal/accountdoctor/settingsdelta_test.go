package accountdoctor

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const deltaSentinel = "zzq-delta-sentinel"

// Criterion 10: the pluginConfigs arm, proven on a fixture — dir A carries a
// pluginConfigs object with three keys, dir B none. Exactly one pluginConfigs
// entry, onlyIn A, listing the three KEY names and no value.
func TestSettingsDelta(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	mustWrite(t, filepath.Join(a, "settings.json"), `{
		"pluginConfigs": {
			"one@m": {"options": {"k": "`+deltaSentinel+`-1"}},
			"two@m": {"options": {"k": "`+deltaSentinel+`-2"}},
			"three@m": {"options": {"k": "`+deltaSentinel+`-3"}}
		},
		"enabledPlugins": {"p@m": true},
		"env": {"SHARED": "`+deltaSentinel+`-a", "ONLY_A": "x"}
	}`, 0o644)
	mustWrite(t, filepath.Join(b, "settings.json"), `{
		"enabledPlugins": {"p@m": false},
		"env": {"SHARED": "`+deltaSentinel+`-b"}
	}`, 0o644)

	got := SettingsDeltas([]AccountSettings{{Account: "bee", Path: filepath.Join(b, "settings.json")},
		{Account: "ay", Path: filepath.Join(a, "settings.json")}})

	var pc []SettingsDelta
	for _, d := range got {
		if d.Key == "pluginConfigs" {
			pc = append(pc, d)
		}
	}
	if len(pc) != 1 {
		t.Fatalf("pluginConfigs entries = %+v, want exactly one", pc)
	}
	if pc[0].OnlyIn != "ay" || pc[0].Kind != "only-in" || pc[0].Count != 3 ||
		!reflect.DeepEqual(pc[0].Names, []string{"one@m", "three@m", "two@m"}) {
		t.Errorf("pluginConfigs entry = %+v", pc[0])
	}
	// enabledPlugins differs by value (true vs false), env by value and name.
	want := map[string]bool{"enabledPlugins/differs": false, "env/differs": false, "env/only-in": false}
	for _, d := range got {
		if _, ok := want[d.Key+"/"+d.Kind]; ok {
			want[d.Key+"/"+d.Kind] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("no %s entry in %+v", k, got)
		}
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), deltaSentinel) {
		t.Error("a settings VALUE reached the delta")
	}
}

// The live baseline shape: identical or absent files make no entry, and the
// result is never nil.
func TestSettingsDeltaEmpty(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.json"), `{"enabledPlugins":{"p@m":true}}`, 0o644)
	mustWrite(t, filepath.Join(root, "b.json"), `{"enabledPlugins":{"p@m":true}}`, 0o644)
	got := SettingsDeltas([]AccountSettings{{"a", filepath.Join(root, "a.json")}, {"b", filepath.Join(root, "b.json")},
		{"c", filepath.Join(root, "missing.json")}})
	for _, d := range got {
		if strings.Join(d.Between, "|") == "a|b" {
			t.Errorf("identical files produced %+v", d)
		}
	}
	if got := SettingsDeltas(nil); got == nil || len(got) != 0 {
		t.Errorf("SettingsDeltas(nil) = %#v, want []", got)
	}
}
