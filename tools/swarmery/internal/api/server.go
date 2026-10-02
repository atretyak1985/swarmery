// Package api exposes the REST API and serves the embedded SPA.
package api

import (
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/docsfs"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/provision"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/retroanalysis"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskcap"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
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

// inboxTTL is the sweeper TTL the board DTO derives staleAfter from. Set by
// AttachInboxTTL at startup from the same SWARMERY_INBOX_TTL resolution the
// sweeper itself uses; the default matches the sweeper's default so a daemon
// that never calls Attach still reports the truth.
var inboxTTL = taskcap.DefaultInboxTTL

// AttachInboxTTL records the inbox sweeper's TTL so board cards can report when
// they expire (boardTaskDTO.StaleAfter). <= 0 means the sweep is disabled and
// every card reads null. Call before NewServer.
func AttachInboxTTL(d time.Duration) { inboxTTL = d }

// NewServer builds the full HTTP handler: API routes + embedded SPA fallback.
// watching reports whether the live ingest pipeline is attached (serve
// without --no-ingest); /api/health surfaces it to the dashboard.
func NewServer(db *sql.DB, watching bool) (http.Handler, error) {
	docs, err := docsfs.Content()
	if err != nil {
		return nil, fmt.Errorf("embedded docs: %w", err)
	}
	mux := http.NewServeMux()
	h := &Handler{DB: db, Watching: watching, Docs: docs, InboxTTL: inboxTTL,
		Improve: &improve.Service{
			DB:     db,
			Runner: improve.ClaudeRunner{},
			// Repo/Exec drive the phase-4 apply/PR pipeline. Repo is the
			// marketplace clone attached via AttachImproveRepo (empty until then —
			// generation works regardless; apply's git ops need it).
			Repo: improveRepoRoot,
			Exec: improve.OSExec{},
		}}
	// The page-level improver: one system analysis at a time, gated by a human
	// before anything downstream runs. Heal rows a previous daemon left
	// 'running' — the process that owned them is gone, so they can never
	// finish (the same startup contract as Provision.HealStale below).
	h.RetroAnalysis = &retroanalysis.Service{DB: db, Runner: retroanalysis.ClaudeRunner{}}
	if err := h.RetroAnalysis.HealStale(); err != nil {
		log.Printf("warning: retro analysis heal on startup: %v", err)
	}
	// Provision runs "enable pack → install + generate" jobs behind the plugin
	// toggle. Heal in-flight rows a prior daemon left behind (restart/crash) to
	// 'failed' so they don't dangle — best-effort, never blocks startup.
	h.Provision = provision.NewService(db, provision.ClaudeRunner{})
	if err := h.Provision.HealStale(); err != nil {
		log.Printf("warning: provision heal on startup: %v", err)
	}
	// The resume path re-creates a finished run's worktree on its existing branch
	// so a stopped session can still be answered (session_message.go).
	h.Wt = &worktree.Manager{Git: worktree.ExecGit{}}
	Routes(mux, h)

	dist, err := web.Dist()
	if err != nil {
		return nil, fmt.Errorf("embedded SPA: %w", err)
	}
	// The SPA shell is fenced like the API (origin.go): one rule for the whole
	// daemon — a request must name it in Host — is simpler to reason about
	// than a public shell whose every fetch then fails.
	mux.Handle("/", requireLocalHost(spaHandler(dist)))
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
