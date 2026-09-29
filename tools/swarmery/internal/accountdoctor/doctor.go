// Package accountdoctor diagnoses which credentials a project's Claude Code
// session can see — by NAME, never by value.
//
// It is its own package, not part of internal/claudeacct, because
// internal/usage already imports claudeacct: a doctor that later reaches usage
// from inside claudeacct would be an import cycle.
//
// # What Fast measures, and why that way
//
// The only thing that ever escalates is COVERAGE: a ${VAR} that an ENABLED
// pack's .mcp.json names and that is not set in the environment. Fast reports
// it as three flat lists of NAMES —
//
//	varsExpected  the ${NAME}s the enabled packs' MCP configs reference
//	varsPresent   of exactly those, the ones set and non-empty in THIS process
//	varsMissing   the remainder — THE only escalation trigger
//
// varsPresent is measured on the process's own environment, not on the store's
// inventory: that is what MCP substitution actually saw, so the measurement
// survives any change in how the variables got there.
//
// `credentials` — how many names the estate's store supplies — is CONTEXT and
// never a trigger. 0 is healthy in all three of its forms (no estate, an estate
// declaring no store, a store file that does not exist): an estate may
// legitimately supply nothing because nothing asks for anything.
//
// # The arms
//
// Fast is pure filesystem plus the Lock 1 git probe claudeacct.Resolve runs
// itself, plus at most one optional read-only SQLite lookup (the first-sight
// arm). It never opens a socket and never starts a `claude` process — it runs
// at turn zero from a SessionStart hook. Its arms, each in its own file:
//
//	resolution.go    the account, the rung that decided it, the estate, the
//	                 default account's two profiles, shadowing / ignored pins
//	enabled.go       the enabled-pack union (three layers, never the estate)
//	doctor.go        coverage: varsExpected / varsPresent / varsMissing
//	trust.go         estate-unanchored, store-rootless, estate-settings-unusable
//	settingsdelta.go the two accounts' settings.json, by key name and hash
//	parity.go        installed_plugins.json per (id, scope), orphan checkouts
//	firstsight.go    the once-per-path warning under an estate root (D3)
//	probe.go         the stored channel-probe verdict for the installed CLI
//	sysgaps.go       two fixed info findings (surfaces still single-account)
//	duplicates.go    staleDuplicates: second live copies of a store, of an
//	                 estate settings block, of a lent binding
//
// Full is Fast plus two findings that cost further git calls (binding-tracked,
// estate-settings-tracked). Probe (probe.go) is the only arm that spawns
// anything, and neither Fast nor Full reaches it. Nothing in Report, Finding
// or Duplicate ever holds a variable's value; render.go is the one choke point
// every printed string goes through.
package accountdoctor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// Schema is the Report's JSON schema version.
const Schema = 1

// LaunchPathEnv is the marker `swarmery account exec` sets for the child: the
// project path it composed the environment for. Only that exec sets it — no
// daemon seam does.
const LaunchPathEnv = "SWARMERY_LAUNCH_PATH"

// maxConfigBytes caps every JSON file Fast reads; a larger one is skipped.
const maxConfigBytes = 1 << 20

// Options is the doctor's input. This is the FINAL signature: later arms read
// the other fields, and every one of them is optional here — Fast is correct
// with DB nil, Now zero (=> time.Now()), StateDir empty, Timeout zero (=> no
// deadline) and Record false (=> every arm read-only).
type Options struct {
	Path string // the project path to diagnose
	// DB is an OPTIONAL read-only handle, used ONLY by the first-sight arm's
	// `projects`-row lookup. nil, a missing file or a locked one: that arm
	// relies on its ledger alone and a warn finding says so — never a failure.
	DB       *sql.DB
	Now      time.Time     // the ledger's clock; zero => time.Now()
	StateDir string        // the first-sight ledger root; "" => no ledger is read or written
	Timeout  time.Duration // bounds the whole call; 0 => no deadline
	Record   bool          // false => the ledger arm is read-only
}

