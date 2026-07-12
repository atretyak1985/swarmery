package api

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// docsServer wires the docs handlers against an in-memory fs.FS — the same
// fs.FS seam the embedded docsfs content flows through in production.
func docsServer(t *testing.T, docs fs.FS) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	Routes(mux, &Handler{Docs: docs})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDocsListAndDetail(t *testing.T) {
	const onboardingMD = "# Onboarding a project onto swarmery\n\nThe one-command way.\n"
	srv := docsServer(t, fstest.MapFS{
		".gitkeep":      {Data: []byte("")},
		"ONBOARDING.md": {Data: []byte(onboardingMD)},
		"EXTENDING.md":  {Data: []byte("intro paragraph\n# Extending swarmery\nbody\n")},
		"NEUTRALITY.md": {Data: []byte("no heading at all\n")},
	})

	var list []struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
		File  string `json:"file"`
	}
	getJSON(t, srv.URL+"/api/docs", &list)

	want := []struct{ slug, title, file string }{
		{"onboarding", "Onboarding a project onto swarmery", "ONBOARDING.md"},
		{"extending", "Extending swarmery", "EXTENDING.md"},
		{"neutrality", "NEUTRALITY.md", "NEUTRALITY.md"}, // no "# " heading → filename fallback
	}
	if len(list) != len(want) {
		t.Fatalf("docs = %d, want %d (%+v)", len(list), len(want), list)
	}
	for i, w := range want {
		if list[i].Slug != w.slug || list[i].Title != w.title || list[i].File != w.file {
			t.Errorf("docs[%d] = %+v, want %+v", i, list[i], w)
		}
	}

	// Detail carries the full markdown.
	var detail struct {
		Slug     string `json:"slug"`
		Title    string `json:"title"`
		File     string `json:"file"`
		Markdown string `json:"markdown"`
	}
	getJSON(t, srv.URL+"/api/docs/onboarding", &detail)
	if detail.Slug != "onboarding" || detail.File != "ONBOARDING.md" ||
		detail.Title != "Onboarding a project onto swarmery" {
		t.Errorf("detail = %+v", detail)
	}
	if detail.Markdown != onboardingMD {
		t.Errorf("markdown = %q, want the full file content", detail.Markdown)
	}

	// Unknown slug → 404.
	resp, err := http.Get(srv.URL + "/api/docs/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", resp.StatusCode)
	}
}

// TestDocsEmptyEmbed pins the CI behavior: with only .gitkeep in the embed
// (no `make copy-docs` ran), /api/docs is an empty JSON array, not null.
func TestDocsEmptyEmbed(t *testing.T) {
	srv := docsServer(t, fstest.MapFS{".gitkeep": {Data: []byte("")}})
	resp, err := http.Get(srv.URL + "/api/docs")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := strings.TrimSpace(string(body)); got != "[]" {
		t.Errorf("empty docs body = %q, want []", got)
	}
}
