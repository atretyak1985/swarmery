package api

// phase: projects — GET /api/projects/{id}/plugins merges the swarmery
// marketplace catalog (the clone under ~/.claude/plugins/marketplaces/swarmery,
// read via internal/marketplace) with the project's enabledPlugins state
// (projectscan.ReadPluginState). Read-only and unfenced; the canWrite flag
// tells the UI whether the PUT fence (step 03, same file) would admit a write.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/marketplace"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/onboard"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
)

// pluginMarketplace is the only marketplace this surface manages — matches
// projectscan's marketplaceSuffix ("@swarmery") view of enabledPlugins.
const pluginMarketplace = "swarmery"

// pluginCatalogDir is attached once at startup (or per-test); empty ⇒ resolve
// ~/.claude at request time. Mirrors AttachOnboard (onboard.go:41).
var pluginCatalogDir string

// AttachPluginCatalog points the project-plugins endpoints at the directory
// holding plugins/marketplaces/ (production: ~/.claude; tests: a temp dir).
func AttachPluginCatalog(claudeDir string) { pluginCatalogDir = claudeDir }

func catalogDir() (string, error) {
	if pluginCatalogDir != "" {
		return pluginCatalogDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

type projectPluginDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	// Locked marks plugins this surface refuses to toggle: core's lifecycle is
	// attach/detach (hooks + statusline + project.json travel with it).
	Locked bool `json:"locked"`
}

type projectPluginsResponse struct {
	MarketplaceVersion string             `json:"marketplaceVersion"`
	CanWrite           bool               `json:"canWrite"`
	Plugins            []projectPluginDTO `json:"plugins"`
}

// projectPlugins handles GET /api/projects/{id}/plugins.
func (h *Handler) projectPlugins(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid project id"}`, http.StatusBadRequest)
		return
	}
	var path string
	err = h.DB.QueryRow(`SELECT path FROM projects WHERE id = ?`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	cdir, err := catalogDir()
	if err != nil {
		writeErr(w, err)
		return
	}
	cat, err := marketplace.Read(cdir, pluginMarketplace)
	if errors.Is(err, fs.ErrNotExist) {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{
			"error": "swarmery marketplace is not installed on this machine — run a Claude Code marketplace update",
		})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	// Enabled state: Managed covers core, Packs the domain packs. A nil state
	// (telemetry-only project, unreadable settings) renders everything off.
	// roots=nil: UnderOnboardRoot is unused here — canWrite is derived
	// separately below via resolveUnderRoots.
	enabledCore, enabledPacks := false, []string{}
	if st, serr := projectscan.ReadPluginState(path, nil); serr == nil && st != nil {
		enabledCore = st.Managed
		enabledPacks = st.Packs
	}

	// canWrite mirrors the attach/detach fence (attach.go:42-87): roots must be
	// configured AND the project path must resolve under one of them.
	canWrite := false
	if len(onboardCfg.Roots) > 0 {
		if _, ferr := resolveUnderRoots(path, onboardCfg.Roots); ferr == nil {
			canWrite = true
		}
	}

	resp := projectPluginsResponse{MarketplaceVersion: cat.Version, CanWrite: canWrite, Plugins: []projectPluginDTO{}}
	seen := map[string]bool{}
	for _, p := range cat.Plugins {
		seen[p.Name] = true
		enabled := (p.Name == "core" && enabledCore) || slices.Contains(enabledPacks, p.Name)
		resp.Plugins = append(resp.Plugins, projectPluginDTO{
			Name: p.Name, Description: p.Description,
			Enabled: enabled, Locked: p.Name == "core",
		})
	}
	// Enabled-but-unknown packs (stale clone) must stay visible.
	for _, name := range enabledPacks {
		if !seen[name] {
			resp.Plugins = append(resp.Plugins, projectPluginDTO{
				Name:        name,
				Description: "(enabled here, but missing from the local marketplace clone — refresh marketplaces)",
				Enabled:     true,
			})
		}
	}
	writeJSON(w, resp, nil)
}

type putPluginRequest struct {
	Enabled bool `json:"enabled"`
}

type putPluginResponse struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Changed bool   `json:"changed"`
	Backup  string `json:"backup,omitempty"`
}

// putProjectPlugin handles PUT /api/projects/{id}/plugins/{name}. Fenced like
// attach: requireLocalOrigin at the route, SWARMERY_ONBOARD_ROOTS here,
// resolveUnderRoots before the write.
func (h *Handler) putProjectPlugin(w http.ResponseWriter, r *http.Request) {
	if len(onboardCfg.Roots) == 0 {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{
			"error": "plugin toggles are disabled — start the daemon with SWARMERY_ONBOARD_ROOTS set to the allowed parent directories",
		})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid project id"}`, http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	// onboard.TogglePlugin has its own ErrCoreLocked guard; this check exists to
	// answer 400 before any I/O — neither is redundant, keep both.
	if name == "core" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "core is managed via attach/detach"})
		return
	}
	var req putPluginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	var path string
	err = h.DB.QueryRow(`SELECT path FROM projects WHERE id = ?`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	target, err := resolveUnderRoots(path, onboardCfg.Roots)
	if err != nil {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}

	// Enabling requires the pack to exist in the catalog; disabling does not
	// (a stale clone must not trap an enabled pack in the on state).
	if req.Enabled {
		cdir, cerr := catalogDir()
		if cerr != nil {
			writeErr(w, cerr)
			return
		}
		cat, cerr := marketplace.Read(cdir, pluginMarketplace)
		if cerr != nil {
			writeErr(w, cerr)
			return
		}
		known := false
		for _, p := range cat.Plugins {
			if p.Name == name {
				known = true
				break
			}
		}
		if !known {
			writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "unknown plugin: " + name})
			return
		}
	}

	res, err := onboard.TogglePlugin(target, name, req.Enabled)
	switch {
	case errors.Is(err, onboard.ErrCoreLocked):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, onboard.ErrNoSettings), errors.Is(err, onboard.ErrBadSettings):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	// Auto-provision: only on a real enable (a no-op re-enable or any disable
	// must not kick off install/generate). Best-effort — enqueueProvision never
	// blocks or fails the toggle response.
	if res.Changed && req.Enabled {
		h.enqueueProvision(id, target, name)
	}
	writeJSON(w, putPluginResponse{Name: name, Enabled: req.Enabled, Changed: res.Changed, Backup: res.Backup}, nil)
}
