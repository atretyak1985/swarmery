package sysscan

import (
	"os"
	"path/filepath"
	"testing"
)

func mkVersions(t *testing.T, versions []string, inUse ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, v := range versions {
		if err := os.MkdirAll(filepath.Join(dir, v), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range inUse {
		if err := os.MkdirAll(filepath.Join(dir, v, ".in_use"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// An old session keeps its version's marker after an update; the scan must
// follow the newest marked version, not the oldest one still open somewhere.
func TestPickVersionDir_NewestInUseWins(t *testing.T) {
	dir := mkVersions(t, []string{"3.11.0", "3.27.0", "3.28.2", "3.29.0"}, "3.27.0", "3.28.2", "3.29.0")
	if got := filepath.Base(pickVersionDir(dir)); got != "3.29.0" {
		t.Errorf("picked %s, want 3.29.0", got)
	}
}

// A marked version beats a newer unmarked one: an unmarked dir may be a
// half-written install nobody loads.
func TestPickVersionDir_MarkerBeatsNewerUnmarked(t *testing.T) {
	dir := mkVersions(t, []string{"3.28.2", "3.29.0"}, "3.28.2")
	if got := filepath.Base(pickVersionDir(dir)); got != "3.28.2" {
		t.Errorf("picked %s, want 3.28.2", got)
	}
}

func TestPickVersionDir_NoMarkerNumericOrder(t *testing.T) {
	dir := mkVersions(t, []string{"3.9.0", "3.10.0", "unknown"})
	if got := filepath.Base(pickVersionDir(dir)); got != "unknown" {
		// "unknown" vs "3.x": non-numeric first field compares as a string,
		// and "u" > "3" — opaque names never fail, they sort lexically.
		t.Errorf("picked %s, want unknown", got)
	}
	dir = mkVersions(t, []string{"3.9.0", "3.10.0"})
	if got := filepath.Base(pickVersionDir(dir)); got != "3.10.0" {
		t.Errorf("picked %s, want 3.10.0 (numeric, not lexical)", got)
	}
}

func TestPickVersionDir_Empty(t *testing.T) {
	if got := pickVersionDir(t.TempDir()); got != "" {
		t.Errorf("picked %q from an empty dir, want \"\"", got)
	}
}
