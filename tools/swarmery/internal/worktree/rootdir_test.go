package worktree

import (
	"path/filepath"
	"testing"
)

// RootDir is how the janitor learns which checkouts are the daemon's own, so it
// must name exactly the directory Acquire creates them under.
func TestRootDir(t *testing.T) {
	if got, err := (&Manager{Root: "/custom/root"}).RootDir(); err != nil || got != "/custom/root" {
		t.Errorf("RootDir with Root set = %q, %v; want /custom/root, nil", got, err)
	}
	t.Setenv("HOME", "/home/u")
	want := filepath.Join("/home/u", DefaultRoot)
	if got, err := (&Manager{}).RootDir(); err != nil || got != want {
		t.Errorf("RootDir with Root empty = %q, %v; want %q, nil", got, err, want)
	}
}
