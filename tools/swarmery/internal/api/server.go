// Package api exposes the REST API and serves the embedded SPA.
package api

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/web"
)

// NewServer builds the full HTTP handler: API routes + embedded SPA fallback.
func NewServer(db *sql.DB) (http.Handler, error) {
	mux := http.NewServeMux()
	Routes(mux, &Handler{DB: db})

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
		w.Write(index)
	})
}
