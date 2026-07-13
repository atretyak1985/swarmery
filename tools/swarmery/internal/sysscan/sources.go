package sysscan

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// source classifies where a discovered item lives — it fully determines the
// registry row's scope / project_id / origin / plugin_name columns.
type source struct {
	scope     string        // global | project
	projectID sql.NullInt64 // set for scope=project
	origin    string        // local | plugin
	plugin    string        // plugin name when origin=plugin
}

// mdFile is one discovered markdown component file (agent or command).
type mdFile struct {
	path string
	src  source
}

// skillDir is one discovered skill directory (identity = dir name, format
// doc §2.2); the versioned content is the SKILL.md inside it.
type skillDir struct {
	dir string // the skill directory (skills.dir_path)
	src source
}

// settingsFile is one hooks source candidate (settings.json tier).
type settingsFile struct {
	path string
	src  source
}

// pluginRoot is the active cache root of one installed plugin:
// <ClaudeDir>/plugins/cache/<marketplace>/<plugin>/<version>/ (§5.1 — note
// the version level).
type pluginRoot struct {
	marketplace string
	plugin      string
	root        string
}

// projectRow is one non-archived, absolute-path project from the daemon DB.
type projectRow struct {
	id   int64
	path string
}

// loadProjects lists the scannable project universe: archived=0 rows with an
// absolute path — the '(unknown)' placeholder row is skipped exactly like
// hookcfg.ProjectsFromDB does (format doc §0). Paths with spaces are fine:
// nothing here shells out.
func (s *Scanner) loadProjects() ([]projectRow, error) {
	rows, err := s.db.Query(`SELECT id, path FROM projects WHERE archived = 0 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []projectRow
	for rows.Next() {
		var pr projectRow
		if err := rows.Scan(&pr.id, &pr.path); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(pr.path) {
			continue // '(unknown)' and friends
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// walkMD lists every *.md under root recursively (project agent trees have
// nested subdirs — format doc §1.1). A missing root is a silent no-op; any
// other error is reported through warn and the walk continues.
func walkMD(root string, warn func(string, ...any)) []string {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			warn("walk %s: %v", path, err)
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		warn("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// walkSkillDirs lists every directory under root that contains a SKILL.md
// (recursive — project layouts nest, format doc §2.1).
func walkSkillDirs(root string, warn func(string, ...any)) []string {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			warn("walk %s: %v", path, err)
			return nil
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != root {
			return fs.SkipDir
		}
		if !d.IsDir() && d.Name() == "SKILL.md" {
			out = append(out, filepath.Dir(path))
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		warn("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// globSkillDirs lists <root>/skills/*/SKILL.md dirs ONLY — one level. For
// plugins this is deliberate: only `skills/` is auto-discovered by Claude
// Code; `workflow-skills/` and similar dirs are not loaded (format doc §2.2).
func globSkillDirs(skillsRoot string) []string {
	matches, _ := filepath.Glob(filepath.Join(skillsRoot, "*", "SKILL.md"))
	sort.Strings(matches)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, filepath.Dir(m))
	}
	return out
}

// pluginRoots resolves the active cache root per installed plugin under
// <ClaudeDir>/plugins/cache/<mkt>/<plugin>/<version>/ (§5.1). Version pick:
// the dir carrying an `.in_use` marker, else the lexically greatest (version
// strings are opaque — semver, `unknown`, git-sha prefixes all observed).
// A plugin name shipped by two marketplaces (agentry/core vs swarmery/core,
// §7 caveat) prefers the marketplace recorded in installed_plugins.json —
// an orphaned cache of a removed marketplace must not shadow the enabled
// plugin (§5.2 staleness). With no install record on either side, the first
// marketplace alphabetically wins so "plugin:name" stays deterministic.
func pluginRoots(claudeDir string, warn func(string, ...any)) []pluginRoot {
	cacheDir := filepath.Join(claudeDir, "plugins", "cache")
	mkts, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil // no plugin cache — fine
	}
	installed := installedMarketplaces(claudeDir)
	var out []pluginRoot
	byName := map[string]int{} // plugin name → index in out
	sort.Slice(mkts, func(i, j int) bool { return mkts[i].Name() < mkts[j].Name() })
	for _, mkt := range mkts {
		if !mkt.IsDir() || strings.HasPrefix(mkt.Name(), ".") {
			continue
		}
		plugins, err := os.ReadDir(filepath.Join(cacheDir, mkt.Name()))
		if err != nil {
			warn("plugin cache %s: %v", mkt.Name(), err)
			continue
		}
		for _, pl := range plugins {
			if !pl.IsDir() || strings.HasPrefix(pl.Name(), ".") {
				continue
			}
			verDir := pickVersionDir(filepath.Join(cacheDir, mkt.Name(), pl.Name()))
			if verDir == "" {
				continue // empty plugin dir (dangling install record, §5.2)
			}
			if i, dup := byName[pl.Name()]; dup {
				prev := out[i]
				keep, drop := prev.marketplace, mkt.Name()
				if installed[pl.Name()+"@"+mkt.Name()] && !installed[pl.Name()+"@"+prev.marketplace] {
					out[i] = pluginRoot{marketplace: mkt.Name(), plugin: pl.Name(), root: verDir}
					keep, drop = mkt.Name(), prev.marketplace
				}
				warn("plugin %q shipped by marketplaces %q and %q — keeping %q (cross-marketplace collision, format doc §7)",
					pl.Name(), keep, drop, keep)
				continue
			}
			byName[pl.Name()] = len(out)
			out = append(out, pluginRoot{marketplace: mkt.Name(), plugin: pl.Name(), root: verDir})
		}
	}
	return out
}

// installedMarketplaces reads <ClaudeDir>/plugins/installed_plugins.json
// (§5.2: {"version":2,"plugins":{"<plugin>@<marketplace>":[…]}}) and returns
// the set of "<plugin>@<marketplace>" keys. Missing or malformed file → empty
// set (the alphabetical fallback still applies).
func installedMarketplaces(claudeDir string) map[string]bool {
	raw, err := os.ReadFile(filepath.Join(claudeDir, "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	set := make(map[string]bool, len(doc.Plugins))
	for key := range doc.Plugins {
		set[key] = true
	}
	return set
}

// pickVersionDir chooses the active <version> dir of one cached plugin:
// prefer the one containing an `.in_use` marker (an empty DIRECTORY on
// active versions, §5.1), else the lexically greatest dir name.
func pickVersionDir(pluginDir string) string {
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		return ""
	}
	var versions []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(pluginDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, ".in_use")); err == nil {
			return dir
		}
		versions = append(versions, e.Name())
	}
	if len(versions) == 0 {
		return ""
	}
	sort.Strings(versions)
	return filepath.Join(pluginDir, versions[len(versions)-1])
}

// settingsCandidates enumerates every hooks source file for this scan pass.
// User tier: settings.json only; project tier: settings.json AND
// settings.local.json as SEPARATE source_file rows (recorded decision,
// format doc §3.4). *.bak files are never candidates by construction.
func settingsCandidates(claudeDir string, projects []projectRow) []settingsFile {
	out := []settingsFile{{
		path: filepath.Join(claudeDir, "settings.json"),
		src:  source{scope: "global", origin: "local"},
	}}
	for _, pr := range projects {
		src := source{scope: "project", projectID: sql.NullInt64{Int64: pr.id, Valid: true}, origin: "local"}
		out = append(out,
			settingsFile{path: filepath.Join(pr.path, ".claude", "settings.json"), src: src},
			settingsFile{path: filepath.Join(pr.path, ".claude", "settings.local.json"), src: src},
		)
	}
	return out
}

// overlayFiles lists every file under the overlays dir (templates tab is
// read-on-demand in the API, step-05 — the scan only reports the paths).
func overlayFiles(dir string, warn func(string, ...any)) []string {
	if dir == "" {
		return nil
	}
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			warn("overlays %s: %v", path, err)
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != dir {
				return fs.SkipDir
			}
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		warn("overlays %s: %v", dir, err)
	}
	sort.Strings(out)
	return out
}
