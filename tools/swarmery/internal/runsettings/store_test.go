package runsettings

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreIsContentAddressed(t *testing.T) {
	dir := isolate(t)
	b := []byte(`{"a":1}` + "\n")
	sum := sha256.Sum256(b)
	want := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	if got := write(b); got != want {
		t.Fatalf("write = %s, want %s", got, want)
	}
	if got := write([]byte(`{"a":2}`)); got == want {
		t.Fatal("different content must land at a different path")
	}
	raw, err := os.ReadFile(want)
	if err != nil || string(raw) != string(b) {
		t.Fatal("stored bytes differ from the input")
	}
}

func TestStoreModes(t *testing.T) {
	dir := isolate(t)
	// A pre-existing, too-open directory is tightened.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := write([]byte(`{}`))
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %04o, want 0700", di.Mode().Perm())
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %04o, want 0600", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("a temp file was left behind: %d entries", len(ents))
	}
}

func TestStoreSkipsRewrite(t *testing.T) {
	isolate(t)
	b := []byte(`{"x":true}`)
	p := write(b)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if again := write(b); again != p {
		t.Fatalf("same content, different path: %s vs %s", again, p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Not rewritten (same bytes by construction), but its mtime is refreshed so
	// a file still in use never looks stale to Prune.
	if !fi.ModTime().After(old.Add(47 * time.Hour)) {
		t.Fatalf("mtime = %s, want it refreshed to now", fi.ModTime())
	}
	raw, err := os.ReadFile(p)
	if err != nil || string(raw) != string(b) {
		t.Fatal("the reused file's content changed")
	}
}

func TestPruneRemovesOnlyStaleFiles(t *testing.T) {
	dir := isolate(t)
	stale := write([]byte(`{"stale":1}`))
	fresh := write([]byte(`{"fresh":1}`))
	other := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-15 * 24 * time.Hour)
	for _, p := range []string{stale, other} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	yesterday := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(fresh, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}
	// A temp file a crashed write left behind goes after an hour; a fresh one —
	// a write in flight — stays.
	oldTmp := filepath.Join(dir, ".compose-111.tmp")
	newTmp := filepath.Join(dir, ".compose-222.tmp")
	for _, p := range []string{oldTmp, newTmp} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	twoHours := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldTmp, twoHours, twoHours); err != nil {
		t.Fatal(err)
	}
	if n := Prune(14 * 24 * time.Hour); n != 2 {
		t.Fatalf("Prune removed %d, want 2 (the stale file and the old temp file)", n)
	}
	for _, p := range []string{stale, oldTmp} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived", filepath.Base(p))
		}
	}
	for _, p := range []string{fresh, other, newTmp} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was removed", filepath.Base(p))
		}
	}
	// A missing directory is zero removals, not an error.
	t.Setenv(runDirEnv, filepath.Join(t.TempDir(), "absent"))
	if n := Prune(time.Hour); n != 0 {
		t.Fatalf("missing dir: removed %d", n)
	}
}

// A file already at the content-addressed name is reused only if it IS what the
// name claims: the same bytes, 0600, owned by this user. Anything else is
// replaced through the temp-file-and-rename path.
func TestStoreRewritesAFileThatIsNotItsOwn(t *testing.T) {
	isolate(t)
	b := []byte(`{"pluginConfigs":{}}` + "\n")
	p := write(b)
	if err := os.WriteFile(p, []byte(`{"planted":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if again := write(b); again != p {
		t.Fatalf("path changed: %s vs %s", again, p)
	}
	if raw, _ := os.ReadFile(p); string(raw) != string(b) {
		t.Fatalf("tampered content was reused: %q", raw)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	write(b)
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("a 0644 file was reused: %v %v", fi.Mode().Perm(), err)
	}
}
