package accountprune

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct/accttest"
)

// ── fixture ──────────────────────────────────────────────────────────────────

// estateFixture is an ADMITTED estate "est" at root, with its own settings
// file carrying both EstateKeys, and a fresh HOME / quarantine elsewhere.
type estateFixture struct {
	root, home, q string
	estateFile    string
	bindingFile   string
}

const estateJSON = `{
  "enabledPlugins": {
    "a@m": true
  },
  "extraKnownMarketplaces": {
    "mk": {
      "source": {
        "source": "github",
        "repo": "o/r"
      }
    }
  },
  "permissions": {
    "allow": [
      "Bash(ls)"
    ]
  },
  "pluginConfigs": {
    "a@m": {
      "options": {
        "k": "v1"
      }
    },
    "b@m": {
      "options": {}
    }
  }
}
`

func newEstate(t *testing.T) estateFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := estateFixture{
		root: filepath.Join(base, "estate"),
		home: filepath.Join(base, "home"),
		q:    filepath.Join(base, "home", ".swarmery", "quarantine", "2026-01-02"),
	}
	for _, d := range []string{f.root, f.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", f.home)
	t.Setenv("SWARMERY_SECRETS_DIR", filepath.Join(base, "secrets"))
	f.estateFile = write(t, filepath.Join(f.root, ".claude", "settings.json"), estateJSON)
	f.bindingFile = write(t, filepath.Join(f.root, ".claude", "settings.local.json"),
		`{"swarmery":{"claudeAccount":"default","estate":"est"},"pluginConfigs":{"a@m":{"options":{"k":"v1"}}}}`+"\n")
	accttest.AdmitEstate(t, "est", f.root)
	return f
}

