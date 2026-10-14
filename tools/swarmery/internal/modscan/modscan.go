// Package modscan finds Claude Code mods — plugins whose hooks/hooks.json
// lists `modules` — and audits them with `claude plugin validate --strict
// --json` (docs/mods-format.md §M6).
//
// It is READ-ONLY on disk: it reads plugin.json, hooks.json and the module
// files it hashes, and runs the claude CLI's validator. It never writes a
// file and never touches the DB — internal/sysscan owns persistence, the
// bus note and the parse_error finding.
//
// The validator reports what a module hooks and calls only as prose `notes`
// lines ("<module> hooks: …", "<module> calls: …", "<module> env reads: …"),
// plus a structured `gatingHooks` list. There are no top-level events / uses /
// env / state keys (M6 verdict), so Validate parses the notes by their known
// labels, checks every value's shape, requires a hooks and a calls line per
// module, and fails closed (Report.Unrecognized → an unknown badge) on
// anything it cannot read.
package modscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
)

// Source values for ModDir.Source and the mods.source column.
const (
	SourceCache     = "cache"      // <claudeDir>/plugins/cache/…
	SourcePluginDir = "plugin-dir" // CLAUDE_CODE_PLUGIN_DIRS (settings `env`)
)

// PluginDirsEnv is the variable Claude Code reads extra plugin folders from.
// The scanner reads it from the settings `env` block only: `--plugin-dir` on
// a live process has no on-disk source and is not discoverable.
const PluginDirsEnv = "CLAUDE_CODE_PLUGIN_DIRS"

// ValidateTimeout bounds one `claude plugin validate` run. A var only so
// tests can shorten it; production never assigns it.
var ValidateTimeout = 20 * time.Second

// maxCacheDepth is how many directory levels below plugins/cache a plugin
// root may sit (<mkt>/<plugin>/<version> is 3; one spare level).
const maxCacheDepth = 4

// MsgNoBinary is the one error a Report carries when no claude executable
// could be found.
const MsgNoBinary = "claude binary not found"

// ModDir is one discovered mod.
type ModDir struct {
	Name     string   `json:"name"`     // plugin.json name, else the folder name
	PluginID string   `json:"pluginId"` // "<name>@<marketplace>" for cache mods, "" for plugin-dir
	Dir      string   `json:"dir"`      // absolute plugin root
	Source   string   `json:"source"`   // SourceCache | SourcePluginDir
	Version  string   `json:"version"`  // plugin.json version, else the cache version dir name
	Modules  []string `json:"modules"`  // hooks.json `modules`, verbatim
}

// GatingHook is one entry of the validator's structured gatingHooks list.
type GatingHook struct {
	Module   string `json:"module"`
	Pattern  string `json:"pattern"`
	Hook     string `json:"hook"`
	HasCatch bool   `json:"hasCatch"`
}

// EnvAccess is what a mod's modules read and write in the environment.
type EnvAccess struct {
	Reads  []string `json:"reads"`
	Writes []string `json:"writes"`
}

// Report is the parsed result of one validate run.
type Report struct {
	// Validated is true when the validator ran and its JSON parsed. False means
	// nothing is known about the mod's behaviour (Risk answers "unknown").
	Validated   bool            `json:"validated"`
	Success     bool            `json:"success"`
	Errors      []string        `json:"errors"`
	Warnings    []string        `json:"warnings"`
	Events      []string        `json:"events"` // hooked events, verbatim incl. {filters}
	Uses        []string        `json:"uses"`   // $.* calls, without "$." and " (via …)"
	Env         EnvAccess       `json:"env"`
	State       []string        `json:"state"`
	GatingHooks []GatingHook    `json:"gatingHooks"`
	Raw         json.RawMessage `json:"raw,omitempty"` // the validator's stdout, verbatim

	// Unrecognized lists what the parser could not read: "<module> <label>: …"
	// notes whose label it does not know, notes holding a value that is not
	// event/call/env-shaped (a list joined by a separator other than ", "),
	// and a marker for every module the output mentions without both a
	// "hooks:" and a "calls:" line. Non-empty → Risk fails closed with an
	// unknown badge instead of claiming the mod is harmless.
	Unrecognized []string `json:"unrecognized"`

	// modules records, per module key (normModule), which required lines were
	// parsed. In-memory only: Validate extends it with hooks.json's modules.
	modules map[string]*moduleLines
	// expectModules: the output shows module content (a hooks part, a gating
	// hook, or a "<module> <label>:" note) even if it names no module.
	expectModules bool
}

