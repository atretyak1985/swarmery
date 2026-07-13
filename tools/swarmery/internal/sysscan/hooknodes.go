package sysscan

// Hook-node parsing shared between the scanner (registry.go) and the Stage 2
// hooks editor (internal/sysedit, step-10). ParseHookNodes is the ONE place
// that defines the (event, seq) identity of settings-hooks entries — the
// editor locates its surgical target with the exact same flattening semantics
// the scanner used to index the row.
//
// Disable mechanism (step-10, format doc §3.3/§3.3.1): Claude Code has no
// native per-hook disable, so a disabled entry is MOVED out of hooks[event]
// into the service top-level key "_swarmery_disabled_hooks" — CC treats
// unknown top-level settings keys as data (step-01 forward-compat stance,
// same tolerance the overlay snippets' "//" comment-key convention relies
// on). Each record keeps the verbatim entry plus enough position context
// (event, matcher, groupIndex, hookIndex) for an exact re-enable roundtrip.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DisabledHooksKey is the service top-level settings key that carries hook
// entries disabled through the System UI. Underscore prefix signals "foreign
// tool state" to human readers; Claude Code ignores the key (unknown keys are
// data, not errors).
const DisabledHooksKey = "_swarmery_disabled_hooks"

// HookNode is one hook command entry of a settings file — active (inside
// hooks[event]) or disabled (a DisabledHooksKey record). Seq is the row
// identity within its source file: active entries flatten to 0..n-1 per event
// (scan order), disabled entries of that event continue at n in record order,
// so ORDER BY event, seq stays a total order.
type HookNode struct {
	Event         string
	Seq           int
	Matcher       *string // nil = absent in JSON ("" is a real observed value)
	Command       string
	Timeout       *int64
	StatusMessage *string
	Managed       bool // command carries the "swarmery hook" installer marker
	Enabled       bool
	Hash          string // row-level content_hash (includes a disabled marker)

	// Surgical-edit location. Active entries: hooks[Event][GroupIdx] is the
	// matcher group, .hooks[HookIdx] the entry (DisabledIdx = -1). Disabled
	// entries: DisabledHooksKey[DisabledIdx] is the record (Group/HookIdx = -1).
	GroupIdx    int
	HookIdx     int
	DisabledIdx int
}

// ParseHookNodes extracts every hook node from one settings file. The walk is
// hookcfg-style defensive map traversal: odd shapes degrade to skipped
// entries (reported through warn, which may be nil), only invalid JSON is an
// error. Unknown event names are data, not errors (format doc §3.5).
func ParseHookNodes(raw []byte, warn func(string, ...any), path string) ([]HookNode, error) {
	if warn == nil {
		warn = func(string, ...any) {}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	hooks, _ := root["hooks"].(map[string]any)

	// Disabled records, grouped by their original event.
	disabled := map[string][]HookNode{}
	for i, r := range sliceOf(root[DisabledHooksKey]) {
		rec, ok := r.(map[string]any)
		if !ok {
			warn("hooks %s: %s[%d] is not an object — skipped", path, DisabledHooksKey, i)
			continue
		}
		event, _ := rec["event"].(string)
		entry, _ := rec["hook"].(map[string]any)
		n := nodeFromEntry(event, entry)
		if event == "" || n.Command == "" {
			warn("hooks %s: %s[%d] without event/hook.command — skipped", path, DisabledHooksKey, i)
			continue
		}
		if mv, ok := rec["matcher"].(string); ok {
			n.Matcher = &mv
		}
		n.Enabled = false
		n.GroupIdx, n.HookIdx, n.DisabledIdx = -1, -1, i
		disabled[event] = append(disabled[event], n)
	}

	// Sorted union of active and disabled event names.
	eventSet := map[string]bool{}
	for ev := range hooks {
		eventSet[ev] = true
	}
	for ev := range disabled {
		eventSet[ev] = true
	}
	events := make([]string, 0, len(eventSet))
	for ev := range eventSet {
		events = append(events, ev)
	}
	sort.Strings(events)

	var out []HookNode
	for _, ev := range events {
		seq := 0
		for gi, g := range sliceOf(hooks[ev]) {
			group, ok := g.(map[string]any)
			if !ok {
				continue
			}
			var matcher *string
			if mv, ok := group["matcher"].(string); ok {
				matcher = &mv
			}
			for hi, h := range sliceOf(group["hooks"]) {
				entry, ok := h.(map[string]any)
				if !ok {
					continue
				}
				n := nodeFromEntry(ev, entry)
				if n.Command == "" {
					warn("hooks %s: %s entry without a command — skipped", path, ev)
					continue
				}
				n.Matcher = matcher
				n.Enabled = true
				n.Seq = seq
				n.GroupIdx, n.HookIdx, n.DisabledIdx = gi, hi, -1
				n.Hash = nodeHash(n)
				out = append(out, n)
				seq++
			}
		}
		for _, n := range disabled[ev] {
			n.Seq = seq
			n.Hash = nodeHash(n)
			out = append(out, n)
			seq++
		}
	}
	return out, nil
}

// nodeFromEntry maps one raw entry object onto a HookNode (fields only — the
// caller sets matcher/position/enabled and computes the hash).
func nodeFromEntry(event string, entry map[string]any) HookNode {
	n := HookNode{Event: event}
	n.Command, _ = entry["command"].(string)
	if tv, ok := entry["timeout"].(float64); ok {
		t := int64(tv)
		n.Timeout = &t
	}
	if sm, ok := entry["statusMessage"].(string); ok {
		n.StatusMessage = &sm
	}
	n.Managed = strings.Contains(n.Command, hookcfgMarker)
	return n
}

// nodeHash renders one node's identity hash (the hooks row content_hash).
// Active entries keep the exact pre-step-10 hookHash input (no row churn on
// upgrade); disabled entries append a marker so an entry never hashes the
// same active vs disabled.
func nodeHash(n HookNode) string {
	matcher := "\x00absent"
	if n.Matcher != nil {
		matcher = *n.Matcher
	}
	timeout := "\x00absent"
	if n.Timeout != nil {
		timeout = fmt.Sprint(*n.Timeout)
	}
	status := "\x00absent"
	if n.StatusMessage != nil {
		status = *n.StatusMessage
	}
	parts := []string{n.Event, fmt.Sprint(n.Seq), matcher, n.Command, timeout, status}
	if !n.Enabled {
		parts = append(parts, "\x00disabled")
	}
	return sha256Hex([]byte(strings.Join(parts, "\n")))
}
