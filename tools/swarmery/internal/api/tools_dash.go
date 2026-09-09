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
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/archmap"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/githead"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/pluginreq"
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
	graftPack        = "graft-pack"
	architecturePack = "architecture-pack"
	// defaultGraftDir is graft's own default index location; a project may move
	// it with graft.graphDir in project.json (see plugins/graft-pack/requirements.json).
	defaultGraftDir = "graft"
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

type graftProjectDTO struct {
	ID   int64   `json:"id"`
	Slug string  `json:"slug"`
	Name *string `json:"name"`
	// GraphDir is repo-relative and reported so the UI can name the path a
	// missing graph is missing FROM — a project that moved its index with
	// graft.graphDir would otherwise read as "never built".
	GraphDir string  `json:"graphDir"`
	HasGraph bool    `json:"hasGraph"`
	BuiltAt  *string `json:"builtAt"`
	Nodes    int     `json:"nodes"`
	Edges    int     `json:"edges"`
}

type toolsSerenaSection struct {
	Available bool               `json:"available"`
	Projects  []serenaProjectDTO `json:"projects"`
}

// toolsGraftSection carries Available (the graft binary on PATH) because
// graft-pack is useless without the CLI: unlike graphify, whose artifacts the
// daemon can serve on their own, every graft answer comes from running the CLI.
// A project on this list with Available=false is a real misconfiguration, and
// the UI should be able to say so.
type toolsGraftSection struct {
	Available bool              `json:"available"`
	Projects  []graftProjectDTO `json:"projects"`
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
	ID               int64   `json:"id"`
	Slug             string  `json:"slug"`
	Name             *string `json:"name"`
	HasMap           bool    `json:"hasMap"`
	BuiltAt          *string `json:"builtAt"`
	MapPath          string  `json:"mapPath"`
	AnalyzedAtCommit *string `json:"analyzedAtCommit"`
	HeadCommit       *string `json:"headCommit"`
	// Freshness as a number rather than a boolean. All three degrade to nil —
	// never 0 — on any failure (no git, no map, unparseable map, unknown
	// commit), exactly like HeadCommit: 0 would read as "current" and "no
	// modules", which is the opposite of "we could not tell".
	CommitsBehind  *int          `json:"commitsBehind"`
	TouchedModules *int          `json:"touchedModules"`
	ModuleCount    *int          `json:"moduleCount"`
	Provision      *provisionDTO `json:"provision"`
	// Repos is the per-member freshness of a MULTI-repo workspace, whose root
	// has no .git at all and whose HeadCommit is therefore null. omitempty is
	// load-bearing: a single-repo project must keep sending the exact bytes it
	// always did, so the field is absent rather than `[]` for them.
	Repos []repoHeadDTO `json:"repos,omitempty"`
}

// repoHeadDTO is one member checkout of a multi-repo workspace. A member that
// is not on disk, or is not a checkout, arrives with ok=false and NULL commits
// instead of being omitted — the page must be able to say "we could not see
// 2 of 8", because a shorter list reads as a smaller, healthier workspace.
type repoHeadDTO struct {
	Name string `json:"name"`
	// OK reports that this member's HEAD was readable. Everything below is
	// null when it is false.
	OK               bool    `json:"ok"`
	HeadCommit       *string `json:"headCommit"`
	AnalyzedAtCommit *string `json:"analyzedAtCommit"`
	// Same contract as the project-level fields: null is "unmeasurable",
	// 0 is "measured, and current".
	CommitsBehind  *int `json:"commitsBehind"`
	TouchedModules *int `json:"touchedModules"`
}

type toolsArchitectureSection struct {
	Projects []architectureProjectDTO `json:"projects"`
}

