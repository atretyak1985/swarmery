package sysedit

// Step-10: hooks toggle/edit — the ONLY code path that modifies settings.json
// / settings.local.json (the hookcfg CLI install/uninstall path stays separate
// and untouched). Same pipeline discipline as WriteFile:
//
//	kill-switch → DB-resolved source_file fenced into known roots →
//	managed='swarmery' refusal → parse (abort on invalid JSON, never
//	overwrite) → 409 against the DISK entry hash → mutate ONE node →
//	canonical serialize → backup → atomic write → forced rescan.
//
// Disable mechanism (no native per-hook disable exists — format doc §3.3):
// the entry is MOVED into the service top-level "_swarmery_disabled_hooks"
// key (sysscan.DisabledHooksKey) with its original event/matcher/position;
// enable is the exact reverse move. Serialization is the canonical stdlib
// form hookcfg has always written (json.MarshalIndent, 2-space indent, sorted
// object keys, trailing newline): a canonical-form file roundtrips
// byte-for-byte; a non-canonical file is normalized (semantically identical)
// on its first edit, after which every edit is byte-surgical.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// ErrHookManaged guards swarmery's own data-collection hooks from the UI —
// one click must never cut off the approvals channel. The API maps it to 403.
var ErrHookManaged = errors.New("sysedit: hook is managed by the swarmery installer — manage it via `swarmery hooks`")

// ToggleHook enables or disables one settings hook entry. Disable moves the
// entry into the DisabledHooksKey section; enable moves it back to its
// recorded position. A no-op toggle (already in the requested state) succeeds
// without touching the file.
func (e *Editor) ToggleHook(id int64, enabled bool, baseHash string) error {
	return e.editHooksFile(id, baseHash, func(root map[string]any, n sysscan.HookNode) (bool, error) {
		if n.Enabled == enabled {
			return false, nil
		}
		if enabled {
			return true, enableNode(root, n)
		}
		return true, disableNode(root, n)
	})
}

// UpdateHook replaces the editable fields of one hook entry: command
// (required) and timeout (nil removes the key — full-replace semantics).
// Every other entry field (type, statusMessage, unknown keys) is untouched.
func (e *Editor) UpdateHook(id int64, command string, timeout *int64, baseHash string) error {
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("sysedit: hook command must not be empty")
	}
	return e.editHooksFile(id, baseHash, func(root map[string]any, n sysscan.HookNode) (bool, error) {
		entry, err := entryMap(root, n)
		if err != nil {
			return false, err
		}
		entry["command"] = command
		if timeout != nil {
			entry["timeout"] = *timeout
		} else {
			delete(entry, "timeout")
		}
		return true, nil
	})
}

// editHooksFile runs the shared surgical-edit pipeline around one mutation.
// mutate reports whether it changed anything (false = clean no-op).
func (e *Editor) editHooksFile(id int64, baseHash string,
	mutate func(root map[string]any, n sysscan.HookNode) (bool, error)) error {
	if readonly() {
		return ErrReadOnly
	}

	// Resolve the row: the id is the only API input — the file path comes
	// from the DB, then is fenced into known config roots.
	var sourceFile, event string
	var seq, enabled int
	var managed sql.NullString
	err := e.db.QueryRow(
		`SELECT source_file, event, seq, enabled, managed FROM hooks WHERE id = ?`, id).
		Scan(&sourceFile, &event, &seq, &enabled, &managed)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("hook:%d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("sysedit: resolve hook:%d: %w", id, err)
	}
	if managed.Valid && managed.String == "swarmery" {
		return fmt.Errorf("hook:%d: %w", id, ErrHookManaged)
	}
	path := filepath.Clean(sourceFile)
	roots, err := e.roots()
	if err != nil {
		return err
	}
	if !underAny(path, roots) {
		return fmt.Errorf("hook:%d at %s: %w", id, path, ErrPathOutsideRoots)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("sysedit: read %s: %w", path, err)
	}
	nodes, err := sysscan.ParseHookNodes(raw, nil, path)
	if err != nil {
		return fmt.Errorf("sysedit: %s is not valid JSON (%v) — aborting without writing", path, err)
	}

	// Locate the entry on DISK (never trust the possibly-stale DB copy) and
	// verify the caller's baseHash against disk truth — 409 on any drift.
	var node *sysscan.HookNode
	for i := range nodes {
		if nodes[i].Event == event && nodes[i].Seq == seq && nodes[i].Enabled == (enabled == 1) {
			node = &nodes[i]
			break
		}
	}
	if node == nil {
		return &ConflictError{BaseHash: baseHash} // entry moved or vanished under the caller
	}
	if node.Hash != baseHash {
		return &ConflictError{DiskHash: node.Hash, BaseHash: baseHash}
	}
	if node.Managed { // defense-in-depth: disk truth beats a stale DB row
		return fmt.Errorf("hook:%d: %w", id, ErrHookManaged)
	}

	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("sysedit: parse %s: %w", path, err) // unreachable after ParseHookNodes
	}
	changed, err := mutate(root, *node)
	if err != nil {
		return fmt.Errorf("sysedit: hook:%d in %s: %w", id, path, err)
	}
	if !changed {
		return nil
	}

	// Canonical serialization (hookcfg.writeSettings shape): 2-space indent,
	// stdlib map key ordering (sorted), trailing newline.
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if bytes.Equal(out, raw) {
		return nil
	}

	if _, err := e.backupFile(path); err != nil {
		return fmt.Errorf("sysedit: backup %s: %w", path, err)
	}
	if err := e.atomicWrite(path, out); err != nil {
		return err
	}
	if _, err := e.scanner.Scan(); err != nil {
		return fmt.Errorf("sysedit: %s written but rescan failed: %w", path, err)
	}
	return nil
}

