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

// docOrder pins the dashboard nav order for the well-known docs (the
// onboarding → extending → neutrality reading order); anything else sorts
// alphabetically after them.
var docOrder = map[string]int{"onboarding": 0, "extending": 1, "neutrality": 2}

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
	return len(docOrder)
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
