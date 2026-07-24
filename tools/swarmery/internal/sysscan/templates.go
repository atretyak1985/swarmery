package sysscan

// Template discovery (fusion phase 18 — System Hub Toolkit): the template
// surface the sysscan registry never tracked in a table. Templates are the
// copy-to-project scaffolds Claude Code agents resolve via
//
//	${CLAUDE_PROJECT_DIR}/.claude/templates/   (project override — wins)
//	${CLAUDE_PLUGIN_ROOT}/templates/           (an installed pack's built-in)
//
// (docs/CLAUDE.md "Template resolution"). Unlike agents/skills/commands these
// carry NO YAML frontmatter (a leading `# Title`, not `---`), so there is
// nothing to lint or version — they are pure content scaffolds. Rather than add
// a DB table + migration for a read-mostly surface, this file scans the two
// roots on demand and returns the discovered set; the API layer (system_hub.go)
// resolves the effective view and serves content live from disk. The ONLY write
// is copy-to-project (O_EXCL), handled in the API, mirroring the playbook
// duplicate-to-project idiom + the graduation rule.
//
// Scan shape mirrors commands: the top-level *.md files of each templates/ dir
// (flat — the nested example dirs core ships, e.g. templates/working/, are
// scaffolding directories, not selectable templates). A missing root is a
// silent no-op, exactly like walkMD.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TemplateSource classifies where a template resolves from.
type TemplateSource string

const (
	// TemplateSourcePlugin is a built-in shipped by an installed pack's
	// templates/ dir (PluginName carries the pack name, e.g. "core").
	TemplateSourcePlugin TemplateSource = "plugin"
	// TemplateSourceProject is a project-local override under
	// <project>/.claude/templates/ (ProjectSlug carries the project).
	TemplateSourceProject TemplateSource = "project"
)

// Template is one discovered template scaffold. Content is read on demand by the
// API (never held here) so the scan stays cheap and always current.
type Template struct {
	// Name is the file stem (identity within a resolution scope — a project
	// override and a pack built-in of the same stem share this Name, and the
	// project one wins in the effective view).
	Name string
	// FileName is the on-disk basename (Name + ".md").
	FileName string
	// Path is the absolute on-disk path — the read/copy handle.
	Path string
	// Source is plugin (built-in) or project (override).
	Source TemplateSource
	// PluginName is the pack name for Source=plugin ("" otherwise).
	PluginName string
	// ProjectSlug is the owning project slug for Source=project ("" otherwise).
	ProjectSlug string
	// ProjectID is the owning project row id for Source=project (0 otherwise).
	ProjectID int64
}

// TemplateProject is the minimal project handle the template scan needs: the
// slug + row id + absolute path of a scannable project. The API builds these
// from its own projects query so this stays decoupled from the DB.
type TemplateProject struct {
	ID   int64
	Slug string
	Path string
}

// ScanTemplates discovers every template under (a) each installed plugin's
// templates/ dir and (b) each project's .claude/templates/ dir. Built-ins come
// first (sorted by plugin then name), project overrides after. The result is the
// RAW discovered set — the caller folds it into an effective view (project
// shadows pack) and gates pack templates by the project's enabled plugins.
//
// claudeDir is the same ~/.claude the scanner uses (plugin cache root). A nil
// warn tolerates errors silently (the API passes a logging warn).
func ScanTemplates(claudeDir string, projects []TemplateProject, warn func(string, ...any)) []Template {
	if warn == nil {
		warn = func(string, ...any) {}
	}
	var out []Template

	// (a) plugin built-ins: <pluginRoot>/templates/*.md
	for _, pl := range pluginRoots(claudeDir, warn) {
		for _, p := range templateFiles(filepath.Join(pl.root, "templates")) {
			out = append(out, Template{
				Name:       stem(p),
				FileName:   filepath.Base(p),
				Path:       p,
				Source:     TemplateSourcePlugin,
				PluginName: pl.plugin,
			})
		}
	}

	// (b) project overrides: <project>/.claude/templates/*.md
	for _, pr := range projects {
		for _, p := range templateFiles(filepath.Join(pr.Path, ".claude", "templates")) {
			out = append(out, Template{
				Name:        stem(p),
				FileName:    filepath.Base(p),
				Path:        p,
				Source:      TemplateSourceProject,
				ProjectSlug: pr.Slug,
				ProjectID:   pr.ID,
			})
		}
	}
	return out
}

// templateFiles lists the top-level *.md files of one templates/ dir (flat, name
// order). A missing dir yields nil — a project or pack without templates simply
// contributes nothing. Nested dirs are skipped (scaffolding, not selectable).
func templateFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // missing / unreadable → no templates here
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}
