package wsingest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const classedDoc = "# Phase 3\n\n" +
	"- [x] parser lands\n" +
	"- [ ] settle uses the effective total\n" +
	"- [ ] [LAND] push the branch and open the PR\n" +
	"- [x] [LAND] an already-landed one\n" +
	"- [ ] [MANUAL] check the production console\n" +
	"- [ ] **[LAND]** emphasis is not a marker\n" +
	"- [ ] the word [LAND] mid-label is not a marker\n" +
	"\n```markdown\n" +
	"- [ ] [LAND] a quoted template line\n" +
	"- [ ] [MANUAL] another quoted line\n" +
	"```\n"

func TestCountCriteria(t *testing.T) {
	got := CountCriteria(classedDoc)
	want := CriteriaCounts{Done: 2, Total: 7, LandOpen: 1, ManualOpen: 1}
	if got != want {
		t.Fatalf("CountCriteria = %+v, want %+v", got, want)
	}
	// Done/Total are exactly CountCheckboxes' numbers: one parser, one meaning.
	done, total := CountCheckboxes(classedDoc)
	if done != got.Done || total != got.Total {
		t.Errorf("CountCheckboxes = %d/%d, CountCriteria = %d/%d", done, total, got.Done, got.Total)
	}
	if got.Executable() != 5 {
		t.Errorf("Executable = %d, want 5", got.Executable())
	}
}

func TestCountCriteria_NoMarkers(t *testing.T) {
	got := CountCriteria("- [ ] a\n- [x] b\n")
	if got != (CriteriaCounts{Done: 1, Total: 2}) {
		t.Errorf("CountCriteria = %+v", got)
	}
}

func TestUntickedExecutable(t *testing.T) {
	got := UntickedExecutable(classedDoc)
	want := []string{
		"settle uses the effective total",
		"[LAND]** emphasis is not a marker",
		"the word [LAND] mid-label is not a marker",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UntickedExecutable = %q, want %q", got, want)
	}
	if n := len(UntickedCheckboxes(classedDoc)); n != 5 {
		t.Errorf("UntickedCheckboxes = %d labels, want 5 (classes included)", n)
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "phase-3.md")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTickCriteriaByClass(t *testing.T) {
	p := writeTemp(t, classedDoc)
	n, err := TickCriteriaByClass(p, ClassLand)
	if err != nil || n != 1 {
		t.Fatalf("TickCriteriaByClass = %d, %v; want 1, nil", n, err)
	}
	out := readFile(t, p)
	if !strings.Contains(out, "- [x] [LAND] push the branch and open the PR") {
		t.Errorf("LAND line not ticked:\n%s", out)
	}
	if !strings.Contains(out, "- [ ] [MANUAL] check the production console") {
		t.Error("MANUAL line must stay unticked")
	}
	if !strings.Contains(out, "- [ ] [LAND] a quoted template line") {
		t.Error("a fenced line was ticked")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 kept", st.Mode().Perm())
	}
	// Second call: nothing left, nothing written.
	if n, err := TickCriteriaByClass(p, ClassLand); err != nil || n != 0 {
		t.Errorf("second tick = %d, %v; want 0, nil", n, err)
	}
	if _, err := TickCriteriaByClass(p, "SHIP"); !errors.Is(err, ErrUnknownCriterionClass) {
		t.Errorf("unknown class err = %v", err)
	}
}

func TestMarkCriterionClass(t *testing.T) {
	body := "# P\n\n- [ ] push the branch\n- [ ] [LAND] check prod\nprose\n```\n- [ ] fenced\n```\n"
	p := writeTemp(t, body)

	if err := MarkCriterionClass(p, 3, ClassLand); err != nil {
		t.Fatalf("mark line 3: %v", err)
	}
	// Replaces an existing marker rather than stacking a second one.
	if err := MarkCriterionClass(p, 4, ClassManual); err != nil {
		t.Fatalf("mark line 4: %v", err)
	}
	want := "# P\n\n- [ ] [LAND] push the branch\n- [ ] [MANUAL] check prod\nprose\n```\n- [ ] fenced\n```\n"
	if got := readFile(t, p); got != want {
		t.Errorf("doc =\n%q\nwant\n%q", got, want)
	}
	// Same class again: a no-op.
	if err := MarkCriterionClass(p, 3, ClassLand); err != nil {
		t.Errorf("idempotent mark: %v", err)
	}
	if got := readFile(t, p); got != want {
		t.Errorf("idempotent mark rewrote the doc:\n%q", got)
	}
	for _, line := range []int{5, 7, 0, 99} { // prose, fenced, out of range
		if err := MarkCriterionClass(p, line, ClassLand); !errors.Is(err, ErrNoCriterionAtLine) {
			t.Errorf("line %d: err = %v, want ErrNoCriterionAtLine", line, err)
		}
	}
	if err := MarkCriterionClass(p, 3, "land"); !errors.Is(err, ErrUnknownCriterionClass) {
		t.Errorf("lower-case class err = %v", err)
	}
}

// ListCriteria walks the same lines the counts do: classedDoc's seven criteria
// (fenced template lines excluded), each with its 0-based line, its label with
// the class marker stripped, its state and its class whether ticked or not.
func TestListCriteria(t *testing.T) {
	got := ListCriteria(classedDoc)
	want := []Criterion{
		{Line: 2, Text: "parser lands", Done: true},
		{Line: 3, Text: "settle uses the effective total"},
		{Line: 4, Text: "push the branch and open the PR", Class: ClassLand},
		{Line: 5, Text: "an already-landed one", Done: true, Class: ClassLand},
		{Line: 6, Text: "check the production console", Class: ClassManual},
		{Line: 7, Text: "[LAND]** emphasis is not a marker"},
		{Line: 8, Text: "the word [LAND] mid-label is not a marker"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListCriteria =\n%+v\nwant\n%+v", got, want)
	}
	done, total := CountCheckboxes(classedDoc)
	n := 0
	for _, c := range got {
		if c.Done {
			n++
		}
	}
	if len(got) != total || n != done {
		t.Errorf("ListCriteria = %d/%d, CountCheckboxes = %d/%d", n, len(got), done, total)
	}
	// Every Line addresses a checkbox line, so a flip by line hits the criterion.
	lines := strings.Split(classedDoc, "\n")
	for _, c := range got {
		if !checkboxRe.MatchString(lines[c.Line]) {
			t.Errorf("line %d %q is not a checkbox", c.Line, lines[c.Line])
		}
	}
	if got := ListCriteria("# no criteria\n"); got == nil || len(got) != 0 {
		t.Errorf("empty doc = %#v, want []", got)
	}
}
