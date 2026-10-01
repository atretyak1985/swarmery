package plugindrift

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
)

// stubRunner returns a canned payload (or error) instead of running the CLI —
// no test in this package may touch the real claude binary.
type stubRunner struct {
	out []byte
	err error
}

func (s stubRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return s.out, s.err
}

// listJSON renders installed[] the way `claude plugin list --json` does.
func listJSON(t *testing.T, installed ...Installed) []byte {
	t.Helper()
	raw, err := json.Marshal(listOutput{Installed: installed})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// writeCatalog builds a marketplace clone under claudeDir holding one pack at
// the given version, so catalogVersion has something to read.
func writeCatalog(t *testing.T, claudeDir, mkt, pack, version string) {
	t.Helper()
	root := filepath.Join(claudeDir, "plugins", "marketplaces", mkt)
	mustWrite(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), `{
		"name": "`+mkt+`",
		"metadata": {"version": "`+version+`"},
		"plugins": [{"name": "`+pack+`", "source": "./plugins/`+pack+`", "description": "d"}]
	}`)
	mustWrite(t, filepath.Join(root, "plugins", pack, ".claude-plugin", "plugin.json"),
		`{"name": "`+pack+`", "version": "`+version+`"}`)
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// only asserts that rule holds exactly one item and every other rule is empty.
func only(t *testing.T, res map[string][]findings.Item, rule string) findings.Item {
	t.Helper()
	for _, r := range Rules {
		n := len(res[r])
		if r == rule {
			if n != 1 {
				t.Fatalf("rule %s: %d findings, want 1 (%+v)", r, n, res[r])
			}
			continue
		}
		if n != 0 {
			t.Errorf("rule %s: %d findings, want 0 (%+v)", r, n, res[r])
		}
	}
	return res[rule][0]
}

// TestScan_IncidentEnabledButInstalledForAnotherProject is the regression test
// for the incident that motivated this package: core@swarmery enabled in
// settings.json, installed only project-scoped for a DIFFERENT project, and
// therefore never loaded here — silently.
func TestScan_IncidentEnabledButInstalledForAnotherProject(t *testing.T) {
	d := &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{out: listJSON(t, Installed{
		ID: "core@swarmery", Scope: "project", ProjectPath: "/other/project", Version: "1.2.0",
	})}}

	res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"core@swarmery"}}})

	it := only(t, res, RuleEnabledNotInstalled)
	if it.Severity != "error" {
		t.Errorf("severity = %q, want error", it.Severity)
	}
	if it.Target != "plugin:core@swarmery|/my/project" {
		t.Errorf("target = %q", it.Target)
	}
	if !strings.Contains(it.Message, "/other/project") {
		t.Errorf("message = %q, want it to name /other/project", it.Message)
	}
}

func TestScan_Resolution(t *testing.T) {
	tests := []struct {
		name        string
		installed   Installed
		projectPath string
		wantMissing bool
		wantMessage string // substring, only checked when wantMissing
	}{
		{
			name:      "user scope resolves for any project",
			installed: Installed{ID: "core@swarmery", Scope: "user", Version: "2.4.0"},
		},
		{
			name:        "project scope with matching path resolves",
			installed:   Installed{ID: "core@swarmery", Scope: "project", ProjectPath: "/my/project", Version: "2.4.0"},
			projectPath: "/my/project",
		},
		{
			// `--scope local` records the install in .claude/settings.local.json
			// and carries the same projectPath as `project`. Counting only the
			// latter reported every locally-installed pack as missing for the
			// project that installed it.
			name:        "local scope with matching path resolves",
			installed:   Installed{ID: "core@swarmery", Scope: "local", ProjectPath: "/my/project", Version: "2.4.0"},
			projectPath: "/my/project",
		},
		{
			name:        "local scope elsewhere names the other project",
			installed:   Installed{ID: "core@swarmery", Scope: "local", ProjectPath: "/other/project"},
			wantMissing: true,
			wantMessage: "/other/project",
		},
		{
			name:        "different id does not resolve",
			installed:   Installed{ID: "web-pack@swarmery", Scope: "user", Version: "2.4.0"},
			wantMissing: true,
			wantMessage: "not installed on this machine",
		},
		{
			name:        "project scope elsewhere names the other project",
			installed:   Installed{ID: "core@swarmery", Scope: "project", ProjectPath: "/other/project"},
			wantMissing: true,
			wantMessage: "/other/project",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.projectPath
			if path == "" {
				path = "/my/project"
			}
			d := &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{out: listJSON(t, tc.installed)}}

			res := d.Scan(context.Background(), []Project{{Path: path, Enabled: []string{"core@swarmery"}}})

			got := len(res[RuleEnabledNotInstalled])
			if tc.wantMissing != (got == 1) {
				t.Fatalf("%s findings = %d, wantMissing = %v", RuleEnabledNotInstalled, got, tc.wantMissing)
			}
			if tc.wantMissing && !strings.Contains(res[RuleEnabledNotInstalled][0].Message, tc.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", res[RuleEnabledNotInstalled][0].Message, tc.wantMessage)
			}
		})
	}
}

