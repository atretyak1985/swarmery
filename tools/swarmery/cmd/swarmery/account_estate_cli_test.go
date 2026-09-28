package main

// Output contracts of the estate half of `swarmery account`: the `estate`
// subcommands, `which`'s rung/estate/shadow lines, `use`'s pin listing and its
// selective --clear-pins, `clear` naming a surviving estate, and `exec`
// resolving a name collision to the estate's value through a REAL execve.
//
// Every test points $HOME at a t.TempDir() and every store at another; values
// are literal non-secrets.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

const estateSettings = `{
  "swarmery": {"claudeAccount": "work"},
  "permissions": {"allow": ["Bash(ls:*)"]},
  "enabledPlugins": {"core@swarmery": true}
}
`

func settingsPath(dir string) string { return filepath.Join(dir, ".claude", "settings.local.json") }

func writeSettings(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(dir), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSettingsJSON(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(settingsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("settings no longer parse: %v", err)
	}
	return m
}

func TestAccountEstateUseIsSurgical(t *testing.T) {
	fakeHome(t, "default", "work")
	t.Setenv(secretsDirVar, t.TempDir()) // no demo.env: a store-less estate
	dir := t.TempDir()
	writeSettings(t, dir, estateSettings)

	var out, errOut bytes.Buffer
	if err := accountEstate([]string{"use", "demo", "--path", dir}, &out, &errOut); err != nil {
		t.Fatalf("estate use: %v", err)
	}
	if !strings.Contains(out.String(), "no credential store on this machine — it supplies 0 credentials") {
		t.Errorf("estate use output %q does not say the store is absent", out.String())
	}
	m := readSettingsJSON(t, dir)
	for _, k := range []string{"swarmery", "permissions", "enabledPlugins"} {
		if _, ok := m[k]; !ok {
			t.Errorf("top-level key %q lost", k)
		}
	}
	ns := m["swarmery"].(map[string]any)
	if ns["claudeAccount"] != "work" || ns["estate"] != "demo" {
		t.Errorf("swarmery namespace = %v, want claudeAccount work + estate demo", ns)
	}
	first, _ := os.ReadFile(settingsPath(dir))
	if bak, _ := os.ReadFile(settingsPath(dir) + ".bak"); string(bak) != estateSettings {
		t.Error(".bak does not hold the pre-write bytes")
	}
	if err := accountEstate([]string{"use", "demo", "--path", dir}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(settingsPath(dir)); !bytes.Equal(again, first) {
		t.Error("a second identical estate use changed the file")
	}

	// Unsafe key: refused, nothing written.
	if err := accountEstate([]string{"use", "../../etc", "--path", dir}, io.Discard, io.Discard); err == nil {
		t.Error("estate use ../../etc accepted")
	}
	if again, _ := os.ReadFile(settingsPath(dir)); !bytes.Equal(again, first) {
		t.Error("a refused key changed the file")
	}

	// Corrupted file: aborted WITHOUT writing.
	broken := t.TempDir()
	writeSettings(t, broken, "{")
	if err := accountEstate([]string{"use", "demo", "--path", broken}, io.Discard, io.Discard); err == nil {
		t.Error("estate use over unparseable JSON returned nil")
	}
	if raw, _ := os.ReadFile(settingsPath(broken)); string(raw) != "{" {
		t.Errorf("unparseable file rewritten: %q", raw)
	}

	// A store that exists is described by a COUNT, never a name.
	seedSecretStore(t, "demo")
	out.Reset()
	if err := accountEstate([]string{"use", "demo", "--path", t.TempDir()}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "credential store present: 1 names") || strings.Contains(out.String(), storeSecretVar) {
		t.Errorf("estate use output = %q, want a count and no variable name", out.String())
	}
}

func TestAccountClearAndEstateClearKeepTheOtherAxis(t *testing.T) {
	fakeHome(t, "default", "work")
	t.Setenv(secretsDirVar, t.TempDir())
	dir := t.TempDir()
	writeSettings(t, dir, estateSettings)
	if err := claudeacct.SetEstate(dir, "demo"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := accountClear([]string{"--path", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if key, _ := claudeacct.Estate(dir); key != "demo" {
		t.Fatalf("account clear removed the estate: %q", key)
	}
	if n := strings.Count(out.String(), "estate"); n < 1 || countLines(out.String(), "estate") != 1 {
		t.Errorf("account clear stdout %q: want exactly one line naming the surviving estate", out.String())
	}

	// estate clear with a binding re-added keeps the binding.
	if err := claudeacct.SetBinding(dir, "work"); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	out.Reset()
	if err := accountEstate([]string{"clear", "--path", dir}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if got := claudeacct.Binding(dir); got != "work" {
		t.Fatalf("estate clear removed the binding: %q", got)
	}
	if !strings.Contains(errOut.String(), "zero estate credentials") {
		t.Errorf("estate clear stderr = %q, want the zero-credentials warning", errOut.String())
	}
	// Nothing of ours left → the namespace is pruned.
	if err := claudeacct.SetBinding(dir, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSettingsJSON(t, dir)["swarmery"]; ok {
		t.Error("the swarmery namespace survived with nothing in it")
	}
	out.Reset()
	if err := accountEstate([]string{"clear", "--path", dir}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "nothing to clear") {
		t.Errorf("clearing an absent estate: err=%v out=%q", err, out.String())
	}
}

func TestAccountEstateShowWalks(t *testing.T) {
	fakeHome(t, "default")
	t.Setenv(secretsDirVar, t.TempDir())
	root := t.TempDir()
	if err := claudeacct.SetEstate(root, "demo"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := accountEstate([]string{"show", "--path", filepath.Join(root, "sub", "dir")}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"estate:      demo", "root:        " + root + " (an ancestor)", "credentials: 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("estate show = %q, missing %q", out.String(), want)
		}
	}
	out.Reset()
	if err := accountEstate([]string{"show", "--path", root}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "(this path itself)") {
		t.Errorf("estate show at the root: err=%v out=%q", err, out.String())
	}
	out.Reset()
	if err := accountEstate([]string{"show", "--path", t.TempDir()}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "none") {
		t.Errorf("estate show with no estate: err=%v out=%q", err, out.String())
	}
	for _, bad := range [][]string{nil, {"bogus"}, {"use"}, {"show", "extra"}, {"clear", "extra"}} {
		if err := accountEstate(bad, io.Discard, io.Discard); err == nil {
			t.Errorf("accountEstate(%v) accepted", bad)
		}
	}
}

// R1: a declaration Resolve would never read back is refused BEFORE anything is
// written — $HOME (never a rung), a daemon worktree (resolved from its source),
// a binding file the walk ignores — each with the reason.
func TestAccountEstateUseRefusesAnUnreadableDeclaration(t *testing.T) {
	home := fakeHome(t, "default")
	t.Setenv(secretsDirVar, t.TempDir())

	var out bytes.Buffer
	err := accountEstate([]string{"use", "acme", "--path", home}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "would never be read") || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("estate use --path $HOME: err = %v, want a refusal saying why", err)
	}
	if _, statErr := os.Lstat(settingsPath(home)); !os.IsNotExist(statErr) {
		t.Fatalf("estate use --path $HOME wrote %s (%v)", settingsPath(home), statErr)
	}

	src := filepath.Join(home, "projects", "acme", "repo")
	wt := filepath.Join(home, ".swarmery", "worktrees", strings.ReplaceAll(src, "/", "-"), "T-1")
	admin := filepath.Join(src, ".git", "worktrees", "T-1")
	for _, d := range []string{wt, admin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(wt, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = accountEstate([]string{"use", "acme", "--path", wt}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "daemon worktree") {
		t.Fatalf("estate use --path <worktree>: err = %v, want the worktree refusal", err)
	}
	if _, statErr := os.Lstat(settingsPath(wt)); !os.IsNotExist(statErr) {
		t.Fatalf("estate use on a worktree wrote %s", settingsPath(wt))
	}

	gw := t.TempDir()
	writeSettings(t, gw, estateSettings)
	if err := os.Chmod(settingsPath(gw), 0o666); err != nil {
		t.Fatal(err)
	}
	err = accountEstate([]string{"use", "acme", "--path", gw}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("estate use over a group-writable file: err = %v, want the mode refusal", err)
	}
	if raw, _ := os.ReadFile(settingsPath(gw)); string(raw) != estateSettings {
		t.Fatalf("a refused estate use changed the file: %q", raw)
	}
	if _, statErr := os.Lstat(settingsPath(gw) + ".bak"); !os.IsNotExist(statErr) {
		t.Fatal("a refused estate use left a .bak")
	}
}

// R2: clearing a NESTED estate names the outer estate the tree now falls back
// to; it does not claim zero credentials.
func TestAccountEstateClearNamesTheFallbackEstate(t *testing.T) {
	fakeHome(t, "default")
	t.Setenv(secretsDirVar, t.TempDir())
	outer := t.TempDir()
	inner := filepath.Join(outer, "sub")
	if err := claudeacct.SetEstate(outer, "outer"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(inner, "inner"); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	if err := accountEstate([]string{"clear", "--path", inner}, io.Discard, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "fall back to estate outer (root "+outer+")") {
		t.Errorf("nested clear stderr = %q, want it to name the outer estate and its root", errOut.String())
	}
	if strings.Contains(errOut.String(), "zero estate credentials") {
		t.Errorf("nested clear claimed zero credentials while an outer estate still applies: %q", errOut.String())
	}
}

func countLines(s, needle string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

// The rung, the estate and the shadow — and nothing on stderr for a store-less
// estate under the default account.
func TestAccountWhichPrintsRungEstateAndShadow(t *testing.T) {
	home := fakeHome(t, "default", "work")
	t.Setenv(secretsDirVar, t.TempDir())
	root := filepath.Join(home, "projects", "acme")
	proj := filepath.Join(root, "deployment", "src", "php")
	if err := claudeacct.SetBinding(root, "work"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetBinding(proj, "default"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := accountWhich([]string{"--path", proj}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"account:    default (pin at " + proj + ")",
		"source:     pin",
		"estate:     acme (root " + root + ")",
		"shadowed:   " + root + " says work",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("which = %q, missing %q", out.String(), want)
		}
	}
	out.Reset()
	if err := accountWhich([]string{"--path", filepath.Join(root, "repos", "one")}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "source:     pin(parent)") || strings.Contains(out.String(), "shadowed:") {
		t.Errorf("inheriting which = %q, want pin(parent) and no shadow", out.String())
	}
}

// --clear-pins is SELECTIVE; =all is the blanket; --keep-pins and the no-flag,
// no-terminal case clear nothing. Every listed pin is named before the write.
func TestAccountUseShadowingPins(t *testing.T) {
	fakeHome(t, "default", "work")
	build := func(t *testing.T) (root string, agree1, agree2, disagree string) {
		root = t.TempDir()
		if err := claudeacct.SetBinding(root, "work"); err != nil {
			t.Fatal(err)
		}
		if err := claudeacct.SetEstate(root, "acme"); err != nil {
			t.Fatal(err)
		}
		agree1, agree2, disagree = filepath.Join(root, "a"), filepath.Join(root, "b", "c"), filepath.Join(root, "d")
		for p, k := range map[string]string{agree1: "work", agree2: "work", disagree: "default"} {
			if err := claudeacct.SetBinding(p, k); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	remaining := func(root string) int { return len(claudeacct.ShadowingPins(root)) }

	t.Run("keep-pins lists all and clears none", func(t *testing.T) {
		root, a, b, d := build(t)
		var errOut bytes.Buffer
		if err := accountUse([]string{"work", "--path", root, "--keep-pins"}, io.Discard, &errOut, nil); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{a, b, d} {
			if !strings.Contains(errOut.String(), p+" holds") {
				t.Errorf("pin %s not listed: %q", p, errOut.String())
			}
		}
		if countLines(errOut.String(), root+string(filepath.Separator)) != 3 || remaining(root) != 3 {
			t.Errorf("keep-pins: %d listed lines, %d remaining; want 3 and 3", countLines(errOut.String(), root+"/"), remaining(root))
		}
	})
	t.Run("no flag and no terminal lists but never clears", func(t *testing.T) {
		root, _, _, _ := build(t)
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer devnull.Close()
		var errOut bytes.Buffer
		if err := accountUse([]string{"default", "--path", root}, io.Discard, &errOut, devnull); err != nil {
			t.Fatal(err)
		}
		if remaining(root) != 3 || claudeacct.Binding(root) != "default" {
			t.Errorf("no-flag use cleared pins (%d left) or did not write (%q)", remaining(root), claudeacct.Binding(root))
		}
		if strings.Contains(errOut.String(), "[y/N]") {
			t.Errorf("/dev/null on stdin was treated as a terminal: %q", errOut.String())
		}
	})
	t.Run("clear-pins is selective", func(t *testing.T) {
		root, _, _, d := build(t)
		var errOut bytes.Buffer
		if err := accountUse([]string{"work", "--path", root, "--clear-pins"}, io.Discard, &errOut, nil); err != nil {
			t.Fatal(err)
		}
		if got := claudeacct.ShadowingPins(root); len(got) != 1 || got[0] != d {
			t.Fatalf("after --clear-pins: %v, want only the disagreeing %s", got, d)
		}
		if n := countLines(errOut.String(), "diverg"); n != 1 {
			t.Errorf("divergence named on %d lines, want 1: %q", n, errOut.String())
		}
	})
	t.Run("clear-pins=all is the blanket", func(t *testing.T) {
		root, _, _, _ := build(t)
		if err := accountUse([]string{"work", "--path", root, "--clear-pins=all"}, io.Discard, io.Discard, nil); err != nil {
			t.Fatal(err)
		}
		if remaining(root) != 0 {
			t.Fatalf("after --clear-pins=all: %d pins left, want 0", remaining(root))
		}
	})
	t.Run("contradictory flags refused", func(t *testing.T) {
		root, _, _, _ := build(t)
		if err := accountUse([]string{"work", "--path", root, "--clear-pins", "--keep-pins"}, io.Discard, io.Discard, nil); err == nil {
			t.Fatal("--clear-pins with --keep-pins accepted")
		}
	})
	t.Run("a non-estate root lists nothing", func(t *testing.T) {
		root := t.TempDir()
		if err := claudeacct.SetBinding(filepath.Join(root, "x"), "work"); err != nil {
			t.Fatal(err)
		}
		var errOut bytes.Buffer
		if err := accountUse([]string{"work", "--path", root, "--clear-pins"}, io.Discard, &errOut, nil); err != nil {
			t.Fatal(err)
		}
		if errOut.Len() != 0 || remaining(root) != 1 {
			t.Errorf("non-estate root: stderr %q, %d pins left", errOut.String(), remaining(root))
		}
	})
}

// The =value forms: only "all" (and the bare form) are accepted, and the
// refusal names the = form, since the space form cannot parse.
func TestClearPinsFlagForms(t *testing.T) {
	var f clearPinsFlag
	if !f.IsBoolFlag() || f.String() != "" {
		t.Fatal("clearPinsFlag must be bool-shaped and empty by default")
	}
	if err := f.Set("true"); err != nil || f.String() != "true" {
		t.Fatalf("bare form: %v %q", err, f.String())
	}
	if err := f.Set("all"); err != nil || f.String() != "all" {
		t.Fatalf("=all form: %v %q", err, f.String())
	}
	err := f.Set("yes")
	if err == nil || strings.Count(err.Error(), "clear-pins=all") != 1 {
		t.Fatalf("=yes: err %v, want a refusal naming --clear-pins=all once", err)
	}
	var nilFlag *clearPinsFlag
	if nilFlag.String() != "" {
		t.Fatal("nil flag String")
	}
}

// `account --help` documents the estate verbs and no longer claims the binding
// is never looked up in parent directories.
func TestAccountUsageDocumentsEstates(t *testing.T) {
	for _, want := range []string{"estate use", "estate show", "estate clear", "An estate with no credential store"} {
		if strings.Count(accountUsage, want) != 1 {
			t.Errorf("usage mentions %q %d times, want 1", want, strings.Count(accountUsage, want))
		}
	}
	if strings.Contains(accountUsage, "never searched for in parent directories") {
		t.Error("usage still says the binding is never searched for in parent directories")
	}
}

// THE collision through a real execve: an account store and an estate store
// both set one name; the child's getenv (first match) must see the ESTATE's
// value, because the delta carries exactly one entry per name.
func TestAccountExecEstateWinsACollision(t *testing.T) {
	home := fakeHome(t, "default", "work")
	secrets := t.TempDir()
	if err := os.Chmod(secrets, 0o700); err != nil { // the loader refuses a store dir open beyond its owner
		t.Fatal(err)
	}
	for name, value := range map[string]string{"work": "from-account", "acme": "from-estate"} {
		p := filepath.Join(secrets, name+".env")
		if err := os.WriteFile(p, []byte(storeSecretVar+"="+value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(home, "projects", "acme")
	if err := claudeacct.SetBinding(root, "work"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(root, "repo")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	printenv, err := exec.LookPath("printenv")
	if err != nil {
		t.Skipf("printenv not on this machine: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAccountExecSecretHelper")
	cmd.Env = []string{
		execSecretHelperProject + "=" + proj,
		"HOME=" + home,
		secretsDirVar + "=" + secrets,
		storeSecretVar + "=inherited-from-the-shell",
		"PATH=" + filepath.Dir(printenv),
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v (stdout %q)", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "from-estate" {
		t.Fatalf("child's getenv saw %q, want the estate's value", got)
	}
}
