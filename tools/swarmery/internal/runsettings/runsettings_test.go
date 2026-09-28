package runsettings

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// isolate points the store at a fresh temp dir and returns the settings dir.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv(runDirEnv, t.TempDir())
	return Dir()
}

// captureLog routes the package's log lines into a buffer and forgets which
// lines were already printed, so each test sees its own "once".
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	logMu.Lock()
	logSeen = map[string]bool{}
	logMu.Unlock()
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })
	return &buf
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readComposed(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read composed %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("composed file is not JSON: %v", err)
	}
	return m
}

func storeEntries(t *testing.T, dir string) int {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}

// estate builds an admitted estate at a temp root whose settings.json holds v,
// and returns the Resolution a spawn site would carry.
func estate(t *testing.T, v any) claudeacct.Resolution {
	t.Helper()
	root := t.TempDir()
	f := filepath.Join(root, ".claude", "settings.json")
	writeJSON(t, f, v)
	return claudeacct.Resolution{Estate: "acme", EstateRoot: root, SettingsFile: f, EstateAdmitted: true}
}

// the estate file every happy-path test starts from: the three keys plus the
// keys that must never travel.
func fullEstate() map[string]any {
	return map[string]any{
		"pluginConfigs":          map[string]any{"ops-pack@mkt": map[string]any{"kubeconfig_path": "/k"}},
		"enabledPlugins":         map[string]any{"ops-pack@mkt": true, "core@swarmery": true},
		"extraKnownMarketplaces": map[string]any{"mkt": map[string]any{"source": map[string]any{"source": "directory", "path": "/m"}}},
		"permissions":            map[string]any{"deny": []any{"Bash(rm:*)"}},
		"enabledMcpjsonServers":  []any{"playwright-test"},
		"swarmery":               map[string]any{"claudeAccount": "work", "estate": "acme"},
	}
}

func TestComposeNoEstateReturnsFallbackVerbatim(t *testing.T) {
	dir := isolate(t)
	buf := captureLog(t)
	for _, fb := range []string{"", "/some/lent/settings.json", "  padded  ", `{"inline":true}`} {
		if got := Compose("dispatch", claudeacct.Resolution{}, Inputs{Fallback: fb}); got != fb {
			t.Errorf("no estate, Fallback %q: got %q", fb, got)
		}
	}
	if n := storeEntries(t, dir); n != 0 {
		t.Errorf("store has %d entries, want none", n)
	}
	if buf.Len() != 0 {
		t.Errorf("logged %q, want nothing", buf.String())
	}
}

func TestComposeUnanchoredEstateReturnsFallback(t *testing.T) {
	dir := isolate(t)
	buf := captureLog(t)
	res := estate(t, fullEstate())
	res.EstateAdmitted = false
	if got := Compose("dispatch", res, Inputs{Fallback: "/lent.json"}); got != "/lent.json" {
		t.Errorf("unadmitted estate: got %q, want the Fallback", got)
	}
	if n := storeEntries(t, dir); n != 0 || buf.Len() != 0 {
		t.Errorf("unadmitted estate wrote %d files and logged %q; want nothing", n, buf.String())
	}
}

func TestComposeMissingEstateSettingsReturnsFallback(t *testing.T) {
	isolate(t)
	buf := captureLog(t)
	res := claudeacct.Resolution{Estate: "acme", EstateRoot: t.TempDir(), EstateAdmitted: true}
	if got := Compose("verify", res, Inputs{}); got != "" {
		t.Errorf("credentials-only estate: got %q, want the empty Fallback", got)
	}
	if buf.Len() != 0 {
		t.Errorf("a missing estate settings file logged %q", buf.String())
	}
}

