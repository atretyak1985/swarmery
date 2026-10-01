package channelprobe

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// harnessFS is the probe harness snapshotted into the binary by the Makefile's
// copy-probe step: script/cc-channel-probe.sh and its fixture plugin under
// script/fixtures/. The copies are gitignored; a committed .gitkeep keeps this
// pattern valid on a fresh clone and in CI, where the binary then simply has no
// harness (HasHarness false) and `doctor --probe` needs SWARMERY_PROBE_SCRIPT.
// The installed daemon has no path to the repo's scripts/, and go:embed cannot
// reach outside the module — hence the snapshot.
//
//go:embed all:script
var harnessEmbed embed.FS

// harnessFS is the tree WriteHarness reads — the embedded one; a test swaps it.
var harnessFS fs.FS = harnessEmbed

// harnessScript is the script's name inside the embedded tree.
const harnessScript = "cc-channel-probe.sh"

// ErrNoHarness is returned when the binary was built without the copy-probe
// snapshot.
var ErrNoHarness = errors.New("channelprobe: this binary carries no embedded probe harness (built without `make copy-probe`)")

// HasHarness reports whether the embedded harness is present.
func HasHarness() bool {
	_, err := fs.Stat(harnessFS, path.Join("script", harnessScript))
	return err == nil
}

// WriteHarness lays the embedded harness out under dir the way the script
// expects to find itself — <dir>/scripts/tests/cc-channel-probe.sh, its fixture
// plugin beside it, and the baseline at
// <dir>/tools/swarmery/internal/channelprobe/baseline.json — and returns the
// script's path. dir should be a fresh 0700 temp dir the caller removes.
func WriteHarness(dir string) (string, error) {
	if !HasHarness() {
		return "", ErrNoHarness
	}
	tests := filepath.Join(dir, "scripts", "tests")
	err := fs.WalkDir(harnessFS, "script", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("script", filepath.FromSlash(p))
		dst := filepath.Join(tests, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		if d.Name() == ".gitkeep" {
			return nil
		}
		data, err := fs.ReadFile(harnessFS, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if d.Name() == harnessScript {
			mode = 0o700
		}
		return os.WriteFile(dst, data, mode)
	})
	if err != nil {
		return "", fmt.Errorf("channelprobe: write harness: %w", err)
	}
	base := filepath.Join(dir, "tools", "swarmery", "internal", "channelprobe")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("channelprobe: write harness: %w", err)
	}
	if err := os.WriteFile(filepath.Join(base, "baseline.json"), baselineJSON, 0o600); err != nil {
		return "", fmt.Errorf("channelprobe: write harness: %w", err)
	}
	return filepath.Join(tests, harnessScript), nil
}
