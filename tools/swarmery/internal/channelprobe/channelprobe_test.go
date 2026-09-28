package channelprobe

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func goldenResult(t *testing.T) Result {
	t.Helper()
	r, err := Parse(readFixture(t, "result-2.1.280.json"))
	if err != nil {
		t.Fatalf("Parse(golden): %v", err)
	}
	return r
}

func TestParseGolden(t *testing.T) {
	r := goldenResult(t)
	if r.Schema != SchemaVersion || r.CLIVersion != "2.1.280" || r.CLIVersionRaw != "2.1.280 (Claude Code)" {
		t.Fatalf("header = %d %q %q", r.Schema, r.CLIVersion, r.CLIVersionRaw)
	}
	if want := time.Date(2026, 9, 23, 8, 40, 49, 0, time.UTC); !r.MeasuredAt.Equal(want) {
		t.Fatalf("MeasuredAt = %v, want %v", r.MeasuredAt, want)
	}
	if got := strings.Join(sortedFacts(r.Facts), ","); got != "G1,G2,G3" {
		t.Fatalf("facts = %s, want G1,G2,G3", got)
	}
	for name, f := range r.Facts {
		if f.Verdict != VerdictPass {
			t.Errorf("%s verdict = %q, want pass", name, f.Verdict)
		}
	}
	if !r.Facts["G1"].Observed["user"] || r.Facts["G1"].Observed["project"] {
		t.Fatalf("G1 observations decoded wrong: %v", r.Facts["G1"].Observed)
	}
}

