package api

// Tests for the numeric-freshness DTO fields and GET …/architecture/blast.
// Both need a REAL git repo (archmap forks git rather than reading .git files),
// so these build one in the managed project's temp dir rather than seeding the
// loose-ref fake the older tools_dash tests use for githead.

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/archmap"
)

// fixtureMapJSON is the archmap fixture, reused here so module ids and paths
// stay in one place.
func fixtureMapJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "architecture-map.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// gitRun executes git in dir with a pinned identity and no global config, so
// the test does not inherit the developer's commit.gpgsign or init.defaultBranch.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFileIn(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedArchProject turns the managed project's directory into a git repo on
// `main` with the fixture map installed, then puts two more commits on a
// feature branch that touch two of the fixture's modules. It returns the repo
// path and the sha the map claims to have been analysed at (the `main` tip).
func seedArchProject(t *testing.T, path string) (repo, analyzed string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	archmap.ResetMemo()
	t.Cleanup(archmap.ResetMemo)

	// Fresh .git — the managed project dir is reused across subtests in a run.
	os.RemoveAll(filepath.Join(path, ".git"))
	os.RemoveAll(filepath.Join(path, "architecture-out"))
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(path, ".git"))
		os.RemoveAll(filepath.Join(path, "architecture-out"))
	})

	gitRun(t, path, "init", "--initial-branch=main", ".")
	// architecture-out is a BUILD artifact, not source. Ignoring it keeps it
	// out of the diff (otherwise the map would report itself as touched) and,
	// more importantly, lets it survive the `git checkout main` the fresh-map
	// subtest does.
	writeFileIn(t, path, ".gitignore", "architecture-out/\n")
	writeFileIn(t, path, "README.md", "one\n")
	writeFileIn(t, path, "internal/api/routes.go", "package api\n")
	writeFileIn(t, path, "web/src/main.tsx", "export {};\n")
	gitRun(t, path, "add", "-A")
	gitRun(t, path, "commit", "-m", "base")
	analyzed = gitRun(t, path, "rev-parse", "HEAD")

	// The artifact: HTML (what makes hasMap true) + the fixture JSON stamped
	// with the base sha so the map claims to describe `main`.
	out := filepath.Join(path, "architecture-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "architecture-map.html"), []byte("<html>map</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(fixtureMapJSON(t), &m); err != nil {
		t.Fatal(err)
	}
	m["analyzedAtCommit"] = analyzed
	stamped, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "architecture-map.json"), stamped, 0o644); err != nil {
		t.Fatal(err)
	}

	// Two commits on a branch, touching web/src/pages (module web-pages, and
	// the render-page flow's first step) and internal/api (module api).
	gitRun(t, path, "checkout", "-b", "feature")
	writeFileIn(t, path, "web/src/pages/Home.tsx", "export const Home = () => null;\n")
	gitRun(t, path, "add", "-A")
	gitRun(t, path, "commit", "-m", "page")
	writeFileIn(t, path, "internal/api/routes.go", "package api // touched\n")
	writeFileIn(t, path, "CHANGELOG.md", "unmatched by every module path\n")
	gitRun(t, path, "add", "-A")
	gitRun(t, path, "commit", "-m", "api + changelog")

	return path, analyzed
}

