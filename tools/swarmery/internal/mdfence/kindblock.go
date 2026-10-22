package mdfence

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// A phase doc carries machine-read yaml blocks told apart by a top-level `kind:`
// key: `prior` / `posterior` (the forecast, read by wsingest), `guard` (the
// planner's protected paths and stop triggers) and `review` (one block per review
// round). The forecast parser is tolerant by contract — a forecast is a guess and
// a broken one is a lint. Guard and review blocks are the opposite: later stages
// gate on them, so a block that exists but cannot be read is reported as
// unparsable, with the doc line and the reason, never folded into "absent".

// ParseError kinds. Absent and unparsable are different observations and every
// consumer must be able to tell them apart: "the planner wrote no guard" is a
// plan written before the format existed; "the planner wrote a guard nobody can
// read" is a defect someone has to fix.
const (
	ErrAbsent     = "absent"
	ErrUnparsable = "unparsable"
)

// ParseError is why a kind block could not be used. Line is the 1-based line in
// the WHOLE document (0 for an absent block); Reason is the operator's sentence.
type ParseError struct {
	Kind   string
	Line   int
	Reason string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s (line %d): %s", e.Kind, e.Line, e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Reason)
}

// kindLineRe matches a top-level `kind: <value>` line — no indentation, so a
// nested `kind:` inside a finding's map never identifies a block.
var kindLineRe = regexp.MustCompile(`^kind:\s*["']?([A-Za-z_-]+)["']?\s*(#.*)?$`)

// kindBlocks returns the ```yaml / ```yml blocks of doc whose top-level kind is
// kind, in document order. The kind is read by a line match, NOT by decoding the
// yaml, so a block that is broken yaml is still found — and reported as
// unparsable instead of vanishing into "absent".
//
// Bare ``` blocks are skipped on purpose: a phase doc's copy-paste agent prompt
// is one, and a `kind: guard` line quoted there describes the format, it is not
// this phase's guard.
func kindBlocks(doc, kind string) []Block {
	var out []Block
	for _, b := range Blocks(doc) {
		info := strings.ToLower(b.Info)
		if i := strings.IndexAny(info, " \t{"); i >= 0 {
			info = info[:i] // ```yaml {title=…} still names yaml
		}
		if info != "yaml" && info != "yml" {
			continue
		}
		for _, line := range strings.Split(b.Content, "\n") {
			if m := kindLineRe.FindStringSubmatch(strings.TrimRight(line, " \t\r")); m != nil {
				if strings.EqualFold(m[1], kind) {
					out = append(out, b)
				}
				break // the first top-level kind line decides
			}
		}
	}
	return out
}

// docLine converts a 1-based line inside block b's content to a 1-based line in
// the whole document: the content's first line sits right after the opening
// fence, whose 0-based index is b.Start.
func docLine(b Block, contentLine int) int {
	if contentLine < 1 {
		contentLine = 1
	}
	return b.Start + 1 + contentLine
}

// yamlLineRe pulls the line number out of a yaml.v3 syntax error
// ("yaml: line 3: did not find expected …").
var yamlLineRe = regexp.MustCompile(`line (\d+):`)

// decodeMapping parses a block body into its top-level mapping node. A syntax
// error, an empty body or a body that is not a mapping is an unparsable
// ParseError located in the document.
func decodeMapping(b Block) (*yaml.Node, *ParseError) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(b.Content), &root); err != nil {
		line := 1
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, line),
			Reason: "not valid yaml: " + strings.TrimPrefix(err.Error(), "yaml: ")}
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, &ParseError{Kind: ErrUnparsable, Line: docLine(b, 1), Reason: "the block is not a yaml mapping"}
	}
	return root.Content[0], nil
}

// mappingKeys indexes a mapping node's values by key. A duplicated key keeps the
// first value; yaml.v3 itself rejects duplicates at decode time.
func mappingKeys(m *yaml.Node) map[string]*yaml.Node {
	out := make(map[string]*yaml.Node, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if _, seen := out[m.Content[i].Value]; !seen {
			out[m.Content[i].Value] = m.Content[i+1]
		}
	}
	return out
}

// isNull reports a missing value or an explicit yaml null.
func isNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// scalar returns a scalar node's trimmed text, or ok=false for any other node.
func scalar(n *yaml.Node) (string, bool) {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return "", false
	}
	return strings.TrimSpace(n.Value), true
}

// stringList reads a list of non-empty scalars. A bare scalar is accepted as a
// one-element list and an explicit null as an empty one; a nested map or list
// entry, or an empty entry, is an error naming the entry's line.
func stringList(b Block, key string, n *yaml.Node) ([]string, *ParseError) {
	if isNull(n) {
		return nil, nil
	}
	bad := func(at *yaml.Node, why string) *ParseError {
		return &ParseError{Kind: ErrUnparsable, Line: docLine(b, at.Line), Reason: fmt.Sprintf("%s: %s", key, why)}
	}
	switch n.Kind {
	case yaml.ScalarNode:
		s, _ := scalar(n)
		if s == "" {
			return nil, nil
		}
		return []string{s}, nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			s, ok := scalar(c)
			if !ok {
				return nil, bad(c, "every entry must be a plain string")
			}
			if s == "" {
				return nil, bad(c, "empty entry")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, bad(n, "must be a list of strings")
}
