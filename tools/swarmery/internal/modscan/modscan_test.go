package modscan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixturePath is the real §M6 output committed by phase 0.
const fixturePath = "../../testdata/fixtures/mods/validate-json.json"

// fakeClaude returns the absolute path of the fake CLI script and points it
// at the M6 fixture. The real claude is never run by these tests.
func fakeClaude(t *testing.T, mode string) string {
	t.Helper()
	bin, err := filepath.Abs("testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	t.Setenv("FAKE_CLAUDE_FIXTURE", fixture)
	return bin
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makePlugin lays out a plugin root. modules nil → hooks.json without a
// `modules` key; noHooks → no hooks.json at all.
func makePlugin(t *testing.T, root, name, version string, modules []string, noHooks bool) {
	t.Helper()
	manifest := map[string]string{"name": name}
	if version != "" {
		manifest["version"] = version
	}
	b, _ := json.Marshal(manifest)
	writeFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), string(b))
	if noHooks {
		return
	}
	hooks := map[string]any{"hooks": map[string]any{}}
	if modules != nil {
		hooks["modules"] = modules
		for _, m := range modules {
			writeFile(t, filepath.Join(root, "hooks", m), "export default () => {} // "+m)
		}
	}
	hb, _ := json.Marshal(hooks)
	writeFile(t, filepath.Join(root, "hooks", "hooks.json"), string(hb))
}

func dirs(ms []ModDir) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Dir)
	}
	return out
}

func TestDiscoverFindsModsAndSkipsPlainPlugins(t *testing.T) {
	claude := t.TempDir()
	cache := filepath.Join(claude, "plugins", "cache")
	makePlugin(t, filepath.Join(cache, "mkt", "link", "1.0.0"), "link", "1.0.0", []string{"./register.tsx"}, false)
	makePlugin(t, filepath.Join(cache, "mkt", "link", "0.9.0"), "link", "0.9.0", []string{"./register.tsx"}, false)
	makePlugin(t, filepath.Join(cache, "mkt", "plain", "2.0.0"), "plain", "2.0.0", nil, false)
	makePlugin(t, filepath.Join(cache, "mkt", "nohooks", "1.0.0"), "nohooks", "1.0.0", nil, true)
	makePlugin(t, filepath.Join(cache, "mkt", "emptymods", "1.0.0"), "emptymods", "1.0.0", []string{}, false)
	// No name/version in plugin.json → folder names fill in.
	writeFile(t, filepath.Join(cache, "other", "anon", "abc123", ".claude-plugin", "plugin.json"), `{}`)
	writeFile(t, filepath.Join(cache, "other", "anon", "abc123", "hooks", "hooks.json"), `{"modules":["./m.ts", 7]}`)
	// Too deep (plugin root 5 levels down) and inside node_modules: never found.
	makePlugin(t, filepath.Join(cache, "a", "b", "c", "d", "deep"), "deep", "1", []string{"./x.ts"}, false)
	makePlugin(t, filepath.Join(cache, "mkt", "node_modules", "dep"), "dep", "1", []string{"./x.ts"}, false)

	extra := t.TempDir()
	makePlugin(t, filepath.Join(extra, "local-mod"), "local-mod", "", []string{"./a.tsx", "./b.tsx"}, false)
	makePlugin(t, filepath.Join(extra, "local-plain"), "local-plain", "", nil, false)

	got := Discover(claude, []string{
		filepath.Join(extra, "local-mod"),
		filepath.Join(extra, "local-plain"),
		"relative/dir",                               // not absolute → skipped
		filepath.Join(cache, "mkt", "link", "1.0.0"), // already found in the cache → once
	})

	want := []string{
		filepath.Join(cache, "other", "anon", "abc123"),
		filepath.Join(cache, "mkt", "link", "1.0.0"),
		filepath.Join(extra, "local-mod"),
	}
	if !reflect.DeepEqual(dirs(got), want) {
		t.Fatalf("Discover dirs = %v\nwant %v", dirs(got), want)
	}
	anon, link, local := got[0], got[1], got[2]
	if anon.Name != "anon" || anon.Version != "abc123" || anon.PluginID != "anon@other" ||
		!reflect.DeepEqual(anon.Modules, []string{"./m.ts"}) {
		t.Errorf("anon = %+v", anon)
	}
	if link.Name != "link" || link.Version != "1.0.0" || link.PluginID != "link@mkt" || link.Source != SourceCache {
		t.Errorf("link = %+v", link)
	}
	if local.Source != SourcePluginDir || local.PluginID != "" || len(local.Modules) != 2 {
		t.Errorf("local = %+v", local)
	}
}