// moduleLines is which of the required per-module lines a report carried.
type moduleLines struct {
	name         string // the module as first written (marker text)
	hooks, calls bool
}

// ParseError reports a validate run that produced no usable report: a
// timeout, output that is not the expected JSON, or a failed exec. The caller
// records it as a config_lint_findings parse_error.
type ParseError struct {
	Dir    string
	Reason string // "timeout" | "canceled" | "invalid JSON" | "exec failed"
	Err    error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("claude plugin validate %s: %s: %v", e.Dir, e.Reason, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// ── discovery ────────────────────────────────────────────────────────────────

// PluginDirsFromSettings returns the CLAUDE_CODE_PLUGIN_DIRS entries of the
// `env` block in <claudeDir>/settings.json, split on the OS path-list
// separator (':' on macOS/Linux). A missing or malformed file yields nil.
func PluginDirsFromSettings(claudeDir string) []string {
	raw, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Env map[string]any `json:"env"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	v, _ := doc.Env[PluginDirsEnv].(string)
	return SplitPluginDirs(v)
}

// SplitPluginDirs splits a CLAUDE_CODE_PLUGIN_DIRS value into trimmed,
// non-empty entries.
func SplitPluginDirs(v string) []string {
	var out []string
	for _, p := range filepath.SplitList(v) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Discover lists every mod: plugin roots under <claudeDir>/plugins/cache (at
// most maxCacheDepth levels down; for a <mkt>/<plugin>/<version> layout only
// the active version — the one with an `.in_use` marker, else the lexically
// greatest, the same pick sysscan makes) plus the extraDirs plugin folders.
// A plugin whose hooks/hooks.json has no non-empty `modules` is not a mod and
// is skipped. Unreadable entries are skipped, never fatal. The result is
// sorted by source, name, dir and holds each dir once.
func Discover(claudeDir string, extraDirs []string) []ModDir {
	seen := map[string]bool{}
	var out []ModDir
	add := func(m ModDir, ok bool) {
		if ok && !seen[m.Dir] {
			seen[m.Dir] = true
			out = append(out, m)
		}
	}

	cache := filepath.Join(claudeDir, "plugins", "cache")
	for _, root := range cacheRoots(cache) {
		add(readMod(root, SourceCache, cache))
	}
	for _, d := range extraDirs {
		if d = expandHome(d); filepath.IsAbs(d) {
			add(readMod(filepath.Clean(d), SourcePluginDir, ""))
		}
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Dir < b.Dir
	})
	return out
}

// cacheRoots walks the plugin cache for dirs holding .claude-plugin/plugin.json
// and keeps one version per <mkt>/<plugin>.
func cacheRoots(cache string) []string {
	var found []string
	_ = filepath.WalkDir(cache, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == cache {
				return fs.SkipAll
			}
			return nil // unreadable subtree — skip it
		}
		if !d.IsDir() {
			return nil
		}
		depth := relDepth(cache, path)
		if path != cache && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
			return fs.SkipDir
		}
		if depth > 0 && isFile(filepath.Join(path, ".claude-plugin", "plugin.json")) {
			found = append(found, path)
			return fs.SkipDir // a plugin root is never searched for nested plugins
		}
		if depth >= maxCacheDepth {
			return fs.SkipDir
		}
		return nil
	})

	// One version per <mkt>/<plugin>: group depth-3 roots by their parent.
	byParent := map[string][]string{}
	var out []string
	for _, r := range found {
		if relDepth(cache, r) == 3 {
			byParent[filepath.Dir(r)] = append(byParent[filepath.Dir(r)], r)
			continue
		}
		out = append(out, r)
	}
	for _, versions := range byParent {
		out = append(out, pickVersion(versions))
	}
	sort.Strings(out)
	return out
}

// pickVersion prefers the version dir with an `.in_use` marker, else the
// lexically greatest name (sysscan.pickVersionDir's rule).
func pickVersion(dirs []string) string {
	sort.Strings(dirs)
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, ".in_use")); err == nil {
			return d
		}
	}
	return dirs[len(dirs)-1]
}

func relDepth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(rel, string(filepath.Separator)))
}

// readMod builds the ModDir for one plugin root; ok is false when the root
// is not a mod (no hooks.json modules).
func readMod(root, source, cache string) (ModDir, bool) {
	modules, err := readModules(root)
	if err != nil || len(modules) == 0 {
		return ModDir{}, false
	}
	m := ModDir{Dir: root, Source: source, Modules: modules}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if raw, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json")); err == nil {
		_ = json.Unmarshal(raw, &manifest)
	}
	m.Name, m.Version = manifest.Name, manifest.Version
	if source == SourceCache {
		if rel, err := filepath.Rel(cache, root); err == nil {
			parts := strings.Split(rel, string(filepath.Separator))
			if m.Name == "" && len(parts) >= 2 {
				m.Name = parts[1]
			}
			if m.Version == "" && len(parts) >= 3 {
				m.Version = parts[len(parts)-1]
			}
			if m.Name != "" {
				m.PluginID = m.Name + "@" + parts[0]
			}
		}
	}
	if m.Name == "" {
		m.Name = filepath.Base(root)
	}
	return m, true
}

// readModules returns hooks/hooks.json's `modules` (string entries only).
func readModules(root string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "hooks", "hooks.json"))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Modules []any `json:"modules"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range doc.Modules {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// ── hashing ──────────────────────────────────────────────────────────────────

// Hash is the mod's version key: sha256 over hooks/hooks.json and, in listed
// order, each module path and its content (resolved against hooks/). A module
// that is missing, a directory, or outside the plugin root hashes as a marker
// instead of failing — validate reports it, the hash just has to move.
func Hash(dir string) (string, error) {
	hooksJSON := filepath.Join(dir, "hooks", "hooks.json")
	raw, err := os.ReadFile(hooksJSON)
	if err != nil {
		return "", err
	}
	modules, err := readModules(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte("hooks.json\x00"))
	h.Write(raw)
	root := filepath.Clean(dir)
	for _, m := range modules {
		h.Write([]byte("\x00" + m + "\x00"))
		p := filepath.Clean(filepath.Join(dir, "hooks", m))
		if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			h.Write([]byte("<outside>"))
			continue
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			h.Write([]byte("<unreadable>"))
			continue
		}
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── validate ─────────────────────────────────────────────────────────────────

// Validate runs `<claudeBin> plugin validate --strict --json <dir>` with
// ValidateTimeout and parses its output. claudeBin "" resolves the binary via
// internal/claudebin. No binary → a Report whose only error is MsgNoBinary
// (and a nil error). A timeout, output that is not the expected JSON, or a
// failed exec → *ParseError.
func Validate(ctx context.Context, claudeBin, dir string) (Report, error) {
	if claudeBin == "" {
		bin, err := claudebin.Resolve()
		if err != nil {
			return noBinary(), nil
		}
		claudeBin = bin
	}
	ctx, cancel := context.WithTimeout(ctx, ValidateTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin, "plugin", "validate", "--strict", "--json", dir)
	cmd.WaitDelay = 2 * time.Second // a grandchild holding stdout must not outlive the kill
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	if err := ctx.Err(); err != nil {
		reason := "timeout"
		if !errors.Is(err, context.DeadlineExceeded) {
			reason = "canceled"
		}
		return Report{}, &ParseError{Dir: dir, Reason: reason, Err: err}
	}
	if runErr != nil && (errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, fs.ErrNotExist)) {
		return noBinary(), nil
	}
	rep, perr := ParseReport(stdout.Bytes())
	if perr != nil {
		if runErr != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 300 {
				msg = msg[:300]
			}
			return Report{}, &ParseError{Dir: dir, Reason: "exec failed", Err: fmt.Errorf("%v: %s", runErr, msg)}
		}
		return Report{}, &ParseError{Dir: dir, Reason: "invalid JSON", Err: perr}
	}
	// The dir is a mod: every module its hooks.json lists must be covered by a
	// hooks and a calls line, whatever ParseReport could infer from the
	// output alone.
	if modules, merr := readModules(dir); merr == nil && len(modules) > 0 {
		rep.requireModuleLines(modules...)
	}
	// A non-zero exit with a parsable report is a failed validation, not a
	// scanner failure: the report already says success=false.
	return rep, nil
}

// EmptyReport is an unvalidated report with every list non-nil, so its JSON never
// carries null. Risk answers "unknown" for it.
func EmptyReport() Report {
	return Report{Errors: []string{}, Warnings: []string{}, Events: []string{},
		Uses: []string{}, Env: EnvAccess{Reads: []string{}, Writes: []string{}},
		State: []string{}, GatingHooks: []GatingHook{}, Unrecognized: []string{}}
}

func noBinary() Report {
	r := EmptyReport()
	r.Errors = []string{MsgNoBinary}
	return r
}

// validateDoc is the §M6 shape.
type validateDoc struct {
	Success  *bool          `json:"success"`
	Manifest *validatePart  `json:"manifest"`
	Contents []validatePart `json:"contents"`
}

type validatePart struct {
	Type        string            `json:"type"` // "plugin" | "hooks" | …
	Errors      []json.RawMessage `json:"errors"`
	Warnings    []json.RawMessage `json:"warnings"`
	Notes       []string          `json:"notes"`
	GatingHooks []GatingHook      `json:"gatingHooks"`
}

// ParseReport parses validate's --json stdout (§M6). It is an error when the
// bytes are not a JSON object carrying a boolean `success`.
func ParseReport(raw []byte) (Report, error) {
	var doc validateDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Report{}, err
	}
	if doc.Success == nil {
		return Report{}, errors.New("no boolean `success` key")
	}
	r := EmptyReport()
	r.Validated, r.Success = true, *doc.Success
	r.Raw = json.RawMessage(bytes.TrimSpace(raw))

	parts := doc.Contents
	if doc.Manifest != nil {
		parts = append([]validatePart{*doc.Manifest}, parts...)
	}
	// Every module the output mentions — in a note head or a gating hook —
	// must be covered by its own hooks and calls lines; a hooks part with no
	// module at all must still say something.
	var mentioned []string
	for _, p := range parts {
		r.Errors = appendMessages(r.Errors, p.Errors)
		r.Warnings = appendMessages(r.Warnings, p.Warnings)
		r.GatingHooks = append(r.GatingHooks, p.GatingHooks...)
		if p.Type == "hooks" {
			r.expectModules = true
		}
		for _, g := range p.GatingHooks {
			mentioned = append(mentioned, g.Module)
		}
		for _, n := range p.Notes {
			r.addNote(n)
		}
	}
	r.requireModuleLines(mentioned...)
	// $.state.* calls are state use whether or not a state line exists (M6
	// saw no state line; the label is matched by prefix in addNote).
	for _, u := range r.Uses {
		if strings.HasPrefix(u, "state.") {
			r.State = appendUnique(r.State, u)
		}
	}
	return r, nil
}

// msgNoModuleLines is the Unrecognized marker for output that shows module
// content (a hooks part) but names no module at all.
const msgNoModuleLines = `no "<module> hooks:" note in the validator output`

// msgMissingLine is the Unrecognized marker for a module the output mentions
// (or hooks.json lists) without one of its required lines.
func msgMissingLine(module, label string) string {
	if module == "" {
		return fmt.Sprintf(`no %q note in the validator output`, label+":")
	}
	return fmt.Sprintf(`no %q note in the validator output`, module+" "+label+":")
}

// normModule is the key a module is tracked under: the path as written,
// trimmed and cleaned, so "./m.tsx" in hooks.json and "m.tsx" in a note
// meet. The module-less form of a bare label keys as "".
func normModule(m string) string {
	if m = strings.TrimSpace(m); m == "" {
		return ""
	}
	return path.Clean(m)
}

// lines returns the tracking entry for module, creating it.
func (r *Report) lines(module string) *moduleLines {
	if r.modules == nil {
		r.modules = map[string]*moduleLines{}
	}
	key := normModule(module)
	ml, ok := r.modules[key]
	if !ok {
		ml = &moduleLines{name: strings.TrimSpace(module)}
		r.modules[key] = ml
	}
	return ml
}

// requireModuleLines adds extra (hooks.json modules, gating-hook modules) to
// the modules the report must cover, then marks the report not understood
// for every module lacking a hooks or a calls line, and for output that
// shows module content without naming any module. Idempotent.
//
// Both lines are required: they are the two that feed badges, and the
// validator writes explicit "nothing" lines rather than omitting a fact (M6:
// "env writes: nothing"). M6 never showed a module without calls, so a
// calls-less module is treated as drift — the fail-closed direction; if the
// validator turns out to omit an empty calls line, such a mod shows the
// unknown badge until this rule is relaxed. The state line is NOT required:
// M6 notes that a module without $.state gets none, and state feeds no badge.
func (r *Report) requireModuleLines(extra ...string) {
	for _, m := range extra {
		r.lines(m)
	}
	if r.expectModules && len(r.modules) == 0 {
		r.Unrecognized = appendUnique(r.Unrecognized, msgNoModuleLines)
	}
	keys := make([]string, 0, len(r.modules))
	for k := range r.modules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !r.modules[k].hooks {
			r.Unrecognized = appendUnique(r.Unrecognized, msgMissingLine(r.modules[k].name, "hooks"))
		}
		if !r.modules[k].calls {
			r.Unrecognized = appendUnique(r.Unrecognized, msgMissingLine(r.modules[k].name, "calls"))
		}
	}
}

// noteLabels are the labels the validator writes after the module path, in
// match order. The module path may contain spaces, so a label is found as a
// suffix of the note head — never as "everything after the first space".
var noteLabels = []string{"hooks", "calls", "env reads", "env writes",
	"gating hook with .catch", "gating hook without .catch"}

// Value shapes. A value that fails its shape is not a value the parser
// understood — most often a list joined by a separator other than ", "
// ("a,b", "a; b", "a | b", "a and b") collapsed into one string that no risk
// rule would ever match — so its note goes to Unrecognized.
var (
	// eventShape: dotted lowercase-led name plus an optional {filter} —
	// "session.start", "tool.call{tool=AskUserQuestion}".
	eventShape = regexp.MustCompile(`^[a-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+(\{[^{}]*\})?$`)
	// patternShape: a gatingHooks pattern — an event name whose segments may
	// be globs ("tool.call", "tool.*").
	patternShape = regexp.MustCompile(`^[a-z*][A-Za-z0-9_*]*(\.[A-Za-z0-9_*]+)+$`)
	// callShape: a $.* call after normalizeCall — "clock.now", "http.fetch".
	callShape = regexp.MustCompile(`^[a-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)
	// envShape: an environment variable name — "HOME".
	envShape = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// callNote: a trailing " (via …)" annotation on a call.
	callNote = regexp.MustCompile(` \([^()]*\)$`)
)

// addNote files one "<module> <label>: a, b, c" line under its fact. A line
// whose head is "<module> <label>" (it holds a space) or a known bare label
// is fact-shaped: a known label marks the module as mentioned and files the
// values that pass their shape; an unknown label, an empty list or any value
// failing its shape puts the whole note in Unrecognized. Any other line ("no
// colon here", "plugin.json: …") is prose and ignored.
func (r *Report) addNote(note string) {
	head, list, ok := strings.Cut(note, ": ")
	if !ok {
		// "<module> hooks:" with nothing after the colon is a fact line with
		// an empty list, not prose.
		h, found := strings.CutSuffix(strings.TrimSpace(note), ":")
		if !found {
			return
		}
		head, list = h, ""
	}
	head = strings.TrimSpace(head)
	label := matchLabel(head)
	if label == "" {
		if strings.Contains(head, " ") {
			r.expectModules = true
			r.Unrecognized = appendUnique(r.Unrecognized, note)
		}
		return
	}
	r.expectModules = true
	ml := r.lines(strings.TrimSuffix(strings.TrimSuffix(head, label), " "))

	values, ok := splitList(list)
	if !ok {
		r.Unrecognized = appendUnique(r.Unrecognized, note)
		return
	}
	valid := func(shape *regexp.Regexp, v string) bool {
		if shape.MatchString(v) {
			return true
		}
		r.Unrecognized = appendUnique(r.Unrecognized, note)
		return false
	}
	kind := label
	if strings.HasPrefix(label, "state") {
		kind = "state" // no other label starts with "state"
	}
	switch kind {
	case "hooks":
		ml.hooks = true
		for _, v := range values {
			if valid(eventShape, v) {
				r.Events = appendUnique(r.Events, v)
			}
		}
	case "calls":
		ml.calls = true
		for _, v := range values {
			if c := normalizeCall(v); valid(callShape, c) {
				r.Uses = appendUnique(r.Uses, c)
			}
		}
	case "env reads", "env writes":
		for _, v := range values {
			if !valid(envShape, v) {
				continue
			}
			if label == "env reads" {
				r.Env.Reads = appendUnique(r.Env.Reads, v)
			} else {
				r.Env.Writes = appendUnique(r.Env.Writes, v)
			}
		}
	case "state":
		// State keys are the mod's own free-form names and feed no badge, so
		// they are filed verbatim.
		r.State = appendUnique(r.State, values...)
	default:
		// The two gating-hook labels restate the structured gatingHooks list,
		// which Risk reads; their values are still checked as events.
		for _, v := range values {
			valid(eventShape, v)
		}
	}
}

// matchLabel returns the known label that ends head, or "" when none does.
// For the "state…" forms it returns the whole suffix ("state keys"), so
// trimming the label off head always leaves the module.
func matchLabel(head string) string {
	for _, l := range noteLabels {
		if head == l || strings.HasSuffix(head, " "+l) {
			return l
		}
	}
	if i := stateLabelAt(head); i >= 0 {
		return head[i:]
	}
	return ""
}

// stateLabelAt returns where head's "state" label starts, or -1: the word
// "state" (at the start or after a space) followed only by lowercase words —
// "state", "state keys", "./a b.tsx state reads". The rightmost valid
// occurrence wins, so a "state" inside the module path is never the label.
func stateLabelAt(head string) int {
	for i := strings.LastIndex(head, "state"); i >= 0; i = strings.LastIndex(head[:i], "state") {
		if i > 0 && head[i-1] != ' ' {
			continue
		}
		rest := head[i+len("state"):]
		if rest == "" || (rest[0] == ' ' && isLowerWords(rest)) {
			return i
		}
	}
	return -1
}

func isLowerWords(s string) bool {
	for _, c := range s {
		if c != ' ' && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

// splitList splits "a, b, c" — the separator is comma+space, so a filter like
// tool.call{tool=A,B} stays one value. The literal "nothing" is the empty
// list. ok is false for a list the validator never writes: an empty one, or
// one with an empty entry ("a, , b").
func splitList(s string) (values []string, ok bool) {
	s = strings.TrimSpace(s)
	switch s {
	case "nothing":
		return nil, true
	case "":
		return nil, false
	}
	for _, v := range strings.Split(s, ", ") {
		if v = strings.TrimSpace(v); v == "" {
			return nil, false
		}
		values = append(values, v)
	}
	return values, true
}

// normalizeCall maps "$.ui.log (via post)" to "ui.log". Only a trailing
// parenthetical is stripped, so "$.ui.log (via post) and $.fs.write" keeps
// its tail and fails the call shape instead of hiding $.fs.write.
func normalizeCall(v string) string {
	v = callNote.ReplaceAllString(strings.TrimSpace(v), "")
	return strings.TrimPrefix(strings.TrimSpace(v), "$.")
}

func appendUnique(dst []string, vs ...string) []string {
	for _, v := range vs {
		dup := false
		for _, d := range dst {
			if d == v {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, v)
		}
	}
	return dst
}

// appendMessages renders validator error/warning entries: a string verbatim,
// an object by its `message` field, anything else as compact JSON.
func appendMessages(dst []string, msgs []json.RawMessage) []string {
	for _, m := range msgs {
		var s string
		if json.Unmarshal(m, &s) == nil {
			dst = append(dst, s)
			continue
		}
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(m, &obj) == nil && obj.Message != "" {
			dst = append(dst, obj.Message)
			continue
		}
		var buf bytes.Buffer
		if json.Compact(&buf, m) == nil {
			dst = append(dst, buf.String())
		}
	}
	return dst
}

// ── risk ─────────────────────────────────────────────────────────────────────

// Badge levels.
const (
	LevelRed     = "red"
	LevelYellow  = "yellow"
	LevelUnknown = "unknown"
)

// RuleValidatorOutput is the unknown badge's rule when the validator ran but
// its notes could not be read (Report.Unrecognized non-empty).
const RuleValidatorOutput = "validator-output"

// Badge is one risk marker derived from a mod's hooked events and calls.
type Badge struct {
	Level   string   `json:"level"`   // red | yellow | unknown
	Rule    string   `json:"rule"`    // the matching rule: "tool.check", "process.*", …
	Label   string   `json:"label"`   // human text: "can approve tool calls"
	Matches []string `json:"matches"` // the events/calls that hit the rule
}

type riskRule struct {
	level, rule, label string
}

// riskRules is the badge table (phase-9 Implementation Details 3), red first.
// A rule ending in ".*" matches by prefix; the others match exactly. Events
// are matched without their {filter}.
var riskRules = []riskRule{
	{LevelRed, "tool.check", "can approve tool calls"},
	{LevelRed, "prompt.submit", "drives the session"},
	{LevelRed, "turn.abort", "drives the session"},
	{LevelRed, "http.fetch", "network"},
	{LevelRed, "process.*", "spawns processes"},
	{LevelRed, "fs.write", "writes files"},
	{LevelRed, "session.append", "rewrites what Claude reads"},
	{LevelYellow, "fs.read", "reads files"},
	{LevelYellow, "store.*", "keeps a store"},
	{LevelYellow, "model.*", "calls the model"},
}

// INVARIANT: Risk returns an empty slice ONLY when every module's facts were
// fully understood. Anything the parser could not positively understand — a
// note with an unknown label, a value that is not event/call/env-shaped, a
// module without its hooks or calls line (Report.Unrecognized), or a value
// handed to Risk that no rule could ever be matched against — produces the
// unknown RuleValidatorOutput badge, first, never silence.
//
// Risk derives the badges from Events, the gatingHooks' hook and pattern, and
// Uses, deterministically: one badge per matching rule, in riskRules order. A
// report that was never validated gets the single "unknown" badge. A validated
// report that was not fully understood leads with the unknown
// RuleValidatorOutput badge (its matches are the unread lines and values)
// before whatever the readable part matched.
func Risk(r Report) []Badge {
	if !r.Validated {
		return []Badge{{Level: LevelUnknown, Rule: "validate", Label: "not validated", Matches: []string{}}}
	}
	stripFilter := func(e string) string {
		if i := strings.Index(e, "{"); i >= 0 {
			return e[:i]
		}
		return e
	}
	// Re-check every value's shape here too, so the invariant holds for a
	// report Risk did not see ParseReport build.
	unread := append([]string{}, r.Unrecognized...)
	check := func(shape *regexp.Regexp, v string) {
		if !shape.MatchString(v) {
			unread = appendUnique(unread, v)
		}
	}
	names := make([]string, 0, len(r.Events)+2*len(r.GatingHooks)+len(r.Uses))
	for _, e := range r.Events {
		check(eventShape, e)
		names = append(names, stripFilter(e))
	}
	for _, g := range r.GatingHooks {
		check(eventShape, g.Hook)
		check(patternShape, g.Pattern)
		names = append(names, stripFilter(g.Hook), stripFilter(g.Pattern))
	}
	for _, u := range r.Uses {
		check(callShape, u)
	}
	names = append(names, r.Uses...)

	out := []Badge{}
	if len(unread) > 0 {
		out = append(out, Badge{Level: LevelUnknown, Rule: RuleValidatorOutput,
			Label: "validator output not understood", Matches: unread})
	}
	for _, rule := range riskRules {
		var matches []string
		for _, n := range names {
			if ruleMatches(rule.rule, n) {
				matches = appendUnique(matches, n)
			}
		}
		if len(matches) > 0 {
			out = append(out, Badge{Level: rule.level, Rule: rule.rule, Label: rule.label, Matches: matches})
		}
	}
	return out
}

func ruleMatches(rule, name string) bool {
	if p, ok := strings.CutSuffix(rule, "*"); ok {
		return strings.HasPrefix(name, p)
	}
	return name == rule
}
