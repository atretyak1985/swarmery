package worktree

// Commit B of the estate plan (Phase 2): a worktree never receives the
// `swarmery` binding object of the source's settings.local.json, and copies
// lent before that rule are stripped — foreign keys kept, anything unparseable
// or git-tracked left byte-for-byte.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const lentWithBinding = `{
  "swarmery": {"claudeAccount": "default", "estate": "acme"},
  "enabledPlugins": {"core@swarmery": true},
  "extraKnownMarketplaces": {"swarmery": {"source": {"source": "directory", "path": "/x"}}},
  "permissions": {"allow": ["Bash(ls:*)"]}
}
`

// decodeKeys is a file's top-level object, for comparing key sets and values.
func decodeKeys(t *testing.T, p string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s is not a JSON object: %v", p, err)
	}
	return m
}

func TestSyncUntrackedConfigNeverLendsTheBinding(t *testing.T) {
	root := t.TempDir()
	repo, wt := filepath.Join(root, "repo"), filepath.Join(root, "wt")
	mustMkdir(t, filepath.Join(repo, ".claude"))
	mustWrite(t, filepath.Join(repo, ".claude", "settings.local.json"), lentWithBinding)

	syncUntrackedConfig(repo, wt)

	got := decodeKeys(t, filepath.Join(wt, ".claude", "settings.local.json"))
	if _, ok := got["swarmery"]; ok {
		t.Fatal("the worktree was lent the swarmery binding object")
	}
	want := decodeKeys(t, filepath.Join(repo, ".claude", "settings.local.json"))
	delete(want, "swarmery")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("foreign keys changed:\n got  %v\n want %v", got, want)
	}
	// The source itself is untouched.
	if src := decodeKeys(t, filepath.Join(repo, ".claude", "settings.local.json")); src["swarmery"] == nil {
		t.Fatal("the SOURCE lost its binding")
	}
}

func TestSyncUntrackedConfigLendsOtherShapesVerbatim(t *testing.T) {
	for name, body := range map[string]string{
		"unparseable":      `{"swarmery": {"claudeAccount": "x"`,
		"no binding":       "{\"enabledPlugins\":{\"a\":true}}\n",
		"not an object":    `["swarmery"]`,
		"settings.json ok": "",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo, wt := filepath.Join(root, "repo"), filepath.Join(root, "wt")
			mustMkdir(t, filepath.Join(repo, ".claude"))
			if body == "" {
				// settings.json is never filtered, even when it carries the key.
				mustWrite(t, filepath.Join(repo, ".claude", "settings.json"), lentWithBinding)
				syncUntrackedConfig(repo, wt)
				got, _ := os.ReadFile(filepath.Join(wt, ".claude", "settings.json"))
				if string(got) != lentWithBinding {
					t.Fatal("settings.json was altered on the way")
				}
				return
			}
			mustWrite(t, filepath.Join(repo, ".claude", "settings.local.json"), body)
			syncUntrackedConfig(repo, wt)
			got, err := os.ReadFile(filepath.Join(wt, ".claude", "settings.local.json"))
			if err != nil || string(got) != body {
				t.Fatalf("lent copy = %q (%v), want the source byte-for-byte", got, err)
			}
		})
	}
}

// trackGit answers `ls-files --error-unmatch` per worktree dir: tracked,
// untracked, or a git failure.
type trackGit map[string]string

func (g trackGit) Run(dir string, _ ...string) (string, error) {
	switch g[dir] {
	case "tracked":
		return ".claude/settings.local.json\n", nil
	case "untracked":
		return "error: pathspec '.claude/settings.local.json' did not match any file(s) known to git", errors.New("exit status 1")
	default:
		return "fatal: not a git repository", errors.New("exit status 128")
	}
}

func TestStripLentBindings(t *testing.T) {
	root := t.TempDir()
	wt := func(slug, task string) string { return filepath.Join(root, slug, task) }
	write := func(dir, body string) string {
		p := filepath.Join(dir, ".claude", "settings.local.json")
		mustMkdir(t, filepath.Dir(p))
		mustWrite(t, p, body)
		if err := os.Chmod(p, 0o640); err != nil {
			t.Fatal(err)
		}
		return p
	}
	lent := write(wt("proj", "phase-1"), lentWithBinding)
	tracked := write(wt("proj", "phase-2"), lentWithBinding)
	unknown := write(wt("proj", "phase-3"), lentWithBinding)
	broken := write(wt("proj", "phase-4"), `{"swarmery":`)
	clean := write(wt("other", "T-1"), "{\"enabledPlugins\":{}}\n")
	linkDir := wt("other", "T-2")
	mustMkdir(t, filepath.Join(linkDir, ".claude"))
	if err := os.Symlink(lent, filepath.Join(linkDir, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}

	before := map[string][]byte{}
	for _, p := range []string{tracked, unknown, broken, clean} {
		before[p], _ = os.ReadFile(p)
	}
	m := &Manager{Root: root, Git: trackGit{
		wt("proj", "phase-1"): "untracked",
		wt("proj", "phase-2"): "tracked",
		wt("proj", "phase-4"): "untracked",
		wt("other", "T-1"):    "untracked",
		wt("other", "T-2"):    "untracked",
	}}
	n, err := m.StripLentBindings()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("stripped %d files, want exactly the one untracked lent copy", n)
	}
	got := decodeKeys(t, lent)
	if _, ok := got["swarmery"]; ok {
		t.Fatal("the lent copy still carries the binding")
	}
	for _, k := range []string{"enabledPlugins", "extraKnownMarketplaces", "permissions"} {
		if _, ok := got[k]; !ok {
			t.Errorf("foreign key %s was dropped", k)
		}
	}
	if fi, _ := os.Stat(lent); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %o", fi.Mode().Perm())
	}
	for p, want := range before {
		if got, _ := os.ReadFile(p); string(got) != string(want) {
			t.Errorf("%s was rewritten; tracked, unclassifiable, unparseable and binding-free files must stay byte-for-byte", p)
		}
	}
	// Idempotent.
	if n, _ := m.StripLentBindings(); n != 0 {
		t.Fatalf("a second pass stripped %d files", n)
	}
}
