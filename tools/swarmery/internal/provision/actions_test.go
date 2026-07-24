package provision

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGitHead lays down a minimal loose-ref .git resolving HEAD to sha,
// mirroring the fixture idiom in internal/githead/githead_test.go.
func writeGitHead(t *testing.T, dir, sha string) {
	t.Helper()
	must := func(p, c string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	must(filepath.Join(dir, ".git", "refs", "heads", "main"), sha+"\n")
}

func TestArchitectureFresh(t *testing.T) {
	sha := "1111111111111111111111111111111111111111"
	dir := t.TempDir()
	writeGitHead(t, dir, sha)

	if architectureFresh(dir) {
		t.Fatal("no map yet → must be stale")
	}
	mapDir := filepath.Join(dir, "architecture-out")
	if err := os.MkdirAll(mapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMap := func(commit string) {
		if err := os.WriteFile(filepath.Join(mapDir, "architecture-map.json"),
			[]byte(`{"analyzedAtCommit":"`+commit+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeMap("2222222222222222222222222222222222222222")
	if architectureFresh(dir) {
		t.Fatal("commit mismatch → stale")
	}
	writeMap(sha)
	if !architectureFresh(dir) {
		t.Fatal("commit match → fresh")
	}
}
