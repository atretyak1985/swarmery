package improve

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoChange is the sentinel for the model's sanctioned "no justified
// change" answer: a well-formed two-section output whose diff block is empty.
var ErrNoChange = errors.New("model returned an empty diff (no justified change)")

// splitDiffRationale enforces the improvePrompt output contract on raw model
// stdout: a "## Diff" section containing EXACTLY ONE fenced ```diff block,
// followed by a non-empty "## Rationale" section. Leading chatter before
// "## Diff" is tolerated; any other deviation is an error (→ row status
// 'failed'). An empty (whitespace-only) diff block returns ErrNoChange.
func splitDiffRationale(out string) (diff, rationale string, err error) {
	diffIdx := sectionIndex(out, "## Diff")
	if diffIdx < 0 {
		return "", "", errors.New(`missing "## Diff" section`)
	}
	rest := out[diffIdx+len("## Diff"):]
	ratIdx := sectionIndex(rest, "## Rationale")
	if ratIdx < 0 {
		return "", "", errors.New(`missing "## Rationale" section`)
	}
	diffSection := rest[:ratIdx]
	rationale = strings.TrimSpace(rest[ratIdx+len("## Rationale"):])
	if rationale == "" {
		return "", "", errors.New(`empty "## Rationale" section`)
	}

	diff, err = extractDiffFence(diffSection)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(diff) == "" {
		return "", "", ErrNoChange
	}
	return diff, rationale, nil
}

// sectionIndex finds a markdown heading at the start of a line.
func sectionIndex(s, heading string) int {
	if strings.HasPrefix(s, heading) {
		return 0
	}
	if i := strings.Index(s, "\n"+heading); i >= 0 {
		return i + 1
	}
	return -1
}

// extractDiffFence pulls the body of the single ```diff fenced block out of
// the Diff section. No fence, more than one fence, or an unterminated fence
// is a contract violation.
func extractDiffFence(section string) (string, error) {
	const open = "```diff"
	first := strings.Index(section, open)
	if first < 0 {
		return "", errors.New("no fenced ```diff block in the Diff section")
	}
	body := section[first+len(open):]
	// The fence opener must end its line.
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		if strings.TrimSpace(body[:nl]) != "" {
			return "", fmt.Errorf("malformed ```diff fence opener: %q", open+body[:nl])
		}
		body = body[nl+1:]
	} else {
		return "", errors.New("unterminated ```diff fence")
	}
	closeIdx := strings.Index(body, "```")
	if closeIdx < 0 {
		return "", errors.New("unterminated ```diff fence")
	}
	after := body[closeIdx+3:]
	if strings.Contains(after, open) {
		return "", errors.New("more than one ```diff block in the Diff section")
	}
	return body[:closeIdx], nil
}