func write(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// git runs one fixture-building git command with the operator's global and
// system config neutralised (their excludes file would otherwise decide what a
// fixture tracks) and every inherited GIT_* variable dropped.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false", "-C", dir}, args...)
	cmd := exec.Command("git", full...)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// trackedRepo makes <root>/<name> a repository whose .claude/settings.json
// (body) is committed.
func trackedRepo(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	p := write(t, filepath.Join(dir, ".claude", "settings.json"), body)
	git(t, dir, "init", "-q", ".")
	git(t, dir, "add", "-f", ".claude/settings.json")
	git(t, dir, "commit", "-q", "-m", "fixture")
	return p
}

func byPath(ts []Target) map[string]Target {
	m := map[string]Target{}
	for _, t := range ts {
		m[t.Path] = t
	}
	return m
}

func mustPlan(t *testing.T, root string) []Target {
	t.Helper()
	ts, err := Plan([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func topKey(t *testing.T, path, key string) (any, bool) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	v, ok := m[key]
	return v, ok
}

// ── scope and the subset rule ────────────────────────────────────────────────

// A strict subset of the estate's entries is pruned; a key with one entry the
// estate lacks, or holds differently, is kept whole and the entry is NAMED.
func TestSubsetRule(t *testing.T) {
	f := newEstate(t)
	sub := write(t, filepath.Join(f.root, "sub", ".claude", "settings.local.json"), `{
  "pluginConfigs": {"a@m": {"options": {"k": "v1"}}},
  "extraKnownMarketplaces": {"mk": {"source": {"source": "github", "repo": "o/r"}}, "extra": {"source": {"source": "directory", "path": "/x"}}}
}
`)
	diff := write(t, filepath.Join(f.root, "diff", ".claude", "settings.json"),
		`{"pluginConfigs": {"a@m": {"options": {"k": "OTHER"}}}}`)
	got := byPath(mustPlan(t, f.root))

	s := got[sub]
	if !s.Eligible || !reflect.DeepEqual(s.Keys, []string{"pluginConfigs"}) || s.Reason != ReasonRedundant {
		t.Errorf("subset file = %+v, want eligible with pluginConfigs only", s)
	}
	if len(s.Kept) != 1 || s.Kept[0].Key != "extraKnownMarketplaces" || !reflect.DeepEqual(s.Kept[0].NotInEstate, []string{"extra"}) {
		t.Errorf("kept = %+v, want extraKnownMarketplaces naming extra", s.Kept)
	}
	if s.Status != StatusNoRepo || s.EstateRoot != f.root {
		t.Errorf("status/estate = %s %s", s.Status, s.EstateRoot)
	}
	d := got[diff]
	if d.Eligible || d.Reason != ReasonNothingRedundant || len(d.Keys) != 0 ||
		len(d.Kept) != 1 || !reflect.DeepEqual(d.Kept[0].NotInEstate, []string{"a@m"}) {
		t.Errorf("differing file = %+v, want nothing redundant naming a@m", d)
	}
}

// A file with no EstateKey is not listed at all.
func TestNoEstateKeyNotListed(t *testing.T) {
	f := newEstate(t)
	perms := write(t, filepath.Join(f.root, "p", ".claude", "settings.json"),
		`{"permissions":{"allow":["Bash(ls)"]},"enabledPlugins":{"a@m":true}}`)
	if _, ok := byPath(mustPlan(t, f.root))[perms]; ok {
		t.Errorf("%s carries no EstateKey and must not be listed", perms)
	}
}

// Outside any repository a file is eligible (NO-REPO is not tracked).
func TestNoRepoEligible(t *testing.T) {
	f := newEstate(t)
	p := write(t, filepath.Join(f.root, "n", ".claude", "settings.json"), `{"pluginConfigs":{"b@m":{"options":{}}}}`)
	got := byPath(mustPlan(t, f.root))[p]
	if !got.Eligible || got.Status != StatusNoRepo {
		t.Fatalf("got %+v", got)
	}
	if _, err := Apply([]Target{got}, Options{QuarantineDir: f.q}); err != nil {
		t.Fatal(err)
	}
	if _, ok := topKey(t, p, "pluginConfigs"); ok {
		t.Error("pluginConfigs survived the apply")
	}
}

// ── the estate's own files ───────────────────────────────────────────────────

// Both of the estate's files are listed "estate source", never eligible, and
// never written — even though, compared with themselves, everything in them is
// redundant. Matched by resolved path: a symlinked spelling is excluded too.
func TestEstateSourceExcluded(t *testing.T) {
	f := newEstate(t)
	before := map[string]string{f.estateFile: read(t, f.estateFile), f.bindingFile: read(t, f.bindingFile)}
	targets := mustPlan(t, f.root)
	got := byPath(targets)
	for p := range before {
		tg, ok := got[p]
		if !ok || tg.Eligible || tg.Reason != ReasonEstateSource || tg.Status != StatusNoRepo || len(tg.Keys) != 0 {
			t.Errorf("%s = %+v (listed %v), want estate source, not eligible, NO-REPO", p, tg, ok)
		}
	}
	if _, err := Apply(targets, Options{QuarantineDir: f.q}); err != nil {
		t.Fatal(err)
	}
	for p, b := range before {
		if read(t, p) != b {
			t.Errorf("%s was written", p)
		}
	}

	// A scan root that reaches the estate's .claude through a symlinked
	// directory still recognises it.
	link := filepath.Join(filepath.Dir(f.root), "link")
	if err := os.Symlink(f.root, link); err != nil {
		t.Fatal(err)
	}
	for _, tg := range mustPlan(t, link) {
		if filepath.Dir(filepath.Dir(tg.Path)) == link && tg.Reason != ReasonEstateSource {
			t.Errorf("via symlink %s = %+v, want estate source", tg.Path, tg)
		}
	}
}

// The swarmery object is never a candidate, even with EstateKeys widened to
// include it and an identical copy in the estate.
func TestBindingObjectNeverPruned(t *testing.T) {
	neverPrunedAcrossWiderKeys(t, "swarmery", `{"claudeAccount":"default","estate":"est"}`)
}

func TestPermissionsNeverPruned(t *testing.T) {
	neverPrunedAcrossWiderKeys(t, "permissions", `{"allow":["Bash(ls)"]}`)
}

func TestMcpjsonServersNeverPruned(t *testing.T) {
	neverPrunedAcrossWiderKeys(t, "enabledMcpjsonServers", `{"playwright-test":true}`)
}

// enabledPlugins is never a target, and every listed file's enabledPlugins is
// byte-identical (canonically) before and after an apply.
func TestEnabledPluginsNeverPruned(t *testing.T) {
	neverPrunedAcrossWiderKeys(t, "enabledPlugins", `{"a@m":true}`)

	f := newEstate(t)
	files := []string{
		write(t, filepath.Join(f.root, "x", ".claude", "settings.json"),
			`{"enabledPlugins":{"a@m":true,"z@m":false},"pluginConfigs":{"a@m":{"options":{"k":"v1"}}}}`),
		write(t, filepath.Join(f.root, "y", ".claude", "settings.local.json"),
			`{"pluginConfigs":{"b@m":{"options":{}}},"enabledPlugins":{"b@m":true}}`),
	}
	before := map[string]string{}
	for _, p := range files {
		v, _ := topKey(t, p, "enabledPlugins")
		before[p] = canon(t, v)
	}
	targets := mustPlan(t, f.root)
	res, err := Apply(targets, Options{QuarantineDir: f.q})
	if err != nil || len(res.Changed) != 2 {
		t.Fatalf("apply = %+v, %v; want two changes", res, err)
	}
	for _, p := range files {
		v, ok := topKey(t, p, "enabledPlugins")
		if !ok || canon(t, v) != before[p] {
			t.Errorf("%s enabledPlugins changed: %s -> %s", p, before[p], canon(t, v))
		}
	}
}

// neverPrunedAcrossWiderKeys widens the inspected key list to include key, puts
// an identical copy of it in the estate and in a sub-repo file, and asserts the
// prune neither lists it nor removes it.
func neverPrunedAcrossWiderKeys(t *testing.T, key, value string) {
	t.Helper()
	f := newEstate(t)
	orig := estateKeys
	estateKeys = func() []string { return append(orig(), key) }
	t.Cleanup(func() { estateKeys = orig })

	var est map[string]any
	if err := json.Unmarshal([]byte(read(t, f.estateFile)), &est); err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal([]byte(value), &v); err != nil {
		t.Fatal(err)
	}
	est[key] = v
	b, _ := json.MarshalIndent(est, "", "  ")
	write(t, f.estateFile, string(b)+"\n")

	p := write(t, filepath.Join(f.root, "s", ".claude", "settings.local.json"),
		`{"`+key+`":`+value+`,"pluginConfigs":{"a@m":{"options":{"k":"v1"}}}}`)
	targets := mustPlan(t, f.root)
	got := byPath(targets)[p]
	for _, k := range got.Keys {
		if k == key {
			t.Fatalf("%s is a target: %+v", key, got)
		}
	}
	for _, kk := range got.Kept {
		if kk.Key == key {
			t.Fatalf("%s is even a candidate: %+v", key, got)
		}
	}
	if _, err := Apply(targets, Options{QuarantineDir: f.q}); err != nil {
		t.Fatal(err)
	}
	after, ok := topKey(t, p, key)
	if !ok || canon(t, after) != canon(t, v) {
		t.Errorf("%s was pruned or changed", key)
	}
}

// ── tracked files ────────────────────────────────────────────────────────────

// An ELIGIBLE tracked file refuses the whole run and nothing is written — not
// even the untracked eligible file beside it; IncludeTracked writes both.
func TestTrackedRefusal(t *testing.T) {
	f := newEstate(t)
	tracked := trackedRepo(t, f.root, "repo", `{"pluginConfigs":{"a@m":{"options":{"k":"v1"}}}}`+"\n")
	other := write(t, filepath.Join(f.root, "other", ".claude", "settings.json"), `{"pluginConfigs":{"b@m":{"options":{}}}}`)
	before := map[string]string{tracked: read(t, tracked), other: read(t, other)}

	targets := mustPlan(t, f.root)
	tg := byPath(targets)[tracked]
	if !tg.Eligible || tg.Status != StatusTracked {
		t.Fatalf("tracked target = %+v", tg)
	}
	_, err := Apply(targets, Options{QuarantineDir: f.q})
	if !IsTrackedRefusal(err) || !strings.Contains(err.Error(), tracked) {
		t.Fatalf("err = %v, want a refusal naming %s", err, tracked)
	}
	for p, b := range before {
		if read(t, p) != b {
			t.Errorf("%s written despite the refusal", p)
		}
	}
	if _, err := os.Stat(f.q); !os.IsNotExist(err) {
		t.Errorf("the refusal created the quarantine: %v", err)
	}
	// A dry run refuses the same way: the operator sees the refusal before the apply.
	if _, err := Apply(targets, Options{DryRun: true}); !IsTrackedRefusal(err) {
		t.Errorf("dry run err = %v, want the refusal", err)
	}

	res, err := Apply(targets, Options{IncludeTracked: true, QuarantineDir: f.q})
	if err != nil || len(res.Changed) != 2 {
		t.Fatalf("with IncludeTracked: %+v %v", res, err)
	}
	if _, ok := topKey(t, tracked, "pluginConfigs"); ok {
		t.Error("IncludeTracked did not write the tracked file")
	}
}

// A tracked file with NO redundant key is listed "nothing redundant" and never
// written, with or without IncludeTracked.
func TestTrackedNothingRedundant(t *testing.T) {
	f := newEstate(t)
	tracked := trackedRepo(t, f.root, "repo",
		`{"extraKnownMarketplaces":{"other":{"source":{"source":"github","repo":"x/y"}}},"enabledPlugins":{"p@other":true}}`+"\n")
	before := read(t, tracked)
	targets := mustPlan(t, f.root)
	tg := byPath(targets)[tracked]
	if tg.Eligible || tg.Status != StatusTracked || tg.Reason != ReasonNothingRedundant || len(tg.Keys) != 0 {
		t.Fatalf("got %+v", tg)
	}
	for _, inc := range []bool{false, true} {
		if _, err := Apply(targets, Options{IncludeTracked: inc, QuarantineDir: f.q}); err != nil {
			t.Fatalf("IncludeTracked=%v: %v", inc, err)
		}
		if read(t, tracked) != before {
			t.Fatalf("IncludeTracked=%v wrote the file", inc)
		}
	}
}

// ── apply: idempotence, pre-images, dry run ─────────────────────────────────

func TestIdempotentAndPreImages(t *testing.T) {
	f := newEstate(t)
	a := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), `{
  "permissions": {"allow": []},
  "pluginConfigs": {"a@m": {"options": {"k": "v1"}}},
  "extraKnownMarketplaces": {"mk": {"source": {"source": "github", "repo": "o/r"}}}
}
`)
	b := write(t, filepath.Join(f.root, "b", ".claude", "settings.local.json"), `{"pluginConfigs":{"b@m":{"options":{}}}}`)
	if err := os.Chmod(b, 0o600); err != nil {
		t.Fatal(err)
	}
	pre := map[string]string{a: read(t, a), b: read(t, b)}

	// Dry run: reports, writes nothing, creates no quarantine.
	dry, err := Apply(mustPlan(t, f.root), Options{DryRun: true, QuarantineDir: f.q})
	if err != nil || len(dry.Changed) != 2 || !dry.DryRun {
		t.Fatalf("dry = %+v %v", dry, err)
	}
	for p, s := range pre {
		if read(t, p) != s {
			t.Errorf("dry run wrote %s", p)
		}
	}
	if _, err := os.Stat(f.q); !os.IsNotExist(err) {
		t.Error("dry run created the quarantine")
	}

	res, err := Apply(mustPlan(t, f.root), Options{QuarantineDir: f.q})
	if err != nil || len(res.Changed) != 2 {
		t.Fatalf("apply = %+v %v", res, err)
	}
	for _, c := range res.Changed {
		if filepath.Dir(c.Backup) != filepath.Join(f.q, "prune") {
			t.Errorf("backup %s outside <Q>/prune", c.Backup)
		}
		if got := read(t, c.Backup); got != pre[c.Path] {
			t.Errorf("pre-image of %s differs from the file as it was", c.Path)
		}
	}
	for _, d := range []string{f.q, filepath.Join(f.q, "prune")} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v %v, want 0700", d, fi.Mode().Perm(), err)
		}
	}
	if fi, _ := os.Stat(b); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: %v", fi.Mode().Perm())
	}
	bak := filepath.Join(f.q, "prune", backupName(b))
	if fi, _ := os.Stat(bak); fi.Mode().Perm() != 0o600 {
		t.Errorf("pre-image mode = %v, want the file's own 0600", fi.Mode().Perm())
	}
	if got := read(t, a); got != "{\n  \"permissions\": {\"allow\": []}\n}\n" {
		t.Errorf("rewrite = %q", got)
	}

	// Second run: nothing eligible, zero changes.
	again, err := Apply(mustPlan(t, f.root), Options{QuarantineDir: f.q})
	if err != nil || len(again.Changed) != 0 {
		t.Errorf("second apply = %+v %v, want zero changes", again, err)
	}

	// Rollback: restore one pre-image; it is a target again, and re-applying
	// reuses the identical pre-image.
	if err := os.WriteFile(b, []byte(pre[b]), 0o600); err != nil {
		t.Fatal(err)
	}
	if tg := byPath(mustPlan(t, f.root))[b]; !tg.Eligible {
		t.Errorf("restored file not a target again: %+v", tg)
	}
	if res, err := Apply(mustPlan(t, f.root), Options{QuarantineDir: f.q}); err != nil || len(res.Changed) != 1 {
		t.Errorf("re-apply = %+v %v", res, err)
	}
}

