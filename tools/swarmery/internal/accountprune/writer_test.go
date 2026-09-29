package accountprune

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The splice removes whole members and leaves every other byte in place:
// first, middle, last and only member, duplicates, and an absent key.
func TestRemoveTopLevelKeys(t *testing.T) {
	const doc = "{\n  \"a\": 1,\n  \"b\": {\n    \"x\": [1, 2]\n  },\n  \"c\": \"s\"\n}\n"
	cases := []struct {
		name string
		in   string
		keys []string
		want string
	}{
		{"first", doc, []string{"a"}, "{\n  \"b\": {\n    \"x\": [1, 2]\n  },\n  \"c\": \"s\"\n}\n"},
		{"middle", doc, []string{"b"}, "{\n  \"a\": 1,\n  \"c\": \"s\"\n}\n"},
		{"last", doc, []string{"c"}, "{\n  \"a\": 1,\n  \"b\": {\n    \"x\": [1, 2]\n  }\n}\n"},
		{"two", doc, []string{"a", "c"}, "{\n  \"b\": {\n    \"x\": [1, 2]\n  }\n}\n"},
		{"all", doc, []string{"a", "b", "c"}, "{}\n"},
		{"only", `{"k": {"n": 1}}`, []string{"k"}, "{}"},
		{"absent", doc, []string{"zz"}, doc},
		{"duplicate", `{"k":1,"j":2,"k":3}`, []string{"k"}, `{"j":2}`},
		{"escaped key", `{"q\"x": 1, "k": 2}`, []string{"k"}, `{"q\"x": 1}`},
	}
	for _, c := range cases {
		got, err := removeTopLevelKeys([]byte(c.in), c.keys)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if err := verifyRemoval([]byte(c.in), got, c.keys); err != nil {
			t.Errorf("%s: self-check: %v", c.name, err)
		}
	}
	for _, bad := range []string{`[1]`, `{"a":1} {"b":2}`, `{"a":`, ``} {
		if _, err := removeTopLevelKeys([]byte(bad), []string{"a"}); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// The self-check catches a rewrite that lost or changed anything else.
func TestVerifyRemoval(t *testing.T) {
	before := []byte(`{"a":1,"b":2}`)
	if err := verifyRemoval(before, []byte(`{"b":3}`), []string{"a"}); err == nil {
		t.Error("a changed value passed the self-check")
	}
	if err := verifyRemoval(before, []byte(`{`), []string{"a"}); err == nil {
		t.Error("an unparseable rewrite passed")
	}
	if err := verifyRemoval([]byte(`x`), []byte(`{}`), nil); err == nil {
		t.Error("an unparseable original passed")
	}
}

// Never written through a symlink; a non-regular or missing path is an error.
func TestPruneFileRefusals(t *testing.T) {
	dir := t.TempDir()
	real := write(t, filepath.Join(dir, "real.json"), `{"k":1}`)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pruneFile(link, []string{"k"}, filepath.Join(dir, "q")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink: %v", err)
	}
	if _, _, err := pruneFile(filepath.Join(dir, "missing.json"), []string{"k"}, filepath.Join(dir, "q")); err == nil {
		t.Error("missing file: want an error")
	}
	if _, _, err := pruneFile(dir, []string{"k"}, filepath.Join(dir, "q")); err == nil {
		t.Error("directory: want an error")
	}
	big := write(t, filepath.Join(dir, "big.json"), `{"k":"`+strings.Repeat("x", maxFileBytes)+`"}`)
	if _, _, err := pruneFile(big, []string{"k"}, filepath.Join(dir, "q")); err == nil {
		t.Error("oversize: want an error")
	}
	bad := write(t, filepath.Join(dir, "bad.json"), `[`)
	if _, _, err := pruneFile(bad, []string{"k"}, filepath.Join(dir, "q")); err == nil {
		t.Error("malformed: want an error")
	}
	// Nothing to remove: no write, no backup, no quarantine.
	changed, bak, err := pruneFile(real, []string{"zz"}, filepath.Join(dir, "q"))
	if err != nil || changed || bak != "" {
		t.Errorf("no-op = %v %q %v", changed, bak, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "q")); !os.IsNotExist(err) {
		t.Error("a no-op created the quarantine")
	}
}

// A quarantine that cannot be created aborts before the file is touched.
func TestQuarantineUnwritable(t *testing.T) {
	dir := t.TempDir()
	p := write(t, filepath.Join(dir, "s.json"), `{"k":1}`)
	blocker := write(t, filepath.Join(dir, "q"), "a file where the quarantine dir belongs")
	if _, _, err := pruneFile(p, []string{"k"}, blocker); err == nil {
		t.Error("want an error")
	}
	if read(t, p) != `{"k":1}` {
		t.Error("file written without a pre-image")
	}
}

func TestBackupName(t *testing.T) {
	if got := backupName("/Users/me/p/.claude/settings.json"); got != "-Users-me-p-.claude-settings.json.bak.json" {
		t.Errorf("backupName = %q", got)
	}
}