func TestDiscoverPrefersInUseVersion(t *testing.T) {
	claude := t.TempDir()
	cache := filepath.Join(claude, "plugins", "cache")
	makePlugin(t, filepath.Join(cache, "mkt", "link", "1.0.0"), "link", "1.0.0", []string{"./r.tsx"}, false)
	makePlugin(t, filepath.Join(cache, "mkt", "link", "0.9.0"), "link", "0.9.0", []string{"./r.tsx"}, false)
	if err := os.MkdirAll(filepath.Join(cache, "mkt", "link", "0.9.0", ".in_use"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := Discover(claude, nil)
	if len(got) != 1 || got[0].Version != "0.9.0" {
		t.Fatalf("Discover = %+v, want only the .in_use version 0.9.0", got)
	}
}

func TestDiscoverNoCache(t *testing.T) {
	if got := Discover(t.TempDir(), nil); len(got) != 0 {
		t.Fatalf("Discover on an empty claude dir = %v", got)
	}
}

func TestPluginDirsFromSettings(t *testing.T) {
	claude := t.TempDir()
	if got := PluginDirsFromSettings(claude); got != nil {
		t.Errorf("missing settings → %v, want nil", got)
	}
	writeFile(t, filepath.Join(claude, "settings.json"), `{not json`)
	if got := PluginDirsFromSettings(claude); got != nil {
		t.Errorf("malformed settings → %v, want nil", got)
	}
	writeFile(t, filepath.Join(claude, "settings.json"),
		`{"env":{"CLAUDE_CODE_PLUGIN_DIRS":" /a/one : /b/two ::"}}`)
	if got := PluginDirsFromSettings(claude); !reflect.DeepEqual(got, []string{"/a/one", "/b/two"}) {
		t.Errorf("PluginDirsFromSettings = %v", got)
	}
}

func TestDiscoverExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	makePlugin(t, filepath.Join(home, "mods", "m"), "m", "", []string{"./r.tsx"}, false)
	got := Discover(t.TempDir(), []string{"~/mods/m"})
	if len(got) != 1 || got[0].Dir != filepath.Join(home, "mods", "m") {
		t.Fatalf("Discover(~/mods/m) = %+v", got)
	}
}

func TestValidateParsesFixture(t *testing.T) {
	bin := fakeClaude(t, "ok")
	rep, err := Validate(context.Background(), bin, "/some/plugin")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !rep.Validated || !rep.Success {
		t.Errorf("validated=%v success=%v, want true/true", rep.Validated, rep.Success)
	}
	wantEvents := []string{"session.start", "session.measure", "prompt.submit", "turn.complete", "tool.call",
		"tool.call{tool=AskUserQuestion}", "tool.check", "ui.render{component=AbovePrompt}", "session.end"}
	if !reflect.DeepEqual(rep.Events, wantEvents) {
		t.Errorf("Events = %v\nwant %v", rep.Events, wantEvents)
	}
	wantUses := []string{"clock.now", "env.get", "fs.read", "http.fetch", "session.cwd", "session.id",
		"session.root", "session.usage", "session.version", "ui.log", "ui.resolve"}
	if !reflect.DeepEqual(rep.Uses, wantUses) {
		t.Errorf("Uses = %v\nwant %v", rep.Uses, wantUses)
	}
	if !reflect.DeepEqual(rep.Env, EnvAccess{Reads: []string{"HOME"}, Writes: []string{}}) {
		t.Errorf("Env = %+v, want reads [HOME], writes []", rep.Env)
	}
	if len(rep.State) != 0 {
		t.Errorf("State = %v, want empty (the fixture has no state line)", rep.State)
	}
	if len(rep.GatingHooks) != 4 || rep.GatingHooks[3] != (GatingHook{Module: "./register.tsx", Pattern: "tool.check", Hook: "tool.check", HasCatch: true}) {
		t.Errorf("GatingHooks = %+v", rep.GatingHooks)
	}
	if len(rep.Errors) != 0 || len(rep.Warnings) != 0 {
		t.Errorf("errors=%v warnings=%v, want none", rep.Errors, rep.Warnings)
	}
	if !json.Valid(rep.Raw) || !strings.Contains(string(rep.Raw), `"gatingHooks"`) {
		t.Errorf("Raw is not the validator output: %.80s", rep.Raw)
	}
}

func TestValidateFailedValidationIsAReport(t *testing.T) {
	bin := fakeClaude(t, "fail-json") // success:false and exit 1
	rep, err := Validate(context.Background(), bin, "/p")
	if err != nil {
		t.Fatalf("a parsable report on a non-zero exit must not be an error: %v", err)
	}
	if !rep.Validated || rep.Success {
		t.Errorf("validated=%v success=%v, want true/false", rep.Validated, rep.Success)
	}
}

func assertParseError(t *testing.T, err error, reason string) {
	t.Helper()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v (%T), want *ParseError", err, err)
	}
	if pe.Reason != reason || pe.Dir != "/p" {
		t.Errorf("ParseError = %+v, want reason %q dir /p", pe, reason)
	}
	if !strings.Contains(pe.Error(), reason) || pe.Unwrap() == nil {
		t.Errorf("Error() = %q / Unwrap() = %v", pe.Error(), pe.Unwrap())
	}
}

