// Package plugindrift detects plugins that a project enables but that Claude
// Code cannot actually load: never installed for this project, installed at a
// version the marketplace has moved past, or installed into a cache directory
// that has since been reclaimed.
//
// Source of truth is `claude plugin list --json --available`, a supported
// interface, NOT the cache directory layout. Cache markers (.orphaned_at) are
// undocumented internals; a detector built on them keeps reporting "healthy"
// the day they are renamed, which is exactly the silent failure this package
// exists to prevent. The one place a marker is read (RuleCacheOrphaned) only
// corroborates a path the CLI itself reported, and only ever at warn.
package plugindrift

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/marketplace"
)

// Rule names. Disjoint from sysscan's linterRules, which is what makes sharing
// config_lint_findings safe (findings.Sync is per rule).
const (
	RuleEnabledNotInstalled = "plugin_enabled_not_installed"
	RuleVersionBehind       = "plugin_version_behind"
	RuleCacheOrphaned       = "plugin_cache_orphaned"
	RuleNote                = "plugin_note"
	RuleDetectorUnavailable = "plugin_detector_unavailable"
)

// Rules is the full owned set — every pass syncs each one, so a rule with no
// findings this pass resolves its previously active rows.
var Rules = []string{
	RuleEnabledNotInstalled,
	RuleVersionBehind,
	RuleCacheOrphaned,
	RuleNote,
	RuleDetectorUnavailable,
}

// detectorTarget is the single target used by RuleDetectorUnavailable — the
// failure is machine-wide, not per project.
const detectorTarget = "plugin:detector"

// Runner executes the claude CLI. Injected so tests never need the binary.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// Installed mirrors one `claude plugin list --json` installed[] entry. Unknown
// fields are ignored; the fields below are the ones rules depend on.
type Installed struct {
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Scope       string   `json:"scope"`
	ProjectPath string   `json:"projectPath"`
	Enabled     bool     `json:"enabled"`
	InstallPath string   `json:"installPath"`
	Notes       []string `json:"notes"`
}

type listOutput struct {
	Installed []Installed `json:"installed"`
}

// decodeList accepts both shapes `claude plugin list --json` has been observed
// to emit: a bare array of installed entries (what the CLI actually prints as
// of 2026-07), and an {"installed": [...]} envelope. Tolerating both is the
// §8.2 mitigation made concrete — the alternative is a decoder that reports
// plugin_detector_unavailable forever the day the shape moves, which is loud
// but still useless. Anything that is neither shape IS a decode failure and
// must surface as blindness, never as "no drift".
func decodeList(raw []byte) ([]Installed, error) {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var flat []Installed
		if err := json.Unmarshal(raw, &flat); err != nil {
			return nil, err
		}
		return flat, nil
	}
	var wrapped listOutput
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Installed, nil
}

// Project is one scan subject: a registered project and the plugin ids its
// settings.json enables.
type Project struct {
	Path    string   // already canonicalised by the caller
	Enabled []string // "<name>@<marketplace>"
}

// Detector holds the injected environment. ClaudeDir is the ~/.claude root.
type Detector struct {
	ClaudeDir string
	Runner    Runner
}

// Scan returns findings grouped by rule — every rule in Rules is present as a
// key (possibly with an empty slice) so the caller can Sync all of them and
// let stale rows resolve.
//
// A detector failure short-circuits: the result carries ONLY
// RuleDetectorUnavailable, and every other rule maps to nil rather than an
// empty slice, so the caller can tell "nothing found" from "could not look".
func (d *Detector) Scan(ctx context.Context, projects []Project) map[string][]findings.Item {
	out, err := d.Runner.Run(ctx, "", "plugin", "list", "--json")
	if err != nil {
		return unavailable(fmt.Sprintf("cannot run the claude CLI: %v — plugin drift is NOT being detected", err))
	}
	installed, err := decodeList(out)
	if err != nil {
		return unavailable(fmt.Sprintf("cannot parse `claude plugin list --json`: %v — plugin drift is NOT being detected", err))
	}

	res := map[string][]findings.Item{}
	for _, r := range Rules {
		res[r] = []findings.Item{}
	}

	for _, p := range projects {
		for _, id := range p.Enabled {
			name, mkt := splitID(id)
			inst, ok := resolveFor(installed, id, p.Path)
			target := Target(id, p.Path)
			if !ok {
				res[RuleEnabledNotInstalled] = append(res[RuleEnabledNotInstalled], findings.Item{
					Target:   target,
					Severity: "error",
					Message:  enabledNotInstalledMessage(id, installed),
				})
				continue
			}
			if v := d.catalogVersion(mkt, name); v != "" && versionBehind(inst.Version, v) {
				res[RuleVersionBehind] = append(res[RuleVersionBehind], findings.Item{
					Target:   target,
					Severity: "warn",
					Message:  fmt.Sprintf("installed %s, marketplace has %s — run update to pick it up", inst.Version, v),
				})
			}
			if reason := orphanReason(inst.InstallPath); reason != "" {
				res[RuleCacheOrphaned] = append(res[RuleCacheOrphaned], findings.Item{
					Target: target, Severity: "warn", Message: reason,
				})
			}
			for _, n := range inst.Notes {
				res[RuleNote] = append(res[RuleNote], findings.Item{
					Target: target, Severity: "info", Message: n,
				})
			}
		}
	}
	return res
}

func unavailable(msg string) map[string][]findings.Item {
	res := map[string][]findings.Item{}
	for _, r := range Rules {
		res[r] = nil // nil ⇒ "not evaluated"; the caller must NOT resolve these
	}
	res[RuleDetectorUnavailable] = []findings.Item{{
		Target: detectorTarget, Severity: "error", Message: msg,
	}}
	return res
}

