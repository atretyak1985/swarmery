package runsettings

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// decode is a JSON fixture as the settings readers see it: objects are
// map[string]any all the way down.
func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("fixture %s: %v", s, err)
	}
	return v
}

const sentinel = "zzq-redundant-sentinel-value"

func TestRedundant(t *testing.T) {
	estate := decode(t, `{
		"a@m": {"options": {"x": "`+sentinel+`-a", "n": 1}},
		"b@m": {"options": {"y": "`+sentinel+`-b"}},
		"c@m": {}
	}`)

	cases := []struct {
		name        string
		key         string
		estate      any
		file        any
		want        bool
		notInEstate []string
	}{
		{
			name: "strict subset, identical values, key order differs",
			key:  "pluginConfigs", estate: estate,
			file: decode(t, `{"a@m": {"options": {"n": 1, "x": "`+sentinel+`-a"}}}`),
			want: true, notInEstate: []string{},
		},
		{
			name: "whole-value equality is also redundant",
			key:  "extraKnownMarketplaces", estate: estate, file: estate,
			want: true, notInEstate: []string{},
		},
		{
			name: "one extra entry the estate lacks",
			key:  "pluginConfigs", estate: estate,
			file: decode(t, `{"a@m": {"options": {"x": "`+sentinel+`-a", "n": 1}}, "z@m": {"k": "`+sentinel+`-z"}}`),
			want: false, notInEstate: []string{"z@m"},
		},
		{
			name: "one entry whose value differs",
			key:  "pluginConfigs", estate: estate,
			file: decode(t, `{"b@m": {"options": {"y": "`+sentinel+`-other"}}, "c@m": {}}`),
			want: false, notInEstate: []string{"b@m"},
		},
		{name: "enabledPlugins is never redundant", key: "enabledPlugins", estate: estate, file: estate},
		{name: "permissions is never redundant", key: "permissions", estate: estate, file: estate},
		{name: "swarmery is never redundant", key: "swarmery", estate: estate, file: estate},
		{name: "enabledMcpjsonServers is never redundant", key: "enabledMcpjsonServers", estate: estate, file: estate},
		{name: "non-object file value", key: "pluginConfigs", estate: estate, file: decode(t, `["a@m"]`)},
		{name: "non-object estate value", key: "pluginConfigs", estate: decode(t, `"`+sentinel+`"`), file: estate},
		{name: "nil estate", key: "pluginConfigs", estate: nil, file: estate},
		{
			name: "empty file object", key: "pluginConfigs", estate: estate, file: decode(t, `{}`),
			want: false, notInEstate: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, names := Redundant(tc.key, tc.estate, tc.file)
			if got != tc.want {
				t.Errorf("Redundant = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(names, tc.notInEstate) {
				t.Errorf("notInEstate = %#v, want %#v", names, tc.notInEstate)
			}
			// (v) no fixture VALUE in anything returned.
			if out := fmt.Sprint(got, names); strings.Contains(out, sentinel) {
				t.Errorf("a fixture value reached the output: %s", out)
			}
		})
	}
}

// TestRedundantKeysComeFromEstateKeys: every EstateKey can be redundant and the
// rule reads the constant, not a copy of it.
func TestRedundantKeysComeFromEstateKeys(t *testing.T) {
	v := decode(t, `{"a": 1}`)
	for _, k := range EstateKeys {
		if ok, _ := Redundant(k, v, v); !ok {
			t.Errorf("Redundant(%q) over identical objects = false", k)
		}
	}
	// A value json cannot marshal is never equal to anything.
	if sameCanonical(func() {}, func() {}) {
		t.Error("sameCanonical matched two unmarshallable values")
	}
}
