package api

// phase: onboarding — POST /api/projects/onboard bootstraps a NEW consumer
// project from the dashboard, reusing internal/onboard (the same code the
// `swarmery onboard` CLI and scripts/init.sh run). This is the ONLY write
// surface that touches a caller-supplied filesystem path outside ~/.claude, so
// it is fenced twice: requireLocalOrigin at the route, and an explicit
// allow-list of parent roots here — with an empty allow-list the endpoint is
// DISABLED (opt-in, safe default on shared machines).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/onboard"
)

// onboardCfg is attached once at daemon startup. The zero value (no Roots)
// keeps the endpoint disabled until the operator opts in via
// SWARMERY_ONBOARD_ROOTS.
var onboardCfg OnboardConfig

// OnboardConfig fences and parameterises the onboarding endpoint.
type OnboardConfig struct {
	// Roots is the allow-list of parent directories a project may be onboarded
	// under. Empty → the endpoint is disabled (403).
	Roots []string
	// WorkspaceRoot is the shared workspace repo where the namespace is carved.
	WorkspaceRoot string
	// StatuslineSrc, when set, is the plugins/core/statusline dir the statusline
	// scripts are copied from. Empty skips the (opt-in) statusline step.
	StatuslineSrc string
}

// AttachOnboard wires the onboarding config; call once at startup.
func AttachOnboard(cfg OnboardConfig) { onboardCfg = cfg }

// maxOnboardBody bounds the request — a slug, a path, and a short pack list.
const maxOnboardBody = 1 << 16

type onboardRequest struct {
	Slug  string   `json:"slug"`
	Path  string   `json:"path"`
	Packs []string `json:"packs"`
	// WorkspaceRoot overrides the server default for this one project; empty
	// falls back to onboardCfg.WorkspaceRoot (AGENT_WORKSPACE_ROOT baked in it).
	WorkspaceRoot string `json:"workspaceRoot"`
}

type onboardResponse struct {
	Slug string `json:"slug"`
	Path string `json:"path"`
	// WorkspaceRoot echoes the value actually used (default or override) so the
	// UI can show where the namespace landed.
	WorkspaceRoot string   `json:"workspaceRoot"`
	Steps         []string `json:"steps"`
}

// onboardConfigResponse feeds the dashboard modal its default values and the
// enabled state, so it can prefill placeholders and disable the form up front
// instead of only discovering the 403 on submit.
type onboardConfigResponse struct {
	Enabled       bool     `json:"enabled"`
	WorkspaceRoot string   `json:"workspaceRoot"`
	Roots         []string `json:"roots"`
}

// onboardConfig handles GET /api/projects/onboard/config (read-only, no fence).
func (h *Handler) onboardConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, onboardConfigResponse{
		Enabled:       len(onboardCfg.Roots) > 0,
		WorkspaceRoot: onboardCfg.WorkspaceRoot,
		Roots:         onboardCfg.Roots,
	}, nil)
}

// onboardProject handles POST /api/projects/onboard.
func (h *Handler) onboardProject(w http.ResponseWriter, r *http.Request) {
	if len(onboardCfg.Roots) == 0 {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{
			"error": "project onboarding is disabled — start the daemon with SWARMERY_ONBOARD_ROOTS set to the allowed parent directories",
		})
		return
	}

	var req onboardRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxOnboardBody)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		http.Error(w, `{"error":"path is required"}`, http.StatusBadRequest)
		return
	}

	// Fence the target path under an allowed root (symlink-safe), THEN validate
	// slug/packs — so a caller can never write .claude/ outside the allow-list.
	target, err := resolveUnderRoots(req.Path, onboardCfg.Roots)
	if err != nil {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if info, statErr := os.Stat(target); statErr != nil || !info.IsDir() {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{
			"error": "path does not exist or is not a directory: " + target,
		})
		return
	}

	// Per-project workspace override; empty falls back to the server default.
	wsRoot := strings.TrimSpace(req.WorkspaceRoot)
	if wsRoot == "" {
		wsRoot = onboardCfg.WorkspaceRoot
	}

	cfg := onboard.Config{
		Slug:          req.Slug,
		ProjectDir:    target,
		Packs:         req.Packs,
		WorkspaceRoot: wsRoot,
		StatuslineSrc: onboardCfg.StatuslineSrc,
	}
	if err := onboard.Validate(cfg); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	res, err := onboard.Run(cfg)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, onboardResponse{
		Slug: req.Slug, Path: target, WorkspaceRoot: wsRoot, Steps: res.Steps,
	})
}

// resolveUnderRoots returns the cleaned absolute target path only if it lives
// under one of roots. It resolves symlinks on the nearest EXISTING ancestor
// (the target itself may not exist yet) so neither `..` traversal nor a symlink
// escape can point the write outside the allow-list.
func resolveUnderRoots(target string, roots []string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid path: %v", err)
	}
	abs = filepath.Clean(abs)

	// Lexical pre-fence (path-injection barrier): reject any target that is not
	// textually nested under an allowed root before touching the filesystem.
	// filepath.Clean has already collapsed every ".." in the absolute path, so
	// this prefix test is a sound guard; the symlink-safe re-check below remains
	// the authoritative gate against symlink escapes on the existing ancestor.
	if !lexicallyUnderRoots(abs, roots) {
		return "", fmt.Errorf("path %s is not under any allowed onboarding root", abs)
	}

	anc := abs
	for {
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	realAnc, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path: %v", err)
	}

	for _, root := range roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if underDir(realAnc, realRoot) {
			rel, err := filepath.Rel(anc, abs)
			if err != nil {
				return "", fmt.Errorf("invalid path: %v", err)
			}
			return filepath.Join(realAnc, rel), nil
		}
	}
	return "", fmt.Errorf("path %s is not under any allowed onboarding root", abs)
}

// lexicallyUnderRoots reports whether the cleaned absolute path abs is textually
// nested under any root — matched against both the root as configured and its
// symlink-resolved form, so a caller may reference a root through either. It is a
// pre-filter only: the symlink-safe containment check in resolveUnderRoots stays
// authoritative for the accept decision.
func lexicallyUnderRoots(abs string, roots []string) bool {
	for _, root := range roots {
		bases := []string{filepath.Clean(root)}
		if real, err := filepath.EvalSymlinks(root); err == nil {
			bases = append(bases, real)
		}
		for _, base := range bases {
			if abs == base || strings.HasPrefix(abs, base+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// underDir reports whether path is dir itself or nested inside it.
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && !filepath.IsAbs(rel))
}
