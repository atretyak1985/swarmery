package wsingest

import (
	"reflect"
	"testing"
)

func TestSection(t *testing.T) {
	doc := "# Phase 1 — X\n" +
		"Status: Done\n\n" +
		"## Goal\n" +
		"Ship the thing.\n\n" +
		"Second paragraph.\n" +
		"### Detail\n" +
		"still the goal\n" +
		"## Files\n" +
		"- a.go\n" +
		"## Agent Prompt\n" +
		"```\n" +
		"## Goal\n" +
		"quoted template goal\n" +
		"## Completion Report\n" +
		"```\n" +
		"after fence\n" +
		"## Completion Report\n" +
		"Shipped a.go.\n"

	cases := []struct {
		name, heading, want string
	}{
		{"goal stops at next level-2 heading, keeps ###", "Goal",
			"Ship the thing.\n\nSecond paragraph.\n### Detail\nstill the goal"},
		{"case-insensitive", "goal",
			"Ship the thing.\n\nSecond paragraph.\n### Detail\nstill the goal"},
		{"fenced headings are body, not boundaries", "Agent Prompt",
			"```\n## Goal\nquoted template goal\n## Completion Report\n```\nafter fence"},
		{"last section runs to EOF", "Completion Report", "Shipped a.go."},
		{"absent", "Dependencies", ""},
		{"empty heading", "  ", ""},
		{"subheading is not a section", "Detail", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Section(doc, c.heading); got != c.want {
				t.Errorf("Section(%q) = %q, want %q", c.heading, got, c.want)
			}
		})
	}
}

func TestSectionEmptyAndLevel1Boundary(t *testing.T) {
	doc := "## Completion Report\n\n## Next\nbody\n# Top\n## Next2\n"
	if got := Section(doc, "Completion Report"); got != "" {
		t.Errorf("empty section = %q, want empty", got)
	}
	if got := Section(doc, "Next"); got != "body" {
		t.Errorf("level-1 heading must close a section: got %q", got)
	}
	if got := Section("## Goal ##\ntext\n", "Goal"); got != "text" {
		t.Errorf("closing sequence: got %q", got)
	}
	if got := Section("##Goal\ntext\n", "Goal"); got != "" {
		t.Errorf("'##Goal' is not a heading: got %q", got)
	}
}

func TestTickedCheckboxes(t *testing.T) {
	doc := "## Acceptance Criteria\n" +
		"- [x] first done\n" +
		"- [ ] not done\n" +
		"  * [X] nested done\n" +
		"```\n" +
		"- [x] quoted example\n" +
		"```\n"
	want := []string{"- [x] first done", "* [X] nested done"}
	if got := TickedCheckboxes(doc); !reflect.DeepEqual(got, want) {
		t.Errorf("TickedCheckboxes = %q, want %q", got, want)
	}
	done, _ := CountCheckboxes(doc)
	if done != len(want) {
		t.Errorf("TickedCheckboxes disagrees with CountCheckboxes: %d vs %d", len(want), done)
	}
	if got := TickedCheckboxes("- [ ] nothing\n"); got != nil {
		t.Errorf("no ticked boxes: got %q", got)
	}
}