// ResolveInstalled is what the repair endpoint builds its command from, so the
// two failure modes must stay distinguishable: "not installed" is a repair
// decision, a lookup error is not.
func TestResolveInstalled(t *testing.T) {
	body := listJSON(t,
		Installed{ID: "core@swarmery", Scope: "user", Version: "0.1.0"},
		Installed{ID: "web-pack@swarmery", Scope: "local", ProjectPath: "/my/project", Version: "1.1.0"},
	)

	in, ok, err := ResolveInstalled(context.Background(), stubRunner{out: body}, "core@swarmery", "/my/project")
	if err != nil || !ok || in.Scope != "user" || in.Version != "0.1.0" {
		t.Errorf("user-scope lookup = (%+v, %v, %v), want the 0.1.0 user entry", in, ok, err)
	}

	in, ok, err = ResolveInstalled(context.Background(), stubRunner{out: body}, "web-pack@swarmery", "/my/project")
	if err != nil || !ok || in.Scope != "local" {
		t.Errorf("local-scope lookup = (%+v, %v, %v), want the local entry", in, ok, err)
	}

	_, ok, err = ResolveInstalled(context.Background(), stubRunner{out: body}, "iot-pack@swarmery", "/my/project")
	if err != nil || ok {
		t.Errorf("absent plugin = (%v, %v), want (false, nil)", ok, err)
	}

	if _, ok, err = ResolveInstalled(context.Background(), stubRunner{err: errors.New("boom")}, "core@swarmery", "/my/project"); err == nil || ok {
		t.Errorf("CLI failure = (%v, %v), want an error and not-found", ok, err)
	}

	if _, ok, err = ResolveInstalled(context.Background(), stubRunner{out: []byte("not json")}, "core@swarmery", "/my/project"); err == nil || ok {
		t.Errorf("undecodable output = (%v, %v), want an error and not-found", ok, err)
	}
}

func TestScan_VersionBehind(t *testing.T) {
	tests := []struct {
		name      string
		installed string
		catalog   string
		want      bool
	}{
		{name: "behind", installed: "2.2.0", catalog: "2.4.0", want: true},
		{name: "up to date", installed: "2.4.0", catalog: "2.4.0"},
		{name: "ahead", installed: "2.5.0", catalog: "2.4.0"},
		{name: "unknown installed version is incomparable", installed: "unknown", catalog: "2.4.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claudeDir := t.TempDir()
			writeCatalog(t, claudeDir, "swarmery", "core", tc.catalog)
			d := &Detector{ClaudeDir: claudeDir, Runner: stubRunner{out: listJSON(t, Installed{
				ID: "core@swarmery", Scope: "user", Version: tc.installed,
			})}}

			res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"core@swarmery"}}})

			if !tc.want {
				if n := len(res[RuleVersionBehind]); n != 0 {
					t.Fatalf("%s findings = %d, want 0 (%+v)", RuleVersionBehind, n, res[RuleVersionBehind])
				}
				return
			}
			it := only(t, res, RuleVersionBehind)
			if it.Severity != "warn" {
				t.Errorf("severity = %q, want warn", it.Severity)
			}
			if !strings.Contains(it.Message, tc.installed) || !strings.Contains(it.Message, tc.catalog) {
				t.Errorf("message = %q, want both versions named", it.Message)
			}
		})
	}
}

