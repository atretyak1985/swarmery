// Package settingsoverlay resolves DECLARED settings files that apply to a
// project even though they do not live in the project's repo.
//
// Why this exists: swarmery decides whether a project is "managed" (and which
// packs it runs) by reading the repo's .claude/settings.json. That is the whole
// truth only when sessions are started plainly. Some operators start Claude Code
// through an external launcher that injects a settings file at CLI precedence
// (`claude --settings <file>`) and deliberately keeps enabledPlugins OUT of the
// repo — for those projects the daemon reported managed:false while every real
// session ran the full plugin set. The dashboard was describing the repo, not
// the sessions.
//
// The daemon must stay ignorant of any particular launcher. It never inspects
// how a session is started, never shells out, never guesses: an operator
// DECLARES "this settings file also applies to these project roots" in a small
// descriptor file, and that declaration is the only thing this package knows.
//
// Descriptor — default ~/.swarmery/overlays.json, override with the
// SWARMERY_SETTINGS_OVERLAYS env (a filesystem path):
//
//	{
//	  "overlays": [
//	    {
//	      "name": "acme",
//	      "settingsPath": "~/launcher/orgs/acme/settings.json",
//	      "roots": ["~/work/acme"]
//	    }
//	  ]
//	}
//
// "~" expands to the user's home in settingsPath and in every root. "name" is a
// short operator-chosen label echoed back as provenance (overlaySources on the
// API DTOs) so a reader can tell WHERE a managed:true came from.
//
// Superseded — the descriptor FILE, not this package. For a project under an
// estate (`swarmery.estate` in a .claude/settings.local.json at or above it),
// the descriptor file ~/.swarmery/overlays.json is superseded: the estate
// binding carries the estate's settings to every session, and the dashboard
// answers from each repo's own .claude/settings.local.json, which
// internal/api/settings_overlays.go folds in first (localSettingsOverlay). No
// estate settings file is folded into overlaysFor() — the dashboard's rows come
// from the repo's own files. The PACKAGE is not retired: it remains the
// SWARMERY_SETTINGS_OVERLAYS mechanism for setups without an estate, and with
// no descriptor on disk it resolves nothing, silently (below).
//
// Degradation is the hard requirement: a missing, unreadable or malformed
// descriptor, an entry with no roots, or a settingsPath that has since been
// deleted must all collapse to repo-only detection. None of them may fail a
// request, and each distinct problem is logged AT MOST once (see memo) —
// project state is read on every list request, so an unmemoised log line would
// be a per-request spam loop.
package settingsoverlay

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
)

// DefaultPath is ~/.swarmery/overlays.json. An unresolvable home yields "",
// which Reader treats as "no descriptor" — the same silent repo-only fallback
// as a missing file.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".swarmery", "overlays.json")
}

// descriptor is the on-disk shape. Unknown keys are ignored so the file can
// grow without this reader rejecting it.
type descriptor struct {
	Overlays []struct {
		Name         string   `json:"name"`
		SettingsPath string   `json:"settingsPath"`
		Roots        []string `json:"roots"`
	} `json:"overlays"`
}

// entry is one validated descriptor row with paths already home-expanded and
// cleaned, so matching is pure string work on every request.
type entry struct {
	name         string
	settingsPath string
	roots        []string
}

// Reader answers "which declared settings files also apply to this project".
//
// The descriptor is cached by (mtime, size) rather than re-parsed per call, but
// the overlay settings files themselves are read fresh on every match: a
// launcher may rewrite them at any time, they are ~1KB, and only the overlays
// that actually cover the project are touched. Correctness over cleverness.
//
// Safe for concurrent use; the zero value is not usable — call New.
type Reader struct {
	path string

	mu      sync.Mutex
	loaded  bool
	modTime int64
	size    int64
	entries []entry
	// logged memoises problems already reported, keyed by a stable description
	// of the problem (kind + path + error text). A CHANGED error logs again; the
	// same one never repeats. Cleared whenever the descriptor is re-read, so a
	// fixed-then-broken-again file is reported again.
	logged map[string]bool
}

// New builds a Reader over the descriptor at path. An empty path falls back to
// DefaultPath(); an empty DefaultPath() (no resolvable home) leaves the Reader
// permanently overlay-free rather than erroring.
func New(path string) *Reader {
	if path == "" {
		path = DefaultPath()
	}
	return &Reader{path: expandHome(path), logged: map[string]bool{}}
}