// internal/accountdoctor — the whole JSON contract. Two SHELL consumers parse it on the hot
// path (plugins/accounts-pack/hooks/preflight-account.sh at SessionStart, and
// plugins/core/statusline/statusline.sh on every turn), so the hot fields are FLAT scalars
// and lists — no nested objects. camelCase tags, matching swarmery's existing API DTOs
// (overlaySources, marketplaceVersion, underOnboardRoot, canWrite).
type Report struct {
	// … resolution fields (account, source, configDir, estate, estateRoot, settingsFile) …
	Schema       int    `json:"schema"`       // Schema (1)
	Path         string `json:"path"`         // the diagnosed project path
	Account      string `json:"account"`      // effective account key ("default" when nothing pins one)
	Source       string `json:"source"`       // which rung decided the account (claudeacct.Source*)
	ConfigDir    string `json:"configDir"`    // the account's config dir (~/.claude for the default)
	Estate       string `json:"estate"`       // the estate key, or ""
	EstateRoot   string `json:"estateRoot"`   // the dir that declared the estate, or ""
	SettingsFile string `json:"settingsFile"` // the estate's .claude/settings.json when it exists, or ""

	Credentials     int         `json:"credentials"`     // NAMES the estate's store supplies. 0 is HEALTHY (D2a).
	CredentialStore string      `json:"credentialStore"` // resolved store path, or "" when none exists
	VarsExpected    []string    `json:"varsExpected"`    // ${VAR}s referenced by the enabled packs' .mcp.json
	VarsPresent     []string    `json:"varsPresent"`
	VarsMissing     []string    `json:"varsMissing"`     // THE only escalation trigger. Never null; [] when clean.
	StaleDuplicates []Duplicate `json:"staleDuplicates"` // Never null; [] when clean.

	LaunchedViaSwarmery bool      `json:"launchedViaSwarmery"` // SWARMERY_LAUNCH_PATH covers Path
	Daemon              bool      `json:"daemon"`              // Path is under the daemon's worktree root
	Findings            []Finding `json:"findings"`            // Never null; [] when clean.

	// Added by the doctor phase — additive only; nothing above changes.
	EnabledPacks []string `json:"enabledPacks"` // the three-layer union arm (b) computes. Never null.
	Admission    string   `json:"admission"`    // the admission line(s) `swarmery account which` prints, or its ignored lines
	// DefaultProfile names the two .claude.json files a DEFAULT resolution can
	// read and which one it will. Absent for any other account.
	DefaultProfile *DefaultProfile `json:"defaultProfile,omitempty"`
	SettingsDelta  []SettingsDelta `json:"settingsDelta"` // arm (c). Never null.
	Parity         []ParityEntry   `json:"parity"`        // arm (d). Never null.
}

type Duplicate struct {
	Kind    string   `json:"kind"`    // "credential-store" | "settings-block" | "binding"
	Paths   []string `json:"paths"`   // the two or more live copies
	Key     string   `json:"key"`     // the settings key; "" for a credential store
	Overlap []string `json:"overlap"` // variable NAMES or option keys in common — never values
	Count   int      `json:"count"`
}

// Finding is one diagnosed problem; the list is never null. ID is a stable
// kebab-case identifier (docs/claude-cli-config-channels.md lists every one), Severity one
// of the Sev* constants. Detail and File name paths, counts and variable NAMES
// only — never a value.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	File     string `json:"file"`
}

// Severities.
const (
	SevInfo  = "info"
	SevWarn  = "warn"
	SevError = "error"
)

// ErrNoPath is Fast's only error: an Options without a project path.
var ErrNoPath = errors.New("accountdoctor: Options.Path is required")

// wallClock is the deadline clock (Options.Timeout). Options.Now is the
// LEDGER's clock and may be pinned by a test; a deadline must see real time.
var wallClock = time.Now

// run carries one doctor call's state between arms.
type run struct {
	opts     Options
	path     string
	res      claudeacct.Resolution
	rep      *Report
	deadline time.Time
	timedOut bool
	// ledgerFile is the first-sight ledger to record this path in once the
	// run completes ("" when there is nothing to record).
	ledgerFile string
}

// afterArm is a test seam called after each Fast arm; nil in production.
var afterArm func(name string)

// add appends a finding.
func (r *run) add(f Finding) { r.rep.Findings = append(r.rep.Findings, f) }

// expired reports — once, with a warn finding — that Options.Timeout has run
// out, so the caller skips every arm that is left. The report keeps what the
// arms before it found: a partial answer beats none at turn zero.
func (r *run) expired(next string) bool {
	if r.timedOut {
		return true
	}
	if r.deadline.IsZero() || !wallClock().After(r.deadline) {
		return false
	}
	r.timedOut = true
	r.add(Finding{ID: "timeout", Severity: SevWarn, Title: "the doctor ran out of time",
		Detail: "Options.Timeout (" + r.opts.Timeout.String() + ") expired before the " + next +
			" arm; it and every arm after it were skipped"})
	return true
}

// getenv is the environment seam varsPresent and the launch marker read.
var getenv = os.Getenv

// userHomeDir is the $HOME seam for the daemon worktree root.
var userHomeDir = os.UserHomeDir

// Fast is the read-only, turn-zero arm set (see the package doc). An input
// that cannot be read becomes a Report field (an empty list, a zero count) or
// a warn finding — never an error. The only error is ErrNoPath.
func Fast(opts Options) (Report, error) {
	r, err := start(opts)
	if err != nil {
		return emptyReport(""), err
	}
	r.fast()
	r.commitLedger()
	return *r.rep, nil
}