// TestResolveForMirrorsLoaderOrder pins the rule Claude Code's loader uses:
// when a plugin has both a user-scope and a project-scope record for the same
// project, the first applicable record in list order is the one that loads, so
// it is the one drift is judged on. A stale project copy listed after a fresh
// user copy is not drift; listed before it, it is.
func TestResolveForMirrorsLoaderOrder(t *testing.T) {
	stale := Installed{ID: "core@swarmery", Scope: "project", ProjectPath: "/my/project", Version: "2.2.0"}
	fresh := Installed{ID: "core@swarmery", Scope: "user", Version: "2.4.0"}
	other := Installed{ID: "core@swarmery", Scope: "project", ProjectPath: "/other/project", Version: "1.0.0"}

	tests := []struct {
		name       string
		installed  []Installed
		wantScope  string
		wantBehind bool
	}{
		{name: "stale project copy first loads", installed: []Installed{stale, fresh}, wantScope: "project", wantBehind: true},
		{name: "fresh user copy first loads", installed: []Installed{fresh, stale}, wantScope: "user"},
		{name: "another project's record is skipped", installed: []Installed{other, fresh, stale}, wantScope: "user"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in, ok := resolveFor(tc.installed, "core@swarmery", "/my/project")
			if !ok || in.Scope != tc.wantScope {
				t.Fatalf("resolveFor = (%+v, %v), want the %s entry", in, ok, tc.wantScope)
			}

			claudeDir := t.TempDir()
			writeCatalog(t, claudeDir, "swarmery", "core", "2.4.0")
			d := &Detector{ClaudeDir: claudeDir, Runner: stubRunner{out: listJSON(t, tc.installed...)}}
			res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"core@swarmery"}}})

			if !tc.wantBehind {
				if n := len(res[RuleVersionBehind]); n != 0 {
					t.Fatalf("%s findings = %d, want 0 (%+v)", RuleVersionBehind, n, res[RuleVersionBehind])
				}
				return
			}
			if it := only(t, res, RuleVersionBehind); !strings.Contains(it.Message, "2.2.0") {
				t.Errorf("message = %q, want the loaded 2.2.0 copy named", it.Message)
			}
		})
	}
}

func TestScan_VersionBehindNoCatalog(t *testing.T) {
	// No marketplace clone at all: the pack version is unknowable, which must
	// stay silent rather than fire a warning the user cannot act on.
	d := &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{out: listJSON(t, Installed{
		ID: "core@swarmery", Scope: "user", Version: "1.0.0",
	})}}

	res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"core@swarmery"}}})

	if n := len(res[RuleVersionBehind]); n != 0 {
		t.Errorf("%s findings = %d, want 0", RuleVersionBehind, n)
	}
}

func TestScan_VersionBehindPackNotCatalogued(t *testing.T) {
	claudeDir := t.TempDir()
	writeCatalog(t, claudeDir, "swarmery", "core", "2.4.0")
	d := &Detector{ClaudeDir: claudeDir, Runner: stubRunner{out: listJSON(t, Installed{
		ID: "web-pack@swarmery", Scope: "user", Version: "1.0.0",
	})}}

	res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"web-pack@swarmery"}}})

	if n := len(res[RuleVersionBehind]); n != 0 {
		t.Errorf("%s findings = %d, want 0 (pack absent from catalogue)", RuleVersionBehind, n)
	}
}

func TestScan_CacheOrphaned(t *testing.T) {
	live := t.TempDir()
	reclaimed := t.TempDir()
	mustWrite(t, filepath.Join(reclaimed, ".orphaned_at"), "2026-07-28T00:00:00Z")
	gone := filepath.Join(t.TempDir(), "does-not-exist")

	tests := []struct {
		name        string
		installPath string
		wantMessage string // "" ⇒ no finding
	}{
		{name: "live cache dir", installPath: live},
		{name: "empty install path", installPath: ""},
		{name: "orphan marker", installPath: reclaimed, wantMessage: ".orphaned_at"},
		{name: "path gone", installPath: gone, wantMessage: "no longer exists"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{out: listJSON(t, Installed{
				ID: "core@swarmery", Scope: "user", Version: "unknown", InstallPath: tc.installPath,
			})}}

			res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"core@swarmery"}}})

			if tc.wantMessage == "" {
				if n := len(res[RuleCacheOrphaned]); n != 0 {
					t.Fatalf("%s findings = %d, want 0 (%+v)", RuleCacheOrphaned, n, res[RuleCacheOrphaned])
				}
				return
			}
			it := only(t, res, RuleCacheOrphaned)
			if it.Severity != "warn" {
				t.Errorf("severity = %q, want warn", it.Severity)
			}
			if !strings.Contains(it.Message, tc.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", it.Message, tc.wantMessage)
			}
		})
	}
}

