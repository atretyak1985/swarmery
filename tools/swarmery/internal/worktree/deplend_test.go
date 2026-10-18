package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// lendFixture lays out a source checkout with an installed dependency tree and
// an empty worktree beside it, returning both roots.
func lendFixture(t *testing.T, depDirs ...string) (repoRoot, worktreePath string) {
	t.Helper()
	base := t.TempDir()
	repoRoot = filepath.Join(base, "repo")
	worktreePath = filepath.Join(base, "wt")
	for _, d := range []string{repoRoot, worktreePath} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dep := range depDirs {
		full := filepath.Join(repoRoot, dep)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(full, "marker.txt"), []byte("installed"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repoRoot, worktreePath
}

// The point of the whole file: a fresh worktree must be able to read the source
// checkout's installed tree through the lent path. Assert the file is reachable,
// not merely that a link exists.
func TestLendDependencies_LinksInstalledTree(t *testing.T) {
	t.Setenv(LendEnv, "")
	repoRoot, wt := lendFixture(t, "node_modules")

	lendDependencies(repoRoot, wt)

	link := filepath.Join(wt, "node_modules")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("node_modules not lent: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("node_modules is not a symlink (mode %v) — a copy would cost gigabytes per run", info.Mode())
	}
	body, err := os.ReadFile(filepath.Join(link, "marker.txt"))
	if err != nil {
		t.Fatalf("read through the lent tree: %v", err)
	}
	if string(body) != "installed" {
		t.Errorf("marker.txt = %q, want the source checkout's content", body)
	}
}

// An ecosystem the project does not use must be a silent no-op, not a dangling
// link that later breaks a tool walking the tree.
func TestLendDependencies_SkipsAbsentSource(t *testing.T) {
	t.Setenv(LendEnv, "")
	repoRoot, wt := lendFixture(t) // nothing installed

	lendDependencies(repoRoot, wt)

	for _, rel := range defaultLendPaths {
		if _, err := os.Lstat(filepath.Join(wt, rel)); !os.IsNotExist(err) {
			t.Errorf("%s exists in the worktree (err=%v), want nothing created", rel, err)
		}
	}
}

// A project that COMMITS its vendor directory has git's copy in the worktree
// already; that copy is the specific answer and must survive.
func TestLendDependencies_LeavesTrackedCopyAlone(t *testing.T) {
	t.Setenv(LendEnv, "")
	repoRoot, wt := lendFixture(t, "vendor")
	tracked := filepath.Join(wt, "vendor")
	if err := os.MkdirAll(tracked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tracked, "marker.txt"), []byte("committed"), 0o644); err != nil {
		t.Fatal(err)
	}

	lendDependencies(repoRoot, wt)

	body, err := os.ReadFile(filepath.Join(tracked, "marker.txt"))
	if err != nil {
		t.Fatalf("read the worktree's own vendor: %v", err)
	}
	if string(body) != "committed" {
		t.Errorf("vendor/marker.txt = %q, want the worktree's committed copy untouched", body)
	}
	if info, err := os.Lstat(tracked); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		t.Error("vendor was replaced by a symlink; git's tracked copy must win")
	}
}

func TestLendDependencies_EnvOverrideAndNestedPath(t *testing.T) {
	repoRoot, wt := lendFixture(t, filepath.Join("web", "node_modules"))
	t.Setenv(LendEnv, "web/node_modules")

	lendDependencies(repoRoot, wt)

	if _, err := os.ReadFile(filepath.Join(wt, "web", "node_modules", "marker.txt")); err != nil {
		t.Fatalf("nested lend path not honoured: %v", err)
	}
	// Not in the list ⇒ not lent, even though the default list names it.
	if _, err := os.Lstat(filepath.Join(wt, "node_modules")); !os.IsNotExist(err) {
		t.Errorf("root node_modules was lent although %s did not name it", LendEnv)
	}
}

func TestLendDependencies_OffDisables(t *testing.T) {
	repoRoot, wt := lendFixture(t, "node_modules")
	for _, off := range []string{"off", "NONE", "-"} {
		if err := os.RemoveAll(filepath.Join(wt, "node_modules")); err != nil {
			t.Fatal(err)
		}
		t.Setenv(LendEnv, off)

		lendDependencies(repoRoot, wt)

		if _, err := os.Lstat(filepath.Join(wt, "node_modules")); !os.IsNotExist(err) {
			t.Errorf("%s=%q still lent node_modules", LendEnv, off)
		}
	}
}

// The knob must not become "link anything anywhere": an absolute path or a ..
// escape is refused, and refusal is a no-op rather than an error.
func TestLendDependencies_RefusesEscapingPaths(t *testing.T) {
	repoRoot, wt := lendFixture(t, "node_modules")
	// "" is NOT in this list: an empty knob means "unset" and resolves to the
	// defaults, which is a different behaviour (asserted above), not a refusal.
	for _, bad := range []string{"/etc", "../../elsewhere", ".."} {
		t.Setenv(LendEnv, bad)
		lendDependencies(repoRoot, wt)
	}
	entries, err := os.ReadDir(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("worktree got %d entries from refused lend paths, want 0", len(entries))
	}
}

