package systemspawn

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct/accttest"
)

// No estate over ~/.swarmery: Args is untouched — no flag day for the five
// System engines.
func TestAttachWithoutEstateLeavesArgsAlone(t *testing.T) {
	systemHome(t)
	t.Setenv("SWARMERY_RUN_DIR", t.TempDir())
	cmd := exec.Command("claude", "-p", "--output-format", "text")
	want := append([]string(nil), cmd.Args...)
	Attach(cmd)
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Args = %q, want %q unchanged", cmd.Args, want)
	}
}

// An admitted estate declared on ~/.swarmery: the composed file is spliced right
// after argv[0] (a root option), on the same path that sets Dir and Env.
func TestAttachSplicesComposedSettingsAfterArgv0(t *testing.T) {
	dir := systemHome(t)
	run := t.TempDir()
	t.Setenv("SWARMERY_RUN_DIR", run)
	if err := claudeacct.SetEstate(dir, "sys"); err != nil {
		t.Fatalf("SetEstate: %v", err)
	}
	accttest.AdmitEstate(t, "sys", dir) // D5 Lock 2: only an ADMITTED estate composes
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(`{"pluginConfigs":{"a@m":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("claude", "-p", "--output-format", "text")
	Attach(cmd)
	if cmd.Dir != dir {
		t.Fatalf("Dir = %q, want %q", cmd.Dir, dir)
	}
	if len(cmd.Args) != 6 || cmd.Args[1] != "--settings" || !strings.HasPrefix(cmd.Args[2], filepath.Join(run, "settings")+string(filepath.Separator)) {
		t.Fatalf("Args = %q, want [claude --settings <run>/settings/<sha>.json -p ...]", cmd.Args)
	}
	if cmd.Args[3] != "-p" {
		t.Fatalf("the original argv must follow the splice, got %q", cmd.Args)
	}
}