func TestParseRefusesUntrustedShapes(t *testing.T) {
	cases := map[string]string{
		"not json":       `{`,
		"other schema":   `{"schema":2,"cliVersion":"1","method":"m","facts":{"G1":{"verdict":"pass"}}}`,
		"no facts":       `{"schema":1,"cliVersion":"1","method":"m","facts":{}}`,
		"unknown verdct": `{"schema":1,"cliVersion":"1","method":"m","facts":{"G1":{"verdict":"ok"}}}`,
		"bad time":       `{"schema":1,"measuredAt":"yesterday","facts":{"G1":{"verdict":"pass"}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(body)); err == nil {
				t.Fatalf("Parse(%s) accepted it", body)
			}
		})
	}
}

// The embedded baseline must itself be a valid result, and the golden fixture —
// a real run on the CLI the baseline was confirmed against — must not drift
// from it. If this fails, the baseline and the evidence disagree.
func TestBaselineParsesAndMatchesGolden(t *testing.T) {
	base, err := Baseline()
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if got := strings.Join(sortedFacts(base.Facts), ","); got != "G1,G2,G3" {
		t.Fatalf("baseline facts = %s", got)
	}
	golden := goldenResult(t)
	if d := Compare(base, golden); len(d) != 0 {
		t.Fatalf("golden result drifts from the baseline: %+v", d)
	}
	if u := Unobserved(base, golden); len(u) != 0 {
		t.Fatalf("golden result leaves baseline observations unmade: %v", u)
	}
}

func TestCompareReportsDriftWithVersion(t *testing.T) {
	base, err := Baseline()
	if err != nil {
		t.Fatal(err)
	}
	drifted, err := Parse(readFixture(t, "result-drifted.json"))
	if err != nil {
		t.Fatalf("Parse(drifted): %v", err)
	}
	got := Compare(base, drifted)
	want := []Drift{
		{Fact: "G1", Observation: "project", Want: "false", Got: "true", CLIVersion: "9.9.9"},
		{Fact: "G1", Observation: "project_under_project_local", Want: "false", Got: "true", CLIVersion: "9.9.9"},
		{Fact: "G3", Observation: "flag", Want: "true", Got: "false", CLIVersion: "9.9.9"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Compare =\n  %+v\nwant\n  %+v", got, want)
	}
	// G2's two missing observations are silence, not drift.
	if u := Unobserved(base, drifted); !reflect.DeepEqual(u, []string{"G2.diag_mcp_config_invalid", "G2.warn_missing_env"}) {
		t.Fatalf("Unobserved = %v", u)
	}
}

func TestCompareIgnoresMissingFactsAndUnknownObservations(t *testing.T) {
	base := Result{Facts: map[string]Fact{
		"G1": {Observed: map[string]bool{"user": true}},
		"G9": {Observed: map[string]bool{"x": true}},
	}}
	got := Result{CLIVersion: "1", Facts: map[string]Fact{
		"G1": {Observed: map[string]bool{"user": true, "brand_new": false}},
	}}
	if d := Compare(base, got); len(d) != 0 {
		t.Fatalf("Compare = %+v, want none", d)
	}
	if u := Unobserved(base, got); !reflect.DeepEqual(u, []string{"G9.x"}) {
		t.Fatalf("Unobserved = %v", u)
	}
}

func TestValidateRejectsValues(t *testing.T) {
	t.Run("a value key in the file is refused by Parse", func(t *testing.T) {
		_, err := Parse(readFixture(t, "result-with-value.json"))
		if err == nil || !strings.Contains(err.Error(), `"value"`) {
			t.Fatalf("Parse(result-with-value) err = %v, want a refusal naming the key", err)
		}
		// Never echo the suspected value back.
		if err != nil && strings.Contains(err.Error(), "fixture-not-a-real-credential") {
			t.Fatalf("refusal echoed the value: %v", err)
		}
	})
	for _, key := range []string{"value", "Secret", "TOKEN"} {
		t.Run("observed key "+key, func(t *testing.T) {
			r := goldenResult(t)
			r.Facts["G1"].Observed[key] = true
			if err := Validate(r); err == nil {
				t.Fatalf("Validate accepted an observed key named %q", key)
			}
		})
	}
	t.Run("a long string is refused without being quoted", func(t *testing.T) {
		r := goldenResult(t)
		long := strings.Repeat("x", maxStringBytes+1)
		r.Facts["G2"] = Fact{Verdict: VerdictPass, Note: long}
		err := Validate(r)
		if err == nil {
			t.Fatal("Validate accepted a 201-byte string")
		}
		if strings.Contains(err.Error(), long) {
			t.Fatal("refusal echoed the string")
		}
	})
	t.Run("a long key is refused", func(t *testing.T) {
		r := goldenResult(t)
		r.Facts["G1"].Observed[strings.Repeat("k", maxStringBytes+1)] = true
		if err := Validate(r); err == nil {
			t.Fatal("Validate accepted a 201-byte key")
		}
	})
	t.Run("arrays are walked", func(t *testing.T) {
		if err := validateJSON([]byte(`{"a":[1,{"token":"x"}]}`)); err == nil {
			t.Fatal("validateJSON missed a forbidden key inside an array")
		}
		if err := validateJSON([]byte(`{"a":["ok",2,true,null]}`)); err != nil {
			t.Fatalf("validateJSON refused a clean array: %v", err)
		}
		if err := validateJSON([]byte(`{`)); err == nil {
			t.Fatal("validateJSON accepted broken JSON")
		}
	})
	t.Run("the golden result is clean", func(t *testing.T) {
		if err := Validate(goldenResult(t)); err != nil {
			t.Fatalf("Validate(golden) = %v", err)
		}
	})
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "probes")
	r := goldenResult(t)
	path, err := Save(dir, r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Base(path) != "2.1.280.json" {
		t.Fatalf("Save wrote %s, want 2.1.280.json", path)
	}
	assertMode(t, dir, dirMode)
	assertMode(t, path, fileMode)
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(back, r) {
		t.Fatalf("round trip changed the result:\n got %+v\nwant %+v", back, r)
	}
	// No temp file is left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want 1", len(entries))
	}
}

func TestSaveTightensAnExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "probes")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // defeat the umask
		t.Fatal(err)
	}
	if _, err := Save(dir, goldenResult(t)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertMode(t, dir, dirMode)
}

func TestSaveRefuses(t *testing.T) {
	if _, err := Save("  ", goldenResult(t)); err == nil {
		t.Fatal("Save accepted an empty directory")
	}
	leaky := goldenResult(t)
	leaky.Facts["G1"].Observed["secret"] = true
	dir := filepath.Join(t.TempDir(), "probes")
	if _, err := Save(dir, leaky); err == nil {
		t.Fatal("Save wrote a result Validate rejects")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a refused Save still created %s", dir)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(filepath.Join(blocker, "probes"), goldenResult(t)); err == nil {
		t.Fatal("Save succeeded under a regular file")
	}
}

func TestLoadRefusesLooseModes(t *testing.T) {
	good := func(t *testing.T) (string, string) {
		dir := filepath.Join(t.TempDir(), "probes")
		path, err := Save(dir, goldenResult(t))
		if err != nil {
			t.Fatal(err)
		}
		return dir, path
	}
	t.Run("file readable beyond its owner", func(t *testing.T) {
		_, path := good(t)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "0644") {
			t.Fatalf("Load(0644) err = %v, want a refusal naming the mode", err)
		}
	})
	t.Run("directory traversable beyond its owner", func(t *testing.T) {
		dir, path := good(t)
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "0755") {
			t.Fatalf("Load(dir 0755) err = %v, want a refusal naming the mode", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
			t.Fatal("Load of a missing file succeeded")
		}
	})
	t.Run("a directory is not a result", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Load(dir); err == nil {
			t.Fatal("Load of a directory succeeded")
		}
	})
	t.Run("a private file that does not parse", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("Load of broken JSON succeeded")
		}
	})
}

func TestPathHonoursOverrideThenHome(t *testing.T) {
	t.Setenv(probesDirEnv, " /tmp/somewhere ")
	if got := Path(); got != "/tmp/somewhere" {
		t.Fatalf("Path() with override = %q", got)
	}
	t.Setenv(probesDirEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := Path(), filepath.Join(home, ".swarmery", "probes"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
	t.Setenv("HOME", "")
	if got := Path(); got != "" {
		t.Fatalf("Path() with no home = %q, want \"\"", got)
	}
}

func TestFileName(t *testing.T) {
	cases := map[string]string{
		"2.1.280":        "2.1.280.json",
		" 2.1.280 ":      "2.1.280.json",
		"2.1.280-beta.1": "2.1.280-beta.1.json",
		"":               "unknown.json",
		"../../etc":      "unknown.json",
		"a/b":            "unknown.json",
		"1..2":           "unknown.json",
		".hidden":        "unknown.json",
	}
	for in, want := range cases {
		if got := FileName(in); got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
