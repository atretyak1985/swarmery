package api

// projectDTO.Onboarded — "was THIS project onboarded onto swarmery on its own",
// as opposed to merely reading `managed: true`.
//
// `managed` (enabledPlugins["core@swarmery"] in the project's
// .claude/settings.json) is necessary but not sufficient: a multi-repo
// umbrella onboarded once at its own root copies that settings.json into every
// sub-repo it declares, so each sub-repo reads back managed without anyone
// having onboarded it. A managed project nested under another managed project
// is therefore treated as covered by that umbrella, not onboarded itself.
//
// The nesting rule needs the same exclusions the attribution ancestor rule in
// internal/ingest (CanonicalProjectPath) applies, and the client cannot know
// them — which is why this is computed here and not in the browser:
//   - an onboarding root (SWARMERY_ONBOARD_ROOTS) is a parent dir of many
//     unrelated repos, never an umbrella;
//   - $HOME: with core enabled at user scope, ~/.claude/settings.json IS the
//     `~` row's project settings, so a session once run in ~ makes that row
//     managed — it must not swallow every project on the machine;
//   - "/" for the same reason;
//   - archived rows and the System project.
// Such a row is neither an umbrella for others nor onboarded itself.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
)

// umbrellaBarred reports whether path may never act as an umbrella (and is
// never onboarded itself): "/", $HOME, the System dir, or an onboarding root.
func umbrellaBarred(path, home string, roots []string) bool {
	clean := filepath.Clean(path)
	if clean == "/" || clean == "." || (home != "" && clean == home) {
		return true
	}
	if sys := ingest.SystemDir(); sys != "" && clean == filepath.Clean(sys) {
		return true
	}
	for _, r := range roots {
		if strings.TrimSpace(r) != "" && clean == filepath.Clean(r) {
			return true
		}
	}
	return false
}

func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return ""
	}
	return filepath.Clean(h)
}

// isUmbrellaFor reports whether candidate is an eligible umbrella that p is
// strictly nested under.
func isUmbrellaFor(p, candidate *projectDTO, home string, roots []string) bool {
	if candidate.ID == p.ID || candidate.Archived || candidate.IsSystem {
		return false
	}
	if candidate.Plugin == nil || !candidate.Plugin.Managed {
		return false
	}
	if umbrellaBarred(candidate.Path, home, roots) {
		return false
	}
	return strings.HasPrefix(filepath.Clean(p.Path), filepath.Clean(candidate.Path)+"/")
}

// onboardedAmong computes p.Onboarded against the given candidate umbrellas.
func onboardedAmong(p *projectDTO, candidates []projectDTO, home string, roots []string) bool {
	if p.IsSystem || p.Plugin == nil || !p.Plugin.Managed || umbrellaBarred(p.Path, home, roots) {
		return false
	}
	for i := range candidates {
		if isUmbrellaFor(p, &candidates[i], home, roots) {
			return false
		}
	}
	return true
}

// markOnboarded fills Onboarded on every row of a full project list.
func markOnboarded(projects []projectDTO, roots []string) {
	home := userHome()
	for i := range projects {
		projects[i].Onboarded = onboardedAmong(&projects[i], projects, home, roots)
	}
}

// markOnboardedOne fills Onboarded for a single project (the detail route),
// loading only its registered, non-archived ancestors as candidate umbrellas —
// a bare id/path query plus a settings read each, not the aggregate
// projectSelect.
func (h *Handler) markOnboardedOne(p *projectDTO, roots []string) error {
	rows, err := h.DB.Query(
		`SELECT id, path FROM projects WHERE ? LIKE path || '/%' AND archived = 0`, p.Path)
	if err != nil {
		return err
	}
	defer func(rows *sql.Rows) { _ = rows.Close() }(rows)
	var ancestors []projectDTO
	for rows.Next() {
		var a projectDTO
		if err := rows.Scan(&a.ID, &a.Path); err != nil {
			return err
		}
		if st, err := projectscan.ReadPluginState(a.Path, roots, overlaysFor(a.Path)...); err == nil {
			a.Plugin = st
		}
		ancestors = append(ancestors, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	p.Onboarded = onboardedAmong(p, ancestors, userHome(), roots)
	return nil
}
