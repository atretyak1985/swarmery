package api

// Multi-repo freshness on the /api/tools architecture DTO.
//
// Four of the six real projects the dashboard indexes are multi-repo
// workspaces: their root has no .git, so headCommit was null, every freshness
// number was null, and the map read as fine forever. These tests pin the shape
// that replaced it — and, just as importantly, pin that single-repo projects
// did not move a byte.
//
// Split out of tools_dash_test.go rather than appended to it because the
// fixture here needs REAL git checkouts (archmap forks git), the way
// architecture_blast_test.go does — whose gitRun/writeFileIn helpers this file
// reuses.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/archmap"
)

// singleRepoDTOWireShape is the EXACT JSON a single-repo project has always
// sent. It is a literal, not a computed comparison: the whole point of the
// multi-repo work is that it is additive, and a golden string is the only
// assertion that catches a renamed key, a reordered field, or a `repos: []`
// leaking into projects that have no members.
const singleRepoDTOWireShape = `{"id":7,"slug":"single","name":"Single","hasMap":true,` +
	`"builtAt":"2026-07-24T00:00:00Z",` +
	`"mapPath":"/api/projects/7/architecture/architecture-map.html",` +
	`"analyzedAtCommit":"aabbccddee112233445566778899001122334455",` +
	`"headCommit":"aabbccddee112233445566778899001122334455",` +
	`"commitsBehind":0,"touchedModules":0,"moduleCount":1,"provision":null}`

