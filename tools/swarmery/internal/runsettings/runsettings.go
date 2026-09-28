// Package runsettings composes the settings file every swarmery-launched
// `claude` receives through the `--settings` root option.
//
// Why a composed file at all: Claude Code reads `pluginConfigs` ONLY from user
// settings, from `--settings` (flagSettings) and from policy settings; project-
// and local-scope copies are written and ignored. So the one project-portable
// channel for an estate's plugin options is `--settings`.
//
// # One source, three keys (D6)
//
// The composed file comes from ONE file — the admitted estate's
// <EstateRoot>/.claude/settings.json — and carries exactly EstateKeys, copied
// verbatim after a JSON type check. Everything else in that file is dropped BY
// NAME. The composer reads no project, worktree or local file: Claude Code loads
// those itself, behind its own trust gate, and merges them with the flag file.
// A `--settings` file is trusted configuration — on the terminal route it is
// applied without the workspace-trust dialog — so `env`, `permissions`, `hooks`,
// `apiKeyHelper` and every other key stay out: only the three plugin-wiring keys
// the operator declared, from a file the estate store admits (D5 Lock 2).
//
// # The contract, in order
//
//   - a: no ADMITTED estate ⇒ in.Fallback, verbatim; nothing written or logged.
//     This is what makes the change no flag day.
//   - b: admitted, no settings.json ⇒ in.Fallback, silently (a credentials-only
//     estate is healthy).
//   - c: the file is unusable (D9) — outside EstateRoot, refused by the trusted
//     loader, or an EstateKeys value that is not an object ⇒ in.Fallback VERBATIM
//     and one WARN per (engine, path, mtime); never a partial compose.
//   - d: filter to EstateKeys; e: Fallback parity for a lent file; f: write the
//     content-addressed file (store.go); g: log counts and key names once.
//
// Compose never returns an error and never blocks a launch. No log line or
// return value ever carries a settings VALUE.
package runsettings

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// EstateKeys are the ONLY keys a composed file takes from the estate. An array,
// so every reader gets a copy and none can mutate it. Phase 5's settings-block
// detector and Phase 7's prune compare exactly this list; never copy it.
var EstateKeys = [...]string{"pluginConfigs", "enabledPlugins", "extraKnownMarketplaces"}

// keySwarmery is the binding object. It is dropped silently: a binding copied
// into a run would freeze it.
const keySwarmery = "swarmery"

// reasonWrongType is the one reason the composer adds to claudeacct's Trust*
// codes, + the EstateKeys key whose value is not a JSON object.
const reasonWrongType = "wrong-type:"

// Inputs is what a spawn site hands the composer. There is no project, repo or
// worktree path: the composer reads no file there.
type Inputs struct {
	// Fallback is the run's settings file without an estate: "" for most seams,
	// the lent project settings.json for planrun, phaserun and resume.
	Fallback string
}

// Compose returns the settings file a run of engine should receive: a composed
// file, or in.Fallback. See the package doc for the contract.
func Compose(engine string, res claudeacct.Resolution, in Inputs) string {
	path, _ := compose(engine, res, in, false)
	return path
}

// ComposeQuiet is Compose for the operator's terminal (swarmery account exec):
// the same file and the same rules, and nothing written to the log. reason is
// non-empty only when an admitted estate's settings file was unusable, so the
// caller can print its one stderr line.
func ComposeQuiet(res claudeacct.Resolution, in Inputs) (path, reason string) {
	return compose("terminal", res, in, true)
}

// SpliceTerminal inserts "--settings", f immediately after argv[0] when ALL of:
// argv[0] names claude by BASE name (a bare word, an absolute path or a stub
// all count), f is non-empty, and argv[1:] carries no --settings or
// --settings=… of the caller's own — the caller's flag always wins. --settings
// is a ROOT option, parsed only before a subcommand, so "claude --settings F mcp
// list" works where "claude mcp list --settings F" does not. Otherwise argv is
// returned unchanged. It lives here, not in cmd/swarmery, so the rule sits
// inside the coverage gate.
func SpliceTerminal(argv []string, f string) []string {
	if len(argv) == 0 || f == "" || filepath.Base(argv[0]) != "claude" {
		return argv
	}
	for _, a := range argv[1:] {
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return argv
		}
	}
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[0], "--settings", f)
	return append(out, argv[1:]...)
}

