package accountdoctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// loggedInAccount provisions ~/.claude-<key> with a completed login, which the
// release table requires before a rootless account store is released from a
// rung.
func (f fixture) loggedInAccount(t *testing.T, key string) string {
	t.Helper()
	dir := filepath.Join(f.home, ".claude-"+key)
	mustMkdir(t, filepath.Join(dir, "projects"), 0o755)
	mustWrite(t, filepath.Join(dir, ".claude.json"), `{"oauthAccount":{"emailAddress":"x@example.invalid"}}`, 0o600)
	return dir
}

func TestTrustStoreRootless(t *testing.T) {
	f := newFixture(t)
	f.loggedInAccount(t, "work")
	proj := t.TempDir()
	mustWrite(t, filepath.Join(proj, ".claude", "settings.local.json"), `{"swarmery":{"claudeAccount":"work"}}`, 0o644)
	store := f.store(t, "work", "PACK_W=zzq-rootless\n")

	rep, _ := Fast(Options{Path: proj})
	if n := countFindings(rep, "store-rootless", SevWarn); n != 1 {
		t.Fatalf("store-rootless = %d: %+v", n, rep.Findings)
	}
	for _, fd := range rep.Findings {
		if fd.ID == "store-rootless" && (fd.File != store || !strings.Contains(fd.Detail, "# swarmery-root: ")) {
			t.Errorf("finding = %+v, want the store path and the root line to add", fd)
		}
	}
	// Anchored: no finding.
	f.store(t, "work", "# swarmery-root: "+proj+"\nPACK_W=x\n")
	rep, _ = Fast(Options{Path: proj})
	if n := countFindings(rep, "store-rootless", ""); n != 0 {
		t.Errorf("an anchored store still reported rootless: %+v", rep.Findings)
	}
}

// Every D9 reason the composer refuses an estate settings file for is an
// estate-settings-unusable ERROR naming the path and the reason.
func TestTrustEstateSettingsUnusable(t *testing.T) {
	cases := map[string]func(t *testing.T, root, file string){
		"malformed":     func(t *testing.T, root, file string) { mustWrite(t, file, `{not json`, 0o644) },
		"not an object": func(t *testing.T, root, file string) { mustWrite(t, file, `["a"]`, 0o644) },
		"wrong type":    func(t *testing.T, root, file string) { mustWrite(t, file, `{"pluginConfigs":["a@m"]}`, 0o644) },
		"too large": func(t *testing.T, root, file string) {
			mustWrite(t, file, `{"a":"`+strings.Repeat("x", 1<<20)+`"}`, 0o644)
		},
		"not regular":    func(t *testing.T, root, file string) { mustMkdir(t, file, 0o755) },
		"group-writable": func(t *testing.T, root, file string) { mustWrite(t, file, `{}`, 0o664) },
		"outside root": func(t *testing.T, root, file string) {
			outside := filepath.Join(t.TempDir(), "s.json")
			mustWrite(t, outside, `{}`, 0o644)
			mustMkdir(t, filepath.Dir(file), 0o755)
			if err := os.Symlink(outside, file); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			root := t.TempDir()
			f.anchoredEstate(t, root, "estate", "")
			file := filepath.Join(root, ".claude", "settings.json")
			plant(t, root, file)
			rep, _ := Fast(Options{Path: root})
			if n := countFindings(rep, "estate-settings-unusable", SevError); n != 1 {
				t.Fatalf("estate-settings-unusable = %d: %+v", n, rep.Findings)
			}
		})
	}
	// A usable file: nothing.
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estate", "")
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{"pluginConfigs":{}}`, 0o644)
	if rep, _ := Fast(Options{Path: root}); countFindings(rep, "estate-settings-unusable", "") != 0 {
		t.Errorf("a usable file was reported: %+v", rep.Findings)
	}
}

// binding-tracked and estate-settings-tracked are computed by Full only —
// present there on a git fixture, absent from Fast (the turn-zero budget).
func TestTrustFullOnlyFindings(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estate", "")
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{"pluginConfigs":{}}`, 0o644)
	tracked := filepath.Join(root, "sub")
	mustWrite(t, filepath.Join(tracked, ".claude", "settings.local.json"), `{"swarmery":{"claudeAccount":"default"}}`, 0o644)
	git(t, root, "init", "-q", ".")
	git(t, root, "add", "-f", ".claude/settings.json", "sub/.claude/settings.local.json")
	git(t, root, "commit", "-q", "-m", "fixture")

	fast, _ := Fast(Options{Path: root})
	for _, id := range []string{"binding-tracked", "estate-settings-tracked"} {
		if n := countFindings(fast, id, ""); n != 0 {
			t.Errorf("Fast computed %s: %+v", id, fast.Findings)
		}
	}
	if countFindings(fast, "binding-ignored", SevInfo) != 1 {
		t.Errorf("Fast should list the ignored binding with its reason: %+v", fast.Findings)
	}
	if countFindings(fast, "shadowed-pin", "") != 0 {
		t.Error("an ignored binding was listed as a shadowing pin")
	}
	full, err := Full(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if countFindings(full, "binding-tracked", SevWarn) != 1 || countFindings(full, "estate-settings-tracked", SevWarn) != 1 {
		t.Errorf("Full findings = %+v", full.Findings)
	}
	if _, err := Full(Options{}); err == nil {
		t.Error("Full(Options{}) = nil error")
	}
}

// git runs a fixture-building git command with the operator's global config
// neutralised (their excludesfile must not decide what is tracked).
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A descendant pin naming another account than the estate's is a shadowed-pin.
func TestResolutionShadowedPin(t *testing.T) {
	f := newFixture(t)
	f.loggedInAccount(t, "work")
	root := t.TempDir()
	f.anchoredEstate(t, root, "estate", "")
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"),
		`{"swarmery":{"estate":"estate","claudeAccount":"work"}}`, 0o644)
	sub := filepath.Join(root, "sub")
	mustWrite(t, filepath.Join(sub, ".claude", "settings.local.json"), `{"swarmery":{"claudeAccount":"default"}}`, 0o644)
	same := filepath.Join(root, "same")
	mustWrite(t, filepath.Join(same, ".claude", "settings.local.json"), `{"swarmery":{"claudeAccount":"work"}}`, 0o644)

	rep, _ := Fast(Options{Path: sub})
	if countFindings(rep, "shadowed-pin", SevInfo) != 1 {
		t.Errorf("shadowed-pin = %+v", rep.Findings)
	}
	if rep.Source != "pin" || rep.Account != "default" || rep.DefaultProfile == nil {
		t.Errorf("resolution = %s/%s, profile %v", rep.Account, rep.Source, rep.DefaultProfile)
	}
	if !strings.HasPrefix(rep.Admission, "admission:  estate.env admitted by root ") {
		t.Errorf("admission = %q", rep.Admission)
	}
	work, _ := Fast(Options{Path: same})
	if work.DefaultProfile != nil {
		t.Error("a non-default account carries defaultProfile")
	}
}