type toolsResponse struct {
	Serena       toolsSerenaSection       `json:"serena"`
	Graphify     toolsGraphifySection     `json:"graphify"`
	Graft        toolsGraftSection        `json:"graft"`
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
		Graft:        toolsGraftSection{Projects: []graftProjectDTO{}},
		Architecture: toolsArchitectureSection{Projects: []architectureProjectDTO{}},
	}
	if _, err := lookPathFn("serena"); err == nil {
		resp.Serena.Available = true
	}
	if _, err := lookPathFn("graft"); err == nil {
		resp.Graft.Available = true
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
		// Overlay-aware: a pack switched on by a declared settings overlay is
		// live in that project's sessions, so it belongs on these lists too.
		st, serr := projectscan.ReadPluginState(path, nil, overlaysFor(path)...)
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
		if slices.Contains(st.Packs, graftPack) {
			resp.Graft.Projects = append(resp.Graft.Projects, graftDTO(id, slug, namePtr, path))
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

	archFreshness(&d, projectPath)

	return d, true
}

// archFreshness fills commitsBehind / touchedModules / moduleCount, or leaves
// them nil. It is the one place in the feed that forks git, so it is fenced by
// the cheap checks first: no analysed commit or no resolvable HEAD means there
// is nothing to measure between, and a map that will not parse means there are
// no modules to count. archmap memoises per (repo, from, to), so the page's 3 s
// settle-poll re-reads the answer instead of re-walking the revision graph.
//
// The three fields move together only as far as the data allows: moduleCount
// survives a git failure (it is a property of the artifact alone), while
// touchedModules needs both halves and so goes nil with the diff.
//
// A MULTI-repo workspace takes the other branch: its root is not a checkout,
// so there is no root HEAD to measure against and every number here used to be
// null for the largest projects on the page. archmap.ResolveFreshness answers
// per member instead — the same answer advisor R7 consumes, so the page and the
// advisor cannot disagree.
func archFreshness(d *architectureProjectDTO, projectPath string) {
	m, err := archmap.Load(projectPath)
	if err == nil {
		n := archmap.ModuleCount(m)
		d.ModuleCount = &n
	}
	if f := archmap.ResolveFreshness(projectPath); f.MultiRepo() {
		archFreshnessMulti(d, m, f)
		return
	}
	if d.AnalyzedAtCommit == nil || d.HeadCommit == nil {
		return
	}
	from, to := *d.AnalyzedAtCommit, *d.HeadCommit
	if from == to {
		// Current: zero here is a measurement, not a fallback.
		zero := 0
		d.CommitsBehind = &zero
		empty := 0
		d.TouchedModules = &empty
		return
	}
	behind, err := archmap.Behind(context.Background(), projectPath, from, to)
	if err != nil {
		return
	}
	d.CommitsBehind = &behind
	if m == nil {
		return
	}
	files, err := archmap.Diff(context.Background(), projectPath, from, to)
	if err != nil {
		return
	}
	touched := len(archmap.Match(m, files).Modules)
	d.TouchedModules = &touched
}

// archFreshnessMulti fills the per-repo strip for a multi-repo workspace and
// rolls it up into the project-level commitsBehind / touchedModules.
//
// Two details that are easy to get wrong and both change the answer:
//
//   - Module paths in a multi-repo map are WORKSPACE-relative ("app/src/lib"),
//     while `git diff` inside a member emits REPO-relative ones ("src/lib").
//     Matching the raw diff would land every file in Unmatched and report a
//     confident "0 modules touched".
//   - The rollups are all-or-nothing. A member whose git call failed makes the
//     sum an undercount, and an undercount here is worse than a null: it reads
//     as a smaller blast radius than the one that exists. Members that are
//     merely UNMEASURABLE (no HEAD, or no per-repo stamp in the map) do not
//     taint the rollup — they were never part of the sum, and the per-repo
//     rows say so explicitly.
func archFreshnessMulti(d *architectureProjectDTO, m *archmap.Map, f archmap.Freshness) {
	ctx := context.Background()
	repos := make([]repoHeadDTO, 0, len(f.Repos))

	behindTotal, measured := 0, 0
	behindComplete, touchedComplete := true, true
	touchedIDs := map[string]struct{}{}

	for _, r := range f.Repos {
		row := repoHeadDTO{Name: r.Name, OK: r.OK}
		if r.OK {
			head := r.Head
			row.HeadCommit = &head
		}
		if r.Analyzed != "" {
			analyzed := r.Analyzed
			row.AnalyzedAtCommit = &analyzed
		}
		if !r.Measurable() {
			repos = append(repos, row)
			continue
		}
		measured++
		if r.Head == r.Analyzed {
			// Current: zero here is a measurement, not a fallback.
			zero, none := 0, 0
			row.CommitsBehind, row.TouchedModules = &zero, &none
			repos = append(repos, row)
			continue
		}
		behind, err := archmap.Behind(ctx, r.Path, r.Analyzed, r.Head)
		if err != nil {
			behindComplete, touchedComplete = false, false
			repos = append(repos, row)
			continue
		}
		row.CommitsBehind = &behind
		behindTotal += behind
		if m == nil {
			repos = append(repos, row)
			continue
		}
		files, err := archmap.Diff(ctx, r.Path, r.Analyzed, r.Head)
		if err != nil {
			touchedComplete = false
			repos = append(repos, row)
			continue
		}
		scoped := make([]string, 0, len(files))
		for _, p := range files {
			scoped = append(scoped, path.Join(r.Name, p))
		}
		mods := archmap.Match(m, scoped).Modules
		n := len(mods)
		row.TouchedModules = &n
		for _, tm := range mods {
			touchedIDs[tm.ID] = struct{}{}
		}
		repos = append(repos, row)
	}

	d.Repos = repos
	if measured == 0 {
		return
	}
	if behindComplete {
		total := behindTotal
		d.CommitsBehind = &total
	}
	// Distinct module ids, not a sum of per-repo counts: module paths are
	// repo-scoped so the sets should be disjoint, but a union cannot double
	// count if a map ever anchors one module across two members.
	if m != nil && touchedComplete {
		n := len(touchedIDs)
		d.TouchedModules = &n
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

// graftDTO reports the on-disk graft index under <project>/<graphDir>. Read-only
// and exec-free: the wiring graph is a file, so the daemon never has to run the
// CLI to say whether a project has a graph and how big it is.
//
// graphDir comes from the project's own graft.graphDir (graft's --dir), falling
// back to graft's default. A project that relocated its index must not be
// reported as having no graph at all.
func graftDTO(id int64, slug string, name *string, projectPath string) graftProjectDTO {
	graphDir := defaultGraftDir
	if cfg := pluginreq.ReadProjectConfig(projectPath); cfg != nil {
		if raw, ok := cfg["graft"]; ok {
			var block struct {
				GraphDir string `json:"graphDir"`
			}
			if err := json.Unmarshal(raw, &block); err == nil && block.GraphDir != "" {
				graphDir = block.GraphDir
			}
		}
	}
	d := graftProjectDTO{ID: id, Slug: slug, Name: name, GraphDir: graphDir}

	// filepath.Join cleans the path, but a configured graphDir is operator input
	// and "../.." would walk out of the project. Anything not staying inside is
	// treated as no graph rather than followed.
	wiring := filepath.Join(projectPath, filepath.FromSlash(graphDir), ".graph", "wiring.json")
	if !strings.HasPrefix(wiring, filepath.Clean(projectPath)+string(filepath.Separator)) {
		return d
	}
	fi, err := os.Stat(wiring)
	if err != nil || fi.IsDir() {
		return d
	}
	d.HasGraph = true
	v := fi.ModTime().UTC().Format(time.RFC3339)
	d.BuiltAt = &v

	// Counts come from the file's `meta` header. A wiring graph is unbounded in
	// size (nodes + edges for a whole repo), and this feed is polled, so the
	// decoder reads ONLY the first key and stops — never the node array. graft
	// writes meta first (verified against 0.16.0); if a future version reorders,
	// the counts come back 0 and the project still reports hasGraph correctly,
	// which is the right way for this to degrade. Scanning further to find meta
	// would trade a graceful zero for a whole-file read on every poll.
	f, err := os.Open(wiring)
	if err != nil {
		return d
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if _, err := dec.Token(); err != nil { // opening '{'
		return d
	}
	if !dec.More() {
		return d
	}
	key, err := dec.Token()
	if err != nil || key != "meta" {
		return d
	}
	var meta struct {
		NodeCount int `json:"nodeCount"`
		EdgeCount int `json:"edgeCount"`
	}
	if err := dec.Decode(&meta); err == nil {
		d.Nodes, d.Edges = meta.NodeCount, meta.EdgeCount
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
