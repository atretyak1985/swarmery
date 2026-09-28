package claudeacct

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadTrustedSettings is the one loader both a binding and an estate settings
// file go through; its reason codes are what runsettings logs and what
// `swarmery account exec` prints, so every refusal has exactly one code and no
// reason ever carries the file's contents.
func TestReadTrustedSettingsReasons(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.json", `{"pluginConfigs":{"a":{"k":"SENTINEL-VALUE"}}}`, 0o600)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a-dir")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	big := write("big.json", `{"k":"`+strings.Repeat("x", maxSettingsBytes)+`"}`, 0o600)

	for _, tc := range []struct {
		name, path, want string
	}{
		{"absent", filepath.Join(dir, "nope.json"), ""},
		{"symlink", link, TrustSymlink},
		{"directory", sub, TrustNotRegular},
		{"group-writable", write("g.json", `{}`, 0o620), TrustGroupWritable},
		{"other-writable", write("o.json", `{}`, 0o602), TrustOtherWritable},
		{"too large", big, TrustTooLarge},
		{"malformed", write("bad.json", `{"a":`, 0o600), TrustMalformed},
		{"top-level array", write("arr.json", `[1,2]`, 0o600), TrustMalformed},
		{"null", write("null.json", `null`, 0o600), TrustMalformed},
	} {
		root, reason := ReadTrustedSettings(tc.path)
		if reason != tc.want || root != nil {
			t.Errorf("%s: (root!=nil=%v, reason=%q), want (false, %q)", tc.name, root != nil, reason, tc.want)
		}
		if strings.Contains(reason, "SENTINEL") {
			t.Errorf("%s: reason %q carries file contents", tc.name, reason)
		}
	}

	root, reason := ReadTrustedSettings(good)
	if reason != "" || root == nil || root["pluginConfigs"] == nil {
		t.Errorf("good file: root=%v reason=%q, want the parsed object and no reason", root, reason)
	}
	// The binding walk's loader is the same function: a refused file is nil there too.
	if readTrustedSettings(link) != nil || readTrustedSettings(good) == nil {
		t.Error("readTrustedSettings must agree with ReadTrustedSettings")
	}

	old := currentUID
	currentUID = func() int { return old() + 1 }
	t.Cleanup(func() { currentUID = old })
	if _, reason := ReadTrustedSettings(good); reason != TrustNotOwned {
		t.Errorf("another uid's file: reason = %q, want %q", reason, TrustNotOwned)
	}
}

// WithinRoot is Lock 2's admission test, exported: resolved files compared with
// os.SameFile, never a string prefix.
func TestWithinRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ae")
	evil := filepath.Join(base, "ae-evil")
	outside := filepath.Join(base, "account")
	for _, d := range []string{filepath.Join(root, "inner", ".real"), evil, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	inFile := filepath.Join(root, "inner", "settings.json")
	evilFile := filepath.Join(evil, "settings.json")
	outFile := filepath.Join(outside, "settings.json")
	for _, f := range []string{inFile, evilFile, outFile} {
		if err := os.WriteFile(f, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A link inside the root that points out of it, and one that stays inside.
	escape := filepath.Join(root, "escape")
	stay := filepath.Join(root, "stay")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "inner"), stay); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path string
		want       bool
	}{
		{"file inside", inFile, true},
		{"the root itself", root, true},
		{"prefix sibling", evilFile, false},
		{"outside", outFile, false},
		{"link out of the root", filepath.Join(escape, "settings.json"), false},
		{"link that stays inside", filepath.Join(stay, "settings.json"), true},
		{"missing path", filepath.Join(root, "nope.json"), false},
	} {
		if got := WithinRoot(tc.path, root); got != tc.want {
			t.Errorf("%s: WithinRoot = %v, want %v", tc.name, got, tc.want)
		}
	}
	if WithinRoot(inFile, "") {
		t.Error("an empty root admits nothing")
	}
}

// A hard link planted beside a binding or an estate settings file would pass as
// the operator's own file; the loader refuses any inode with more than one link.
func TestReadTrustedSettingsRefusesHardLinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "account-settings.json")
	if err := os.WriteFile(outside, []byte(`{"pluginConfigs":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Link(outside, link); err != nil {
		t.Fatal(err)
	}
	if root, reason := ReadTrustedSettings(link); root != nil || reason != TrustHardLinked {
		t.Errorf("hard link: (root!=nil=%v, %q), want (false, %q)", root != nil, reason, TrustHardLinked)
	}
	if msg := untrustedSettings(link); !strings.Contains(msg, "hard-linked") {
		t.Errorf("untrustedSettings = %q, want it to name the hard link", msg)
	}
}

// ReadTrustedSettingsWithin re-checks AFTER the open that the inode it holds is
// the one linked inside root, so swapping .claude for a symlink between the
// containment check and the open cannot hand it a file from outside.
func TestReadTrustedSettingsWithinClosesTheSwapRace(t *testing.T) {
	setup := func(t *testing.T) (root, claude, outside string) {
		root = t.TempDir()
		claude = filepath.Join(root, ".claude")
		if err := os.MkdirAll(claude, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{"inside":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		outside = t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "settings.json"), []byte(`{"outside":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return root, claude, outside
	}
	swapOut := func(t *testing.T, claude, outside string) {
		if err := os.Rename(claude, claude+".real"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, claude); err != nil {
			t.Fatal(err)
		}
	}
	swapBack := func(t *testing.T, claude string) {
		if err := os.Remove(claude); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(claude+".real", claude); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { testHookBeforeOpen, testHookAfterOpen = nil, nil })

	t.Run("swapped out before the open and left out", func(t *testing.T) {
		root, claude, outside := setup(t)
		testHookBeforeOpen = func() { swapOut(t, claude, outside) }
		testHookAfterOpen = nil
		got, reason := ReadTrustedSettingsWithin(filepath.Join(claude, "settings.json"), root)
		if got != nil || reason != TrustOutsideRoot {
			t.Errorf("got %v, %q; want nil, %q", got, reason, TrustOutsideRoot)
		}
	})
	t.Run("swapped out before the open and back after it", func(t *testing.T) {
		root, claude, outside := setup(t)
		testHookBeforeOpen = func() { swapOut(t, claude, outside) }
		testHookAfterOpen = func() { swapBack(t, claude) }
		got, reason := ReadTrustedSettingsWithin(filepath.Join(claude, "settings.json"), root)
		if got != nil || reason != TrustOutsideRoot {
			t.Errorf("got %v, %q; want nil, %q", got, reason, TrustOutsideRoot)
		}
	})
	t.Run("no swap reads the inside file", func(t *testing.T) {
		root, claude, _ := setup(t)
		testHookBeforeOpen, testHookAfterOpen = nil, nil
		got, reason := ReadTrustedSettingsWithin(filepath.Join(claude, "settings.json"), root)
		if reason != "" || got["inside"] != true {
			t.Errorf("got %v, %q; want the inside file", got, reason)
		}
	})
}
