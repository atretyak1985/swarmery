package api

// Step 03 — same-origin embedding surfaces for the tool dashboards sidebar:
// serenaProxy reverse-proxies /api/projects/{id}/serena/{rest...} to the
// project's live serena dashboard origin (incl. websocket upgrade passthrough
// via httputil.ReverseProxy), graphifyStatic serves the repo's graphify-out/
// build artifacts read-only behind a traversal jail, and architectureStatic
// serves architecture-out/ artifacts via the same shared jail helper. Neither
// is origin-fenced: the daemon is loopback-only, and the proxy enforces that
// the parsed DashboardURL targets a loopback address before dialing.

import (
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/toolproc"
)

// projectPathByID resolves a project's on-disk path, writing 400/404/500 on
// failure (ok=false means a response was already written).
func (h *Handler) projectPathByID(w http.ResponseWriter, r *http.Request) (id int64, path string, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid project id"}`, http.StatusBadRequest)
		return 0, "", false
	}
	err = h.DB.QueryRow(`SELECT path FROM projects WHERE id = ?`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return 0, "", false
	}
	if err != nil {
		writeErr(w, err)
		return 0, "", false
	}
	return id, path, true
}

// serenaProxy handles /api/projects/{id}/serena/{rest...} — a reverse proxy
// to the origin (scheme+host+port) of the project's serena DashboardURL. The
// rest path and query string pass through as-is; an empty rest redirects to
// the dashboard entry point so step 02's dashboardPath resolves to the UI.
// Websocket upgrades pass through: ReverseProxy re-adds the hop-by-hop
// Upgrade/Connection headers after Rewrite and tunnels the hijacked conn.
func (h *Handler) serenaProxy(w http.ResponseWriter, r *http.Request) {
	if toolMgr == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "tool manager not attached"})
		return
	}
	id, _, ok := h.projectPathByID(w, r)
	if !ok {
		return
	}
	st := toolMgr.Status(id)
	if st.State != toolproc.StateRunning || st.DashboardURL == "" {
		writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": "serena is not running for this project"})
		return
	}
	target, err := url.Parse(st.DashboardURL)
	if err != nil {
		writeErr(w, err)
		return
	}
	rest := r.PathValue("rest")
	if rest == "" {
		http.Redirect(w, r, "/api/projects/"+strconv.FormatInt(id, 10)+"/serena/dashboard/index.html", http.StatusFound)
		return
	}
	// The redirect above never dials, so the loopback fence sits right before
	// the only path that does: refuse to proxy to anything but a loopback
	// target, even if a compromised serena process prints a rogue URL.
	host := target.Hostname()
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && !strings.EqualFold(host, "localhost") {
		writeJSONStatus(w, http.StatusBadGateway,
			map[string]string{"error": "serena dashboard URL is not a loopback address"})
		return
	}
	// Error semantics are the deliberate stdlib defaults: a backend that dies
	// before sending headers yields ReverseProxy's default 502 (its default
	// ErrorHandler logs the dial/roundtrip error); a backend dying mid-stream
	// yields an aborted connection to the client.
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme = target.Scheme
		pr.Out.URL.Host = target.Host
		pr.Out.URL.Path = "/" + rest
		pr.Out.URL.RawPath = ""
		pr.Out.URL.RawQuery = r.URL.RawQuery
		pr.Out.Host = target.Host
	}}
	proxy.ServeHTTP(w, r)
}

// jailedProjectStatic serves <projectPath>/<subdir> read-only. Jail contract
// identical to the original graphifyStatic: method guard here (the "/" SPA
// catch-all would swallow non-GET otherwise), filepath.IsLocal rejects every
// escaping/rooted/empty rest, symlinks inside the artifact dir are trusted
// (produced by tooling in the user's own repo).
func (h *Handler) jailedProjectStatic(w http.ResponseWriter, r *http.Request, subdir, defaultDoc string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	_, projectPath, ok := h.projectPathByID(w, r)
	if !ok {
		return
	}
	rest := r.PathValue("rest")
	if rest == "" {
		rest = defaultDoc
	}
	if !filepath.IsLocal(filepath.FromSlash(rest)) {
		http.Error(w, `{"error":"path escapes `+subdir+`"}`, http.StatusForbidden)
		return
	}
	full := filepath.Join(projectPath, subdir, filepath.FromSlash(rest))
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

// graphifyStatic handles GET|HEAD /api/projects/{id}/graphify/{rest...} — a
// read-only jail over <projectPath>/graphify-out (default document graph.html).
// The method guard lives in jailedProjectStatic. Jail: the mux 301-cleans
// literal "../" segments away, but an encoded ..%2F arrives decoded in rest —
// filepath.IsLocal rejects every escaping, rooted, or empty path outright with
// 403. Symlinks inside graphify-out are trusted: the dir is produced by the
// graphify CLI in the user's own repo.
func (h *Handler) graphifyStatic(w http.ResponseWriter, r *http.Request) {
	h.jailedProjectStatic(w, r, "graphify-out", "graph.html")
}

// architectureStatic handles GET|HEAD /api/projects/{id}/architecture/{rest...}
// — same jail over <projectPath>/architecture-out (default document
// architecture-map.html, produced by the project-local /architecture-map skill).
func (h *Handler) architectureStatic(w http.ResponseWriter, r *http.Request) {
	h.jailedProjectStatic(w, r, "architecture-out", "architecture-map.html")
}
