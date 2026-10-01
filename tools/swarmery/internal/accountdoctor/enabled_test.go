package accountdoctor

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// anchoredEstate declares root as the root of estate key and writes that
// estate's store — empty apart from its root line — so the estate is ADMITTED
// (D5). extra store lines follow the root line. Returns the store path.
func (f fixture) anchoredEstate(t *testing.T, root, key string, extra string) string {
	t.Helper()
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"),
		`{"swarmery":{"estate":"`+key+`"}}`, 0o644)
	return f.store(t, key, "# swarmery-root: "+root+"\n"+extra)
}

// store writes <secrets>/<key>.env at 0600 and keeps the store dir at 0700
// (mustWrite leaves a parent at 0755, which the loader refuses).
func (f fixture) store(t *testing.T, key, body string) string {
	t.Helper()
	p := filepath.Join(f.secrets, key+".env")
	mustWrite(t, p, body, 0o600)
	if err := os.Chmod(f.secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// Criterion 23: the estate's enabledPlugins is NOT a pack source. A pack listed
// only in the (admitted) estate's settings.json is absent from EnabledPacks and
// its ${VAR}s never reach varsExpected.
func TestEnabledPacksIgnoresEstate(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estatex", "")
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"),
		`{"enabledPlugins":{"only-estate@m":true}}`, 0o644)
	sub := filepath.Join(root, "sub")
	mustWrite(t, filepath.Join(sub, ".claude", "settings.local.json"), `{"enabledPlugins":{"own@m":true}}`, 0o644)

	installed := map[string][]installRecord{}
	f.plugin(t, installed, "only-estate@m", `{"mcpServers":{"a":{"env":{"X":"${PACK_ESTATE_ONLY}"}}}}`)
	f.plugin(t, installed, "own@m", `{"mcpServers":{"a":{"env":{"X":"${PACK_OWN}"}}}}`)
	f.writeInstalled(t, installed)
	t.Setenv("PACK_ESTATE_ONLY", "")
	t.Setenv("PACK_OWN", "")

	rep, err := Fast(Options{Path: sub})
	if err != nil {
		t.Fatal(err)
	}
	if rep.SettingsFile == "" {
		t.Fatal("precondition: the estate is not admitted, so the test proves nothing")
	}
	if !slices.Contains(rep.EnabledPacks, "own@m") || slices.Contains(rep.EnabledPacks, "only-estate@m") {
		t.Errorf("EnabledPacks = %v, want own@m and not only-estate@m", rep.EnabledPacks)
	}
	if slices.Contains(rep.VarsExpected, "PACK_ESTATE_ONLY") || !slices.Contains(rep.VarsExpected, "PACK_OWN") {
		t.Errorf("VarsExpected = %v", rep.VarsExpected)
	}
	if got := EnabledPacks(sub, f.cfg); !slices.Equal(got, []string{"own@m"}) {
		t.Errorf("EnabledPacks(sub) = %v", got)
	}
}

// Criterion 26: a sub-repo's own false wins over the estate's true — the pack
// and its variables leave the coverage arm.
func TestEnabledPacksSubRepoDisable(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estatey", "")
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{"enabledPlugins":{"pack@m":true}}`, 0o644)
	installed := map[string][]installRecord{}
	f.plugin(t, installed, "pack@m", `{"mcpServers":{"db":{"env":{"H":"${PACK_DB_HOST}"}}}}`)
	f.writeInstalled(t, installed)
	t.Setenv("PACK_DB_HOST", "")
	sub := filepath.Join(root, "sub")

	for _, tc := range []struct {
		on   bool
		body string
	}{
		{true, `{"enabledPlugins":{"pack@m":true}}`},
		{false, `{"enabledPlugins":{"pack@m":false}}`},
	} {
		mustWrite(t, filepath.Join(sub, ".claude", "settings.local.json"), tc.body, 0o644)
		rep, err := Fast(Options{Path: sub})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(rep.EnabledPacks, "pack@m"); got != tc.on {
			t.Errorf("sub-repo %v: enabledPacks contains pack@m = %v (%v)", tc.on, got, rep.EnabledPacks)
		}
		if got := slices.Contains(rep.VarsExpected, "PACK_DB_HOST"); got != tc.on {
			t.Errorf("sub-repo %v: varsExpected contains PACK_DB_HOST = %v", tc.on, got)
		}
	}
}