func TestValidateTimeout(t *testing.T) {
	bin := fakeClaude(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Validate(ctx, bin, "/p")
	assertParseError(t, err, "timeout")
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("timeout path took %s — the kill did not reach the child", d)
	}
}

func TestValidateCanceled(t *testing.T) {
	bin := fakeClaude(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := Validate(ctx, bin, "/p")
	assertParseError(t, err, "canceled")
}

func TestValidateGarbageOutput(t *testing.T) {
	_, err := Validate(context.Background(), fakeClaude(t, "garbage"), "/p")
	assertParseError(t, err, "invalid JSON")
}

func TestValidateExecFailure(t *testing.T) {
	_, err := Validate(context.Background(), fakeClaude(t, "fail"), "/p")
	assertParseError(t, err, "exec failed")
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("exec failure should carry stderr: %v", err)
	}
}

func TestValidateNoBinary(t *testing.T) {
	check := func(t *testing.T, rep Report, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("no binary must not be an error: %v", err)
		}
		if rep.Validated || !reflect.DeepEqual(rep.Errors, []string{MsgNoBinary}) {
			t.Errorf("report = %+v, want errors [%q]", rep, MsgNoBinary)
		}
		badges := Risk(rep)
		if len(badges) != 1 || badges[0].Level != LevelUnknown {
			t.Errorf("Risk = %+v, want one unknown badge", badges)
		}
	}
	t.Run("explicit path missing", func(t *testing.T) {
		rep, err := Validate(context.Background(), filepath.Join(t.TempDir(), "claude"), "/p")
		check(t, rep, err)
	})
	t.Run("resolved override missing", func(t *testing.T) {
		t.Setenv("SWARMERY_CLAUDE_BIN", filepath.Join(t.TempDir(), "claude"))
		rep, err := Validate(context.Background(), "", "/p")
		check(t, rep, err)
	})
}

