package accountdoctor

import (
	"path/filepath"
	"reflect"
	"testing"
)

// installed writes an installed_plugins.json for one fixture config dir.
func writeInstalledAt(t *testing.T, cfg string, plugins map[string][]map[string]string) {
	t.Helper()
	mustWrite(t, filepath.Join(cfg, "plugins", "installed_plugins.json"),
		mustJSON(t, map[string]any{"version": 2, "plugins": plugins}), 0o644)
}

// Parity is per (id, scope), and an orphaned cache checkout is labelled apart
// from version drift (the correction to the spine, items 2 and 3).
func TestParityPerIDAndScope(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	writeInstalledAt(t, a, map[string][]map[string]string{
		"acct@m": {{"scope": "user", "version": "0.3.0"}},
		"ops@m":  {{"scope": "user", "version": "0.5.1"}, {"scope": "local", "version": "0.5.1"}},
		"same@m": {{"scope": "user", "version": "1.0.0", "installPath": filepath.Join(a, "plugins", "cache", "m", "same", "1.0.0")}},
		"solo@m": {{"scope": "user", "version": "2.0.0"}},
	})
	writeInstalledAt(t, b, map[string][]map[string]string{
		"acct@m": {{"scope": "user", "version": "0.2.1"}},
		"ops@m":  {{"scope": "user", "version": "0.5.1"}, {"scope": "local", "version": "0.5.0"}, {"scope": "local", "version": "0.5.1"}},
		"same@m": {{"scope": "user", "version": "1.0.0"}},
	})
	// A referenced checkout under a, and an unreferenced one (a newer version).
	mustMkdir(t, filepath.Join(a, "plugins", "cache", "m", "same", "1.0.0"), 0o755)
	mustMkdir(t, filepath.Join(a, "plugins", "cache", "m", "same", "1.1.0"), 0o755)

	got := Parity([]AccountPlugins{{"a", a}, {"b", b}})
	type k struct{ id, scope, kind string }
	have := map[k]ParityEntry{}
	for _, e := range got {
		have[k{e.ID, e.Scope, e.Kind}] = e
	}
	for _, want := range []k{
		{"acct@m", "user", "version"},
		{"ops@m", "local", "version"},
		{"solo@m", "user", "missing"},
		{"same@m", "cache", "orphan-cache"},
	} {
		if _, ok := have[want]; !ok {
			t.Errorf("missing %+v in %+v", want, got)
		}
	}
	if _, ok := have[k{"ops@m", "user", "version"}]; ok {
		t.Error("ops@m user scope matches under both accounts and must not be reported")
	}
	if _, ok := have[k{"same@m", "user", "version"}]; ok {
		t.Error("same@m is at parity")
	}
	if v := have[k{"same@m", "cache", "orphan-cache"}].Versions; !reflect.DeepEqual(v, map[string][]string{"a": {"1.1.0"}}) {
		t.Errorf("orphan versions = %v, want only the unreferenced 1.1.0 under a", v)
	}
	if got := Parity(nil); got == nil {
		t.Error("Parity(nil) = nil, want []")
	}
}