func TestArchitectureDTOSingleRepoWireShapeUnchanged(t *testing.T) {
	root := t.TempDir()
	const sha = "aabbccddee112233445566778899001122334455"

	// A root .git in the loose-ref layout githead reads without forking.
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(root, "architecture-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	html := filepath.Join(out, "architecture-map.html")
	if err := os.WriteFile(html, []byte("<html>map</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// builtAt is an mtime — pin it so the golden string can be a literal.
	built := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(html, built, built); err != nil {
		t.Fatal(err)
	}
	mapJSON := `{"schemaVersion":1,"analyzedAtCommit":"` + sha + `",` +
		`"modules":[{"id":"m1","name":"M1","path":"src","layer":"l"}],"flows":[]}`
	if err := os.WriteFile(filepath.Join(out, "architecture-map.json"), []byte(mapJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// A project.json declaring members must be IGNORED once the root is itself
	// a checkout — otherwise a single-repo project could sprout a repos[].
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "project.json"),
		[]byte(`{"repos":["sub-a","sub-b"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "Single"
	d, ok := architectureDTO(7, "single", &name, root, false)
	if !ok {
		t.Fatal("architectureDTO returned ok=false for a project with an artifact")
	}
	if d.Repos != nil {
		t.Errorf("repos = %+v, want nil for a single-repo project", d.Repos)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != singleRepoDTOWireShape {
		t.Errorf("single-repo DTO changed shape.\n got: %s\nwant: %s", raw, singleRepoDTOWireShape)
	}
}

// seedMultiRepoProject turns `root` into a multi-repo workspace: a
// .claude/project.json declaring three members, two real git checkouts, and one
// member that is declared but absent. The map stamps analyzedAtCommits per
// repo, with repo-one one commit behind and repo-two exactly current.
//
// The two "internal/api" modules are the discriminator for path scoping: a
// member's `git diff` emits REPO-relative paths, so matching them without the
// member's own prefix would either hit the wrong module or hit nothing.
func seedMultiRepoProject(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	archmap.ResetMemo()
	t.Cleanup(archmap.ResetMemo)

	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "project.json"),
		[]byte(`{"name":"workspace","repos":["repo-one","repo-two","repo-gone"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// repo-one: two commits, so its HEAD is one past the analysed commit.
	one := filepath.Join(root, "repo-one")
	if err := os.MkdirAll(one, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, one, "init", "--initial-branch=main", ".")
	writeFileIn(t, one, "internal/api/routes.go", "package api\n")
	gitRun(t, one, "add", "-A")
	gitRun(t, one, "commit", "-m", "base")
	oneAnalyzed := gitRun(t, one, "rev-parse", "HEAD")
	writeFileIn(t, one, "internal/api/routes.go", "package api // touched\n")
	gitRun(t, one, "add", "-A")
	gitRun(t, one, "commit", "-m", "moved past the map")

	// repo-two: one commit, and the map is stamped with it — current.
	two := filepath.Join(root, "repo-two")
	if err := os.MkdirAll(two, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, two, "init", "--initial-branch=main", ".")
	writeFileIn(t, two, "internal/api/routes.go", "package api\n")
	gitRun(t, two, "add", "-A")
	gitRun(t, two, "commit", "-m", "base")
	twoAnalyzed := gitRun(t, two, "rev-parse", "HEAD")

	// repo-gone is declared and never created.

	out := filepath.Join(root, "architecture-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "architecture-map.html"), []byte("<html>map</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	mapJSON := `{"schemaVersion":1,"analyzedAt":"2026-09-08",` +
		`"analyzedAtCommit":"` + oneAnalyzed + `",` +
		`"analyzedAtCommits":{"repo-one":"` + oneAnalyzed + `","repo-two":"` + twoAnalyzed + `","repo-gone":"` + oneAnalyzed + `"},` +
		`"modules":[` +
		`{"id":"one-api","name":"One API","path":"repo-one/internal/api","layer":"server"},` +
		`{"id":"two-api","name":"Two API","path":"repo-two/internal/api","layer":"server"}],` +
		`"flows":[]}`
	if err := os.WriteFile(filepath.Join(out, "architecture-map.json"), []byte(mapJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(out)
		os.RemoveAll(one)
		os.RemoveAll(two)
		os.Remove(filepath.Join(root, ".claude", "project.json"))
	})
}

func TestToolsDashArchitectureMultiRepo(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	path := projectPath(t, srv.URL, "1")
	seedMultiRepoProject(t, path)

	ap := archDTOForProject(t, srv.URL, 1)

	if ap.HeadCommit != nil {
		t.Errorf("headCommit = %q, want null — a workspace root is not a checkout", *ap.HeadCommit)
	}
	if len(ap.Repos) != 3 {
		t.Fatalf("repos = %+v, want 3 entries (a missing member must be reported, not dropped)", ap.Repos)
	}
	byName := map[string]repoHeadDTO{}
	for _, r := range ap.Repos {
		byName[r.Name] = r
	}

	one, ok := byName["repo-one"]
	if !ok {
		t.Fatal("repo-one missing from repos[]")
	}
	if !one.OK || one.HeadCommit == nil || one.AnalyzedAtCommit == nil {
		t.Fatalf("repo-one = %+v, want ok with both commits resolved", one)
	}
	if one.CommitsBehind == nil || *one.CommitsBehind != 1 {
		t.Errorf("repo-one commitsBehind = %v, want 1", one.CommitsBehind)
	}
	if one.TouchedModules == nil || *one.TouchedModules != 1 {
		t.Errorf("repo-one touchedModules = %v, want 1 — the diff must be matched under the member's own path prefix", one.TouchedModules)
	}

	two, ok := byName["repo-two"]
	if !ok {
		t.Fatal("repo-two missing from repos[]")
	}
	if two.CommitsBehind == nil || *two.CommitsBehind != 0 {
		t.Errorf("repo-two commitsBehind = %v, want 0 (measured current, not unmeasurable)", two.CommitsBehind)
	}
	if two.TouchedModules == nil || *two.TouchedModules != 0 {
		t.Errorf("repo-two touchedModules = %v, want 0", two.TouchedModules)
	}

	gone, ok := byName["repo-gone"]
	if !ok {
		t.Fatal("repo-gone missing from repos[] — a member that is not on disk must still be listed")
	}
	if gone.OK {
		t.Error("repo-gone ok = true, want false")
	}
	if gone.HeadCommit != nil || gone.CommitsBehind != nil || gone.TouchedModules != nil {
		t.Errorf("repo-gone = %+v, want null commits — unknown is not 'current'", gone)
	}

	if ap.CommitsBehind == nil || *ap.CommitsBehind != 1 {
		t.Errorf("project commitsBehind = %v, want 1 (sum over measurable members)", ap.CommitsBehind)
	}
	if ap.TouchedModules == nil || *ap.TouchedModules != 1 {
		t.Errorf("project touchedModules = %v, want 1 (distinct modules across members)", ap.TouchedModules)
	}
	if ap.ModuleCount == nil || *ap.ModuleCount != 2 {
		t.Errorf("moduleCount = %v, want 2", ap.ModuleCount)
	}
}

// A multi-repo map that carries only the scalar analyzedAtCommit — the shape
// every such map has today — cannot say which member that commit belongs to.
// The members must still be listed (so the page can show the workspace), but
// every number stays null rather than being invented.
func TestToolsDashArchitectureMultiRepoScalarStamp(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)

	path := projectPath(t, srv.URL, "1")
	seedMultiRepoProject(t, path)

	// Rewrite the map without the per-repo stamps.
	out := filepath.Join(path, "architecture-out")
	mapJSON := `{"schemaVersion":1,"analyzedAt":"2026-09-08","analyzedAtCommit":"1097a7f",` +
		`"modules":[{"id":"one-api","name":"One API","path":"repo-one/internal/api","layer":"server"}],"flows":[]}`
	if err := os.WriteFile(filepath.Join(out, "architecture-map.json"), []byte(mapJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	ap := archDTOForProject(t, srv.URL, 1)
	if len(ap.Repos) != 3 {
		t.Fatalf("repos = %+v, want 3 entries", ap.Repos)
	}
	for _, r := range ap.Repos {
		if r.AnalyzedAtCommit != nil {
			t.Errorf("%s analyzedAtCommit = %q, want null — a scalar stamp says nothing per repo", r.Name, *r.AnalyzedAtCommit)
		}
		if r.CommitsBehind != nil || r.TouchedModules != nil {
			t.Errorf("%s = %+v, want null numbers", r.Name, r)
		}
	}
	if ap.CommitsBehind != nil {
		t.Errorf("commitsBehind = %d, want null — 0 would read as 'the map is current'", *ap.CommitsBehind)
	}
	if ap.TouchedModules != nil {
		t.Errorf("touchedModules = %d, want null", *ap.TouchedModules)
	}
}
