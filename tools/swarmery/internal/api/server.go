// Package api exposes the REST API and serves the embedded SPA.
package api

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/docsfs"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/web"
)

// Cache-Control values for the embedded SPA. Vite content-hashes everything
// under /assets/, so those may be cached forever; index.html (and any other
// non-hashed entry point) must be revalidated on every load or browsers keep
// serving a stale bundle across daemon binary upgrades.
const (
	cacheControlNoCache   = "no-cache"
	cacheControlImmutable = "public, max-age=31536000, immutable"
)

// NewServer builds the full HTTP handler: API routes + embedded SPA fallback.
// watching reports whether the live ingest pipeline is attached (serve
// without --no-ingest); /api/health surfaces it to the dashboard.
func NewServer(db *sql.DB, watching bool) (http.Handler, error) {
	docs, err := docsfs.Content()
	if err != nil {
		return nil, fmt.Errorf("embedded docs: %w", err)
	}
	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db, Watching: watching, Docs: docs,
		Improve: &improve.Service{DB: db, Runner: improve.ClaudeRunner{}}})

	dist, err := web.Dist()
	if err != nil {
		return nil, fmt.Errorf("embedded SPA: %w", err)
	}
	mux.Handle("/", spaHandler(dist))
	return mux, nil
}

// spaHandler serves static files from the embedded dist, falling back to
// index.html for client-side routes (never for /api/*, which the mux owns).
func spaHandler(dist fs.FS) http.Handler {
	fileServer := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path != "/" {
			if _, err := fs.Stat(dist, path[1:]); err == nil {
				if strings.HasPrefix(path, "/assets/") {
					w.Header().Set("Cache-Control", cacheControlImmutable)
				} else {
					w.Header().Set("Cache-Control", cacheControlNoCache)
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "SPA not built — run `make build` (web/dist is empty)", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", cacheControlNoCache)
		w.Write(index)
	})
}
