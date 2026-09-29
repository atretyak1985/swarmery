package routines

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct/accttest"
)

// A project-scoped routine whose project sits under an ADMITTED estate carries the
// composed settings as --settings; without one it carries none (the
// byte-identical case TestClaudeRunnerArgs already pins).
func TestClaudeRunnerCarriesComposedSettings(t *testing.T) {
	fakeClaudeRunner(t, `printf '%s\n' "$@" > "$PWD/args.txt"; exit 0`)
	run := t.TempDir()
	t.Setenv("SWARMERY_RUN_DIR", run)
	cwd := t.TempDir()
	if err := claudeacct.SetEstate(cwd, "acme"); err != nil {
		t.Fatalf("SetEstate: %v", err)
	}
	// D5 Lock 2 admits the estate, and D6 composes only from its own settings
	// file: without both, nothing is composed.
	accttest.AdmitEstate(t, "acme", cwd)
	if err := os.WriteFile(filepath.Join(cwd, ".claude", "settings.json"), []byte(`{"pluginConfigs":{"a@m":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (ClaudeRunner{}).Run(context.Background(), cwd, "the prompt", ""); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(cwd, "args.txt"))
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	found := false
	for i, a := range lines {
		if a == "--settings" && i+1 < len(lines) && strings.HasPrefix(lines[i+1], filepath.Join(run, "settings")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("args %q carry no composed --settings", lines)
	}
}
