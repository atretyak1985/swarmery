package sysscan

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// errNoFrontmatter marks a file whose first line is not `---` — per
// docs/system-config-format.md §1.1/§4 such files (README.md, helper notes)
// are NOT registrable items and are skipped silently, unlike files that
// *start* frontmatter but fail to parse (those are saved with a parse_error
// lint finding).
var errNoFrontmatter = errors.New("first line is not ---")

// parseFrontmatter tolerantly extracts the YAML frontmatter block of a
// markdown component file. Contract (format doc §1.3 — all gotchas observed
// in the real corpus must parse):
//
//   - comments between keys, folded scalars (`description: >`), block lists
//     with duplicate items, missing keys — handled by the YAML parser, never
//     by line-oriented string surgery;
//   - a missing closing `---` or invalid YAML returns (nil, err) — the CALLER
//     keeps the item and records a config_lint_findings parse_error row;
//   - every field is optional; type mismatches are the caller's problem
//     (use strField for tolerant extraction).
func parseFrontmatter(content []byte) (map[string]any, error) {
	block, _, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if err := yaml.Unmarshal(block, &fields); err != nil {
		return nil, fmt.Errorf("invalid YAML frontmatter: %v", err)
	}
	return fields, nil
}

// splitFrontmatter locates the raw YAML frontmatter block and the markdown
// body after the closing delimiter. Shared by parseFrontmatter (fields) and
// the step-04 linter (body rules, e.g. agent_no_boundaries). The error
// contract mirrors parseFrontmatter's: errNoFrontmatter for helper files,
// a described error for an unterminated block.
func splitFrontmatter(content []byte) (block, body []byte, err error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf")) // strip UTF-8 BOM
	if !isFrontmatterStart(content) {
		return nil, nil, errNoFrontmatter
	}
	// Skip the opening `---` line.
	nl := bytes.IndexByte(content, '\n')
	if nl < 0 {
		return nil, nil, fmt.Errorf("frontmatter opened but file ends on the first line")
	}
	rest := content[nl+1:]

	// Find the closing delimiter line: `---` (or the YAML document-end `...`).
	for off := 0; off <= len(rest); {
		end := bytes.IndexByte(rest[off:], '\n')
		var line []byte
		if end < 0 {
			line = rest[off:]
			end = len(rest) - off
		} else {
			line = rest[off : off+end]
		}
		t := strings.TrimRight(string(line), "\r \t")
		if t == "---" || t == "..." {
			bodyStart := off + end + 1
			if bodyStart > len(rest) {
				bodyStart = len(rest) // delimiter is the last line, no trailing newline
			}
			return rest[:off], rest[bodyStart:], nil
		}
		off += end + 1
		if off > len(rest) {
			break
		}
	}
	return nil, nil, fmt.Errorf("unterminated frontmatter (no closing ---)")
}

// isFrontmatterStart reports whether the first line of content is exactly
// `---` (trailing CR/space tolerated).
func isFrontmatterStart(content []byte) bool {
	nl := bytes.IndexByte(content, '\n')
	first := content
	if nl >= 0 {
		first = content[:nl]
	}
	return strings.TrimRight(string(first), "\r \t") == "---"
}

// strField extracts a scalar frontmatter field as a trimmed string; missing
// keys yield "" and non-string scalars are rendered (format doc: every field
// is optional, types drift in the wild).
func strField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