func TestScan_NotesPassthrough(t *testing.T) {
	d := &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{out: listJSON(t, Installed{
		ID: "core@swarmery", Scope: "user", Version: "unknown",
		Notes: []string{"marketplace clone is behind its remote"},
	})}}

	res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"core@swarmery"}}})

	it := only(t, res, RuleNote)
	if it.Severity != "info" {
		t.Errorf("severity = %q, want info", it.Severity)
	}
	if it.Message != "marketplace clone is behind its remote" {
		t.Errorf("message = %q", it.Message)
	}
}

// TestScan_DetectorUnavailable pins the contract phase 3 depends on: when
// detection could not run, every non-detector rule maps to nil (not to an
// empty slice), so the caller never resolves real findings it could not see.
func TestScan_DetectorUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		runner stubRunner
		want   string // substring of the message
	}{
		{name: "cli fails", runner: stubRunner{err: errors.New("boom")}, want: "cannot run the claude CLI"},
		{name: "unparsable output", runner: stubRunner{out: []byte("not json")}, want: "cannot parse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &Detector{ClaudeDir: t.TempDir(), Runner: tc.runner}

			res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"core@swarmery"}}})

			items := res[RuleDetectorUnavailable]
			if len(items) != 1 {
				t.Fatalf("%s = %+v, want exactly 1 item", RuleDetectorUnavailable, items)
			}
			if items[0].Severity != "error" {
				t.Errorf("severity = %q, want error", items[0].Severity)
			}
			if items[0].Target != detectorTarget {
				t.Errorf("target = %q, want %q", items[0].Target, detectorTarget)
			}
			if !strings.Contains(items[0].Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", items[0].Message, tc.want)
			}
			for _, r := range Rules {
				if r == RuleDetectorUnavailable {
					continue
				}
				if res[r] != nil {
					t.Errorf("rule %s = %+v, want nil (not evaluated), not an empty slice", r, res[r])
				}
			}
		})
	}
}

func TestScan_EveryRuleKeyPresentWhenClean(t *testing.T) {
	d := &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{out: listJSON(t, Installed{
		ID: "core@swarmery", Scope: "user", Version: "unknown",
	})}}

	res := d.Scan(context.Background(), []Project{{Path: "/my/project", Enabled: []string{"core@swarmery"}}})

	for _, r := range Rules {
		items, ok := res[r]
		if !ok || items == nil {
			t.Errorf("rule %s missing from a clean pass — stale rows would never resolve", r)
		}
		if len(items) != 0 {
			t.Errorf("rule %s = %+v, want no findings", r, items)
		}
	}
}

func TestVersionBehind(t *testing.T) {
	tests := []struct {
		installed, catalog string
		want               bool
	}{
		{"1.2.0", "2.4.0", true},
		{"2.4.0", "2.4.0", false},
		{"2.5.0", "2.4.0", false},
		{"unknown", "2.4.0", false},
		{"2.4", "2.4.1", true},
		{"2.4.0", "", false},
		{"v2.3.0", "v2.4.0", true},
	}
	for _, tc := range tests {
		if got := versionBehind(tc.installed, tc.catalog); got != tc.want {
			t.Errorf("versionBehind(%q, %q) = %v, want %v", tc.installed, tc.catalog, got, tc.want)
		}
	}
}

func TestTargetRoundTrip(t *testing.T) {
	tests := []struct{ id, path string }{
		{"core@swarmery", "/my/project"},
		{"superpowers@claude-plugins-official", "/Volumes/Work/some project"},
	}
	for _, tc := range tests {
		id, path, ok := ParseTarget(Target(tc.id, tc.path))
		if !ok || id != tc.id || path != tc.path {
			t.Errorf("round-trip(%q, %q) = (%q, %q, %v)", tc.id, tc.path, id, path, ok)
		}
	}

	if _, _, ok := ParseTarget(detectorTarget); ok {
		t.Errorf("ParseTarget(%q) reported ok, want false (no project dimension)", detectorTarget)
	}
	if _, _, ok := ParseTarget("agent:12"); ok {
		t.Error("ParseTarget(\"agent:12\") reported ok, want false (not a plugin target)")
	}
}