func archDTOForProject(t *testing.T, srvURL string, id int64) architectureProjectDTO {
	t.Helper()
	resp := getToolsResponse(t, srvURL)
	for _, p := range resp.Architecture.Projects {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("project %d not in architecture.projects (%+v)", id, resp.Architecture.Projects)
	return architectureProjectDTO{}
}

// TestArchitectureDTOFreshness is the DTO half of the phase: real numbers when
// the repo and the map both answer, and nil — never 0 — when either does not.
func TestArchitectureDTOFreshness(t *testing.T) {
	t.Run("stale — real commitsBehind and touchedModules", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)
		path := projectPath(t, srv.URL, "1")
		seedArchProject(t, path)

		ap := archDTOForProject(t, srv.URL, 1)
		if ap.ModuleCount == nil {
			t.Fatal("moduleCount = null, want the fixture's 5")
		}
		if *ap.ModuleCount != 5 {
			t.Errorf("moduleCount = %d, want 5", *ap.ModuleCount)
		}
		if ap.CommitsBehind == nil {
			t.Fatal("commitsBehind = null on a stale map with a real repo, want 2")
		}
		if *ap.CommitsBehind != 2 {
			t.Errorf("commitsBehind = %d, want 2", *ap.CommitsBehind)
		}
		if ap.TouchedModules == nil {
			t.Fatal("touchedModules = null, want 2 (web-pages + api)")
		}
		if *ap.TouchedModules != 2 {
			t.Errorf("touchedModules = %d, want 2 (web-pages + api)", *ap.TouchedModules)
		}
	})

	t.Run("fresh — zero behind is a measurement, not a fallback", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)
		path := projectPath(t, srv.URL, "1")
		seedArchProject(t, path)
		// Rewind to the analysed commit: HEAD == analyzedAtCommit.
		gitRun(t, path, "checkout", "main")

		ap := archDTOForProject(t, srv.URL, 1)
		if ap.CommitsBehind == nil || *ap.CommitsBehind != 0 {
			t.Errorf("commitsBehind = %v, want 0 when HEAD == analyzedAtCommit", derefInt(ap.CommitsBehind))
		}
		if ap.TouchedModules == nil || *ap.TouchedModules != 0 {
			t.Errorf("touchedModules = %v, want 0 for a current map", derefInt(ap.TouchedModules))
		}
		if ap.ModuleCount == nil || *ap.ModuleCount != 5 {
			t.Errorf("moduleCount = %v, want 5", derefInt(ap.ModuleCount))
		}
	})

	t.Run("no git — commitsBehind and touchedModules null, moduleCount survives", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)
		path := projectPath(t, srv.URL, "1")

		// The artifact without any .git at all.
		os.RemoveAll(filepath.Join(path, ".git"))
		out := filepath.Join(path, "architecture-out")
		os.RemoveAll(out)
		t.Cleanup(func() { os.RemoveAll(out) })
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "architecture-map.html"), []byte("<html>map</html>"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "architecture-map.json"), fixtureMapJSON(t), 0o644); err != nil {
			t.Fatal(err)
		}

		ap := archDTOForProject(t, srv.URL, 1)
		if ap.HeadCommit != nil {
			t.Errorf("headCommit = %q with no .git, want null", *ap.HeadCommit)
		}
		if ap.CommitsBehind != nil {
			t.Errorf("commitsBehind = %d with no git, want null (0 would read as 'current')", *ap.CommitsBehind)
		}
		if ap.TouchedModules != nil {
			t.Errorf("touchedModules = %d with no git, want null", *ap.TouchedModules)
		}
		if ap.ModuleCount == nil || *ap.ModuleCount != 5 {
			t.Errorf("moduleCount = %v, want 5 — it is a property of the artifact alone", derefInt(ap.ModuleCount))
		}
	})

	t.Run("unparseable map — every number null, project still listed", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)
		path := projectPath(t, srv.URL, "1")
		seedArchProject(t, path)

		// Corrupt the JSON, keep the HTML and the repo.
		bad := filepath.Join(path, "architecture-out", "architecture-map.json")
		if err := os.WriteFile(bad, []byte("{ not json"), 0o644); err != nil {
			t.Fatal(err)
		}

		ap := archDTOForProject(t, srv.URL, 1)
		if !ap.HasMap {
			t.Error("hasMap = false, want true — the HTML is still there")
		}
		if ap.ModuleCount != nil {
			t.Errorf("moduleCount = %d for an unparseable map, want null", *ap.ModuleCount)
		}
		if ap.TouchedModules != nil {
			t.Errorf("touchedModules = %d for an unparseable map, want null", *ap.TouchedModules)
		}
		// analyzedAtCommit is unreadable too, so there is no range to measure.
		if ap.CommitsBehind != nil {
			t.Errorf("commitsBehind = %d with no analyzedAtCommit, want null", *ap.CommitsBehind)
		}
	})
}

func derefInt(p *int) any {
	if p == nil {
		return "null"
	}
	return *p
}

