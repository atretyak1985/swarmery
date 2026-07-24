package sysscan

import (
	"os"
	"path/filepath"
	"testing"
)

// mkTemplate writes a template file (creating parents), for the scan fixtures.
func mkTemplate(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// pluginTemplatesDir builds a minimal active plugin-cache templates dir:
// <claude>/plugins/cache/<mkt>/<plugin>/<ver>/templates and marks the version
// active with an .in_use directory (pickVersionDir's active-version marker).
func pluginTemplatesDir(t *testing.T, claudeDir, mkt, plugin, ver string) string {
	t.Helper()
	verDir := filepath.Join(claudeDir, "plugins", "cache", mkt, plugin, ver)
	if err := os.MkdirAll(filepath.Join(verDir, ".in_use"), 0o755); err != nil {
		t.Fatalf("mkdir in_use: %v", err)
	}
	dir := filepath.Join(verDir, "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir templates: %v", err)
	}
	return dir
}

func TestScanTemplates_BuiltinsAndProjectOverrides(t *testing.T) {
	claudeDir := t.TempDir()
	projDir := t.TempDir()

	// A pack ships two built-ins; a nested scaffold dir must be ignored.
	pdir := pluginTemplatesDir(t, claudeDir, "swarmery", "core", "2.2.0")
	mkTemplate(t, filepath.Join(pdir, "adr-template.md"), "# ADR\n")
	mkTemplate(t, filepath.Join(pdir, "pr-description-template.md"), "# PR\n")
	mkTemplate(t, filepath.Join(pdir, "working", "example.md"), "# nested — ignored\n")
	// A non-markdown sibling must be ignored too.
	mkTemplate(t, filepath.Join(pdir, "notes.txt"), "not a template")

	// The project overrides adr-template + adds its own local one.
	projTmpl := filepath.Join(projDir, ".claude", "templates")
	mkTemplate(t, filepath.Join(projTmpl, "adr-template.md"), "# project ADR override\n")
	mkTemplate(t, filepath.Join(projTmpl, "runbook.md"), "# project runbook\n")

	projects := []TemplateProject{{ID: 7, Slug: "alpha", Path: projDir}}
	got := ScanTemplates(claudeDir, projects, nil)

	// Expect: 2 plugin built-ins + 2 project overrides = 4 (nested + .txt excluded).
	if len(got) != 4 {
		t.Fatalf("want 4 templates, got %d: %+v", len(got), got)
	}

	// Index by (source, name) for assertions.
	type key struct {
		src  TemplateSource
		name string
	}
	idx := map[key]Template{}
	for _, tm := range got {
		idx[key{tm.Source, tm.Name}] = tm
	}

	adrPlugin, ok := idx[key{TemplateSourcePlugin, "adr-template"}]
	if !ok {
		t.Fatal("missing plugin adr-template")
	}
	if adrPlugin.PluginName != "core" {
		t.Errorf("plugin adr-template PluginName = %q, want core", adrPlugin.PluginName)
	}
	if adrPlugin.FileName != "adr-template.md" {
		t.Errorf("FileName = %q, want adr-template.md", adrPlugin.FileName)
	}
	if adrPlugin.ProjectSlug != "" || adrPlugin.ProjectID != 0 {
		t.Errorf("plugin template must not carry a project: %+v", adrPlugin)
	}

	adrProj, ok := idx[key{TemplateSourceProject, "adr-template"}]
	if !ok {
		t.Fatal("missing project adr-template override")
	}
	if adrProj.ProjectSlug != "alpha" || adrProj.ProjectID != 7 {
		t.Errorf("project template project mismatch: %+v", adrProj)
	}
	if adrProj.PluginName != "" {
		t.Errorf("project template must not carry a plugin name: %+v", adrProj)
	}
	// The project override path must live under the project's .claude/templates.
	if filepath.Dir(adrProj.Path) != projTmpl {
		t.Errorf("project override path = %q, want under %q", adrProj.Path, projTmpl)
	}

	if _, ok := idx[key{TemplateSourceProject, "runbook"}]; !ok {
		t.Error("missing project-local runbook template")
	}
	// The nested scaffold dir must not appear as a template.
	if _, ok := idx[key{TemplateSourcePlugin, "example"}]; ok {
		t.Error("nested templates/working/example.md must be ignored")
	}
}

func TestScanTemplates_MissingRootsAreNoOp(t *testing.T) {
	claudeDir := t.TempDir() // no plugin cache at all
	// A project whose .claude/templates does not exist.
	projects := []TemplateProject{{ID: 1, Slug: "empty", Path: t.TempDir()}}
	got := ScanTemplates(claudeDir, projects, nil)
	if len(got) != 0 {
		t.Fatalf("want 0 templates from empty roots, got %d: %+v", len(got), got)
	}
}

func TestScanTemplates_PluginBuiltinsOrderedFirst(t *testing.T) {
	claudeDir := t.TempDir()
	projDir := t.TempDir()

	pdir := pluginTemplatesDir(t, claudeDir, "swarmery", "core", "2.2.0")
	mkTemplate(t, filepath.Join(pdir, "z-builtin.md"), "# z\n")

	projTmpl := filepath.Join(projDir, ".claude", "templates")
	mkTemplate(t, filepath.Join(projTmpl, "a-project.md"), "# a\n")

	projects := []TemplateProject{{ID: 1, Slug: "p", Path: projDir}}
	got := ScanTemplates(claudeDir, projects, nil)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// Built-ins are emitted before project overrides regardless of name order.
	if got[0].Source != TemplateSourcePlugin || got[1].Source != TemplateSourceProject {
		t.Errorf("ordering: got[0]=%s got[1]=%s, want plugin then project", got[0].Source, got[1].Source)
	}
}