func TestSplitID(t *testing.T) {
	name, mkt := splitID("core@swarmery")
	if name != "core" || mkt != "swarmery" {
		t.Errorf("splitID = (%q, %q)", name, mkt)
	}
	name, mkt = splitID("bare")
	if name != "bare" || mkt != "" {
		t.Errorf("splitID(bare) = (%q, %q)", name, mkt)
	}
}

func TestSameDirResolvesSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if !sameDir(link, real) {
		t.Errorf("sameDir(%q, %q) = false, want true", link, real)
	}
	if sameDir(real, filepath.Join(real, "child")) {
		t.Error("sameDir reported unrelated paths equal")
	}
}

func TestResolveBinExplicitMissing(t *testing.T) {
	if _, err := ResolveBin(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("want error for an explicit binary path that does not exist")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	mustWrite(t, bin, "#!/bin/sh\n")
	got, err := ResolveBin(bin)
	if err != nil || got != bin {
		t.Errorf("ResolveBin(%q) = (%q, %v)", bin, got, err)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abc", 3); got != "abc" {
		t.Errorf("truncate = %q", got)
	}
}

// The shape `claude plugin list --json` actually prints (verified against the
// CLI on 2026-07-28) is a BARE ARRAY, not the {"installed": [...]} envelope the
// design assumed. Decoding only the envelope made every real machine report
// plugin_detector_unavailable — loud, but blind to the drift it exists to find.
func TestDecodeListAcceptsBothShapes(t *testing.T) {
	realOutput := []byte(`[
	  {"id":"architecture-pack@swarmery","version":"1.0.0","scope":"user","enabled":true,
	   "installPath":"/Users/x/.claude/plugins/cache/swarmery/architecture-pack/1.0.0",
	   "installedAt":"2026-07-24T04:53:52.485Z","lastUpdated":"2026-07-24T04:53:52.485Z"},
	  {"id":"core@swarmery","version":"1.2.0","scope":"project","enabled":false,
	   "installPath":"/Users/x/.claude/plugins/cache/swarmery/core/1.2.0",
	   "projectPath":"/Volumes/Work/Skygor/scripts"}
	]`)
	got, err := decodeList(realOutput)
	if err != nil {
		t.Fatalf("bare array must decode: %v", err)
	}
	if len(got) != 2 || got[1].ID != "core@swarmery" || got[1].ProjectPath != "/Volumes/Work/Skygor/scripts" {
		t.Fatalf("bare array decoded wrong: %+v", got)
	}

	wrapped := []byte(`{"installed":[{"id":"core@swarmery","scope":"user"}],"available":[]}`)
	got, err = decodeList(wrapped)
	if err != nil {
		t.Fatalf("envelope must still decode: %v", err)
	}
	if len(got) != 1 || got[0].Scope != "user" {
		t.Fatalf("envelope decoded wrong: %+v", got)
	}

	if _, err := decodeList([]byte("not json")); err == nil {
		t.Error("garbage must be a decode error, so the caller can report blindness")
	}
}

// End-to-end on the real payload shape: the incident must be detected when the
// CLI speaks its actual dialect, not only the fixture dialect.
func TestScan_IncidentThroughRealArrayShape(t *testing.T) {
	projectPath := t.TempDir()
	d := &Detector{ClaudeDir: t.TempDir(), Runner: stubRunner{out: []byte(`[
	  {"id":"core@swarmery","version":"1.2.0","scope":"project","enabled":false,
	   "projectPath":"/Volumes/Work/Skygor/scripts"}
	]`)}}

	res := d.Scan(context.Background(), []Project{{Path: projectPath, Enabled: []string{"core@swarmery"}}})

	items := res[RuleEnabledNotInstalled]
	if len(items) != 1 {
		t.Fatalf("want the incident detected, got %+v", res)
	}
	if !strings.Contains(items[0].Message, "/Volumes/Work/Skygor/scripts") {
		t.Errorf("message must name the foreign project path, got %q", items[0].Message)
	}
	if len(res[RuleDetectorUnavailable]) != 0 {
		t.Errorf("a decodable payload must not report blindness: %+v", res[RuleDetectorUnavailable])
	}
}
