package improve

import (
	"strings"
	"testing"
)

func TestFrontmatterOK(t *testing.T) {
	valid := "---\nname: tech-lead\ndescription: does things\n---\nbody\n"
	if !frontmatterOK(valid) {
		t.Error("valid frontmatter rejected")
	}

	missingName := "---\ndescription: does things\n---\nbody\n"
	if frontmatterOK(missingName) {
		t.Error("frontmatter missing name accepted")
	}

	missingDesc := "---\nname: tech-lead\n---\nbody\n"
	if frontmatterOK(missingDesc) {
		t.Error("frontmatter missing description accepted")
	}

	noOpener := "name: tech-lead\ndescription: x\n"
	if frontmatterOK(noOpener) {
		t.Error("frontmatter without leading --- accepted")
	}

	// name pushed to line 16 (index 15) — outside the first-15-lines window.
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("description: x\n")
	for i := 0; i < 13; i++ { // lines 3..15 filler
		b.WriteString("# pad\n")
	}
	b.WriteString("name: tech-lead\n") // this is line 16 (1-based)
	if frontmatterOK(b.String()) {
		t.Error("name at line 16 accepted (must be within first 15 lines)")
	}
}