func TestComposeCarriesExactlyEstateKeys(t *testing.T) {
	isolate(t)
	captureLog(t)
	in := fullEstate()
	res := estate(t, in)
	// Project and worktree files with other values change nothing: the composer
	// reads none of them.
	for _, d := range []string{t.TempDir(), t.TempDir()} {
		writeJSON(t, filepath.Join(d, ".claude", "settings.json"), map[string]any{"pluginConfigs": map[string]any{"x": map[string]any{"y": "z"}}})
		writeJSON(t, filepath.Join(d, ".claude", "settings.local.json"), map[string]any{"enabledPlugins": map[string]any{"q@r": true}})
	}
	p1 := Compose("dispatch", res, Inputs{})
	p2 := Compose("planning", res, Inputs{})
	if p1 == "" || p1 != p2 {
		t.Fatalf("two composes under one estate: %q and %q, want one shared path", p1, p2)
	}
	got := readComposed(t, p1)
	want := map[string]any{}
	for _, k := range EstateKeys {
		want[k] = in[k]
	}
	// Round-trip the input through JSON so both sides carry the same types.
	b, _ := json.Marshal(want)
	var wantRT map[string]any
	_ = json.Unmarshal(b, &wantRT)
	if !reflect.DeepEqual(got, wantRT) {
		t.Errorf("composed = %v\nwant     = %v", got, wantRT)
	}
}

func TestComposeDropsEveryOtherKeyByName(t *testing.T) {
	isolate(t)
	buf := captureLog(t)
	const sentinel = "SENTINEL-VALUE-7f3a"
	dropped := []string{"env", "permissions", "enabledMcpjsonServers", "hooks", "statusLine",
		"apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper",
		"disableAllHooks", "model", "defaultMode", "futureKey"}
	in := map[string]any{"pluginConfigs": map[string]any{"p@m": map[string]any{"k": "v"}},
		"swarmery": map[string]any{"estate": sentinel}}
	for _, k := range dropped {
		in[k] = sentinel
	}
	p := Compose("dispatch", estate(t, in), Inputs{})
	got := readComposed(t, p)
	for _, k := range append(append([]string{}, dropped...), "swarmery") {
		if _, ok := got[k]; ok {
			t.Errorf("composed file carries %q", k)
		}
	}
	line := buf.String()
	for _, k := range dropped {
		if !strings.Contains(line, k) {
			t.Errorf("log line does not name dropped key %q: %s", k, line)
		}
	}
	if strings.Contains(line, "dropped=") && strings.Contains(line[strings.Index(line, "dropped="):], "swarmery") {
		t.Errorf("swarmery must be dropped silently: %s", line)
	}
	if strings.Contains(line, sentinel) {
		t.Errorf("a settings VALUE reached the log: %s", line)
	}
}

