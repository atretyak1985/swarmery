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

// TestDocsGuidesOrder pins the three-band nav order the dashboard rail relies
// on: guides (docOrder 0–4) ahead of the pinned reference docs (10–14), and
// both ahead of anything unpinned, which falls back to alphabetical.
//
// The rail groups client-side on the `guide-` slug prefix, so this ordering is
// its precondition: if a guide ever sorted into the middle of the reference
// band, the two groups would still render but in a nonsensical order.
func TestDocsGuidesOrder(t *testing.T) {
	srv := docsServer(t, fstest.MapFS{
		".gitkeep": {Data: []byte("")},
		// Deliberately seeded out of order, and with the alphabetically-first
		// name (`aaa.md`) unpinned, so passing cannot be an accident of
		// ReadDir order or of a plain alphabetical sort.
		"zzz.md":                {Data: []byte("# Zulu\n")},
		"onboarding.md":         {Data: []byte("# Onboarding\n")},
		"aaa.md":                {Data: []byte("# Alpha\n")},
		"guide-plans.md":        {Data: []byte("# Plans and the board\n")},
		"neutrality.md":         {Data: []byte("# Neutrality\n")},
		"guide-getting-started": {Data: []byte("not markdown, must be ignored\n")},
	})

	var list []struct {
		Slug string `json:"slug"`
	}
	getJSON(t, srv.URL+"/api/docs", &list)

	want := []string{"guide-plans", "onboarding", "neutrality", "aaa", "zzz"}
	if len(list) != len(want) {
		t.Fatalf("docs = %d, want %d (%+v)", len(list), len(want), list)
	}
	for i, w := range want {
		if list[i].Slug != w {
			t.Errorf("docs[%d].slug = %q, want %q (full order %+v)", i, list[i].Slug, w, list)
		}
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

// docEntry is the wire shape of one doc, list item and detail alike (markdown
// is empty on list items).
type docEntry struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	File     string `json:"file"`
	Lang     string `json:"lang"`
	Markdown string `json:"markdown"`
}

// ukDocsFS is an embed with one translated doc (ONBOARDING), one untranslated
// doc (EXTENDING) and one uk file with no English pair (ORPHAN), which must
// never be served: the English file is the source of truth for existence.
func ukDocsFS() fstest.MapFS {
	return fstest.MapFS{
		".gitkeep":         {Data: []byte("")},
		"ONBOARDING.md":    {Data: []byte("# Onboarding a project onto swarmery\n\nThe one-command way.\n")},
		"EXTENDING.md":     {Data: []byte("# Extending swarmery\n\nbody\n")},
		"uk/.gitkeep":      {Data: []byte("")},
		"uk/ONBOARDING.md": {Data: []byte("# Підключення проєкту до swarmery\n\nОдна команда.\n")},
		"uk/ORPHAN.md":     {Data: []byte("# Сирота\n")},
	}
}

// TestDocsLangUkList: `?lang=uk` swaps the title of a translated doc and keeps
// an untranslated one in English, each labelled with the language it is
// actually in; the slug set and order are the English ones.
func TestDocsLangUkList(t *testing.T) {
	srv := docsServer(t, ukDocsFS())

	var list []docEntry
	getJSON(t, srv.URL+"/api/docs?lang=uk", &list)

	want := []docEntry{
		{Slug: "onboarding", Title: "Підключення проєкту до swarmery", File: "ONBOARDING.md", Lang: "uk"},
		{Slug: "extending", Title: "Extending swarmery", File: "EXTENDING.md", Lang: "en"},
	}
	if len(list) != len(want) {
		t.Fatalf("docs = %d, want %d (%+v) — a uk file with no English pair must not be listed",
			len(list), len(want), list)
	}
	for i, w := range want {
		if list[i] != w {
			t.Errorf("docs[%d] = %+v, want %+v", i, list[i], w)
		}
	}
}

// TestDocsLangUkDetail: the detail of a translated doc carries the uk markdown
// with lang "uk"; an untranslated one falls back to English with lang "en";
// a slug that exists only in uk/ is still a 404.
func TestDocsLangUkDetail(t *testing.T) {
	fsys := ukDocsFS()
	srv := docsServer(t, fsys)

	var uk docEntry
	getJSON(t, srv.URL+"/api/docs/onboarding?lang=uk", &uk)
	if uk.Lang != "uk" || uk.Title != "Підключення проєкту до swarmery" ||
		uk.Markdown != string(fsys["uk/ONBOARDING.md"].Data) || uk.File != "ONBOARDING.md" {
		t.Errorf("translated detail = %+v, want the uk twin with lang uk", uk)
	}

	var en docEntry
	getJSON(t, srv.URL+"/api/docs/extending?lang=uk", &en)
	if en.Lang != "en" || en.Title != "Extending swarmery" || en.Markdown != string(fsys["EXTENDING.md"].Data) {
		t.Errorf("untranslated detail = %+v, want the English original with lang en", en)
	}

	for _, slug := range []string{"orphan", "nope"} {
		resp, err := http.Get(srv.URL + "/api/docs/" + slug + "?lang=uk")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s?lang=uk status = %d, want 404", slug, resp.StatusCode)
		}
	}
}

// TestDocsLangDefaultIsEnglish: no `lang`, an empty one or an unknown one is
// the pre-translation behaviour — English text, lang "en", even when a uk twin
// exists.
func TestDocsLangDefaultIsEnglish(t *testing.T) {
	fsys := ukDocsFS()
	srv := docsServer(t, fsys)

	for _, q := range []string{"", "?lang=", "?lang=en", "?lang=fr", "?lang=UK"} {
		var list []docEntry
		getJSON(t, srv.URL+"/api/docs"+q, &list)
		if len(list) != 2 || list[0].Title != "Onboarding a project onto swarmery" ||
			list[0].Lang != "en" || list[1].Lang != "en" {
			t.Errorf("list%s = %+v, want English titles with lang en", q, list)
		}

		var d docEntry
		getJSON(t, srv.URL+"/api/docs/onboarding"+q, &d)
		if d.Lang != "en" || d.Markdown != string(fsys["ONBOARDING.md"].Data) {
			t.Errorf("detail%s = %+v, want the English markdown with lang en", q, d)
		}
	}
}

// brokenUKFS fails every open under uk/ with something other than "not exist".
// It wraps rather than embeds the MapFS: an embedded MapFS would promote its
// ReadFile, and fs.ReadFile would take that shortcut around this Open.
type brokenUKFS struct{ base fstest.MapFS }

func (b brokenUKFS) Open(name string) (fs.File, error) {
	if strings.HasPrefix(name, "uk/") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return b.base.Open(name)
}

// TestDocsLangUkReadErrorIsNotFallback: only a MISSING twin falls back to
// English; any other read failure is a 500, so a broken embed is visible
// instead of silently serving English under a Ukrainian interface.
func TestDocsLangUkReadErrorIsNotFallback(t *testing.T) {
	srv := docsServer(t, brokenUKFS{base: fstest.MapFS{
		"ONBOARDING.md": {Data: []byte("# Onboarding\n")},
	}})
	for _, path := range []string{"/api/docs?lang=uk", "/api/docs/onboarding?lang=uk"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s status = %d, want 500", path, resp.StatusCode)
		}
	}
	// English requests never touch uk/ and keep working.
	var list []docEntry
	getJSON(t, srv.URL+"/api/docs", &list)
	if len(list) != 1 || list[0].Lang != "en" {
		t.Errorf("English list = %+v", list)
	}
}
