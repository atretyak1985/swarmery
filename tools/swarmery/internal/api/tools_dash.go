package api

// tool dashboards (sidebar feed): GET /api/tools merges each non-archived
// project's enabled packs (projectscan.ReadPluginState) with the daemon-owned
// serena process state (internal/toolproc) and the on-disk graphify build
// artifacts. The start/stop POSTs are fenced exactly like the plugin toggle
// (project_plugins.go): requireLocalOrigin at the route, SWARMERY_ONBOARD_ROOTS
// here, resolveUnderRoots before touching the manager. dashboardPath / vizPath
// are same-origin paths served by step 03 (serena proxy + graphify static jail),
// never raw localhost URLs.

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/toolproc"
)

// toolMgr is attached once at startup (cmdServe) — nil (mock/serve-less
// builds) makes every tools endpoint answer 503.
var toolMgr *toolproc.Manager

// AttachToolManager wires the daemon-owned tool-process manager; tests attach
// a manager built on a stub command.
func AttachToolManager(m *toolproc.Manager) { toolMgr = m }

// lookPathFn is injectable so tests can force serena (un)availability without
// touching PATH.
var lookPathFn = exec.LookPath

const (
	// serenaPack / graphifyPack are the marketplace pack names whose presence in
	// a project's enabledPlugins puts it on the respective sidebar list.
	serenaPack   = "lsp-pack"
	graphifyPack = "graphify-pack"
	// sidebarLogTailCap bounds logTail in the GET /api/tools feed (toolproc
	// keeps up to 40 lines; the sidebar only ever shows the last few).
	sidebarLogTailCap = 10
)

type serenaProjectDTO struct {
	ID            int64   `json:"id"`
	Slug          string  `json:"slug"`
	Name          *string `json:"name"`
	State         string  `json:"state"`
	DashboardPath string  `json:"dashboardPath"`
	// DashboardURL is serena's RAW dashboard origin (e.g.
	// "http://127.0.0.1:24282/dashboard/index.html"), non-empty only while
	// state==running and the URL has been parsed from serena's log. The web UI
	// iframes THIS, not the dashboardPath reverse proxy: serena's dashboard.js
	// issues root-absolute ajax calls (/get_config_overview, /get_tool_stats, …)
	// that escape any path-prefix proxy and land on the daemon's SPA catch-all.
	// Serena sends no X-Frame-Options/CSP, and both daemon and serena are
	// loopback-only, so framing serena's own origin directly is safe. The proxy
	// path is kept for diagnostics. No omitempty — the zero value is "" so the
	// TS type stays a plain string.
	DashboardURL string   `json:"dashboardUrl"`
	StartedAt    *string  `json:"startedAt"`
	LogTail      []string `json:"logTail"`
	Error        string   `json:"error"`
}

type graphifyProjectDTO struct {
	ID       int64   `json:"id"`
	Slug     string  `json:"slug"`
	Name     *string `json:"name"`
	HasViz   bool    `json:"hasViz"`
	HasGraph bool    `json:"hasGraph"`
	BuiltAt  *string `json:"builtAt"`
	VizPath  string  `json:"vizPath"`
}

type toolsSerenaSection struct {
	Available bool               `json:"available"`
	Projects  []serenaProjectDTO `json:"projects"`
}

type toolsGraphifySection struct {
	Projects []graphifyProjectDTO `json:"projects"`
}

type toolsResponse struct {
	Serena   toolsSerenaSection   `json:"serena"`
	Graphify toolsGraphifySection `json:"graphify"`
}

// toolsDash handles GET /api/tools — the read-only, unfenced sidebar feed.
func (h *Handler) toolsDash(w http.ResponseWriter, r *http.Request) {
	if toolMgr == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "tool manager not attached"})
		return
	}
	resp := toolsResponse{
		Serena:   toolsSerenaSection{Projects: []serenaProjectDTO{}},
		Graphify: toolsGraphifySection{Projects: []graphifyProjectDTO{}},
	}
	if _, err := lookPathFn("serena"); err == nil {
		resp.Serena.Available = true
	}

	rows, err := h.DB.Query(`SELECT id, path, slug, name FROM projects WHERE archived = 0 ORDER BY id`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id         int64
			path, slug string
			name       sql.NullString
		)
		if err := rows.Scan(&id, &path, &slug, &name); err != nil {
			writeErr(w, err)
			return
		}
		// nil state = telemetry-only / unreadable settings → on neither list.
		st, serr := projectscan.ReadPluginState(path, nil)
		if serr != nil || st == nil {
			continue
		}
		var namePtr *string
		if name.Valid {
			n := name.String
			namePtr = &n
		}
		if slices.Contains(st.Packs, serenaPack) {
			resp.Serena.Projects = append(resp.Serena.Projects, serenaDTO(id, slug, namePtr, toolMgr.Status(id)))
		}
		if slices.Contains(st.Packs, graphifyPack) {
			resp.Graphify.Projects = append(resp.Graphify.Projects, graphifyDTO(id, slug, namePtr, path))
		}
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, resp, nil)
}

