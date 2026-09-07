package repopath

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// mkRepo creates dir and marks it as a checkout. asFile writes .git as a FILE
// (a linked worktree / submodule) instead of a directory.
func mkRepo(t *testing.T, dir string, asFile bool) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(dir, ".git")
	if asFile {
		if err := os.WriteFile(git, []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTokens(t *testing.T) {
	tests := []struct {
		name string
		cell string
		want []string
	}{
		{
			name: "phase doc header: name plus absolute path, absolute first",
			cell: "`sk-next` (`/Volumes/Work/Skygor/sk-next`)",
			want: []string{"/Volumes/Work/Skygor/sk-next", "sk-next"},
		},
		{
			name: "README table cell with a parenthetical",
			cell: "sk-next (+ helm)",
			want: []string{"sk-next"},
		},
		{
			name: "several backticked repos keep document order",
			cell: "`sk-next` (+ Helm values in `sk-k8s-next` / `dk-infrastructure`)",
			want: []string{"sk-next", "sk-k8s-next", "dk-infrastructure"},
		},
		{name: "empty cell", cell: "", want: nil},
		{name: "em-dash placeholder", cell: "—", want: nil},
		{name: "n/a placeholder", cell: " N/A ", want: nil},
		{name: "bare name", cell: "swarmery", want: []string{"swarmery"}},
		{name: "comma list takes the head", cell: "sk-next, sk-controlbox", want: []string{"sk-next"}},
		{name: "bold markers stripped", cell: "**sk-next**", want: []string{"sk-next"}},
		// A slash is part of a nested repo path, not a list separator: cutting at it
		// would resolve the run to "tools/" and look like it worked.
		{name: "nested path survives", cell: "tools/swarmery", want: []string{"tools/swarmery"}},
		{name: "duplicates collapse", cell: "`sk-next` and `sk-next`", want: []string{"sk-next"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Tokens(tc.cell); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Tokens(%q) = %v, want %v", tc.cell, got, tc.want)
			}
		})
	}
}

// Primary must never hand back an absolute path: it is compared across phases as a
// repo IDENTITY, and one phase writing the path while another writes the name would
// otherwise read as a plan spanning two repos.
func TestPrimary(t *testing.T) {
	tests := []struct{ cell, want string }{
		{"`sk-next` (`/Volumes/Work/Skygor/sk-next`)", "sk-next"},
		{"sk-next (+ helm)", "sk-next"},
		{"`/Volumes/Work/Skygor/sk-next`", "sk-next"},
		{"", ""},
		{"—", ""},
	}
	for _, tc := range tests {
		if got := Primary(tc.cell); got != tc.want {
			t.Errorf("Primary(%q) = %q, want %q", tc.cell, got, tc.want)
		}
	}
}

func TestResolve_MultiRepoProject(t *testing.T) {
	proj := mkDir(t, filepath.Join(t.TempDir(), "Skygor")) // umbrella: no .git
	repo := mkRepo(t, filepath.Join(proj, "sk-next"), false)
	mkRepo(t, filepath.Join(proj, "sk-controlbox"), false)

	got, err := Resolve(proj, "`sk-next` (`"+repo+"`)")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !sameDir(t, got, repo) {
		t.Fatalf("Resolve = %q, want %q", got, repo)
	}
}

// The regression that matters most: every existing single-repo project must keep
// resolving to projects.path, even when a doc declares a repo that is not there.
func TestResolve_SingleRepoProjectIgnoresUnresolvableDeclaration(t *testing.T) {
	proj := mkRepo(t, filepath.Join(t.TempDir(), "swarmery"), false)

	got, err := Resolve(proj, "`nonexistent-repo`")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !sameDir(t, got, proj) {
		t.Fatalf("Resolve = %q, want the project path %q", got, proj)
	}
}

func TestResolve_DotGitFileIsARepo(t *testing.T) {
	proj := mkDir(t, filepath.Join(t.TempDir(), "proj"))
	repo := mkRepo(t, filepath.Join(proj, "linked"), true) // .git is a FILE

	got, err := Resolve(proj, "linked")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !sameDir(t, got, repo) {
		t.Fatalf("Resolve = %q, want %q", got, repo)
	}
}

func TestResolve_RefusesEscapeViaDotDot(t *testing.T) {
	base := t.TempDir()
	proj := mkDir(t, filepath.Join(base, "proj"))
	mkRepo(t, filepath.Join(base, "outside"), false)

	if got, err := Resolve(proj, "../outside"); !errors.Is(err, ErrNoRepoRoot) {
		t.Fatalf("Resolve = (%q, %v), want ErrNoRepoRoot", got, err)
	}
}

func TestResolve_RefusesEscapeViaSymlink(t *testing.T) {
	base := t.TempDir()
	proj := mkDir(t, filepath.Join(base, "proj"))
	outside := mkRepo(t, filepath.Join(base, "outside"), false)
	link := filepath.Join(proj, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The link LOOKS contained (its path is under proj) and it does hold a .git —
	// only symlink resolution before the containment check rejects it.
	if got, err := Resolve(proj, "escape"); !errors.Is(err, ErrNoRepoRoot) {
		t.Fatalf("Resolve = (%q, %v), want ErrNoRepoRoot", got, err)
	}
}

func TestResolve_FirstValidCellWins(t *testing.T) {
	proj := mkDir(t, filepath.Join(t.TempDir(), "proj"))
	first := mkRepo(t, filepath.Join(proj, "first"), false)
	mkRepo(t, filepath.Join(proj, "second"), false)

	got, err := Resolve(proj, "first", "second")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !sameDir(t, got, first) {
		t.Fatalf("Resolve = %q, want %q", got, first)
	}
}

func TestResolve_UnresolvableNamesTheCandidatesItTried(t *testing.T) {
	proj := mkDir(t, filepath.Join(t.TempDir(), "proj"))
	mkDir(t, filepath.Join(proj, "not-a-repo"))

	_, err := Resolve(proj, "not-a-repo")
	if !errors.Is(err, ErrNoRepoRoot) {
		t.Fatalf("err = %v, want ErrNoRepoRoot", err)
	}
	// The message replaces git's "fatal: not a git repository", so it has to name
	// what was checked — that is the entire diagnostic value.
	for _, want := range []string{"not-a-repo", proj} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolve_EmptyProjectPath(t *testing.T) {
	if _, err := Resolve(""); !errors.Is(err, ErrNoRepoRoot) {
		t.Fatalf("err = %v, want ErrNoRepoRoot", err)
	}
}

func TestFileHints(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "mainApp wins, a multi-entry repos list names no single root",
			path: write("multi.json", `{"mainApp":"sk-next","repos":["sk-next","sk-controlbox","docs"]}`),
			want: []string{"sk-next"},
		},
		{
			name: "sole repos entry is a usable hint",
			path: write("one.json", `{"repos":["only-repo"]}`),
			want: []string{"only-repo"},
		},
		{
			name: "both, mainApp first",
			path: write("both.json", `{"mainApp":"app","repos":["app"]}`),
			want: []string{"app", "app"},
		},
		{name: "missing file", path: filepath.Join(dir, "nope.json"), want: nil},
		{name: "unparseable", path: write("bad.json", `{`), want: nil},
		{name: "declares nothing", path: write("empty.json", `{"name":"x"}`), want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FileHints(tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("FileHints = %v, want %v", got, tc.want)
			}
		})
	}
}

