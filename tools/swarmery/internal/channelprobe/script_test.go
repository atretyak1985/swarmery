package channelprobe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestWriteHarnessLaysOutTheRepoShape(t *testing.T) {
	orig := harnessFS
	t.Cleanup(func() { harnessFS = orig })
	harnessFS = fstest.MapFS{
		"script/.gitkeep":                                               {Data: nil},
		"script/cc-channel-probe.sh":                                    {Data: []byte("#!/bin/sh\nexit 0\n")},
		"script/fixtures/cc-channel-probe/plugin/.mcp.json":             {Data: []byte("{}")},
		"script/fixtures/cc-channel-probe/plugin/.claude-plugin/p.json": {Data: []byte("{}")},
	}
	dir := t.TempDir()
	script, err := WriteHarness(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "scripts", "tests", "cc-channel-probe.sh"); script != want {
		t.Errorf("script = %s, want %s", script, want)
	}
	if fi, err := os.Stat(script); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("script mode = %v (%v), want 0700", fi, err)
	}
	for _, p := range []string{
		"scripts/tests/fixtures/cc-channel-probe/plugin/.mcp.json",
		"scripts/tests/fixtures/cc-channel-probe/plugin/.claude-plugin/p.json",
		"tools/swarmery/internal/channelprobe/baseline.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s not laid out: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "scripts", "tests", ".gitkeep")); err == nil {
		t.Error(".gitkeep was copied")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "tools/swarmery/internal/channelprobe/baseline.json"))
	if _, err := Parse(data); err != nil {
		t.Errorf("laid-out baseline does not parse: %v", err)
	}
}

func TestWriteHarnessWithoutSnapshot(t *testing.T) {
	orig := harnessFS
	t.Cleanup(func() { harnessFS = orig })
	harnessFS = fstest.MapFS{"script/.gitkeep": {}}
	if HasHarness() {
		t.Fatal("HasHarness with only .gitkeep")
	}
	if _, err := WriteHarness(t.TempDir()); !errors.Is(err, ErrNoHarness) {
		t.Errorf("err = %v, want ErrNoHarness", err)
	}
}