func TestComposeFailureIsLogged(t *testing.T) {
	type mk func(t *testing.T, f string)
	writeRaw := func(body string, mode os.FileMode) mk {
		return func(t *testing.T, f string) {
			if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(f, mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows := []struct {
		name   string
		make   mk
		reason string
	}{
		{"malformed JSON", writeRaw(`{"pluginConfigs":`, 0o600), claudeacct.TrustMalformed},
		{"top-level array", writeRaw(`[1,2]`, 0o600), claudeacct.TrustMalformed},
		{"pluginConfigs as a string", writeRaw(`{"pluginConfigs":"SENTINEL"}`, 0o600), "wrong-type:pluginConfigs"},
		{"enabledPlugins as a string", writeRaw(`{"enabledPlugins":"SENTINEL"}`, 0o600), "wrong-type:enabledPlugins"},
		{"extraKnownMarketplaces as a string", writeRaw(`{"extraKnownMarketplaces":"SENTINEL"}`, 0o600), "wrong-type:extraKnownMarketplaces"},
		{"more than 1 MiB", writeRaw(`{"x":"`+strings.Repeat("a", 1<<20)+`"}`, 0o600), claudeacct.TrustTooLarge},
		{"a directory", func(t *testing.T, f string) {
			if err := os.MkdirAll(f, 0o700); err != nil {
				t.Fatal(err)
			}
		}, claudeacct.TrustNotRegular},
		{"group-writable", writeRaw(`{}`, 0o620), claudeacct.TrustGroupWritable},
		{"other-writable", writeRaw(`{}`, 0o602), claudeacct.TrustOtherWritable},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			dir := isolate(t)
			buf := captureLog(t)
			root := t.TempDir()
			f := filepath.Join(root, ".claude", "settings.json")
			row.make(t, f)
			res := claudeacct.Resolution{Estate: "acme", EstateRoot: root, SettingsFile: f, EstateAdmitted: true}

			if got := Compose("dispatch", res, Inputs{Fallback: "/lent.json"}); got != "/lent.json" {
				t.Errorf("got %q, want the Fallback verbatim", got)
			}
			if n := storeEntries(t, dir); n != 0 {
				t.Errorf("an unusable file wrote %d composed files", n)
			}
			line := buf.String()
			if strings.Count(line, "\n") != 1 || !strings.Contains(line, "("+row.reason+")") || !strings.Contains(line, f) {
				t.Errorf("want one WARN naming %q and the path, got %q", row.reason, line)
			}
			if strings.Contains(line, "SENTINEL") {
				t.Errorf("file contents reached the log: %q", line)
			}
			// Same mtime: silent. After a touch: reported again.
			Compose("dispatch", res, Inputs{})
			if strings.Count(buf.String(), "\n") != 1 {
				t.Errorf("a second compose at the same mtime logged again: %q", buf.String())
			}
			later := time.Now().Add(time.Minute)
			if err := os.Chtimes(f, later, later); err != nil {
				t.Fatal(err)
			}
			Compose("dispatch", res, Inputs{})
			if strings.Count(buf.String(), "\n") != 2 {
				t.Errorf("after a touch the WARN should repeat once: %q", buf.String())
			}
			if _, reason := ComposeQuiet(res, Inputs{}); reason != row.reason {
				t.Errorf("ComposeQuiet reason = %q, want %q", reason, row.reason)
			}
		})
	}
}

func TestComposeDropsEscapingSymlink(t *testing.T) {
	good := map[string]any{"pluginConfigs": map[string]any{"p@m": map[string]any{"k": "v"}}}

	t.Run("a .claude linked out of the root", func(t *testing.T) {
		isolate(t)
		buf := captureLog(t)
		root, account := t.TempDir(), t.TempDir()
		writeJSON(t, filepath.Join(account, "settings.json"), good) // a fake account dir
		if err := os.Symlink(account, filepath.Join(root, ".claude")); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(root, ".claude", "settings.json")
		res := claudeacct.Resolution{EstateRoot: root, SettingsFile: f, EstateAdmitted: true}
		if got := Compose("dispatch", res, Inputs{}); got != "" {
			t.Errorf("escaping estate composed %q, want the Fallback", got)
		}
		if !strings.Contains(buf.String(), "(outside-root)") || !strings.Contains(buf.String(), f) {
			t.Errorf("want an outside-root WARN naming the path, got %q", buf.String())
		}
	})

	t.Run("a .claude directory linked inside the root (EstateRoot = project root)", func(t *testing.T) {
		isolate(t)
		captureLog(t)
		root := t.TempDir()
		writeJSON(t, filepath.Join(root, "general", "agents", "settings.json"), good)
		if err := os.Symlink(filepath.Join(root, "general", "agents"), filepath.Join(root, ".claude")); err != nil {
			t.Fatal(err)
		}
		res := claudeacct.Resolution{EstateRoot: root, SettingsFile: filepath.Join(root, ".claude", "settings.json"), EstateAdmitted: true}
		p := Compose("dispatch", res, Inputs{})
		if p == "" {
			t.Fatal("an in-root .claude link must compose")
		}
		if got := readComposed(t, p); got["pluginConfigs"] == nil {
			t.Errorf("composed %v, want pluginConfigs", got)
		}
	})

	t.Run("a settings.json that is itself a link, even inside the root", func(t *testing.T) {
		isolate(t)
		buf := captureLog(t)
		root := t.TempDir()
		real := filepath.Join(root, "real.json")
		writeJSON(t, real, good)
		f := filepath.Join(root, ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, f); err != nil {
			t.Fatal(err)
		}
		res := claudeacct.Resolution{EstateRoot: root, SettingsFile: f, EstateAdmitted: true}
		if got, reason := ComposeQuiet(res, Inputs{}); got != "" || reason != claudeacct.TrustSymlink {
			t.Errorf("linked settings.json: (%q, %q), want (\"\", %q)", got, reason, claudeacct.TrustSymlink)
		}
		if buf.Len() != 0 {
			t.Errorf("ComposeQuiet logged %q", buf.String())
		}
	})
}

func TestComposeFallbackParityLentFileVerbatim(t *testing.T) {
	isolate(t)
	buf := captureLog(t)
	res := estate(t, fullEstate())
	lent := filepath.Join(t.TempDir(), ".claude", "settings.json")
	lentBody := map[string]any{
		"hooks":                 map[string]any{"Stop": []any{"x"}},
		"permissions":           map[string]any{"allow": []any{"Read"}},
		"env":                   map[string]any{"A": "1"},
		"statusLine":            map[string]any{"type": "command"},
		"enabledMcpjsonServers": []any{"s"},
		"enabledPlugins":        map[string]any{"lent@mkt": true},
	}
	writeJSON(t, lent, lentBody)

	p := Compose("planrun", res, Inputs{Fallback: lent})
	if p == "" || p == lent {
		t.Fatalf("want a composed file, got %q", p)
	}
	got := readComposed(t, p)
	b, _ := json.Marshal(lentBody)
	var lentRT map[string]any
	_ = json.Unmarshal(b, &lentRT)
	for k, v := range lentRT {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("lent key %q = %v, want the LENT value %v", k, got[k], v)
		}
	}
	if got["pluginConfigs"] == nil || got["extraKnownMarketplaces"] == nil {
		t.Errorf("the keys the lent file lacks must come from the estate: %v", got)
	}
	if !strings.Contains(buf.String(), "lent=1") {
		t.Errorf("log line should mark the lent compose: %q", buf.String())
	}

	// When the estate adds nothing, Compose returns the Fallback itself.
	full := filepath.Join(t.TempDir(), "settings.json")
	writeJSON(t, full, map[string]any{"pluginConfigs": map[string]any{}, "enabledPlugins": map[string]any{}, "extraKnownMarketplaces": map[string]any{}})
	if got := Compose("phaserun", res, Inputs{Fallback: full}); got != full {
		t.Errorf("estate adds nothing: got %q, want the Fallback itself", got)
	}
	// An unreadable lent file leaves the Fallback unchanged.
	missing := filepath.Join(t.TempDir(), "gone.json")
	if got := Compose("resume", res, Inputs{Fallback: missing}); got != missing {
		t.Errorf("unreadable lent file: got %q, want the Fallback", got)
	}
}

