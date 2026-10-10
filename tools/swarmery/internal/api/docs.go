package api

// Parity wave: markdown docs endpoints, backed by the go:embed snapshot in
// internal/docsfs (populated by `make copy-docs` during build/dev).
//
// Response shapes are FROZEN by the parity contract, plus one additive field:
//   list item: {"slug","title","file","lang"}   detail adds: {"markdown"}
//
// slug  = lowercased basename without .md
// title = first "# " heading line (fallback: the file name)
// lang  = the language of the title/markdown actually returned ("en" | "uk")
// An empty embed (fresh clone / CI) yields [].
//
// Translations: `?lang=uk` swaps each doc's title and markdown for its twin in
// the embed's uk/ subdir (content/uk/<file>, snapshotted by `make copy-docs`)
// when one exists. The English file stays the source of truth for slug, order
// and existence — a doc nobody translated yet is still listed, in English, with
// lang "en", and a uk file with no English pair is never served. Any other
// `lang` value, or none, is the English behaviour from before translations.

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"math"
	"net/http"
	"sort"
	"strings"
)

type docDTO struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	File  string `json:"file"`
	Lang  string `json:"lang"`
}

const (
	docLangEN = "en"
	docLangUK = "uk"
	// docUKDir is the embed subdir holding the Ukrainian twins (Makefile
	// copy-docs → DOCS_UK_DST). readDocs skips directories, so it is never
	// listed as a doc of its own.
	docUKDir = "uk"
)

// docLang is the language a request asked for: "uk" only when it says so
// exactly, English for anything else (absent, empty, unknown).
func docLang(r *http.Request) string {
	if r.URL.Query().Get("lang") == docLangUK {
		return docLangUK
	}
	return docLangEN
}

type docDetailDTO struct {
	docDTO
	Markdown string `json:"markdown"`
}

// docOrder pins the dashboard nav order: the illustrated guides first, in
// reading order, then the reference docs (onboarding → concepts → workflow →
// extending → neutrality); anything else sorts alphabetically after both.
//
// The guides follow the sidebar: one per place, in sidebar order (Docs itself
// needs none), which fills the 0–9 band exactly. Reference docs start at 10.
// The `guide-` prefix is load-bearing beyond ordering: the dashboard rail
// groups on it (web/src/pages/docsRail.ts), and it is what survives the
// Makefile flattening guides into the flat embed root.
var docOrder = map[string]int{
	"guide-getting-started": 0,
	"guide-today":           1,
	"guide-inbox":           2,
	"guide-sessions":        3,
	"guide-plans":           4,
	"guide-health":          5,
	"guide-learning":        6,
	"guide-knowledge":       7,
	"guide-system":          8,
	"guide-settings":        9,

	"onboarding": 10,
	"concepts":   11,
	"workflow":   12,
	"extending":  13,
	"neutrality": 14,
}

// GET /api/docs
func (h *Handler) listDocs(w http.ResponseWriter, r *http.Request) {
	docs, err := h.readDocs(docLang(r))
	writeJSON(w, docs, err)
}

// GET /api/docs/{slug}
func (h *Handler) getDoc(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	lang := docLang(r)
	docs, err := h.readDocs(lang)
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, d := range docs {
		if d.Slug != slug {
			continue
		}
		md, got, err := h.docText(d.File, lang)
		if err != nil {
			writeErr(w, err)
			return
		}
		d.Lang = got
		writeJSON(w, docDetailDTO{docDTO: d, Markdown: string(md)}, nil)
		return
	}
	http.Error(w, `{"error":"doc not found"}`, http.StatusNotFound)
}

// docText returns the markdown served for the English doc `file` in `lang`,
// and the language that markdown is actually in: the uk twin when asked for
// and present, the English file otherwise. Only a MISSING twin falls back — any
// other read error is returned, so a broken embed is not papered over.
func (h *Handler) docText(file, lang string) ([]byte, string, error) {
	if lang == docLangUK {
		md, err := fs.ReadFile(h.Docs, docUKDir+"/"+file)
		if err == nil {
			return md, docLangUK, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", err
		}
	}
	md, err := fs.ReadFile(h.Docs, file)
	return md, docLangEN, err
}

// readDocs lists the embedded markdown files as DTOs in nav order, with each
// title in `lang` when that doc has a translation (see docText).
func (h *Handler) readDocs(lang string) ([]docDTO, error) {
	docs := []docDTO{}
	if h.Docs == nil {
		return docs, nil
	}
	entries, err := fs.ReadDir(h.Docs, ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue // .gitkeep and anything non-markdown
		}
		md, got, err := h.docText(name, lang)
		if err != nil {
			return nil, err
		}
		docs = append(docs, docDTO{
			Slug:  strings.ToLower(name[:len(name)-len(".md")]),
			Title: docTitle(md, name),
			File:  name,
			Lang:  got,
		})
	}
	sort.Slice(docs, func(i, j int) bool {
		ri, rj := docRank(docs[i].Slug), docRank(docs[j].Slug)
		if ri != rj {
			return ri < rj
		}
		return docs[i].Slug < docs[j].Slug
	})
	return docs, nil
}

func docRank(slug string) int {
	if r, ok := docOrder[slug]; ok {
		return r
	}
	// Unpinned docs sort after every pinned one, then alphabetically among
	// themselves. This MUST NOT be len(docOrder): the pins are banded (guides
	// 0–4, reference 10–14) with gaps for future entries, so a count is not an
	// upper bound — at 9 entries it would have sorted unpinned docs ahead of
	// the whole reference band.
	return math.MaxInt
}

// docTitle returns the text of the first "# " heading line, or fallback.
func docTitle(md []byte, fallback string) string {
	sc := bufio.NewScanner(bytes.NewReader(md))
	for sc.Scan() {
		if t, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "# "); ok {
			return strings.TrimSpace(t)
		}
	}
	return fallback
}
