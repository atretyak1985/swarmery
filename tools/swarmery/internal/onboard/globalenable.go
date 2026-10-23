package onboard

// Capture/restore of ONE enabledPlugins key in the user-level settings.json.
//
// Why this exists: for a consumer whose <project>/.claude is a symlinked
// overlay (the multi-repo pattern in CLAUDE.md / EXTENDING.md), a `--scope
// project` install cannot run at all — the claude CLI refuses to write settings
// through a symlinked directory (SymlinkWriteRefusedError). The only install
// the CLI will perform for such a project is the default `--scope user` one.
//
// That fallback works for drift purposes: plugindrift.resolveFor counts any
// Scope=="user" entry as available to EVERY project, so a user-scope install
// clears plugin_enabled_not_installed for the symlinked consumer. But the CLI
// also sets enabledPlugins["<id>"]=true in the USER settings.json as a side
// effect, which would silently switch the pack on for every other project on
// the machine.
//
// Verified against claude CLI 2.1.220: deleting that key afterwards leaves the
// entry in `plugin list --json` as scope=user, enabled=false — still installed,
// still drift-resolving, no longer globally enabled. resolveFor reads Scope and
// never reads Enabled, so the undo is genuine rather than cosmetic. That
// measurement is what makes this file correct; if a future CLI drops
// un-enabled plugins from `plugin list --json`, the fallback silently stops
// resolving drift and this comment is the place to start.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SymlinkedClaudeDir reports whether <projectPath>/.claude is itself a symlink
// — the multi-repo consumer overlay pattern (a shared agents/ repo with each
// consuming repo's .claude symlinked into it; see CLAUDE.md's Self-hosting
// section and EXTENDING.md). It is the condition that selects the user-scope
// fallback this file exists to make safe, so it lives next to it: both the
// repair endpoint and the provision engine ask the same question, and two
// copies of the test is how they drift apart.
//
// Lstat (not Stat) so the symlink itself is inspected rather than followed; any
// error (missing/unreadable) reports false so the normal project-scope path
// still runs and surfaces its own error.
func SymlinkedClaudeDir(projectPath string) bool {
	fi, err := os.Lstat(filepath.Join(projectPath, ".claude"))
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// ErrGlobalSettingsUnreadable — the user settings.json exists but cannot be
// read or parsed. The caller must NOT proceed with a user-scope install: the
// global enable it causes could not be reverted afterwards.
var ErrGlobalSettingsUnreadable = errors.New("user settings.json unreadable")

// GlobalEnable is the pre-install state of one enabledPlugins key.
type GlobalEnable struct {
	path    string
	key     string
	present bool
	value   any
}

// CaptureGlobalEnable records the current value of enabledPlugins[pluginID] in
// <claudeDir>/settings.json so a later Restore can put it back verbatim.
//
// A missing settings.json is fine — the key is simply absent, and Restore will
// delete it from whatever the CLI creates. Anything else (unreadable file,
// malformed JSON, enabledPlugins of an unexpected shape) is an error, because
// a snapshot that cannot be trusted must stop the install rather than risk an
// irreversible global enable.
func CaptureGlobalEnable(claudeDir, pluginID string) (*GlobalEnable, error) {
	sPath := filepath.Join(claudeDir, "settings.json")
	g := &GlobalEnable{path: sPath, key: pluginID}

	raw, err := os.ReadFile(sPath)
	if os.IsNotExist(err) {
		return g, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGlobalSettingsUnreadable, err)
	}
	settings := map[string]any{}
	if uerr := json.Unmarshal(raw, &settings); uerr != nil {
		return nil, fmt.Errorf("%w: %v", ErrGlobalSettingsUnreadable, uerr)
	}
	ep, ok := settings["enabledPlugins"]
	if !ok {
		return g, nil
	}
	epMap, ok := ep.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: enabledPlugins has an unexpected shape", ErrGlobalSettingsUnreadable)
	}
	g.value, g.present = epMap[pluginID]
	return g, nil
}

// Restore puts enabledPlugins[key] back to its captured state: deleted when it
// was absent, set to the captured value when it was present.
//
// It deliberately re-reads settings.json instead of writing back the whole
// captured document. The daemon's own provision engine takes this same fallback
// for a symlinked consumer (provision/service.go install), so this file has
// concurrent writers; a narrow read-modify-write touching one key preserves
// whatever else landed in the meantime. The race window is not zero — a
// competing write between this read and this write is lost — but it is
// milliseconds wide and bounded to a single key, whereas restoring a whole
// snapshot would clobber every concurrent change unconditionally.
//
// A no-op (the key already holds the captured state) writes nothing, so the
// common case leaves no backup file and no mtime churn.
func (g *GlobalEnable) Restore() error {
	raw, err := os.ReadFile(g.path)
	if os.IsNotExist(err) {
		// Nothing was created; nothing to revert.
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGlobalSettingsUnreadable, err)
	}
	settings := map[string]any{}
	if uerr := json.Unmarshal(raw, &settings); uerr != nil {
		return fmt.Errorf("%w: %v", ErrGlobalSettingsUnreadable, uerr)
	}

	epMap, _ := settings["enabledPlugins"].(map[string]any)
	if epMap == nil {
		if !g.present {
			return nil // absent before, absent now
		}
		epMap = map[string]any{}
		settings["enabledPlugins"] = epMap
	}

	cur, curPresent := epMap[g.key]
	switch {
	case g.present && curPresent && cur == g.value:
		return nil
	case !g.present && !curPresent:
		return nil
	case g.present:
		epMap[g.key] = g.value
	default:
		delete(epMap, g.key)
	}

	if err := writeBackup(g.path, raw); err != nil {
		return err
	}
	return writeJSON(g.path, settings)
}
