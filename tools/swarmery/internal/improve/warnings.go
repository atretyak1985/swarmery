package improve

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/gitstat"
)

// Always-on net-growth warning (harness² phase 3, the SC-8 half that lives in
// the apply gate).
//
// An agent definition or a CLAUDE.md is read on EVERY run, so each line it
// gains is paid on every run after it. The prompt's cut-to-pay rule asks the
// model to remove at least as many always-on lines as it adds or justify the
// growth in one sentence; this check makes a net growth visible to the
// operator. It is a WARNING, never a gate: it lands in warnings_json and the
// apply proceeds.

// isAlwaysOnFile reports whether a repo-relative path is always-on text: an
// agent definition (plugins/*/agents/*.md) or any CLAUDE.md.
func isAlwaysOnFile(p string) bool {
	return isAgentFile(p) || path.Base(p) == "CLAUDE.md"
}

// alwaysOnGrowthWarning returns the warning text when an always-on file gains
// more lines than it loses, or "" otherwise.
func alwaysOnGrowthWarning(p string, added, removed int) string {
	if !isAlwaysOnFile(p) || added <= removed {
		return ""
	}
	return fmt.Sprintf("always-on growth: %s gains %d lines net (+%d −%d); "+
		"always-on text is read on every run — check the rationale justifies it",
		p, added-removed, added, removed)
}

// alwaysOnGrowthWarnings applies alwaysOnGrowthWarning to every file of a
// numstat, in order.
func alwaysOnGrowthWarnings(files []gitstat.FileStat) []string {
	var out []string
	for _, f := range files {
		if w := alwaysOnGrowthWarning(f.Path, f.Added, f.Removed); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// diffLineCounts counts added and removed lines of a unified diff body,
// skipping the ---/+++ file headers. Generation uses it to warn before the
// operator approves; the apply gate re-measures the staged tree with numstat.
func diffLineCounts(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

// marshalJSONList renders a slice as a JSON array cell; nil or empty is "[]".
func marshalJSONList[T any](v []T) string {
	if len(v) == 0 {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil { // slices of strings / string structs cannot fail
		return "[]"
	}
	return string(b)
}