// disableNode moves one ACTIVE entry out of hooks[event] into the
// DisabledHooksKey section, recording event/matcher/position for the exact
// reverse move. Containers emptied by the removal are dropped (hookcfg
// stripOurs discipline).
func disableNode(root map[string]any, n sysscan.HookNode) error {
	hooks, _ := root["hooks"].(map[string]any)
	groups := anySlice(hooks[n.Event])
	if n.GroupIdx < 0 || n.GroupIdx >= len(groups) {
		return fmt.Errorf("group %d not found under %q", n.GroupIdx, n.Event)
	}
	group, ok := groups[n.GroupIdx].(map[string]any)
	entries := anySlice(group["hooks"])
	if !ok || n.HookIdx < 0 || n.HookIdx >= len(entries) {
		return fmt.Errorf("entry %d/%d not found under %q", n.GroupIdx, n.HookIdx, n.Event)
	}
	entry := entries[n.HookIdx]

	rec := map[string]any{
		"event":      n.Event,
		"hook":       entry,
		"groupIndex": n.GroupIdx,
		"hookIndex":  n.HookIdx,
	}
	if n.Matcher != nil {
		rec["matcher"] = *n.Matcher
	}
	root[sysscan.DisabledHooksKey] = append(anySlice(root[sysscan.DisabledHooksKey]), rec)

	entries = append(entries[:n.HookIdx], entries[n.HookIdx+1:]...)
	if len(entries) > 0 {
		group["hooks"] = entries
		return nil
	}
	groups = append(groups[:n.GroupIdx], groups[n.GroupIdx+1:]...)
	if len(groups) > 0 {
		hooks[n.Event] = groups
	} else {
		delete(hooks, n.Event)
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	}
	return nil
}

// enableNode moves one DISABLED record back into hooks[event]. The original
// group is preferred when it is still at the recorded index with the same
// matcher; otherwise a group is recreated at that position (indices clamp to
// current bounds — the 409 baseHash gate keeps drift out anyway).
func enableNode(root map[string]any, n sysscan.HookNode) error {
	recs := anySlice(root[sysscan.DisabledHooksKey])
	if n.DisabledIdx < 0 || n.DisabledIdx >= len(recs) {
		return fmt.Errorf("disabled record %d not found", n.DisabledIdx)
	}
	rec, ok := recs[n.DisabledIdx].(map[string]any)
	if !ok {
		return fmt.Errorf("disabled record %d is not an object", n.DisabledIdx)
	}
	entry := rec["hook"]
	gi, hi := intField(rec, "groupIndex"), intField(rec, "hookIndex")
	matcher, hasMatcher := rec["matcher"].(string)

	recs = append(recs[:n.DisabledIdx], recs[n.DisabledIdx+1:]...)
	if len(recs) > 0 {
		root[sysscan.DisabledHooksKey] = recs
	} else {
		delete(root, sysscan.DisabledHooksKey)
	}

	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	groups := anySlice(hooks[n.Event])

	if gi >= 0 && gi < len(groups) {
		if group, ok := groups[gi].(map[string]any); ok && matcherEqual(group, matcher, hasMatcher) {
			entries := anySlice(group["hooks"])
			group["hooks"] = insertAt(entries, clamp(hi, len(entries)), entry)
			return nil
		}
	}
	newGroup := map[string]any{"hooks": []any{entry}}
	if hasMatcher {
		newGroup["matcher"] = matcher
	}
	hooks[n.Event] = insertAt(groups, clamp(gi, len(groups)), newGroup)
	return nil
}

// entryMap navigates to the raw entry object of one node (active or disabled)
// for in-place field edits.
func entryMap(root map[string]any, n sysscan.HookNode) (map[string]any, error) {
	if n.Enabled {
		hooks, _ := root["hooks"].(map[string]any)
		groups := anySlice(hooks[n.Event])
		if n.GroupIdx < 0 || n.GroupIdx >= len(groups) {
			return nil, fmt.Errorf("group %d not found under %q", n.GroupIdx, n.Event)
		}
		group, _ := groups[n.GroupIdx].(map[string]any)
		entries := anySlice(group["hooks"])
		if n.HookIdx < 0 || n.HookIdx >= len(entries) {
			return nil, fmt.Errorf("entry %d/%d not found under %q", n.GroupIdx, n.HookIdx, n.Event)
		}
		entry, ok := entries[n.HookIdx].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %d/%d under %q is not an object", n.GroupIdx, n.HookIdx, n.Event)
		}
		return entry, nil
	}
	recs := anySlice(root[sysscan.DisabledHooksKey])
	if n.DisabledIdx < 0 || n.DisabledIdx >= len(recs) {
		return nil, fmt.Errorf("disabled record %d not found", n.DisabledIdx)
	}
	rec, _ := recs[n.DisabledIdx].(map[string]any)
	entry, ok := rec["hook"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("disabled record %d has no hook object", n.DisabledIdx)
	}
	return entry, nil
}

// matcherEqual reports whether a group's matcher matches the recorded one,
// distinguishing an absent matcher from an empty-string matcher.
func matcherEqual(group map[string]any, matcher string, hasMatcher bool) bool {
	gv, ok := group["matcher"].(string)
	if !hasMatcher {
		return !ok
	}
	return ok && gv == matcher
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// intField reads a JSON number field as int (json.Unmarshal yields float64).
func intField(m map[string]any, key string) int {
	f, _ := m[key].(float64)
	return int(f)
}

func clamp(i, max int) int {
	if i < 0 {
		return 0
	}
	if i > max {
		return max
	}
	return i
}

func insertAt(s []any, i int, v any) []any {
	s = append(s, nil)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}