// serenaDTO maps a toolproc.Status snapshot onto the frozen wire shape.
func serenaDTO(id int64, slug string, name *string, s toolproc.Status) serenaProjectDTO {
	tail := s.LogTail
	if tail == nil {
		tail = []string{}
	}
	if len(tail) > sidebarLogTailCap {
		tail = tail[len(tail)-sidebarLogTailCap:]
	}
	var startedAt *string
	if !s.StartedAt.IsZero() {
		v := s.StartedAt.UTC().Format(time.RFC3339)
		startedAt = &v
	}
	// Raw origin only while running (see the DTO comment for why the iframe
	// needs it); "" otherwise so stale URLs never leak past a stop/failure.
	var dashURL string
	if s.State == toolproc.StateRunning {
		dashURL = s.DashboardURL
	}
	return serenaProjectDTO{
		ID:            id,
		Slug:          slug,
		Name:          name,
		State:         string(s.State),
		DashboardPath: fmt.Sprintf("/api/projects/%d/serena/", id),
		DashboardURL:  dashURL,
		StartedAt:     startedAt,
		LogTail:       tail,
		Error:         s.Err,
	}
}

// graphifyDTO reports the on-disk build artifacts under <project>/graphify-out.
func graphifyDTO(id int64, slug string, name *string, projectPath string) graphifyProjectDTO {
	out := filepath.Join(projectPath, "graphify-out")
	d := graphifyProjectDTO{
		ID:      id,
		Slug:    slug,
		Name:    name,
		VizPath: fmt.Sprintf("/api/projects/%d/graphify/graph.html", id),
	}
	if fi, err := os.Stat(filepath.Join(out, "graph.json")); err == nil && !fi.IsDir() {
		d.HasGraph = true
		v := fi.ModTime().UTC().Format(time.RFC3339)
		d.BuiltAt = &v
	}
	if fi, err := os.Stat(filepath.Join(out, "graph.html")); err == nil && !fi.IsDir() {
		d.HasViz = true
	}
	return d
}

// serenaFence runs the shared guard chain for the serena control endpoints —
// 503 no manager → 403 no roots → 400 bad id → 404 unknown project → 403
// outside roots (mirroring putProjectPlugin) — and returns the project id plus
// its resolved (symlink-safe) directory. ok=false means a response was written.
func (h *Handler) serenaFence(w http.ResponseWriter, r *http.Request) (id int64, dir string, ok bool) {
	if toolMgr == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "tool manager not attached"})
		return 0, "", false
	}
	if len(onboardCfg.Roots) == 0 {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{
			"error": "serena controls are disabled — start the daemon with SWARMERY_ONBOARD_ROOTS set to the allowed parent directories",
		})
		return 0, "", false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid project id"}`, http.StatusBadRequest)
		return 0, "", false
	}
	var path string
	err = h.DB.QueryRow(`SELECT path FROM projects WHERE id = ?`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return 0, "", false
	}
	if err != nil {
		writeErr(w, err)
		return 0, "", false
	}
	dir, err = resolveUnderRoots(path, onboardCfg.Roots)
	if err != nil {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return 0, "", false
	}
	return id, dir, true
}

// serenaStart handles POST /api/projects/{id}/serena/start.
func (h *Handler) serenaStart(w http.ResponseWriter, r *http.Request) {
	id, dir, ok := h.serenaFence(w, r)
	if !ok {
		return
	}
	if _, err := lookPathFn("serena"); err != nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "serena binary not found — install serena first"})
		return
	}
	err := toolMgr.Start(id, dir)
	switch {
	case errors.Is(err, toolproc.ErrAlreadyRunning):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "serena is already running for this project"})
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"state": "starting"}, nil)
}

// serenaStop handles POST /api/projects/{id}/serena/stop.
func (h *Handler) serenaStop(w http.ResponseWriter, r *http.Request) {
	id, _, ok := h.serenaFence(w, r)
	if !ok {
		return
	}
	err := toolMgr.Stop(id)
	switch {
	case errors.Is(err, toolproc.ErrNotRunning):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "serena is not running for this project"})
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"state": "stopped"}, nil)
}