// Target encodes the project dimension the findings table lacks, extending the
// documented convention (agent:12 | skill:3 | claude_md:<path>).
func Target(id, projectPath string) string { return "plugin:" + id + "|" + projectPath }

// ParseTarget is the inverse of Target. This package owns the wire format, so
// every consumer (API, insights, session-start hook) calls this rather than
// re-splitting the string — two copies of a format is how they drift apart.
// The detector's own machine-wide target has no "|" and returns ok == false.
func ParseTarget(target string) (id, projectPath string, ok bool) {
	rest, found := strings.CutPrefix(target, "plugin:")
	if !found {
		return "", "", false
	}
	id, projectPath, found = strings.Cut(rest, "|")
	return id, projectPath, found
}

func splitID(id string) (name, marketplace string) {
	name, marketplace, _ = strings.Cut(id, "@")
	return name, marketplace
}

// ResolveInstalled reports the installed entry that makes id available to
// projectPath, asking the CLI directly rather than reading a stored finding.
//
// The repair endpoint needs this because a drift verdict says WHAT is wrong and
// not WHERE the plugin lives: resolveFor counts a user-scoped install as
// available to every project, so "behind" is reported for a user- or
// local-scope copy just as readily as for a project-scope one, while `claude
// plugin update` is scope-bound and fails outright ("not installed at scope
// project") when asked for the wrong one.
//
// err != nil means the question could not be answered (CLI or decode failure).
// The caller must not read that as "not installed" — same rule as Scan's
// RuleDetectorUnavailable short-circuit.
func ResolveInstalled(ctx context.Context, r Runner, id, projectPath string) (Installed, bool, error) {
	out, err := r.Run(ctx, "", "plugin", "list", "--json")
	if err != nil {
		return Installed{}, false, err
	}
	installed, err := decodeList(out)
	if err != nil {
		return Installed{}, false, err
	}
	in, ok := resolveFor(installed, id, projectPath)
	return in, ok, nil
}

// resolveFor implements the availability rule: an installed entry counts for
// this project when it is user-scoped, or scoped to a matching projectPath.
// Caller-supplied projectPath must already be canonicalised.
//
// The FIRST applicable entry wins, by list position — deliberately, with no
// scope ranking. Claude Code's loader does the same over installed_plugins.json
// (CLI 2.1.285), so the verdict describes the copy a session actually loads: a
// stale project-scope record listed after a fresh user-scope one never loads
// and is not drift. Ranking by scope here would report copies nobody runs.
//
// "local" is the CLI's own name for a project-local install (recorded in
// .claude/settings.local.json rather than settings.json) and carries the same
// projectPath as "project" — treating only the latter as available reported
// every locally-installed pack as missing for the project that installed it.
func resolveFor(installed []Installed, id, projectPath string) (Installed, bool) {
	for _, in := range installed {
		if in.ID != id {
			continue
		}
		if in.Scope == "user" {
			return in, true
		}
		if (in.Scope == "project" || in.Scope == "local") && sameDir(in.ProjectPath, projectPath) {
			return in, true
		}
	}
	return Installed{}, false
}

// sameDir compares two directory paths after symlink resolution — on macOS the
// same directory is reachable as /Volumes/... and /private/var/..., and a raw
// string compare would report every project-scoped plugin as missing.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	return erra == nil && errb == nil && ra == rb
}

// enabledNotInstalledMessage names the scopes the plugin IS installed for, so
// the reader can see "installed, but for another project" at a glance.
func enabledNotInstalledMessage(id string, installed []Installed) string {
	var elsewhere []string
	for _, in := range installed {
		if in.ID == id && (in.Scope == "project" || in.Scope == "local") && in.ProjectPath != "" {
			elsewhere = append(elsewhere, in.ProjectPath)
		}
	}
	if len(elsewhere) == 0 {
		return "enabled in settings.json but not installed on this machine — run install"
	}
	sort.Strings(elsewhere)
	return "enabled here, but installed only for " + strings.Join(elsewhere, ", ")
}

func (d *Detector) catalogVersion(marketplaceName, pluginName string) string {
	cat, err := marketplace.Read(d.ClaudeDir, marketplaceName)
	if err != nil {
		return ""
	}
	for _, p := range cat.Plugins {
		if p.Name == pluginName {
			return marketplace.PluginVersion(d.ClaudeDir, marketplaceName, p)
		}
	}
	return ""
}

// orphanReason returns "" when the install path looks live.
func orphanReason(installPath string) string {
	if installPath == "" {
		return ""
	}
	if _, err := os.Stat(installPath); err != nil {
		return "install path no longer exists: " + installPath
	}
	if _, err := os.Stat(filepath.Join(installPath, ".orphaned_at")); err == nil {
		return "cache copy was reclaimed (.orphaned_at present): " + installPath
	}
	return ""
}

// versionBehind reports whether installed < catalog by dotted numeric
// comparison. Non-numeric or empty versions ("unknown") are incomparable and
// never report behind — a false "up to date" is better than a false alarm the
// user cannot act on.
func versionBehind(installed, catalog string) bool {
	ip, iok := parseVersion(installed)
	cp, cok := parseVersion(catalog)
	if !iok || !cok {
		return false
	}
	for i := 0; i < 3; i++ {
		if ip[i] != cp[i] {
			return ip[i] < cp[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 4)
	if len(parts) < 2 {
		return out, false
	}
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