// sameDir compares paths after symlink resolution — macOS /var → /private/var
// makes a raw string compare flaky in exactly the tests that matter.
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

func TestInheritedSettings(t *testing.T) {
	base := t.TempDir()
	project := mkDir(t, filepath.Join(base, "Umbrella"))
	repo := mkRepo(t, filepath.Join(project, "app"), false)
	settings := filepath.Join(project, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"enabledPlugins":{"core@swarmery":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := mkDir(t, filepath.Join(base, "wt"))

	t.Run("multi-repo run inherits the project settings", func(t *testing.T) {
		if got := InheritedSettings(project, repo, worktree); got != settings {
			t.Fatalf("InheritedSettings = %q, want %q", got, settings)
		}
	})

	// Changing this case would alter behaviour for every single-repo project in
	// the registry, which is exactly what the run-root fallback exists to avoid.
	t.Run("single-repo run inherits nothing", func(t *testing.T) {
		if got := InheritedSettings(project, project, worktree); got != "" {
			t.Fatalf("InheritedSettings = %q, want \"\"", got)
		}
	})

	t.Run("a worktree with its own settings keeps them", func(t *testing.T) {
		own := mkDir(t, filepath.Join(base, "wt-own", ".claude"))
		if err := os.WriteFile(filepath.Join(own, "settings.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := InheritedSettings(project, repo, filepath.Dir(own)); got != "" {
			t.Fatalf("InheritedSettings = %q, want \"\" — the repo's own statement is more specific", got)
		}
	})

	t.Run("no project settings file", func(t *testing.T) {
		bare := mkDir(t, filepath.Join(base, "Bare"))
		bareRepo := mkRepo(t, filepath.Join(bare, "app"), false)
		if got := InheritedSettings(bare, bareRepo, worktree); got != "" {
			t.Fatalf("InheritedSettings = %q, want \"\"", got)
		}
	})
}

// The bug this guards: Resolve returns an EvalSymlinks'd path while projects.path
// is stored raw, so on macOS (/var → /private/var) a Clean comparison called a
// single-repo project multi-repo and lent it settings it must not inherit.
func TestSameDir_ResolvesSymlinks(t *testing.T) {
	base := t.TempDir()
	real := mkDir(t, filepath.Join(base, "real"))
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !SameDir(real, link) {
		t.Error("SameDir(real, symlink) = false, want true")
	}
	if !SameDir(real, real+"/.") {
		t.Error("SameDir does not normalise a trailing element")
	}
	if SameDir(real, mkDir(t, filepath.Join(base, "other"))) {
		t.Error("SameDir on two different dirs = true")
	}
	if SameDir("", real) || SameDir(real, "") {
		t.Error("SameDir with an empty path = true")
	}
}

// The cross-project case: a phase in one project's plan declares another
// REGISTERED project's checkout. The registry is the allow-list, so the run goes
// where the doc says instead of silently falling back to the project path.
func TestResolveTrusted_AcceptsRegisteredProjectOutside(t *testing.T) {
	base := t.TempDir()
	proj := mkRepo(t, filepath.Join(base, "english-grammar"), false)
	other := mkRepo(t, filepath.Join(base, "swarmery"), false)

	got, err := ResolveTrusted(proj, []string{other}, "`"+other+"`")
	if err != nil {
		t.Fatalf("ResolveTrusted: %v", err)
	}
	if !sameDir(t, got, other) {
		t.Fatalf("ResolveTrusted = %q, want the registered checkout %q", got, other)
	}
}

// The six-wasted-runs regression: a declared path that IS a git repository but is
// neither inside the project nor registered must be refused at admission — NOT
// fall through to the project checkout, which is a repo and would happily accept
// the run (and then execute a phase written for a different codebase).
func TestResolve_DeclaredRepoOutsideIsRefusedLoudly(t *testing.T) {
	base := t.TempDir()
	proj := mkRepo(t, filepath.Join(base, "proj"), false)
	outside := mkRepo(t, filepath.Join(base, "outside"), false)

	got, err := Resolve(proj, "`"+outside+"`")
	if !errors.Is(err, ErrRepoOutsideProject) {
		t.Fatalf("Resolve = (%q, %v), want ErrRepoOutsideProject", got, err)
	}
	if !errors.Is(err, ErrNoRepoRoot) {
		t.Fatalf("err = %v, must also wrap ErrNoRepoRoot for callers that only know the old sentinel", err)
	}
	for _, want := range []string{"outside", "register"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A trusted root list that does not include the declared repo changes nothing:
// the refusal stands. (Guards against "trusted" degrading into "anything goes".)
func TestResolveTrusted_UntrustedOutsideStillRefused(t *testing.T) {
	base := t.TempDir()
	proj := mkRepo(t, filepath.Join(base, "proj"), false)
	outside := mkRepo(t, filepath.Join(base, "outside"), false)
	trustedElsewhere := mkRepo(t, filepath.Join(base, "elsewhere"), false)

	if _, err := ResolveTrusted(proj, []string{trustedElsewhere}, "`"+outside+"`"); !errors.Is(err, ErrRepoOutsideProject) {
		t.Fatalf("err = %v, want ErrRepoOutsideProject", err)
	}
}

// A declared repo that is simply not on disk keeps falling through (the
// single-repo compatibility guarantee) — only a REAL repository outside is loud.
func TestResolveTrusted_MissingDeclarationStillFallsThrough(t *testing.T) {
	base := t.TempDir()
	proj := mkRepo(t, filepath.Join(base, "proj"), false)

	got, err := ResolveTrusted(proj, []string{filepath.Join(base, "nope")}, "`"+filepath.Join(base, "ghost")+"`")
	if err != nil {
		t.Fatalf("ResolveTrusted: %v", err)
	}
	if !sameDir(t, got, proj) {
		t.Fatalf("ResolveTrusted = %q, want the project path %q", got, proj)
	}
}
