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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/githead"
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
	// serenaPack / graphifyPack / architecturePack are the marketplace pack names
	// whose presence in a project's enabledPlugins puts it on the respective
	// sidebar list.
	serenaPack       = "lsp-pack"
	graphifyPack     = "graphify-pack"
	architecturePack = "architecture-pack"
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

// provisionDTO is the latest provision job for a pack (auto-provision phase 3):
// install→generate progress surfaced so the sidebar can render it. null when the
// project has never had a provision job.
type provisionDTO struct {
	State    string `json:"state"`
	LastLine string `json:"lastLine"`
	Error    string `json:"error"`
}

type architectureProjectDTO struct {
	ID               int64         `json:"id"`
	Slug             string        `json:"slug"`
	Name             *string       `json:"name"`
	HasMap           bool          `json:"hasMap"`
	BuiltAt          *string       `json:"builtAt"`
	MapPath          string        `json:"mapPath"`
	AnalyzedAtCommit *string       `json:"analyzedAtCommit"`
	HeadCommit       *string       `json:"headCommit"`
	Provision        *provisionDTO `json:"provision"`
}

type toolsArchitectureSection struct {
	Projects []architectureProjectDTO `json:"projects"`
}

type toolsResponse struct {
	Serena       toolsSerenaSection       `json:"serena"`
	Graphify     toolsGraphifySection     `json:"graphify"`
	Architecture toolsArchitectureSection `json:"architecture"`
}

// toolsDash handles GET /api/tools — the read-only, unfenced sidebar feed.
func (h *Handler) toolsDash(w http.ResponseWriter, r *http.Request) {
	if toolMgr == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "tool manager not attached"})
		return
	}
	resp := toolsResponse{
		Serena:       toolsSerenaSection{Projects: []serenaProjectDTO{}},
		Graphify:     toolsGraphifySection{Projects: []graphifyProjectDTO{}},
		Architecture: toolsArchitectureSection{Projects: []architectureProjectDTO{}},
	}
	if _, err := lookPathFn("serena"); err == nil {
		resp.Serena.Available = true
	}

	// Drain projects into a slice BEFORE building DTOs: the store caps the pool
	// at one connection (SetMaxOpenConns(1)), so a per-project query (e.g.
	// Provision.Latest) while this cursor is open would self-deadlock. Read all
	// rows, close the cursor, then do the per-project work.
	rows, err := h.DB.Query(`SELECT id, path, slug, name FROM projects WHERE archived = 0 ORDER BY id`)
	if err != nil {
		writeErr(w, err)
		return
	}
	type projRow struct {
		id         int64
		path, slug string
		namePtr    *string
	}
	var projects []projRow
	for rows.Next() {
		var (
			id         int64
			path, slug string
			name       sql.NullString
		)
		if err := rows.Scan(&id, &path, &slug, &name); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		var namePtr *string
		if name.Valid {
			n := name.String
			namePtr = &n
		}
		projects = append(projects, projRow{id: id, path: path, slug: slug, namePtr: namePtr})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeErr(w, err)
		return
	}
	rows.Close()

	for _, p := range projects {
		id, path, slug, namePtr := p.id, p.path, p.slug, p.namePtr
		// Read plugin state first so we can compute packEnabled for the
		// architecture union (pack∪artifact). State nil = telemetry-only /
		// unreadable settings — those still appear in the architecture list if
		// they have an artifact (artifact-only gating for telemetry projects).
		st, serr := projectscan.ReadPluginState(path, nil)
		packEnabled := serr == nil && st != nil && slices.Contains(st.Packs, architecturePack)
		if d, ok := architectureDTO(id, slug, namePtr, path, packEnabled); ok {
			// The latest provision job (if any) rides on the DTO so the sidebar
			// can render install/generate progress. Best-effort — a query error
			// just leaves provision null.
			if h.Provision != nil {
				if j, ok, jerr := h.Provision.Latest(id, architecturePack); jerr == nil && ok {
					d.Provision = &provisionDTO{State: j.Status, LastLine: j.LastLine, Error: j.Error}
				}
			}
			resp.Architecture.Projects = append(resp.Architecture.Projects, d)
		}
		// nil state = telemetry-only / unreadable settings → off the serena/graphify lists.
		if serr != nil || st == nil {
			continue
		}
		if slices.Contains(st.Packs, serenaPack) {
			resp.Serena.Projects = append(resp.Serena.Projects, serenaDTO(id, slug, namePtr, toolMgr.Status(id)))
		}
		if slices.Contains(st.Packs, graphifyPack) {
			resp.Graphify.Projects = append(resp.Graphify.Projects, graphifyDTO(id, slug, namePtr, path))
		}
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

// architectureDTO reports <project>/architecture-out artifacts (produced by
// the project-local /architecture-map skill). ok = packEnabled || artifact
// exists — the union makes pack-enabled projects appear even before a map is
// built. Both AnalyzedAtCommit and HeadCommit degrade to nil on any failure.
func architectureDTO(id int64, slug string, name *string, projectPath string, packEnabled bool) (architectureProjectDTO, bool) {
	out := filepath.Join(projectPath, "architecture-out")
	mapPath := fmt.Sprintf("/api/projects/%d/architecture/architecture-map.html", id)

	fi, statErr := os.Stat(filepath.Join(out, "architecture-map.html"))
	hasMap := statErr == nil && !fi.IsDir()

	if !packEnabled && !hasMap {
		return architectureProjectDTO{}, false
	}

	d := architectureProjectDTO{
		ID:      id,
		Slug:    slug,
		Name:    name,
		HasMap:  hasMap,
		MapPath: mapPath,
	}

	if hasMap {
		v := fi.ModTime().UTC().Format(time.RFC3339)
		d.BuiltAt = &v

		// Parse analyzedAtCommit from architecture-map.json (best-effort).
		if raw, err := os.ReadFile(filepath.Join(out, "architecture-map.json")); err == nil {
			var meta struct {
				AnalyzedAtCommit string `json:"analyzedAtCommit"`
			}
			if err := json.Unmarshal(raw, &meta); err == nil && meta.AnalyzedAtCommit != "" {
				d.AnalyzedAtCommit = &meta.AnalyzedAtCommit
			}
		}
	}

	// Resolve the current HEAD commit from the project's .git — nil on any failure.
	if sha, ok := githead.Resolve(projectPath); ok {
		d.HeadCommit = &sha
	}

	return d, true
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
