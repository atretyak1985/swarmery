package claudeacct

// The bounds of the ONE downward pin walk are asserted here and nowhere else.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func pin(t *testing.T, dir, account string) {
	t.Helper()
	if err := SetBinding(dir, account); err != nil {
		t.Fatalf("SetBinding(%s): %v", dir, err)
	}
}

// nest is root/l1/l2/…/l<depth>.
func nest(root string, depth int) string {
	parts := []string{root}
	for i := 1; i <= depth; i++ {
		parts = append(parts, "l"+string(rune('0'+i)))
	}
	return filepath.Join(parts...)
}

func TestShadowingPins_DepthBound(t *testing.T) {
	fakeHome(t)
	root := t.TempDir()
	atMax := nest(root, PinScanMaxDepth)
	tooDeep := filepath.Join(atMax, "deeper")
	pin(t, atMax, "work")
	pin(t, tooDeep, "work")

	got := ShadowingPins(root)
	if !slices.Contains(got, atMax) {
		t.Fatalf("a pin at exactly PinScanMaxDepth (%d) was not found: %v", PinScanMaxDepth, got)
	}
	if slices.Contains(got, tooDeep) {
		t.Fatalf("a pin one level below PinScanMaxDepth was found: %v", got)
	}
}

func TestShadowingPins_SkipDirs(t *testing.T) {
	fakeHome(t)
	root := t.TempDir()
	for _, skip := range PinScanSkipDirs {
		pin(t, filepath.Join(root, skip, "x"), "work")
		pin(t, filepath.Join(root, "deep", skip, "y"), "work")
	}
	// The lent worktree copy: a binding under <root>/.swarmery/worktrees/x/y.
	pin(t, filepath.Join(root, ".swarmery", "worktrees", "x", "y"), "default")
	kept := filepath.Join(root, "deep", "kept")
	pin(t, kept, "work")

	got := ShadowingPins(root)
	if !slices.Equal(got, []string{kept}) {
		t.Fatalf("ShadowingPins = %v, want only %s — every PinScanSkipDirs entry must be skipped at every level", got, kept)
	}
	if !slices.Contains(PinScanSkipDirs, ".swarmery") {
		t.Fatal(".swarmery left PinScanSkipDirs — the lent worktree copy would be reported as a pinned project")
	}
}

func TestShadowingPins_MalformedRootExcludedSorted(t *testing.T) {
	fakeHome(t)
	root := t.TempDir()
	pin(t, root, "work") // the root's own pin is never listed
	writeSettingsFile(t, filepath.Join(root, "broken"), "{")
	writeSettingsFile(t, filepath.Join(root, "no-account"), `{"swarmery":{"estate":"x"},"permissions":{}}`)
	writeSettingsFile(t, filepath.Join(root, "unsafe"), `{"swarmery":{"claudeAccount":"../../etc"}}`)
	b := filepath.Join(root, "b")
	a2 := filepath.Join(root, "a", "z")
	a := filepath.Join(root, "a-b")
	pin(t, b, "default")
	pin(t, a2, "work")
	pin(t, a, "work")
	// A symlinked directory is not followed (no cycles).
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}

	got := ShadowingPins(root)
	want := []string{a2, a, b}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("ShadowingPins = %v, want %v (sorted; root, malformed, estate-only and unsafe excluded)", got, want)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("result not sorted: %v", got)
	}
	for _, p := range got {
		if strings.Contains(p, "loop") {
			t.Fatalf("walked through a symlinked directory: %s", p)
		}
	}
	if got := ShadowingPins(filepath.Join(root, "does-not-exist")); len(got) != 0 {
		t.Fatalf("ShadowingPins(missing) = %v", got)
	}
}