// A pre-image already in the quarantine with DIFFERENT bytes is never
// replaced: the write aborts and the file is untouched.
func TestPreImageNeverOverwritten(t *testing.T) {
	f := newEstate(t)
	p := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), `{"pluginConfigs":{"b@m":{"options":{}}}}`)
	before := read(t, p)
	write(t, filepath.Join(f.q, "prune", backupName(p)), "something else")
	_, err := Apply(mustPlan(t, f.root), Options{QuarantineDir: f.q})
	if err == nil || !strings.Contains(err.Error(), "different pre-image") {
		t.Fatalf("err = %v", err)
	}
	if read(t, p) != before {
		t.Error("file written although its backup could not be made")
	}
}

// The default quarantine is ~/.swarmery/quarantine/<date of Now>.
func TestDefaultQuarantine(t *testing.T) {
	f := newEstate(t)
	p := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), `{"pluginConfigs":{"b@m":{"options":{}}}}`)
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.Local)
	res, err := Apply(mustPlan(t, f.root), Options{Now: now})
	if err != nil || len(res.Changed) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	want := filepath.Join(f.home, ".swarmery", "quarantine", "2026-01-02", "prune", backupName(p))
	if res.Changed[0].Backup != want {
		t.Errorf("backup = %s, want %s", res.Changed[0].Backup, want)
	}
}

