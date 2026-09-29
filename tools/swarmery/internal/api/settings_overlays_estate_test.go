package api

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
)

// Criterion 34 (A9 withdrawn 2026-09-29): an ADMITTED estate whose
// .claude/settings.json lists enabledPlugins (a @swarmery pack and a foreign
// one) contributes NO overlay to a sub-repo, and the sub-repo's managed/packs
// come from its own files, unchanged.
func TestOverlaysForAdmittedEstateAddsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	secrets := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", secrets)
	attachOverlayDescriptor(t, filepath.Join(t.TempDir(), "absent-overlays.json"))

	estate := t.TempDir()
	writeBindingFile(t, estate, `{"estate":"estatex"}`)
	if err := os.WriteFile(filepath.Join(secrets, "estatex.env"), []byte("# swarmery-root: "+estate+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	estateSettings := filepath.Join(estate, ".claude", "settings.json")
	if err := os.WriteFile(estateSettings,
		[]byte(`{"enabledPlugins":{"uav-pack@swarmery":true,"foreign-pack@other":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(estate, "repos", "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".claude", "settings.json"),
		[]byte(`{"enabledPlugins":{"core@swarmery":true,"iot-pack@swarmery":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := claudeacct.Resolve(sub); !r.EstateAdmitted || r.SettingsFile != estateSettings {
		t.Fatalf("precondition: the estate is not admitted (%+v)", r)
	}

	ovs := overlaysFor(sub)
	for _, o := range ovs {
		if o.Name != localSettingsName {
			t.Errorf("overlaysFor(sub) carries %q — no estate overlay may be folded in", o.Name)
		}
	}
	st, err := projectscan.ReadPluginState(sub, nil, ovs...)
	if err != nil || st == nil {
		t.Fatalf("ReadPluginState = %v, %v", st, err)
	}
	if !st.Managed || !slices.Equal(st.Packs, []string{"iot-pack"}) || len(st.OverlaySources) != 0 {
		t.Errorf("plugin state = %+v, want the sub-repo's own managed + [iot-pack] and no overlay source", st)
	}
}
