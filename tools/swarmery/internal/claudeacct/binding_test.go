package claudeacct

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// foreignSettings is a settings.local.json that belongs to Claude Code, not to
// us: every key here must survive our surgery untouched. The 4-space indent is
// deliberate — a gratuitous reformat is then visible as a byte diff.
const foreignSettings = `{
    "permissions": {
        "allow": [
            "Bash(ls:*)"
        ],
        "deny": []
    },
    "hooks": {
        "Stop": [
            {
                "hooks": [
                    {
                        "type": "command",
                        "command": "/bin/true"
                    }
                ]
            }
        ]
    },
    "enabledPlugins": {
        "core@swarmery": true
    }
}
`

func writeSettingsFile(t *testing.T, project, content string) string {
	t.Helper()
	path := bindingPath(project)
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

func parseJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	return m
}

// ── Binding ──────────────────────────────────────────────────────────────────

// A project that was never bound reads as "" — not as an error, and not as a
// spawn failure.
func TestBindingOnAMissingFileIsEmpty(t *testing.T) {
	if got := Binding(t.TempDir()); got != "" {
		t.Errorf("Binding() = %q, want %q", got, "")
	}
}

// A hand-broken settings file means "default account". A failed spawn here
// would be a worse answer than a quiet one.
func TestBindingOnBrokenJSONIsEmpty(t *testing.T) {
	project := t.TempDir()
	writeSettingsFile(t, project, `{ "swarmery": { "claudeAccount": `)
	if got := Binding(project); got != "" {
		t.Errorf("Binding() = %q, want %q", got, "")
	}
}

func TestBindingReadsTheNamespacedKey(t *testing.T) {
	project := t.TempDir()
	writeSettingsFile(t, project, `{"permissions":{},"swarmery":{"claudeAccount":"nabu-org"}}`)
	if got, want := Binding(project), "nabu-org"; got != want {
		t.Errorf("Binding() = %q, want %q", got, want)
	}
}

// A hand-edited value that is not a safe key never reaches a path join.
func TestBindingRejectsAnUnsafeValue(t *testing.T) {
	for _, val := range []string{"../../etc", "a/b", ".hidden", "  "} {
		project := t.TempDir()
		writeSettingsFile(t, project, `{"swarmery":{"claudeAccount":`+jsonQuote(val)+`}}`)
		if got := Binding(project); got != "" {
			t.Errorf("Binding() with %q = %q, want %q", val, got, "")
		}
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ── SetBinding ───────────────────────────────────────────────────────────────

// The whole point of read-modify-write through map[string]any: Claude Code's own
// settings come back out exactly as they went in.
func TestSetBindingPreservesForeignKeys(t *testing.T) {
	project := t.TempDir()
	path := writeSettingsFile(t, project, foreignSettings)
	before := parseJSON(t, []byte(foreignSettings))

	if err := SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	after := parseJSON(t, readFile(t, path))
	for _, key := range []string{"permissions", "hooks", "enabledPlugins"} {
		if !reflect.DeepEqual(after[key], before[key]) {
			t.Errorf("foreign key %q changed:\n got %#v\nwant %#v", key, after[key], before[key])
		}
	}
	want := map[string]any{"claudeAccount": "nabu-org"}
	if !reflect.DeepEqual(after[bindingNamespace], want) {
		t.Errorf("swarmery namespace = %#v, want %#v", after[bindingNamespace], want)
	}
	if got := Binding(project); got != "nabu-org" {
		t.Errorf("Binding() = %q, want %q", got, "nabu-org")
	}
}

// Never write over a file we cannot read: the error names the file and the
// bytes are untouched — including no .bak, since nothing was written.
func TestSetBindingAbortsOnUnparseableJSON(t *testing.T) {
	project := t.TempDir()
	const broken = `{ "permissions": { oops`
	path := writeSettingsFile(t, project, broken)

	if err := SetBinding(project, "nabu-org"); err == nil {
		t.Fatal("SetBinding on broken JSON returned nil, want an error")
	}
	if got := string(readFile(t, path)); got != broken {
		t.Errorf("file changed:\n got %q\nwant %q", got, broken)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("a .bak was written for a file that was never modified")
	}
}

// A second identical call produces a byte-identical file, and the .bak keeps the
// PRE-swarmery original rather than being refreshed to our own output.
func TestSetBindingIsIdempotent(t *testing.T) {
	project := t.TempDir()
	path := writeSettingsFile(t, project, foreignSettings)

	if err := SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("first SetBinding: %v", err)
	}
	first := readFile(t, path)

	if err := SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("second SetBinding: %v", err)
	}
	if second := readFile(t, path); !slices.Equal(first, second) {
		t.Errorf("second call rewrote the file:\n got %s\nwant %s", second, first)
	}
	if got := string(readFile(t, path+".bak")); got != foreignSettings {
		t.Errorf(".bak = %q, want the untouched original", got)
	}
}

