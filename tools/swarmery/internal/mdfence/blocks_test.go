package mdfence

import (
	"strings"
	"testing"
)

// Blocks is the complement of ForEachLine, and the two must not disagree about
// where a fence closes — that is the entire reason it lives in this package
// rather than in the one reader that wanted it (wsingest's `## Forecast`
// parser, whose payload IS the fenced yaml every other reader skips).
func TestBlocks(t *testing.T) {
	t.Run("info string and content", func(t *testing.T) {
		got := Blocks("intro\n```yaml\nkind: prior\n```\nouttro\n")
		if len(got) != 1 {
			t.Fatalf("blocks = %d, want 1", len(got))
		}
		if got[0].Info != "yaml" {
			t.Errorf("info = %q, want %q", got[0].Info, "yaml")
		}
		if got[0].Content != "kind: prior" {
			t.Errorf("content = %q, want the body without either fence line", got[0].Content)
		}
		if got[0].Start != 1 {
			t.Errorf("start = %d, want 1 (0-based index of the opening fence)", got[0].Start)
		}
	})

	t.Run("a longer fence quoting a shorter one is ONE block", func(t *testing.T) {
		got := Blocks("````markdown\n```yaml\nkind: prior\n```\n````\n")
		if len(got) != 1 {
			t.Fatalf("blocks = %d, want 1 — the inner ``` is content", len(got))
		}
		if got[0].Info != "markdown" {
			t.Errorf("info = %q, want %q", got[0].Info, "markdown")
		}
		if !strings.Contains(got[0].Content, "```yaml") {
			t.Errorf("content = %q, want the inner fence verbatim", got[0].Content)
		}
	})

	t.Run("two blocks in order", func(t *testing.T) {
		got := Blocks("```yaml\na\n```\ntext\n```\nb\n```\n")
		if len(got) != 2 {
			t.Fatalf("blocks = %d, want 2", len(got))
		}
		if got[0].Content != "a" || got[1].Content != "b" {
			t.Errorf("contents = %q, %q", got[0].Content, got[1].Content)
		}
		if got[1].Info != "" {
			t.Errorf("info = %q, want empty for a bare fence", got[1].Info)
		}
	})

	t.Run("no fences", func(t *testing.T) {
		if got := Blocks("just prose\nand more\n"); len(got) != 0 {
			t.Errorf("blocks = %+v, want none", got)
		}
	})

	// EndsOpen documents that ForEachLine treats everything after an unclosed
	// fence as fenced; Blocks has to agree, or a truncated document would be
	// invisible to both readers at once.
	t.Run("an unclosed fence runs to EOF", func(t *testing.T) {
		text := "```yaml\nkind: prior\nareas: [a]\n"
		if !EndsOpen(text) {
			t.Fatal("fixture is not actually unclosed")
		}
		got := Blocks(text)
		if len(got) != 1 || got[0].Content != "kind: prior\nareas: [a]\n" {
			t.Errorf("blocks = %+v, want one block holding both lines", got)
		}
	})

	// The partition property, on the shape that has bitten this codebase: a
	// checklist quoted inside a fence is skipped by one reader and held by the
	// other, never both and never neither.
	t.Run("agrees with ForEachLine about what is fenced", func(t *testing.T) {
		text := "- [ ] real\n```markdown\n- [ ] quoted\n```\n- [x] real too\n"
		ForEachLine(text, func(_ int, line string) {
			if strings.Contains(line, "quoted") {
				t.Fatalf("ForEachLine emitted a fenced line: %q", line)
			}
		})
		blocks := Blocks(text)
		if len(blocks) != 1 || !strings.Contains(blocks[0].Content, "quoted") {
			t.Fatalf("Blocks = %+v, want the quoted checklist", blocks)
		}
	})
}
