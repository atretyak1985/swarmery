package improve

import "strings"

// frontmatterOK mirrors the CI agent-frontmatter gate
// (.github/workflows/ci.yml): the file's first line must be exactly `---` and
// both `name:` and `description:` must appear (as line prefixes) within the
// first 15 lines. Implemented in Go — the daemon never shells to yq/python.
func frontmatterOK(content string) bool {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	head := lines
	if len(head) > 15 {
		head = head[:15]
	}
	hasName, hasDesc := false, false
	for _, l := range head {
		if strings.HasPrefix(l, "name:") {
			hasName = true
		}
		if strings.HasPrefix(l, "description:") {
			hasDesc = true
		}
	}
	return hasName && hasDesc
}