func TestSetBindingBacksUpBeforeTheFirstWrite(t *testing.T) {
	project := t.TempDir()
	path := writeSettingsFile(t, project, foreignSettings)

	if err := SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	if got := string(readFile(t, path+".bak")); got != foreignSettings {
		t.Errorf(".bak = %q, want %q", got, foreignSettings)
	}
}

// Binding a project that has no settings file yet creates one containing only
// our namespace — and nothing to back up means no .bak.
func TestSetBindingCreatesTheFileWhenMissing(t *testing.T) {
	project := t.TempDir()
	if err := SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	path := bindingPath(project)
	want := "{\n  \"swarmery\": {\n    \"claudeAccount\": \"nabu-org\"\n  }\n}\n"
	if got := string(readFile(t, path)); got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("a .bak was written for a file that did not exist before")
	}
}

// Clearing removes the key AND the object it leaves empty — no orphan
// {"swarmery":{}} in the operator's settings file.
func TestSetBindingClearRemovesKeyAndEmptyNamespace(t *testing.T) {
	project := t.TempDir()
	path := writeSettingsFile(t, project,
		`{"permissions":{"allow":["Bash(ls:*)"]},"swarmery":{"claudeAccount":"nabu-org"}}`)

	if err := SetBinding(project, ""); err != nil {
		t.Fatalf("SetBinding(clear): %v", err)
	}
	after := parseJSON(t, readFile(t, path))
	if _, ok := after[bindingNamespace]; ok {
		t.Errorf("swarmery namespace survived the clear: %#v", after[bindingNamespace])
	}
	if _, ok := after["permissions"]; !ok {
		t.Error("clearing the binding dropped a foreign key")
	}
	if got := Binding(project); got != "" {
		t.Errorf("Binding() = %q, want %q", got, "")
	}
}

// Only the binding is ours to remove: a sibling key in our own namespace keeps
// the object alive.
func TestSetBindingClearKeepsOtherKeysInOurNamespace(t *testing.T) {
	project := t.TempDir()
	path := writeSettingsFile(t, project,
		`{"swarmery":{"claudeAccount":"nabu-org","somethingElse":42}}`)

	if err := SetBinding(project, ""); err != nil {
		t.Fatalf("SetBinding(clear): %v", err)
	}
	after := parseJSON(t, readFile(t, path))
	ns, ok := after[bindingNamespace].(map[string]any)
	if !ok {
		t.Fatalf("swarmery namespace = %#v, want a surviving object", after[bindingNamespace])
	}
	if _, gone := ns[bindingField]; gone {
		t.Error("claudeAccount survived the clear")
	}
	if ns["somethingElse"] != float64(42) {
		t.Errorf("somethingElse = %#v, want 42", ns["somethingElse"])
	}
}

// Clearing a binding that is not there touches nothing — not the bytes, not
// even the formatting.
func TestSetBindingClearOnAnUnboundProjectWritesNothing(t *testing.T) {
	project := t.TempDir()
	path := writeSettingsFile(t, project, foreignSettings)
	if err := SetBinding(project, ""); err != nil {
		t.Fatalf("SetBinding(clear): %v", err)
	}
	if got := string(readFile(t, path)); got != foreignSettings {
		t.Errorf("file was rewritten:\n got %q\nwant %q", got, foreignSettings)
	}

	// …and on a project with no settings file at all, none is created.
	empty := t.TempDir()
	if err := SetBinding(empty, ""); err != nil {
		t.Fatalf("SetBinding(clear) on a missing file: %v", err)
	}
	if _, err := os.Stat(bindingPath(empty)); !os.IsNotExist(err) {
		t.Error("clearing an unbound project created a settings file")
	}
}

// An unsafe key is refused before the file is opened.
func TestSetBindingRejectsAnUnsafeKey(t *testing.T) {
	for _, key := range []string{"../evil", "a/b", ".hidden", "a b"} {
		project := t.TempDir()
		if err := SetBinding(project, key); err == nil {
			t.Errorf("SetBinding(%q) = nil, want an error", key)
		}
		if _, err := os.Stat(bindingPath(project)); !os.IsNotExist(err) {
			t.Errorf("SetBinding(%q) created a settings file", key)
		}
	}
}

// ── EnvFor / EnvForAccount ───────────────────────────────────────────────────