func compose(engine string, res claudeacct.Resolution, in Inputs, quiet bool) (string, string) {
	// a. No admitted estate — no estate at all, or one its store does not admit.
	if !res.EstateAdmitted {
		return in.Fallback, ""
	}
	// b. A credentials-only estate ships no settings file.
	if res.SettingsFile == "" {
		return in.Fallback, ""
	}
	// c. The file itself: inside the root, trusted, typed.
	estate, reason := loadEstate(res)
	if reason == "" && estate == nil {
		return in.Fallback, "" // gone since Resolve saw it — as absent as b
	}
	var out map[string]any
	var dropped []string
	if reason == "" {
		// d. Filter. A value of the wrong type makes the whole file unusable.
		out, dropped, reason = filterEstate(estate)
	}
	if reason != "" {
		if !quiet {
			warnUnusable(engine, res.SettingsFile, reason)
		}
		return in.Fallback, reason
	}

	// e. Fallback parity: a lent file goes verbatim, the estate fills what it lacks.
	lent := 0
	if in.Fallback != "" {
		lentRoot, lentReason := claudeacct.ReadTrustedSettings(in.Fallback)
		if lentRoot == nil || lentReason != "" {
			return in.Fallback, ""
		}
		merged := make(map[string]any, len(lentRoot)+len(EstateKeys))
		for k, v := range lentRoot {
			merged[k] = v
		}
		added := 0
		for _, k := range EstateKeys {
			if _, has := merged[k]; has {
				continue
			}
			if v, ok := out[k]; ok {
				merged[k] = v
				added++
			}
		}
		lent = 1
		if added == 0 {
			if !quiet {
				logCompose(engine, in.Fallback, res.EstateRoot, merged, lent, dropped)
			}
			return in.Fallback, ""
		}
		out = merged
	}

	// f. Write the content-addressed file.
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		if !quiet {
			logOnce("encode\x00"+engine, fmt.Sprintf("warning: runsettings: engine=%s could not encode the composed settings; using the fallback", engine))
		}
		return in.Fallback, ""
	}
	path := write(append(b, '\n'))
	if path == "" {
		if !quiet {
			logOnce("nostore\x00"+engine, fmt.Sprintf("warning: runsettings: engine=%s could not write the composed settings under %q; using the fallback", engine, Dir()))
		}
		return in.Fallback, ""
	}
	// g. Counts and key names, once per (engine, path).
	if !quiet {
		logCompose(engine, path, res.EstateRoot, out, lent, dropped)
	}
	return path, ""
}

// loadEstate reads res.SettingsFile through the one trusted loader, and only if
// it lies inside res.EstateRoot — checked before the open and again against the
// descriptor held, so a .claude swapped for a symlink in between cannot hand an
// account's settings.json to flag precedence. (nil, "") means the file is gone.
func loadEstate(res claudeacct.Resolution) (map[string]any, string) {
	return claudeacct.ReadTrustedSettingsWithin(res.SettingsFile, res.EstateRoot)
}

// filterEstate copies exactly the EstateKeys present in root, verbatim, after
// checking each is a JSON object. Every other top-level key's NAME goes to
// dropped, sorted; `swarmery` is dropped silently. The ONLY copy loop ranges
// over EstateKeys — there is no default branch that copies a value.
func filterEstate(root map[string]any) (out map[string]any, dropped []string, reason string) {
	out = make(map[string]any, len(EstateKeys))
	for _, k := range EstateKeys {
		v, ok := root[k]
		if !ok {
			continue
		}
		if _, isObject := v.(map[string]any); !isObject {
			return nil, nil, reasonWrongType + k
		}
		out[k] = v
	}
	for k := range root {
		if k == keySwarmery || isEstateKey(k) {
			continue
		}
		dropped = append(dropped, k)
	}
	sort.Strings(dropped)
	return out, dropped, ""
}

func isEstateKey(k string) bool {
	for _, e := range EstateKeys {
		if k == e {
			return true
		}
	}
	return false
}

// warnUnusable logs the D9 WARN once per (engine, path, mtime): a file fixed or
// touched later is reported again, an unchanged one is not repeated per spawn.
// Path and reason only — never contents.
func warnUnusable(engine, path, reason string) {
	mtime := ""
	if fi, err := os.Stat(path); err == nil {
		mtime = fi.ModTime().UTC().Format("2006-01-02T15:04:05.000000000Z")
	} else if fi, err := os.Lstat(path); err == nil {
		mtime = fi.ModTime().UTC().Format("2006-01-02T15:04:05.000000000Z")
	}
	logOnce("unusable\x00"+engine+"\x00"+path+"\x00"+mtime,
		fmt.Sprintf("warning: runsettings: engine=%s estate settings unusable (%s): %s; using the fallback", engine, reason, path))
}

// logCompose is contract step g: one line per (engine, settings path).
func logCompose(engine, path, estateRoot string, file map[string]any, lent int, dropped []string) {
	logOnce("compose\x00"+engine+"\x00"+path, fmt.Sprintf(
		"runsettings: engine=%s settings=%s estate=%s pluginConfigs=%d enabledPlugins=%d extraKnownMarketplaces=%d lent=%d dropped=%s",
		engine, path, estateRoot, countOf(file["pluginConfigs"]), countOf(file["enabledPlugins"]),
		countOf(file["extraKnownMarketplaces"]), lent, quoteNames(dropped)))
}

// quoteNames renders key NAMES for the log with %q: they come from a settings
// file, and a name carrying a newline must not be able to forge a log line.
func quoteNames(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ",")
}

func countOf(v any) int {
	if m, ok := v.(map[string]any); ok {
		return len(m)
	}
	return 0
}

var (
	logMu   sync.Mutex
	logSeen = map[string]bool{}
)

// logOnce prints msg the first time key is seen in this process.
func logOnce(key, msg string) {
	logMu.Lock()
	defer logMu.Unlock()
	if logSeen[key] {
		return
	}
	logSeen[key] = true
	log.Print(msg)
}