func TestSpliceTerminal(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		f    string
		want []string
	}{
		{"claude", []string{"claude", "-p", "hi"}, "F", []string{"claude", "--settings", "F", "-p", "hi"}},
		{"absolute claude", []string{"/bin/claude", "mcp", "list"}, "F", []string{"/bin/claude", "--settings", "F", "mcp", "list"}},
		{"not claude", []string{"sh", "-c", "x"}, "F", []string{"sh", "-c", "x"}},
		{"no file", []string{"claude"}, "", []string{"claude"}},
		{"caller's own", []string{"claude", "--settings", "mine"}, "F", []string{"claude", "--settings", "mine"}},
		{"caller's own, = form", []string{"claude", "--settings=mine"}, "F", []string{"claude", "--settings=mine"}},
	} {
		if got := SpliceTerminal(tc.argv, tc.f); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Dropped key NAMES come from the estate file, so they are quoted in the log: a
// name carrying a newline cannot forge a second log line.
func TestComposeQuotesDroppedKeyNames(t *testing.T) {
	isolate(t)
	buf := captureLog(t)
	Compose("dispatch", estate(t, map[string]any{
		"pluginConfigs":                    map[string]any{},
		"evil\nrunsettings: engine=forged": 1,
	}), Inputs{})
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("log = %q: %d lines, want exactly one", buf.String(), n)
	}
	if !strings.Contains(buf.String(), `"evil\nrunsettings: engine=forged"`) {
		t.Errorf("the dropped name is not quoted: %q", buf.String())
	}
}
