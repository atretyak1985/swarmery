package wsingest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const untickDoc = "# Phase\n\n" +
	"- [x] **Migration applies** on a fresh store\n" +
	"- [X] `go test` passes\n" +
	"- [ ] not done yet\n" +
	"```markdown\n" +
	"- [x] Migration applies on a fresh store\n" +
	"```\n" +
	"* [x] web build passes\n"

func TestTickedCriteriaLabels(t *testing.T) {
	got := TickedCriteriaLabels(untickDoc)
	want := []string{"Migration applies** on a fresh store", "go test` passes", "web build passes"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TickedCriteriaLabels = %q, want %q", got, want)
	}
	// The mirror of UntickedCheckboxes: together they are every criterion.
	if n := len(got) + len(UntickedCheckboxes(untickDoc)); n != 4 {
		t.Errorf("ticked + unticked = %d, want 4 (the fenced one is not a criterion)", n)
	}
}

func TestUntickCriteriaPure(t *testing.T) {
	// Labels are matched after the same normalisation: surrounding emphasis and
	// whitespace in the request do not matter.
	out, matched := untickCriteria(untickDoc, []string{"  *Migration applies** on a fresh store ", "web build passes", "no such criterion"})
	if want := []string{"Migration applies** on a fresh store", "web build passes"}; !reflect.DeepEqual(matched, want) {
		t.Errorf("matched = %q, want %q", matched, want)
	}
	if !strings.Contains(out, "- [ ] **Migration applies** on a fresh store\n") || !strings.Contains(out, "* [ ] web build passes") {
		t.Errorf("lines not unticked:\n%s", out)
	}
	// The fenced example and the unnamed criterion are untouched.
	if !strings.Contains(out, "```markdown\n- [x] Migration applies on a fresh store\n```") {
		t.Errorf("fenced block changed:\n%s", out)
	}
	if !strings.Contains(out, "- [X] `go test` passes") {
		t.Errorf("unnamed criterion changed:\n%s", out)
	}
	if _, matched := untickCriteria(untickDoc, []string{"", "  "}); matched != nil {
		t.Errorf("blank labels matched %q", matched)
	}
	// An already-unticked criterion is not a match.
	if _, matched := untickCriteria(untickDoc, []string{"not done yet"}); matched != nil {
		t.Errorf("unticked criterion matched %q", matched)
	}
}

func TestUntickCriteriaFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase-1.md")
	if err := os.WriteFile(path, []byte(untickDoc), 0o640); err != nil {
		t.Fatal(err)
	}
	n, err := UntickCriteria(path, []string{"go test` passes"})
	if err != nil || n != 1 {
		t.Fatalf("UntickCriteria = %d, %v; want 1, nil", n, err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "- [ ] `go test` passes") {
		t.Errorf("file not rewritten:\n%s", body)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640 preserved", st.Mode().Perm())
	}
	// No match: nothing written, no temp file left behind.
	before, _ := os.Stat(path)
	n, err = UntickCriteria(path, []string{"nope"})
	if err != nil || n != 0 {
		t.Fatalf("no-match = %d, %v", n, err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a no-match untick rewrote the file")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want only the doc", len(entries))
	}
	if _, err := UntickCriteria(filepath.Join(t.TempDir(), "missing.md"), []string{"x"}); err == nil {
		t.Error("missing doc: want an error")
	}
}