// THE invariant: "no binding" and "bound to default" are byte-identical, both
// producing an EMPTY env delta. The moment the default yields
// CLAUDE_CONFIG_DIR=~/.claude, binding a project to it stops being a no-op.
func TestEnvForUnboundAndDefaultAreBothNil(t *testing.T) {
	fakeHome(t)

	unbound := t.TempDir()
	if got := EnvFor(unbound); got != nil {
		t.Errorf("EnvFor(unbound) = %v, want nil", got)
	}

	bound := t.TempDir()
	if err := SetBinding(bound, "default"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	if got := Binding(bound); got != "default" {
		t.Fatalf("Binding() = %q, want %q", got, "default")
	}
	if got := EnvFor(bound); got != nil {
		t.Errorf("EnvFor(default) = %v, want nil", got)
	}
	if got := EnvForAccount("default"); got != nil {
		t.Errorf("EnvForAccount(%q) = %v, want nil", "default", got)
	}
}

// A non-default account is exactly one variable — never two, never a PATH-style
// append that a spawner would have to merge.
func TestEnvForNonDefaultIsExactlyOneVariable(t *testing.T) {
	home := fakeHome(t)
	project := t.TempDir()
	if err := SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	want := []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude-nabu-org")}
	if got := EnvFor(project); !slices.Equal(got, want) {
		t.Errorf("EnvFor() = %v, want %v", got, want)
	}
}

// An empty key is safe to pass — a RunSpec with no account set must not blow up
// or invent one.
func TestEnvForAccountEmptyIsNil(t *testing.T) {
	fakeHome(t)
	if got := EnvForAccount(""); got != nil {
		t.Errorf("EnvForAccount(%q) = %v, want nil", "", got)
	}
	if got := EnvForAccount("   "); got != nil {
		t.Errorf("EnvForAccount(%q) = %v, want nil", "   ", got)
	}
}

// When the account EXISTS, its real dir wins over the canonical mapping: an
// operator whose dir is ~/.claude.work still keys as "work", and pointing the
// CLI at ~/.claude-work would hand it an empty, logged-out config dir.
func TestEnvForAccountPrefersTheDiscoveredDir(t *testing.T) {
	home := fakeHome(t)
	mkdirs(t, filepath.Join(home, ".claude.work", "projects"))

	want := []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude.work")}
	if got := EnvForAccount("work"); !slices.Equal(got, want) {
		t.Errorf("EnvForAccount(%q) = %v, want %v", "work", got, want)
	}
}

// An account that does not exist yet still resolves — that is the provisioning
// case (Phase 3 creates the dir, this env is what it gets created under).
func TestEnvForAccountFallsBackToTheCanonicalDir(t *testing.T) {
	home := fakeHome(t)
	want := []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude-ghost")}
	if got := EnvForAccount("ghost"); !slices.Equal(got, want) {
		t.Errorf("EnvForAccount(%q) = %v, want %v", "ghost", got, want)
	}
}

// An unsafe key produces no env at all — the spawn falls back to the default
// account instead of joining attacker-shaped input onto $HOME.
func TestEnvForAccountUnsafeKeyIsNil(t *testing.T) {
	fakeHome(t)
	for _, key := range []string{"../evil", "a/b", ".hidden"} {
		if got := EnvForAccount(key); got != nil {
			t.Errorf("EnvForAccount(%q) = %v, want nil", key, got)
		}
	}
}

func TestConfigDirForAccountResolvesBoundKeyOnly(t *testing.T) {
	home := t.TempDir()
	prev := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = prev })

	if _, ok := ConfigDirForAccount(""); ok {
		t.Error("empty key must not resolve — the caller keeps its default dir")
	}
	if _, ok := ConfigDirForAccount(ingest.DefaultAccount); ok {
		t.Error("the default account must not resolve — it IS the default dir")
	}
	got, ok := ConfigDirForAccount("acct")
	if !ok || got != filepath.Join(home, ".claude-acct") {
		t.Errorf("ConfigDirForAccount(acct) = %q, %v; want %q, true", got, ok, filepath.Join(home, ".claude-acct"))
	}

	project := t.TempDir()
	if _, ok := ConfigDirForProject(project); ok {
		t.Error("an unbound project must not resolve")
	}
	if err := SetBinding(project, "acct"); err != nil {
		t.Fatal(err)
	}
	if got, ok := ConfigDirForProject(project); !ok || got != filepath.Join(home, ".claude-acct") {
		t.Errorf("ConfigDirForProject = %q, %v after binding", got, ok)
	}
}
