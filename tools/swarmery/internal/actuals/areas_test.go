package actuals

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSizeBandBoundaries(t *testing.T) {
	for lines, want := range map[int]string{
		0: "XS", 19: "XS", 20: "S", 99: "S", 100: "M", 399: "M",
		400: "L", 1499: "L", 1500: "XL", 100000: "XL",
	} {
		if got := SizeBand(lines); got != want {
			t.Errorf("SizeBand(%d) = %s, want %s", lines, got, want)
		}
	}
}

func TestAreas(t *testing.T) {
	paths := []string{
		"README.md",
		"./apps/web/src/page.tsx",
		"apps/web/src/other.tsx",
		"apps/api/main.go",
		"internal/store/store.go",
		"tools/x/internal/y/z.go",
		"Makefile",
	}
	if got, want := Areas(paths, 2), []string{".", "apps/api", "apps/web", "internal/store", "tools/x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("depth 2 = %q, want %q", got, want)
	}
	if got, want := Areas(paths, 1), []string{".", "apps", "internal", "tools"}; !reflect.DeepEqual(got, want) {
		t.Errorf("depth 1 = %q, want %q", got, want)
	}
	// A depth below 1 is the default, never "everything is the root".
	if got := Areas(paths, 0); !reflect.DeepEqual(got, Areas(paths, DefaultAreaDepth)) {
		t.Errorf("depth 0 = %q, want the default depth's areas", got)
	}
	if got := Areas(nil, 2); len(got) != 0 {
		t.Errorf("no paths = %q, want none", got)
	}
}

func TestAreaDepth(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	valid := write("valid.json", `{"learning":{"areaDepth":4}}`)
	outOfRange := write("range.json", `{"learning":{"areaDepth":99}}`)
	broken := write("broken.json", `{not json`)
	silent := write("silent.json", `{"name":"p"}`)
	missing := filepath.Join(dir, "missing.json")

	if got := AreaDepth(); got != DefaultAreaDepth {
		t.Errorf("no sources = %d, want default", got)
	}
	if got := AreaDepth("", missing, broken, silent, outOfRange, valid); got != 4 {
		t.Errorf("first valid declaration = %d, want 4 (every bad source is skipped)", got)
	}
	if got := AreaDepth(missing, broken, silent, outOfRange); got != DefaultAreaDepth {
		t.Errorf("no valid declaration = %d, want default", got)
	}
}
