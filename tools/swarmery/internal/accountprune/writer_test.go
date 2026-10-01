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

func anyPath(string, string) bool { return true }

// pj is a job for path whose verified bytes are the file's current bytes.
func pj(t *testing.T, path string, keys []string, bak string) job {
	t.Helper()
	raw, _ := os.ReadFile(path)
	return job{t: Target{Path: path, Keys: keys}, raw: raw, backup: bak}
}

// Never written through a symlink; a non-regular or missing path is an error;
// a failed re-check is a skip, never a write.
func TestPruneFileRefusals(t *testing.T) {
	dir := t.TempDir()
	bak := filepath.Join(dir, "b.bak.json")
	real := write(t, filepath.Join(dir, "real.json"), `{"k":1}`)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pruneFile(pj(t, link, []string{"k"}, bak), anyPath); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink: %v", err)
	}
	if _, _, err := pruneFile(pj(t, filepath.Join(dir, "missing.json"), []string{"k"}, bak), anyPath); err == nil {
		t.Error("missing file: want an error")
	}
	if _, _, err := pruneFile(job{t: Target{Path: dir, Keys: []string{"k"}}}, anyPath); err == nil {
		t.Error("directory: want an error")
	}
	big := write(t, filepath.Join(dir, "big.json"), `{"k":"`+strings.Repeat("x", maxFileBytes)+`"}`)
	if _, _, err := pruneFile(job{t: Target{Path: big, Keys: []string{"k"}}}, anyPath); err == nil {
		t.Error("oversize: want an error")
	}
	bad := write(t, filepath.Join(dir, "bad.json"), `[`)
	if _, _, err := pruneFile(pj(t, bad, []string{"k"}, bak), anyPath); err == nil {
		t.Error("malformed: want an error")
	}
	// Nothing to remove: no write, no backup.
	changed, skip, err := pruneFile(pj(t, real, []string{"zz"}, bak), anyPath)
	if err != nil || changed || skip != "" {
		t.Errorf("no-op = %v %q %v", changed, skip, err)
	}
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Error("a no-op wrote a pre-image")
	}
	// The write-time re-checks: a linked path, and bytes that moved.
	if _, skip, _ := pruneFile(pj(t, real, []string{"k"}, bak), func(string, string) bool { return false }); skip != ReasonSymlinked {
		t.Errorf("unlinked=false skip = %q", skip)
	}
	moved := job{t: Target{Path: real, Keys: []string{"k"}}, raw: []byte(`{"k":0}`), backup: bak}
	if _, skip, _ := pruneFile(moved, anyPath); skip != ReasonChangedSincePlan {
		t.Errorf("moved bytes skip = %q", skip)
	}
	if read(t, real) != `{"k":1}` {
		t.Error("a skipped job wrote")
	}
}

// A pre-image that cannot be written aborts before the file is touched.
func TestBackupUnwritable(t *testing.T) {
	dir := t.TempDir()
	p := write(t, filepath.Join(dir, "s.json"), `{"k":1}`)
	if _, _, err := pruneFile(pj(t, p, []string{"k"}, filepath.Join(dir, "no", "such", "dir", "b.json")), anyPath); err == nil {
		t.Error("want an error")
	}
	if read(t, p) != `{"k":1}` {
		t.Error("file written without a pre-image")
	}
}

// A quarantine that cannot be created aborts Apply before any write.
func TestQuarantineUnwritable(t *testing.T) {
	f := newEstate(t)
	p := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), subsetPC)
	blocker := write(t, filepath.Join(filepath.Dir(f.root), "qfile"), "a file where the quarantine dir belongs")
	if _, err := Apply(mustPlan(t, f.root), Options{QuarantineDir: blocker}); err == nil {
		t.Error("want an error")
	}
	if read(t, p) != subsetPC {
		t.Error("file written without a pre-image")
	}
}

func TestBackupName(t *testing.T) {
	got := backupName("/Users/me/p/.claude/settings.json")
	if !strings.HasPrefix(got, "-Users-me-p-.claude-settings.json.") || !strings.HasSuffix(got, ".bak.json") ||
		len(got) != len("-Users-me-p-.claude-settings.json.")+12+len(".bak.json") {
		t.Errorf("backupName = %q", got)
	}
}
