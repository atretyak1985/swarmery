package api

// Isolation contract for repository artifacts served by the jailed static
// routes (architecture-out/, graphify-out/): every response must run in a
// CSP sandbox with an opaque origin — never on the daemon's own origin — and
// a symlink inside the artifact dir must not reach a file outside it.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertSandboxed checks the two headers every jailed artifact response carries.
func assertSandboxed(t *testing.T, res *http.Response, what string) {
	t.Helper()
	csp := res.Header.Values("Content-Security-Policy")
	if len(csp) != 1 || csp[0] != "sandbox allow-scripts; frame-ancestors 'self'" {
		t.Errorf("%s: Content-Security-Policy = %q, want exactly the sandbox + frame-ancestors policy", what, csp)
	}
	if strings.Contains(strings.Join(csp, ","), "allow-same-origin") {
		t.Errorf("%s: CSP must never grant allow-same-origin: %q", what, csp)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", what, got)
	}
}

func getResponse(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(raw)
}

func TestEmbedArtifactSandboxHeaders(t *testing.T) {
	srv, _ := projectsTestServer(t)
	seedArchitectureOut(t, srv.URL)
	seedGraphifyOut(t, srv.URL)

	for _, path := range []string{
		"/api/projects/1/architecture/architecture-map.html",
		"/api/projects/1/architecture/", // default document
		"/api/projects/1/architecture/architecture-map.json",
		"/api/projects/1/graphify/graph.html",
		"/api/projects/1/graphify/", // default document
		"/api/projects/1/graphify/sub/data.json",
	} {
		res, _ := getResponse(t, srv.URL+path)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, res.StatusCode)
		}
		assertSandboxed(t, res, path)
	}

	// HEAD rides along with GET and must carry the same isolation.
	req, err := http.NewRequest(http.MethodHead, srv.URL+"/api/projects/1/architecture/architecture-map.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	assertSandboxed(t, res, "HEAD architecture-map.html")
}

func TestJailedStaticRefusesSymlinkEscape(t *testing.T) {
	srv, _ := projectsTestServer(t)
	project := seedArchitectureOut(t, srv.URL)
	seedGraphifyOut(t, srv.URL)

	const secret = "outside-the-artifact-dir-marker"
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "inner.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file symlink and a directory symlink, both pointing out of the jail.
	for link, target := range map[string]string{
		filepath.Join(project, "architecture-out", "leak.html"): outside,
		filepath.Join(project, "architecture-out", "leakdir"):   outsideDir,
		filepath.Join(project, "graphify-out", "leak.html"):     outside,
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink that stays inside the artifact dir keeps working.
	if err := os.Symlink("architecture-map.html", filepath.Join(project, "architecture-out", "alias.html")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/projects/1/architecture/leak.html",
		"/api/projects/1/architecture/leakdir/inner.txt",
		"/api/projects/1/graphify/leak.html",
	} {
		res, body := getResponse(t, srv.URL+path)
		if res.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200, want a refusal for a symlink leaving the artifact dir", path)
		}
		if strings.Contains(body, secret) {
			t.Errorf("GET %s served bytes from outside the artifact dir", path)
		}
	}

	res, body := getResponse(t, srv.URL+"/api/projects/1/architecture/alias.html")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "arch") {
		t.Errorf("in-jail symlink = %d %q, want 200 + the target's content", res.StatusCode, body)
	}
}

// A symlinked artifact ROOT that points outside the project must not become
// the jail: every file under the link's target would otherwise be in reach.
func TestJailedStaticRefusesSymlinkedRoot(t *testing.T) {
	srv, _ := projectsTestServer(t)
	project := projectPath(t, srv.URL, "1")

	const secret = "outside-the-project-marker"
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "graph.html"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(project, "graphify-out")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/projects/1/graphify/graph.html", "/api/projects/1/graphify/"} {
		res, body := getResponse(t, srv.URL+path)
		if res.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200, want a refusal for an artifact root outside the project", path)
		}
		if strings.Contains(body, secret) {
			t.Errorf("GET %s served bytes from outside the project", path)
		}
	}

	// A root symlinked to a dir INSIDE the project keeps working.
	inside := filepath.Join(project, "build", "arch")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "architecture-map.html"), []byte("<html>arch-inside</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(project, "architecture-out")); err != nil {
		t.Fatal(err)
	}
	res, body := getResponse(t, srv.URL+"/api/projects/1/architecture/architecture-map.html")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "arch-inside") {
		t.Errorf("in-project symlinked root = %d %q, want 200 + content", res.StatusCode, body)
	}
}