func TestSafeRelPath(t *testing.T) {
	for path, want := range map[string]bool{
		"node_modules":      true,
		"web/node_modules":  true,
		".venv":             true,
		"":                  false,
		"..":                false,
		"../node_modules":   false,
		"/abs/node_modules": false,
		"a/../../b":         false,
		"./node_modules":    true,
		"a/../node_modules": true, // cleans to a sibling INSIDE the checkout
	} {
		if got := safeRelPath(path); got != want {
			t.Errorf("safeRelPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestManagerPlacedPaths(t *testing.T) {
	config := []string{".claude/settings.json", ".claude/settings.local.json", ".claude/project.json"}
	for _, tc := range []struct {
		name, lend string
		want       []string
	}{
		{"defaults", "", append([]string{"node_modules", ".venv", "vendor"}, config...)},
		{"lend off", "off", config},
		{"custom list", " web/node_modules/ ,./tools/.venv,../escape", append([]string{"web/node_modules", "tools/.venv"}, config...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(LendEnv, tc.lend)
			got := ManagerPlacedPaths(t.TempDir())
			if len(got) != len(tc.want) {
				t.Fatalf("ManagerPlacedPaths() = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ManagerPlacedPaths()[%d] = %q, want %q (all: %q)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// writePackageJSON commits (in the fixture's sense) a package.json under the
// worktree, which is what makes a directory a nested npm package.
func writePackageJSON(t *testing.T, worktreePath, dir string) {
	t.Helper()
	full := filepath.Join(worktreePath, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Board card T-ybm5i4: the root node_modules was lent, the web app's was not,
// and every run that built the web app failed on the missing tree.
func TestLendDependencies_LendsNestedNodeModules(t *testing.T) {
	t.Setenv(LendEnv, "")
	web := filepath.Join("tools", "app", "web")
	repoRoot, wt := lendFixture(t, "node_modules", filepath.Join(web, "node_modules"))
	writePackageJSON(t, wt, web)

	lendDependencies(repoRoot, wt)

	for _, rel := range []string{"node_modules", filepath.Join(web, "node_modules")} {
		if _, err := os.ReadFile(filepath.Join(wt, rel, "marker.txt")); err != nil {
			t.Errorf("%s not lent: %v", rel, err)
		}
	}
}

// A package.json whose source tree was never installed, or that sits where a
// checkout does not own it (hidden dirs, inside node_modules, too deep), must
// not produce a link.
func TestLendDependencies_NestedSkipsUninstalledAndOutOfScope(t *testing.T) {
	t.Setenv(LendEnv, "")
	hidden := filepath.Join(".claude", "worktrees", "x")
	deep := filepath.Join("a", "b", "c", "d", "e")
	repoRoot, wt := lendFixture(t,
		filepath.Join(hidden, "node_modules"),
		filepath.Join(deep, "node_modules"))
	writePackageJSON(t, wt, "uninstalled")
	writePackageJSON(t, wt, hidden)
	writePackageJSON(t, wt, deep)

	lendDependencies(repoRoot, wt)

	for _, dir := range []string{"uninstalled", hidden, deep} {
		if _, err := os.Lstat(filepath.Join(wt, dir, "node_modules")); !os.IsNotExist(err) {
			t.Errorf("%s/node_modules exists (err=%v), want nothing lent", dir, err)
		}
	}
}

// An explicit knob is the operator's exact list: discovery stays off.
func TestLendDependencies_ExplicitEnvSkipsNestedDiscovery(t *testing.T) {
	repoRoot, wt := lendFixture(t, "node_modules", filepath.Join("web", "node_modules"))
	writePackageJSON(t, wt, "web")
	t.Setenv(LendEnv, "node_modules")

	lendDependencies(repoRoot, wt)

	if _, err := os.Lstat(filepath.Join(wt, "web", "node_modules")); !os.IsNotExist(err) {
		t.Errorf("web/node_modules was discovered although %s named an explicit list", LendEnv)
	}
}

func TestNestedNodeModules(t *testing.T) {
	wt := t.TempDir()
	writePackageJSON(t, wt, ".")
	writePackageJSON(t, wt, filepath.Join("tools", "app", "web"))
	writePackageJSON(t, wt, filepath.Join("pkg", "node_modules", "dep"))

	got := nestedNodeModules(wt)
	want := filepath.Join("tools", "app", "web", "node_modules")
	if len(got) != 1 || got[0] != want {
		t.Errorf("nestedNodeModules = %v, want [%s]", got, want)
	}
}

// phaserun reads untracked paths as leftover work unless ManagerPlacedPaths
// names them. A lent nested node_modules it missed would make every worktree
// look dirty and block the next run.
func TestManagerPlacedPathsIncludesNestedNodeModules(t *testing.T) {
	t.Setenv(LendEnv, "")
	wt := t.TempDir()
	writePackageJSON(t, wt, filepath.Join("tools", "app", "web"))

	want := "tools/app/web/node_modules"
	for _, got := range ManagerPlacedPaths(wt) {
		if got == want {
			return
		}
	}
	t.Errorf("ManagerPlacedPaths(%s) lacks %q", wt, want)
}
