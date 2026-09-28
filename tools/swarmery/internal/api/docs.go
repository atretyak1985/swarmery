package api

// Parity wave: markdown docs endpoints, backed by the go:embed snapshot in
// internal/docsfs (populated by `make copy-docs` during build/dev).
//
// Response shapes are FROZEN by the parity contract:
//   list item: {"slug","title","file"}   detail adds: {"markdown"}
//
// slug  = lowercased basename without .md
// title = first "# " heading line (fallback: the file name)
// An empty embed (fresh clone / CI) yields [].

import (
	"bufio"
	"bytes"
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
}

type docDetailDTO struct {
	docDTO
	Markdown string `json:"markdown"`
}

// docOrder pins the dashboard nav order: the illustrated guides first, in
// reading order, then the reference docs (onboarding → concepts → workflow →
// extending → neutrality); anything else sorts alphabetically after both.
//
// The two bands are numbered 0–4 and 10–14 rather than 0–9 so a guide or a
// reference doc can be slotted in later without renumbering the other band.
// The `guide-` prefix is load-bearing beyond ordering: the dashboard rail
// groups on it (web/src/pages/docsRail.ts), and it is what survives the
// Makefile flattening guides into the flat embed root.
var docOrder = map[string]int{
	"guide-getting-started": 0,
	"guide-board":           1,
	"guide-plans":           2,
	"guide-sessions":        3,
	"guide-decisions":       4,

	"onboarding": 10,
	"concepts":   11,
	"workflow":   12,
	"extending":  13,
	"neutrality": 14,
}

// GET /api/docs
func (h *Handler) listDocs(w http.ResponseWriter, r *http.Request) {
	docs, err := h.readDocs()
	writeJSON(w, docs, err)
}

// GET /api/docs/{slug}
func (h *Handler) getDoc(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	docs, err := h.readDocs()
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, d := range docs {
		if d.Slug != slug {
			continue
		}
		md, err := fs.ReadFile(h.Docs, d.File)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, docDetailDTO{docDTO: d, Markdown: string(md)}, nil)
		return
	}
	http.Error(w, `{"error":"doc not found"}`, http.StatusNotFound)
}

// readDocs lists the embedded markdown files as DTOs in nav order.
func (h *Handler) readDocs() ([]docDTO, error) {
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
		md, err := fs.ReadFile(h.Docs, name)
		if err != nil {
			return nil, err
		}
		docs = append(docs, docDTO{
			Slug:  strings.ToLower(name[:len(name)-len(".md")]),
			Title: docTitle(md, name),
			File:  name,
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