// Path reports the descriptor location the Reader watches ("" = disabled).
func (r *Reader) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// For returns the overlays covering projectPath, in descriptor order, with each
// overlay's settings already parsed. The order IS the precedence order the
// caller must apply: later entries win (see projectscan.ReadPluginState).
//
// A nil Reader — the daemon started without overlays wired, or any test that
// never attached one — returns nil, which is exactly "repo-only detection".
// Every failure path below returns fewer overlays, never an error: the caller
// is rendering a project list and must not fail on one operator's typo.
func (r *Reader) For(projectPath string) []projectscan.SettingsOverlay {
	if r == nil || r.path == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reload()
	if len(r.entries) == 0 {
		return nil
	}

	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return nil
	}
	abs = filepath.Clean(abs)

	var out []projectscan.SettingsOverlay
	for _, e := range r.entries {
		if !underAnyRoot(abs, e.roots) {
			continue
		}
		s, serr := projectscan.ReadSettings(e.settingsPath)
		if serr != nil {
			// A dangling settingsPath is the single most likely descriptor rot
			// (the launcher checkout moved). Say so once, then behave exactly as
			// if the overlay were not declared.
			r.warn("settings", e.settingsPath, serr)
			continue
		}
		out = append(out, projectscan.SettingsOverlay{Name: e.name, Settings: s})
	}
	return out
}

// reload re-parses the descriptor when its (mtime, size) changed. Callers hold
// r.mu.
func (r *Reader) reload() {
	fi, err := os.Stat(r.path)
	if err != nil {
		// No descriptor at all is the NORMAL case for most installs — report it
		// only when it is something other than "not there", and only once.
		if !os.IsNotExist(err) {
			r.warn("descriptor", r.path, err)
		}
		r.loaded, r.entries = true, nil
		return
	}
	mod, size := fi.ModTime().UnixNano(), fi.Size()
	if r.loaded && mod == r.modTime && size == r.size {
		return
	}
	r.loaded, r.modTime, r.size = true, mod, size
	// The file changed: whatever we complained about last time may be fixed, so
	// let the memo forget and re-report if it is not.
	r.logged = map[string]bool{}
	r.entries = parse(r.path, r.warn)
}

// warn logs a problem once. Callers hold r.mu.
func (r *Reader) warn(kind, path string, err error) {
	key := kind + "\x00" + path + "\x00" + fmt.Sprint(err)
	if r.logged[key] {
		return
	}
	r.logged[key] = true
	log.Printf("settings overlays: %s %s: %v — falling back to repo-only plugin detection", kind, path, err)
}

// parse reads and validates the descriptor. Invalid rows are dropped
// individually: one malformed entry must not disable the operator's other,
// correct overlays.
func parse(path string, warn func(kind, path string, err error)) []entry {
	raw, err := os.ReadFile(path)
	if err != nil {
		warn("descriptor", path, err)
		return nil
	}
	var d descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		warn("descriptor", path, err)
		return nil
	}
	var out []entry
	for i, o := range d.Overlays {
		if strings.TrimSpace(o.SettingsPath) == "" {
			warn("descriptor", path, fmt.Errorf("overlays[%d] has no settingsPath", i))
			continue
		}
		roots := make([]string, 0, len(o.Roots))
		for _, root := range o.Roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			abs, aerr := filepath.Abs(expandHome(root))
			if aerr != nil {
				continue
			}
			roots = append(roots, filepath.Clean(abs))
		}
		if len(roots) == 0 {
			// An overlay with no roots covers nothing. Silently keeping it would
			// look identical to a working one from the outside.
			warn("descriptor", path, fmt.Errorf("overlays[%d] has no usable roots", i))
			continue
		}
		name := strings.TrimSpace(o.Name)
		if name == "" {
			// Provenance must never be blank — an unnamed overlay still has to be
			// identifiable in overlaySources.
			name = fmt.Sprintf("overlay-%d", i+1)
		}
		out = append(out, entry{name: name, settingsPath: expandHome(o.SettingsPath), roots: roots})
	}
	return out
}

// expandHome turns a leading "~" into the user's home directory. Anything else
// (including "~user") is returned untouched — this is a convenience for the
// hand-written descriptor, not a shell.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// underAnyRoot reports whether abs is one of roots or nested inside one. Both
// sides are already cleaned absolute paths; the check is lexical, matching
// projectscan's onboarding-root hint — an overlay only affects a read-only
// verdict, so no symlink resolution is warranted here.
func underAnyRoot(abs string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