// ── estates that supply nothing ─────────────────────────────────────────────

func TestNoEstateIsAnError(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	if _, err := Plan([]string{base}); err == nil || !strings.Contains(err.Error(), "no estate") {
		t.Errorf("err = %v", err)
	}
	if _, err := Plan([]string{filepath.Join(base, "missing")}); err == nil {
		t.Error("a missing root is not an error")
	}
}

// An estate whose store does not admit its root delivers nothing: every file
// is listed, nothing is eligible.
func TestNotAdmittedSuppliesNothing(t *testing.T) {
	f := newEstate(t)
	if err := os.Remove(filepath.Join(os.Getenv("SWARMERY_SECRETS_DIR"), "est.env")); err != nil {
		t.Fatal(err)
	}
	p := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), `{"pluginConfigs":{"a@m":{"options":{"k":"v1"}}}}`)
	tg := byPath(mustPlan(t, f.root))[p]
	if tg.Eligible || tg.Reason != ReasonNotAdmitted || len(tg.Kept) != 1 {
		t.Errorf("got %+v", tg)
	}
}

// An existing file the trusted loader refuses is listed, never eligible.
func TestUnreadableListed(t *testing.T) {
	f := newEstate(t)
	p := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), `{"pluginConfigs":{"b@m":{"options":{}}}}`)
	if err := os.Chmod(p, 0o666); err != nil {
		t.Fatal(err)
	}
	tg := byPath(mustPlan(t, f.root))[p]
	if tg.Eligible || !strings.HasPrefix(tg.Reason, reasonUnreadablePrefix) {
		t.Errorf("got %+v", tg)
	}
}

