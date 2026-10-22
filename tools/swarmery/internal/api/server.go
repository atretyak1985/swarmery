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
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/plainsum"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/provision"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/retroanalysis"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskcap"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
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

// Content-Security-Policy values. baselineCSP goes on every response the daemon
// writes (securityHeaders): only the dashboard itself may frame it. spaCSP is
// the dashboard shell's own policy — everything from the daemon's origin, no
// plugins, no <base> rewrites, no foreign framing. Its relaxations are what
// the shipped shell actually needs: 'unsafe-inline' styles (React style props,
// mermaid's generated SVG <style>); the Google Fonts stylesheet + font hosts
// that index.html links; and loopback frames, because the Serena tab frames
// serena's own dashboard origin (tools_dash.go serenaProjectDTO.DashboardURL,
// e.g. http://127.0.0.1:24282/…) rather than the daemon proxy. Routes that
// serve repository content set their own sandbox policy (artifactCSP).
const (
	baselineCSP = "frame-ancestors 'self'"
	spaCSP      = "default-src 'self'; script-src 'self'; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src 'self' https://fonts.gstatic.com; " +
		"img-src 'self' data: blob:; connect-src 'self' ws: wss:; " +
		"frame-src 'self' http://127.0.0.1:* http://localhost:*; " +
		"object-src 'none'; base-uri 'self'; frame-ancestors 'self'"
)

// securityHeaders stamps the daemon-wide browser hardening on every response
// before the route runs: nosniff, so no response is ever content-sniffed into
// something it was not served as, and the baseline frame-ancestors CSP, so no
// other origin can frame any page or API response. A handler that needs a
// stricter policy (the SPA shell, the jailed artifacts) Sets its own
// Content-Security-Policy, replacing the baseline. The ResponseWriter is passed
// through unwrapped, so streaming (Flusher) and websocket upgrades (Hijacker)
// behave exactly as before.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", baselineCSP)
		next.ServeHTTP(w, r)
	})
}

// inboxTTL is the sweeper TTL the board DTO derives staleAfter from. Set by
// AttachInboxTTL at startup from the same SWARMERY_INBOX_TTL resolution the
// sweeper itself uses; the default matches the sweeper's default so a daemon
// that never calls Attach still reports the truth.
var inboxTTL = taskcap.DefaultInboxTTL

// AttachInboxTTL records the inbox sweeper's TTL so board cards can report when
// they expire (boardTaskDTO.StaleAfter). <= 0 means the sweep is disabled and
// every card reads null. Call before NewServer.
func AttachInboxTTL(d time.Duration) { inboxTTL = d }

// readPool is the read-only pool NewServer hands the Handler; nil means every
// read goes through the writer, as before. Set by AttachReader.
var readPool *sql.DB

// AttachReader records the daemon's read-only pool (store.OpenReader) so the
// hot GET handlers stop queueing behind the single writer connection. Call
// before NewServer.
func AttachReader(ro *sql.DB) { readPool = ro }

// rdb is the handle a read-only request reads through: the read pool when the
// daemon attached one, the writer otherwise (tests, a pool that failed to
// open). Only for reads — a write through the pool fails with SQLITE_READONLY.
func (h *Handler) rdb() *sql.DB {
	if h.RO != nil {
		return h.RO
	}
	return h.DB
}

// NewServer builds the full HTTP handler: API routes + embedded SPA fallback.
// watching reports whether the live ingest pipeline is attached (serve
// without --no-ingest); /api/health surfaces it to the dashboard.
func NewServer(db *sql.DB, watching bool) (http.Handler, error) {
	docs, err := docsfs.Content()
	if err != nil {
		return nil, fmt.Errorf("embedded docs: %w", err)
	}
	mux := http.NewServeMux()
	h := &Handler{DB: db, RO: readPool, Watching: watching, Docs: docs, InboxTTL: inboxTTL,
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
	// Plain-language retellings of plan docs, cached in plain_summaries.
	h.Plain = &plainsum.Service{DB: db, Runner: plainsum.ClaudeRunner{}, Model: plainsum.ResolveModel()}
	// The resume path re-creates a finished run's worktree on its existing branch
	// so a stopped session can still be answered (session_message.go).
	h.Wt = &worktree.Manager{Git: worktree.ExecGit{}}
	// A Criteria-tab tick that closes a phase's last criterion starts the plan
	// branch review like a run's `done` stamp (epics.go planReviewOnTick).
	// Guarded: a nil *phaserun.Service in the interface would be a non-nil hook.
	if phaserunSvc != nil {
		h.PlanReview = phaserunSvc
	}
	// The advisor Source needs this Handler (recommendation attribution, the
	// improve registry), so it registers here rather than in main.go;
	// AttachTriage runs before NewServer there. The retire Source registers here
	// too, with lessonVerifyCfg: it must list retirement proposals under the SAME
	// verification config the accept path confirms them with, and main.go sets
	// that config (AttachLessonVerify) only after it builds the triage service.
	// The friction (recurring error groups) and agent (failing agents) Sources
	// read the Retro aggregates on this Handler, so they register here as well.
	if triageSvc != nil {
		triageSvc.Register(&advisorSource{h: h})
		triageSvc.Register(&triage.RetireSource{DB: db, Cfg: lessonVerifyCfg})
		triageSvc.Register(&frictionSource{h: h})
		triageSvc.Register(&agentSource{h: h})
	}
	Routes(mux, h)

	dist, err := web.Dist()
	if err != nil {
		return nil, fmt.Errorf("embedded SPA: %w", err)
	}
	// The SPA shell is fenced like the API (origin.go): one rule for the whole
	// daemon — a request must name it in Host — is simpler to reason about
	// than a public shell whose every fetch then fails.
	mux.Handle("/", requireLocalHost(spaHandler(dist)))
	return securityHeaders(mux), nil
}

// spaHandler serves static files from the embedded dist, falling back to
// index.html for client-side routes (never for /api/*, which the mux owns).
// Every response carries spaCSP: the policy only bites on the HTML documents,
// and stamping all of them keeps a non-index entry point from slipping out
// without one.
func spaHandler(dist fs.FS) http.Handler {
	fileServer := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", spaCSP)
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
