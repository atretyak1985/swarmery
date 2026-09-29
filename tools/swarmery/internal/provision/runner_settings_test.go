package provision

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct/accttest"
)

// --settings is a ROOT option: it must be PREPENDED, before the subcommand.
// Under an estate the composed file leads argv; with no estate, and for dir
// "", argv is untouched.
func TestProvisionPrependsComposedSettings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude PATH shim is POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	run := t.TempDir()
	t.Setenv("SWARMERY_RUN_DIR", run)
	root := filepath.Join(home, "projects", "acme")
	project := filepath.Join(root, "repo")
	bare := filepath.Join(home, "projects", "other")
	for _, d := range []string{project, bare} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}
	// Admitted (D5 Lock 2), and shipping the settings file D6 composes from.
	accttest.AdmitEstate(t, "acme", root)
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"pluginConfigs":{"a@m":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := ClaudeRunner{}.Claude(context.Background(), project, "", "plugin", "install", "x@m")
	if err != nil {
		t.Fatalf("Claude: %v", err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 5 || lines[0] != "--settings" || !strings.HasPrefix(lines[1], filepath.Join(run, "settings")) || lines[2] != "plugin" {
		t.Fatalf("argv = %q, want [--settings <composed> plugin install x@m]", lines)
	}

	for _, dir := range []string{bare, ""} {
		out, err = ClaudeRunner{}.Claude(context.Background(), dir, "", "plugin", "install", "x@m")
		if err != nil {
			t.Fatalf("Claude(%q): %v", dir, err)
		}
		if strings.Contains(out, "--settings") {
			t.Fatalf("dir %q: argv carries --settings without an estate: %q", dir, out)
		}
	}
}
