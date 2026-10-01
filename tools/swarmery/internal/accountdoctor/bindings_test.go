package accountdoctor

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bindingAt(t *testing.T, dir, ns string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, ".claude", "settings.local.json"), `{"swarmery":`+ns+`}`, 0o644)
}

// DiscoverBindings finds bindings with no projects row: the estate root, every
// pin below it (through ScanPins' walk), and the seed's own — and never a
// daemon worktree's lent copy.
func TestDiscoverBindings(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	bindingAt(t, root, `{"estate":"estate","claudeAccount":"work"}`)
	f.loggedInAccount(t, "work")
	deep := filepath.Join(root, "repos", "a", "b")
	bindingAt(t, deep, `{"claudeAccount":"default"}`)
	lone := t.TempDir()
	bindingAt(t, lone, `{"claudeAccount":"work"}`)
	scanned := t.TempDir()
	under := filepath.Join(scanned, "p")
	bindingAt(t, under, `{"claudeAccount":"default"}`)
	lent := filepath.Join(f.home, ".swarmery", "worktrees", "slug", "task")
	bindingAt(t, lent, `{"claudeAccount":"default"}`)
	plain := t.TempDir()
	mustWrite(t, filepath.Join(plain, ".claude", "settings.local.json"), `{"permissions":{}}`, 0o644)

	got := DiscoverBindings([]string{filepath.Join(root, "repos"), lone, plain, ""}, scanned, f.home)
	want := map[string]string{root: "work", deep: "default", lone: "work", under: "default"}
	if len(got) != len(want) {
		t.Fatalf("DiscoverBindings = %+v, want %v", got, want)
	}
	for _, b := range got {
		if key, ok := want[b.Path]; !ok || key != b.Key || b.Ignored {
			t.Errorf("unexpected %+v", b)
		}
		if strings.Contains(b.Path, ".swarmery") {
			t.Errorf("a daemon worktree path was listed: %s", b.Path)
		}
	}
}

// Criterion 35 (doctor half): a TRACKED binding is returned as ignored, with
// its declared key and Lock 1's reason — never dropped, never honoured.
func TestDiscoverBindingsIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	newFixture(t)
	root := t.TempDir()
	bindingAt(t, root, `{"estate":"estate"}`)
	sub := filepath.Join(root, "sub")
	bindingAt(t, sub, `{"claudeAccount":"work"}`)
	git(t, sub, "init", "-q", ".")
	git(t, sub, "add", "-f", ".claude/settings.local.json")
	git(t, sub, "commit", "-q", "-m", "fixture")
	own := t.TempDir()
	bindingAt(t, own, `{"claudeAccount":"work"}`)
	git(t, own, "init", "-q", ".")
	git(t, own, "add", "-f", ".claude/settings.local.json")
	git(t, own, "commit", "-q", "-m", "fixture")

	got := DiscoverBindings([]string{root, own})
	found := map[string]Binding{}
	for _, b := range got {
		found[b.Path] = b
	}
	for _, dir := range []string{sub, own} {
		b, ok := found[dir]
		if !ok || !b.Ignored || b.Key != "work" || !strings.Contains(b.Reason, "tracked by git") {
			t.Errorf("%s: %+v, want ignored with key work and the tracked reason", dir, b)
		}
	}
	if b := found[root]; b.Ignored || b.Key != "" {
		t.Errorf("estate root = %+v, want an honoured estate-only declaration", b)
	}
}