// ── rendering ────────────────────────────────────────────────────────────────

func TestRenderTargets(t *testing.T) {
	ts := []Target{
		{Path: "/e/.claude/settings.json", Reason: ReasonEstateSource, Status: StatusNoRepo, Kept: []KeptKey{{Key: "pluginConfigs"}}},
		{Path: "/e/a/.claude/settings.json", Eligible: true, Keys: []string{"pluginConfigs"}, Reason: ReasonRedundant,
			Kept: []KeptKey{{Key: "extraKnownMarketplaces", NotInEstate: []string{"x"}}}, Status: StatusUntracked},
		{Path: "/e/t/.claude/settings.json", Reason: ReasonNothingRedundant, Status: StatusTracked,
			Kept: []KeptKey{{Key: "extraKnownMarketplaces", NotInEstate: []string{"swarmery"}}}},
	}
	var buf bytes.Buffer
	if err := RenderTargets(&buf, ts); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	eligible := 0
	for _, l := range lines {
		if !strings.HasSuffix(l, "TRACKED") && !strings.HasSuffix(l, "untracked") && !strings.HasSuffix(l, "NO-REPO") {
			t.Errorf("line without a trailing git status: %q", l)
		}
		if strings.Contains(l, "eligible") {
			eligible++
		}
	}
	if eligible != 1 || !strings.Contains(lines[1], "remove pluginConfigs (keep extraKnownMarketplaces[x])") ||
		!strings.Contains(lines[2], "nothing redundant (keep extraKnownMarketplaces[swarmery])") ||
		strings.Contains(lines[0], "keep") {
		t.Errorf("render:\n%s", buf.String())
	}

	buf.Reset()
	_ = RenderResult(&buf, Result{Changed: []Change{{Path: "/p", Keys: []string{"pluginConfigs"}, Backup: "/q/b"}}})
	_ = RenderResult(&buf, Result{DryRun: true, Changed: []Change{}})
	want := "changed /p: removed pluginConfigs; pre-image /q/b\n1 files changed\n0 files would change\n"
	if buf.String() != want {
		t.Errorf("result = %q, want %q", buf.String(), want)
	}
}