func TestArchitectureBlast(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)
	path := projectPath(t, srv.URL, "1")
	seedArchProject(t, path)

	var got blastResponse
	getJSON(t, srv.URL+"/api/projects/1/architecture/blast", &got)

	if got.Base != "main" {
		t.Errorf("base = %q, want \"main\" (the repo's default branch)", got.Base)
	}
	if got.Head == "" {
		t.Error("head is empty, want the feature branch tip")
	}
	// Home.tsx, routes.go, CHANGELOG.md.
	if got.Files != 3 {
		t.Errorf("files = %d, want 3", got.Files)
	}

	ids := map[string][]string{}
	for _, m := range got.Touched.Modules {
		ids[m.ID] = m.Files
	}
	if len(ids) != 2 {
		t.Fatalf("touched modules = %+v, want web-pages and api", got.Touched.Modules)
	}
	if f, ok := ids["web-pages"]; !ok {
		t.Error("web-pages not touched; the branch added web/src/pages/Home.tsx")
	} else if len(f) != 1 || f[0] != "web/src/pages/Home.tsx" {
		t.Errorf("web-pages files = %v, want [web/src/pages/Home.tsx]", f)
	}
	if _, ok := ids["api"]; !ok {
		t.Error("api not touched; the branch changed internal/api/routes.go")
	}
	// web/src/main.tsx was NOT changed on the branch, so the parent module must
	// stay out of the strip — this is the longest-prefix rule end to end.
	if _, ok := ids["web"]; ok {
		t.Error("parent module web was reported touched; only web/src/pages changed")
	}

	if len(got.Touched.Flows) != 1 || got.Touched.Flows[0].ID != "render-page" {
		t.Errorf("touched flows = %+v, want just render-page", got.Touched.Flows)
	}
	if len(got.Touched.Unmatched) != 1 || got.Touched.Unmatched[0] != "CHANGELOG.md" {
		t.Errorf("unmatched = %v, want [CHANGELOG.md]", got.Touched.Unmatched)
	}
}

func TestArchitectureBlastDegrades(t *testing.T) {
	t.Run("no map — 404, not an empty blast", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)
		path := projectPath(t, srv.URL, "1")
		seedArchProject(t, path)
		if err := os.Remove(filepath.Join(path, "architecture-out", "architecture-map.json")); err != nil {
			t.Fatal(err)
		}

		res, err := http.Get(srv.URL + "/api/projects/1/architecture/blast")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404 — an empty Touched would read as 'this branch changed nothing'", res.StatusCode)
		}
	})

	t.Run("not a git repo — 409, never 500", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)
		path := projectPath(t, srv.URL, "1")
		archmap.ResetMemo()
		t.Cleanup(archmap.ResetMemo)
		os.RemoveAll(filepath.Join(path, ".git"))
		out := filepath.Join(path, "architecture-out")
		os.RemoveAll(out)
		t.Cleanup(func() { os.RemoveAll(out) })
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "architecture-map.json"), fixtureMapJSON(t), 0o644); err != nil {
			t.Fatal(err)
		}

		res, err := http.Get(srv.URL + "/api/projects/1/architecture/blast")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusConflict {
			t.Errorf("status = %d, want 409", res.StatusCode)
		}
	})

	t.Run("unknown project — 404", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)

		res, err := http.Get(srv.URL + "/api/projects/9999/architecture/blast")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", res.StatusCode)
		}
	})

	t.Run("option-like base is refused, not passed to argv", func(t *testing.T) {
		srv, _ := projectsTestServer(t)
		attachStubToolManager(t)
		stubLookPath(t, nil)
		path := projectPath(t, srv.URL, "1")
		seedArchProject(t, path)

		res, err := http.Get(srv.URL + "/api/projects/1/architecture/blast?base=--upload-pack%3Decho")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusConflict {
			t.Errorf("status = %d, want 409 for an option-like revision", res.StatusCode)
		}
	})
}

// TestArchitectureBlastRouteOrdering is the ordering assertion the phase asks
// for: …/architecture/blast must reach the blast handler, NOT the static jail
// registered for …/architecture/{rest...}. If the jail won, the request would
// come back as a 404 for a missing FILE named "blast" inside architecture-out,
// so the discriminator is the JSON body, not merely the status.
func TestArchitectureBlastRouteOrdering(t *testing.T) {
	srv, _ := projectsTestServer(t)
	attachStubToolManager(t)
	stubLookPath(t, nil)
	path := projectPath(t, srv.URL, "1")
	seedArchProject(t, path)

	// Plant a decoy FILE called "blast" in the jail's root. The static jail
	// would happily serve it; the blast route must shadow it.
	decoy := filepath.Join(path, "architecture-out", "blast")
	if err := os.WriteFile(decoy, []byte("STATIC JAIL SERVED THIS"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := http.Get(srv.URL + "/api/projects/1/architecture/blast")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var got blastResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("body is not a blastResponse — the static jail won the route: %v", err)
	}
	if got.Base == "" {
		t.Error("base is empty; the response did not come from architectureBlast")
	}

	// The sibling map artifact still routes to the jail, so the specific
	// registration shadows only its own literal segment.
	res2, err := http.Get(srv.URL + "/api/projects/1/architecture/architecture-map.html")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("architecture-map.html status = %d, want 200 — the jail must still serve siblings", res2.StatusCode)
	}
}
