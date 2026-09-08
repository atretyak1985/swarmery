package archmap

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fixtureProject copies testdata/fixtures/architecture-map.json into a temp
// project laid out the way Load expects (<project>/architecture-out/…) and
// returns the project root.
func fixtureProject(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "architecture-map.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	root := t.TempDir()
	out := filepath.Join(root, OutDir)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, MapFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func loadFixture(t *testing.T) *Map {
	t.Helper()
	m, err := Load(fixtureProject(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m
}

func TestLoad(t *testing.T) {
	t.Run("fixture", func(t *testing.T) {
		m := loadFixture(t)
		if m.SchemaVersion != 1 {
			t.Errorf("schemaVersion = %d, want 1", m.SchemaVersion)
		}
		if len(m.Modules) != 5 {
			t.Errorf("modules = %d, want 5", len(m.Modules))
		}
		if len(m.Flows) != 3 {
			t.Errorf("flows = %d, want 3", len(m.Flows))
		}
		if m.AnalyzedAtCommit == "" {
			t.Error("analyzedAtCommit is empty, want the fixture sha")
		}
		if got := ModuleCount(m); got != 5 {
			t.Errorf("ModuleCount = %d, want 5", got)
		}
	})

	t.Run("missing artifact is ErrNoMap", func(t *testing.T) {
		_, err := Load(t.TempDir())
		if !errors.Is(err, ErrNoMap) {
			t.Errorf("err = %v, want ErrNoMap", err)
		}
	})

	t.Run("unparseable JSON is an error, not an empty map", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, OutDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, OutDir, MapFileName), []byte("not JSON!!"), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := Load(root)
		if err == nil {
			t.Fatalf("err = nil, want a parse error (got map %+v)", m)
		}
		if errors.Is(err, ErrNoMap) {
			t.Error("a corrupt map reported ErrNoMap; the two failures must stay distinguishable")
		}
	})

	t.Run("a future schemaVersion is refused", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, OutDir), 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"schemaVersion":2,"modules":[],"flows":[]}`
		if err := os.WriteFile(filepath.Join(root, OutDir, MapFileName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Error("err = nil for schemaVersion 2, want a refusal")
		}
	})
}

// TestMatchLongestPrefix is the core contract: a file under a nested module
// belongs to the NESTED module, not to its parent, and a shared string prefix
// across sibling trees is not a match at all.
func TestMatchLongestPrefix(t *testing.T) {
	m := loadFixture(t)
	got := Match(m, []string{
		"web/src/pages/Home.tsx", // → web-pages (longest prefix beats web/src)
		"web/src/main.tsx",       // → web       (only web/src claims it)
		"web/src-legacy/old.tsx", // → web-legacy, NOT web (string prefix ≠ path prefix)
		"cmd/app/main.go",        // → single-file (module path IS the file)
	})

	want := map[string][]string{
		"web":         {"web/src/main.tsx"},
		"web-pages":   {"web/src/pages/Home.tsx"},
		"web-legacy":  {"web/src-legacy/old.tsx"},
		"single-file": {"cmd/app/main.go"},
	}
	if len(got.Modules) != len(want) {
		t.Fatalf("touched modules = %d (%+v), want %d", len(got.Modules), got.Modules, len(want))
	}
	for _, tm := range got.Modules {
		w, ok := want[tm.ID]
		if !ok {
			t.Errorf("unexpected touched module %q", tm.ID)
			continue
		}
		if !reflect.DeepEqual(tm.Files, w) {
			t.Errorf("module %q files = %v, want %v", tm.ID, tm.Files, w)
		}
		if tm.Name == "" {
			t.Errorf("module %q has an empty display name", tm.ID)
		}
	}
	if len(got.Unmatched) != 0 {
		t.Errorf("unmatched = %v, want none", got.Unmatched)
	}
}

func TestMatchFlowSteps(t *testing.T) {
	m := loadFixture(t)
	got := Match(m, []string{"web/src/pages/Home.tsx", "internal/api/server.go"})

	byID := map[string][]string{}
	for _, f := range got.Flows {
		byID[f.ID] = f.Steps
	}
	if len(byID) != 2 {
		t.Fatalf("touched flows = %+v, want exactly render-page and boot", got.Flows)
	}
	if steps, ok := byID["render-page"]; !ok {
		t.Error("render-page not touched; web/src/pages/Home.tsx anchors its first step")
	} else if len(steps) != 1 {
		t.Errorf("render-page steps = %v, want just the Home.tsx step", steps)
	}
	if steps, ok := byID["boot"]; !ok {
		t.Error("boot not touched; internal/api/server.go anchors its second step")
	} else if len(steps) != 1 {
		t.Errorf("boot steps = %v, want just the server.go step", steps)
	}
	if _, ok := byID["unanchored"]; ok {
		t.Error("unanchored flow matched, but none of its steps carry a file")
	}

	// Flow matching is by EXACT step file. A sibling in the same directory
	// touches the module but must not claim the step.
	sib := Match(m, []string{"web/src/pages/Other.tsx"})
	if len(sib.Flows) != 0 {
		t.Errorf("flows = %+v for a sibling of a step file, want none (exact match only)", sib.Flows)
	}
	if len(sib.Modules) != 1 || sib.Modules[0].ID != "web-pages" {
		t.Errorf("modules = %+v, want just web-pages", sib.Modules)
	}
}

func TestMatchUnmatched(t *testing.T) {
	m := loadFixture(t)
	got := Match(m, []string{"README.md", "docs/adr/0001.md", "internal/api/routes.go"})

	if !reflect.DeepEqual(got.Unmatched, []string{"README.md", "docs/adr/0001.md"}) {
		t.Errorf("unmatched = %v, want the two files no module claims (sorted)", got.Unmatched)
	}
	if len(got.Modules) != 1 || got.Modules[0].ID != "api" {
		t.Errorf("modules = %+v, want just api", got.Modules)
	}
}

func TestMatchNormalisesAndDedupes(t *testing.T) {
	m := loadFixture(t)
	// "./" prefixes, a blank line and a duplicate all arrive from real callers.
	got := Match(m, []string{"./internal/api/routes.go", "internal/api/routes.go", "", "   "})
	if len(got.Modules) != 1 {
		t.Fatalf("modules = %+v, want one", got.Modules)
	}
	if !reflect.DeepEqual(got.Modules[0].Files, []string{"internal/api/routes.go"}) {
		t.Errorf("files = %v, want one normalised entry", got.Modules[0].Files)
	}
}

// TestMatchEmptyAndNil pins the JSON-shape guarantee: never a null slice.
func TestMatchEmptyAndNil(t *testing.T) {
	m := loadFixture(t)
	empty := Match(m, nil)
	if empty.Modules == nil || empty.Flows == nil || empty.Unmatched == nil {
		t.Errorf("Match(m, nil) returned a nil slice: %+v", empty)
	}

	nilMap := Match(nil, []string{"a/b.go"})
	if nilMap.Modules == nil || nilMap.Flows == nil {
		t.Errorf("Match(nil, …) returned a nil slice: %+v", nilMap)
	}
	if !reflect.DeepEqual(nilMap.Unmatched, []string{"a/b.go"}) {
		t.Errorf("Match(nil, …).Unmatched = %v, want every file unmatched", nilMap.Unmatched)
	}
	if ModuleCount(nil) != 0 {
		t.Error("ModuleCount(nil) != 0")
	}
}
