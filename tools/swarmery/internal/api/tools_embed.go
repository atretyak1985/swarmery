package api

// Step 03 — same-origin embedding surfaces for the tool dashboards sidebar:
// serenaProxy reverse-proxies /api/projects/{id}/serena/{rest...} to the
// project's live serena dashboard origin (incl. websocket upgrade passthrough
// via httputil.ReverseProxy), graphifyStatic serves the repo's graphify-out/
// build artifacts read-only behind a traversal jail, and architectureStatic
// serves architecture-out/ artifacts via the same shared jail helper. The two
// static routes serve repository content, so it runs CSP-sandboxed with an
// opaque origin (artifactCSP), never as the daemon's own origin. The proxy's
// relayed responses are sandboxed the same way (serenaProxyCSP), its non-read
// and upgrade requests carry the origin fence (routes.go), and it dials only a
// loopback DashboardURL.

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
	//
	// Proxied documents never run on the daemon's origin: the baseline CSP
	// securityHeaders stamped is dropped and every upstream response gets
	// serenaProxyCSP instead (replacing any policy serena sent).
	w.Header().Del("Content-Security-Policy")
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = "/" + rest
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = target.Host
			// The link credential an Origin-less client authenticated to
			// the daemon with is the daemon's, never the upstream's.
			pr.Out.Header.Del("Authorization")
			// So is the dashboard's session token.
			stripDashboardSession(pr.Out.Header)
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Set("Content-Security-Policy", serenaProxyCSP)
			res.Header.Set("X-Content-Type-Options", "nosniff")
			return nil
		},
	}
	proxy.ServeHTTP(w, r)
}

// serenaProxyCSP goes on every response the serena proxy relays. serena's
// dashboard is a third-party page, and anything it renders (log lines, memory
// files, config) can carry content from the repository it indexes; served from
// /api/projects/{id}/serena/ it would otherwise run with the daemon's own
// origin and reach the whole API. The sandbox gives it an opaque origin. The
// dashboard tab does not use this route — it frames serena's own origin
// (Serena.tsx), because the dashboard's root-absolute API calls cannot work
// under a path prefix — so the sandbox costs no working feature. allow-forms
// is kept for the dashboard's own forms; allow-same-origin must never be added
// (with allow-scripts it cancels the sandbox).
const serenaProxyCSP = "sandbox allow-scripts allow-forms; frame-ancestors 'self'"

// artifactCSP is the Content-Security-Policy every jailed artifact response
// carries. The artifacts are files from a repository, not code the daemon
// ships, so they never run on the daemon's origin: the CSP sandbox gives the
// document an opaque origin (no cookies, storage, or same-origin reach into the
// dashboard or the API) whether it is framed or opened top-level, while
// allow-scripts keeps the interactive viewers working. allow-same-origin must
// never be added — together with allow-scripts it cancels the sandbox. The
// policy replaces securityHeaders' baseline, so it restates frame-ancestors.
const artifactCSP = "sandbox allow-scripts; frame-ancestors 'self'"

// jailedProjectStatic serves <projectPath>/<subdir> read-only. Method guard
// here (the "/" SPA catch-all would swallow non-GET otherwise);
// filepath.IsLocal rejects every escaping/rooted/empty rest; and a symlink is
// followed only while its resolved target stays inside the resolved artifact
// dir — the dir's content comes from the repository, so a link inside it is
// not trusted to point anywhere else on disk. Every response, errors included,
// carries artifactCSP and nosniff.
func (h *Handler) jailedProjectStatic(w http.ResponseWriter, r *http.Request, subdir, defaultDoc string) {
	w.Header().Set("Content-Security-Policy", artifactCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
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
	full, ok := resolveInJail(projectPath, subdir, filepath.FromSlash(rest))
	if !ok {
		http.NotFound(w, r)
		return
	}
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	// The artifacts are regenerated in place (same URL, new content). Without a
	// Cache-Control the browser heuristic-caches off Last-Modified and embeds
	// keep showing a stale copy; no-cache forces an If-Modified-Since
	// revalidation (cheap 304 while unchanged, fresh body right after a rebuild).
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, full)
}

// resolveInJail resolves every symlink on <projectPath>/<subdir>/<rest> and
// reports false unless both containments hold on the resolved paths: the
// artifact dir sits under the project (a symlinked architecture-out → $HOME
// would otherwise turn the whole home dir into the jail), and the file sits
// under the artifact dir. Every side is resolved, so a project reached through
// a symlinked parent (macOS /var → /private/var) compares like for like. The
// returned path is the resolved one, so the file served is the file checked.
func resolveInJail(projectPath, subdir, rest string) (string, bool) {
	realProject, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return "", false
	}
	dir := filepath.Join(projectPath, subdir)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil || !within(realProject, realDir) {
		return "", false
	}
	realFull, err := filepath.EvalSymlinks(filepath.Join(dir, rest))
	if err != nil || !within(realDir, realFull) {
		return "", false
	}
	return realFull, true
}

// within reports whether path is strictly below root (both already resolved).
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// graphifyStatic handles GET|HEAD /api/projects/{id}/graphify/{rest...} — a
// read-only jail over <projectPath>/graphify-out (default document graph.html).
// The method guard lives in jailedProjectStatic. Jail: the mux 301-cleans
// literal "../" segments away, but an encoded ..%2F arrives decoded in rest —
// filepath.IsLocal rejects every escaping, rooted, or empty path outright with
// 403, and a symlink whose target leaves graphify-out is a 404 (resolveInJail).
func (h *Handler) graphifyStatic(w http.ResponseWriter, r *http.Request) {
	h.jailedProjectStatic(w, r, "graphify-out", "graph.html")
}

// architectureStatic handles GET|HEAD /api/projects/{id}/architecture/{rest...}
// — same jail over <projectPath>/architecture-out (default document
// architecture-map.html, produced by the project-local /architecture-map skill).
func (h *Handler) architectureStatic(w http.ResponseWriter, r *http.Request) {
	h.jailedProjectStatic(w, r, "architecture-out", "architecture-map.html")
}