func TestParseReport(t *testing.T) {
	if _, err := ParseReport([]byte(`[]`)); err == nil {
		t.Error("an array parsed as a report")
	}
	if _, err := ParseReport([]byte(`{"strict":true}`)); err == nil {
		t.Error("a report without success parsed")
	}
	raw := `{"success":false,"manifest":{"errors":["missing author"],"warnings":[{"message":"w1"},{"code":3}],"notes":[]},
	  "contents":[{"errors":[],"warnings":[],"notes":[
	    "./a.tsx hooks: tool.check",
	    "./b.tsx hooks: tool.check, turn.abort",
	    "./a.tsx calls: $.state.get, $.process.spawn, $.fs.write (via helper)",
	    "./a.tsx state keys: counter",
	    "./a.tsx env writes: FOO, BAR",
	    "./a.tsx env reads: nothing",
	    "no colon here",
	    "./a.tsx gating hook without .catch: tool.check"
	  ],"gatingHooks":[]}]}`
	rep, err := ParseReport([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Success || !rep.Validated {
		t.Errorf("success=%v validated=%v", rep.Success, rep.Validated)
	}
	if !reflect.DeepEqual(rep.Errors, []string{"missing author"}) ||
		!reflect.DeepEqual(rep.Warnings, []string{"w1", `{"code":3}`}) {
		t.Errorf("errors=%v warnings=%v", rep.Errors, rep.Warnings)
	}
	if !reflect.DeepEqual(rep.Events, []string{"tool.check", "turn.abort"}) {
		t.Errorf("Events = %v (dedup across modules)", rep.Events)
	}
	if !reflect.DeepEqual(rep.Uses, []string{"state.get", "process.spawn", "fs.write"}) {
		t.Errorf("Uses = %v", rep.Uses)
	}
	if !reflect.DeepEqual(rep.State, []string{"counter", "state.get"}) {
		t.Errorf("State = %v", rep.State)
	}
	if !reflect.DeepEqual(rep.Env, EnvAccess{Reads: []string{}, Writes: []string{"FOO", "BAR"}}) {
		t.Errorf("Env = %+v", rep.Env)
	}
}

func TestRiskTable(t *testing.T) {
	cases := []struct {
		name   string
		events []string
		uses   []string
		want   []string // "level rule label"
	}{
		{"nothing risky", []string{"session.start"}, []string{"ui.log", "clock.now"}, nil},
		{"tool.check event", []string{"tool.check"}, nil, []string{"red tool.check can approve tool calls"}},
		{"prompt.submit event", []string{"prompt.submit"}, nil, []string{"red prompt.submit drives the session"}},
		{"turn.abort call", nil, []string{"turn.abort"}, []string{"red turn.abort drives the session"}},
		{"http.fetch", nil, []string{"http.fetch"}, []string{"red http.fetch network"}},
		{"process.* prefix", nil, []string{"process.spawn", "process.exec"}, []string{"red process.* spawns processes"}},
		{"fs.write", nil, []string{"fs.write"}, []string{"red fs.write writes files"}},
		{"session.append", nil, []string{"session.append"}, []string{"red session.append rewrites what Claude reads"}},
		{"fs.read", nil, []string{"fs.read"}, []string{"yellow fs.read reads files"}},
		{"store.*", nil, []string{"store.get"}, []string{"yellow store.* keeps a store"}},
		{"model.*", nil, []string{"model.complete"}, []string{"yellow model.* calls the model"}},
		{"filtered event", []string{"tool.check{tool=Bash}"}, nil, []string{"red tool.check can approve tool calls"}},
		{"not a prefix match", nil, []string{"fs.readdir", "processes.x"}, nil},
		{"red before yellow", nil, []string{"fs.read", "http.fetch"},
			[]string{"red http.fetch network", "yellow fs.read reads files"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Risk(Report{Validated: true, Events: c.events, Uses: c.uses})
			var flat []string
			for _, b := range got {
				flat = append(flat, b.Level+" "+b.Rule+" "+b.Label)
				if len(b.Matches) == 0 {
					t.Errorf("badge %s has no matches", b.Rule)
				}
			}
			if !reflect.DeepEqual(flat, c.want) {
				t.Errorf("Risk = %v, want %v", flat, c.want)
			}
		})
	}
	if got := Risk(Report{Validated: true}); got == nil || len(got) != 0 {
		t.Errorf("Risk of a clean report = %#v, want an empty non-nil slice", got)
	}
}

func TestRiskOnFixture(t *testing.T) {
	rep, err := Validate(context.Background(), fakeClaude(t, "ok"), "/p")
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, b := range Risk(rep) {
		rules = append(rules, b.Level+":"+b.Rule)
	}
	want := []string{"red:tool.check", "red:prompt.submit", "red:http.fetch", "yellow:fs.read"}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("fixture badges = %v, want %v", rules, want)
	}
}

func TestHash(t *testing.T) {
	root := t.TempDir()
	makePlugin(t, root, "m", "1", []string{"./a.tsx"}, false)
	h1, err := Hash(root)
	if err != nil {
		t.Fatal(err)
	}
	if h2, _ := Hash(root); h2 != h1 {
		t.Error("Hash is not stable on unchanged files")
	}
	writeFile(t, filepath.Join(root, "hooks", "a.tsx"), "changed")
	h3, _ := Hash(root)
	if h3 == h1 {
		t.Error("Hash did not move when a module changed")
	}
	// An unlisted file never moves the hash.
	writeFile(t, filepath.Join(root, "hooks", "unlisted.ts"), "x")
	if h4, _ := Hash(root); h4 != h3 {
		t.Error("Hash moved on an unlisted file")
	}
	// Missing and outside modules hash as markers, not errors.
	writeFile(t, filepath.Join(root, "hooks", "hooks.json"), `{"modules":["./gone.tsx","../../../etc/passwd"]}`)
	if _, err := Hash(root); err != nil {
		t.Errorf("Hash with a missing/outside module: %v", err)
	}
	writeFile(t, filepath.Join(root, "hooks", "hooks.json"), `{broken`)
	if _, err := Hash(root); err == nil {
		t.Error("Hash of a broken hooks.json must fail")
	}
	if _, err := Hash(t.TempDir()); err == nil {
		t.Error("Hash without hooks.json must fail")
	}
}