// Full is Fast plus the two findings that need further git calls and so never
// run at turn zero: binding-tracked and estate-settings-tracked (trust.go).
func Full(opts Options) (Report, error) {
	r, err := start(opts)
	if err != nil {
		return emptyReport(""), err
	}
	r.fast()
	if !r.expired("full-trust") {
		r.fullTrust()
	}
	r.commitLedger()
	return *r.rep, nil
}

// start validates opts and resolves the path.
func start(opts Options) (*run, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, ErrNoPath
	}
	path := opts.Path
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	rep := emptyReport(path)
	r := &run{opts: opts, path: path, rep: &rep}
	if opts.Timeout > 0 {
		r.deadline = wallClock().Add(opts.Timeout)
	}
	r.res = claudeacct.Resolve(path)
	return r, nil
}

// fast runs every Fast arm in order, stopping early only on the deadline.
func (r *run) fast() {
	rep, res, path := r.rep, r.res, r.path
	rep.Account = res.Account
	if rep.Account == "" {
		rep.Account = ingest.DefaultAccount
	}
	rep.Source = res.Source
	rep.ConfigDir = accountConfigDir(rep.Account)
	rep.Estate = res.Estate
	rep.EstateRoot = res.EstateRoot
	rep.SettingsFile = res.SettingsFile

	// credentials / credentialStore gate on admission (D5): a store present but
	// rootless, or rooted elsewhere, releases nothing and counts 0 / "".
	rep.Credentials = res.CredentialCount()
	if res.EstateAdmitted && res.HasCredentialStore() {
		rep.CredentialStore = claudeacct.SecretsPath(res.Estate)
	}

	rep.EnabledPacks = EnabledPacks(path, rep.ConfigDir)
	rep.VarsExpected = expectedVars(rep.ConfigDir, path)
	for _, name := range rep.VarsExpected {
		if getenv(name) != "" {
			rep.VarsPresent = append(rep.VarsPresent, name)
		} else {
			rep.VarsMissing = append(rep.VarsMissing, name)
		}
	}
	if len(rep.VarsMissing) > 0 {
		r.add(Finding{ID: "vars-missing", Severity: SevError,
			Title: fmt.Sprintf("%d referenced MCP variable(s) unset", len(rep.VarsMissing)),
			Detail: "the enabled packs' MCP configs reference " + strings.Join(rep.VarsMissing, ", ") +
				", and nothing in this environment supplies them"})
	}

	rep.LaunchedViaSwarmery = launchedFor(getenv(LaunchPathEnv), path, res)
	rep.Daemon = underDaemonRoot(path)

	arms := []struct {
		name string
		fn   func()
	}{
		{"resolution", r.resolution},
		{"trust", r.trust},
		{"settings-delta", r.settingsDelta},
		{"parity", r.parity},
		{"first-sight", r.firstSight},
		{"probe-verdict", r.probeVerdict},
		{"sysgaps", r.sysgaps},
		{"stale-duplicates", r.staleDuplicates},
	}
	for _, a := range arms {
		if r.expired(a.name) {
			return
		}
		a.fn()
		if afterArm != nil {
			afterArm(a.name)
		}
	}
}

// emptyReport is the zero report with every list non-nil, so each marshals as
// [] and never as null: a jq gate `select(.x | length > 0)` over a null field
// selects nothing and would tick green over a check that never ran.
func emptyReport(path string) Report {
	return Report{
		Schema:          Schema,
		Path:            path,
		VarsExpected:    []string{},
		VarsPresent:     []string{},
		VarsMissing:     []string{},
		StaleDuplicates: []Duplicate{},
		Findings:        []Finding{},
		EnabledPacks:    []string{},
		SettingsDelta:   []SettingsDelta{},
		Parity:          []ParityEntry{},
	}
}

// accountConfigDir is where the account's config (and its plugins/) lives. An
// account discovered on disk is reported where it actually is; otherwise the
// canonical location.
func accountConfigDir(key string) string {
	if dir, ok := claudeacct.ConfigDirForAccount(key); ok {
		return dir
	}
	dir, err := claudeacct.ConfigDirFor(key)
	if err != nil {
		return ""
	}
	return dir
}

