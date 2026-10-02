package wtjanitor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PluginRecords prunes Claude Code plugin install records that point at a
// worktree the janitor owns and that no longer exists.
//
// A phase run or an isolated subagent that installs or updates a plugin at
// project scope inside its worktree leaves a record keyed by that worktree's
// path in <config-dir>/plugins/installed_plugins.json. Removing the worktree
// does not remove the record, and `claude plugin uninstall --scope project`
// cannot clean it afterwards because it has to run from the (now missing)
// directory. Without this the file grows by one record per plugin per run.
//
// Only a record that passes BOTH checks is removed: its projectPath is agent
// owned (the same agentOwned rule the sweep uses, so an operator's checkout is
// never touched) and the directory is gone. A live worktree keeps its records.
type PluginRecords struct {
	// ConfigDirs lists the Claude config dirs to clean. nil means
	// DefaultConfigDirs.
	ConfigDirs func() []string
}

// DefaultConfigDirs returns $HOME/.claude and every $HOME/.claude-* account
// dir, the same set scripts/sync-cache.sh writes to: a project bound to
// another account records its installs under that account's dir.
func DefaultConfigDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dirs := []string{filepath.Join(home, ".claude")}
	more, _ := filepath.Glob(filepath.Join(home, ".claude-*"))
	for _, d := range more {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// installRecord is the one field the pruner reads; every other field of an
// entry is kept byte for byte through json.RawMessage.
type installRecord struct {
	ProjectPath string `json:"projectPath"`
}

// errChanged reports that the file moved under the pruner between read and
// write. The pass is abandoned; the next sweep retries.
var errChanged = errors.New("installed_plugins.json changed during the prune")

// Prune removes the stale records from every config dir and returns how many
// it removed (dryRun: how many it would remove). owned decides agent
// ownership of a projectPath. One unreadable or malformed file is reported
// and skipped; it never stops the others.
func (p PluginRecords) Prune(owned func(string) bool, dryRun bool) (int, error) {
	dirs := DefaultConfigDirs
	if p.ConfigDirs != nil {
		dirs = p.ConfigDirs
	}
	total := 0
	var errs []error
	for _, d := range dirs() {
		n, err := pruneFile(filepath.Join(d, "plugins", "installed_plugins.json"), owned, dryRun)
		total += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

// pruneFile handles one installed_plugins.json. A missing file is not an error:
// an account dir that never installed a plugin has none.
func pruneFile(path string, owned func(string) bool, dryRun bool) (int, error) {
	before, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	// Decode only as deep as needed so unknown top-level keys and unknown
	// entry fields survive untouched.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	pluginsRaw, ok := top["plugins"]
	if !ok {
		return 0, nil // not the shape this pruner knows; leave it alone
	}
	var plugins map[string][]json.RawMessage
	if err := json.Unmarshal(pluginsRaw, &plugins); err != nil {
		return 0, fmt.Errorf("parse %s plugins: %w", path, err)
	}

	removed := 0
	for id, entries := range plugins {
		keep := entries[:0:0]
		for _, e := range entries {
			var rec installRecord
			if json.Unmarshal(e, &rec) == nil && stale(rec.ProjectPath, owned) {
				removed++
				continue
			}
			keep = append(keep, e)
		}
		if len(keep) == 0 {
			delete(plugins, id)
		} else {
			plugins[id] = keep
		}
	}
	if removed == 0 || dryRun {
		return removed, nil
	}

	newPlugins, err := json.Marshal(plugins)
	if err != nil {
		return 0, fmt.Errorf("encode %s plugins: %w", path, err)
	}
	top["plugins"] = newPlugins
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("encode %s: %w", path, err)
	}
	out = append(out, '\n')

	if err := replaceIfUnchanged(path, before, raw, out); err != nil {
		return 0, err
	}
	return removed, nil
}

// stale reports whether a record's projectPath is an agent-owned directory
// that no longer exists. A user-scope record (no projectPath) is never stale.
func stale(projectPath string, owned func(string) bool) bool {
	if projectPath == "" || !owned(projectPath) {
		return false
	}
	_, err := os.Stat(projectPath)
	return errors.Is(err, os.ErrNotExist)
}

// replaceIfUnchanged writes out next to path and renames it over path, but only
// when path still has the bytes it had when it was read. Claude Code rewrites
// this file on every plugin install or update; a write that raced one of those
// would silently undo it.
func replaceIfUnchanged(path string, before os.FileInfo, read, out []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".installed_plugins-*.json")
	if err != nil {
		return fmt.Errorf("temp for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(before.Mode().Perm()); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}

	now, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("re-stat %s: %w", path, err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-read %s: %w", path, err)
	}
	if !now.ModTime().Equal(before.ModTime()) || now.Size() != before.Size() || !bytes.Equal(current, read) {
		return fmt.Errorf("%s: %w", path, errChanged)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