// launchedFor reports whether the launch marker covers path: the marker names
// path itself, or an ancestor of it that resolves to the same account and the
// same estate — the environment composed there is the one path would get.
func launchedFor(marker, path string, r claudeacct.Resolution) bool {
	marker = strings.TrimSpace(marker)
	if marker == "" || !filepath.IsAbs(marker) {
		return false
	}
	marker = filepath.Clean(marker)
	if marker == path {
		return true
	}
	// The shell's logical cwd and Claude Code's may differ by a symlink
	// (/tmp vs /private/tmp): compare the resolved forms too.
	realMarker, realPath := resolved(marker), resolved(path)
	if realMarker == realPath {
		return true
	}
	if !within(marker, path) && !within(realMarker, realPath) {
		return false
	}
	lr := claudeacct.Resolve(marker)
	return lr.Account == r.Account && lr.Estate == r.Estate && lr.EstateRoot == r.EstateRoot
}

// resolved is p with every symlink resolved, or p itself when that fails.
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// underDaemonRoot reports whether path lies under the daemon's worktree root
// (<home>/.swarmery/worktrees) — a run the daemon spawned, whose environment
// comes from the spawn seam rather than from a terminal.
func underDaemonRoot(path string) bool {
	home, err := userHomeDir()
	if err != nil || home == "" {
		return false
	}
	root := filepath.Join(home, filepath.FromSlash(worktree.DefaultRoot))
	return within(root, path) && root != path
}

// within reports whether p is dir or a descendant of it.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// ── coverage: which ${NAME}s do the enabled packs reference? ────────────────

// varRef matches ${NAME} and ${NAME:-default}. A reference with a default
// never counts as expected: substitution falls back instead of leaving it
// unset.
var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-[^}]*)?\}`)

// harnessVars are supplied by Claude Code itself for every plugin, never by
// the operator's environment.
var harnessVars = map[string]bool{
	"CLAUDE_PLUGIN_ROOT": true,
	"CLAUDE_PLUGIN_DATA": true,
	"CLAUDE_PROJECT_DIR": true,
}

// expectedVars is the sorted, de-duplicated ${NAME} set the enabled plugins'
// MCP configs reference, for the account at configDir and the project at path.
func expectedVars(configDir, path string) []string {
	out := []string{}
	if configDir == "" {
		return out
	}
	seen := map[string]bool{}
	for _, installPath := range enabledInstallPaths(configDir, path) {
		for _, raw := range mcpConfigs(installPath) {
			for _, m := range varRef.FindAllSubmatch(raw, -1) {
				name := string(m[1])
				if len(m[2]) > 0 || harnessVars[name] || seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// installRecord is one entry of <configDir>/plugins/installed_plugins.json.
type installRecord struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
	ProjectPath string `json:"projectPath"`
}

// enabledInstallPaths maps every enabled plugin to the one install directory
// a session at path would load: a project/local-scope install whose
// projectPath covers path wins over a user-scope one. A plugin that is enabled
// but not installed contributes nothing.
func enabledInstallPaths(configDir, path string) []string {
	var doc struct {
		Plugins map[string][]installRecord `json:"plugins"`
	}
	if !readJSON(filepath.Join(configDir, "plugins", "installed_plugins.json"), &doc) {
		return nil
	}
	var out []string
	for _, id := range EnabledPacks(path, configDir) {
		if p := pickInstall(doc.Plugins[id], path); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pickInstall chooses the install record a session at path loads.
func pickInstall(recs []installRecord, path string) string {
	var user string
	for _, rec := range recs {
		if rec.InstallPath == "" {
			continue
		}
		if rec.ProjectPath != "" {
			if within(filepath.Clean(rec.ProjectPath), path) {
				return rec.InstallPath
			}
			continue
		}
		if user == "" {
			user = rec.InstallPath
		}
	}
	return user
}

// mcpConfigs returns the raw bytes of every MCP server declaration a plugin
// ships: its root .mcp.json, and plugin.json's mcpServers (inline, or a path
// relative to the plugin root).
func mcpConfigs(installPath string) [][]byte {
	var out [][]byte
	if raw, ok := readCapped(filepath.Join(installPath, ".mcp.json")); ok {
		out = append(out, raw)
	}
	var manifest struct {
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	if !readJSON(filepath.Join(installPath, ".claude-plugin", "plugin.json"), &manifest) || len(manifest.MCPServers) == 0 {
		return out
	}
	var rel string
	if json.Unmarshal(manifest.MCPServers, &rel) == nil {
		p := filepath.Join(installPath, filepath.FromSlash(rel))
		if within(installPath, p) {
			if raw, ok := readCapped(p); ok {
				out = append(out, raw)
			}
		}
		return out
	}
	return append(out, manifest.MCPServers)
}

// readJSON decodes a capped regular file into v; false on any failure.
func readJSON(path string, v any) bool {
	raw, ok := readCapped(path)
	if !ok {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

// readCapped reads a regular file of at most maxConfigBytes; false otherwise.
func readCapped(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxConfigBytes {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(raw) > maxConfigBytes {
		return nil, false
	}
	return raw, true
}
