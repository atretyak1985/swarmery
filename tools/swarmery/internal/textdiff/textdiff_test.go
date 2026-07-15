package textdiff

import (
	"strings"
	"testing"
)

// TestUnifiedDiff pins the canonical unified-diff output across the shapes the
// daemon relies on (rollback previews, 409 conflict bodies): identical inputs,
// whole-file insert/delete, in-place edits, and multi-region changes that must
// either merge into one hunk or split into two.
func TestUnifiedDiff(t *testing.T) {
	cases := []struct {
		name         string
		aName, bName string
		aText, bText string
		want         string
	}{
		{
			name:  "identical returns empty",
			aName: "old", bName: "new",
			aText: "a\nb\nc\n", bText: "a\nb\nc\n",
			want: "",
		},
		{
			name:  "append one line",
			aName: "old", bName: "new",
			aText: "a\nb\nc\n", bText: "a\nb\nc\nd\n",
			want: "--- old\n+++ new\n@@ -1,3 +1,4 @@\n a\n b\n c\n+d\n",
		},
		{
			name:  "insert into empty file",
			aName: "old", bName: "new",
			aText: "", bText: "x\ny\n",
			want: "--- old\n+++ new\n@@ -0,0 +1,2 @@\n+x\n+y\n",
		},
		{
			name:  "delete whole file",
			aName: "old", bName: "new",
			aText: "x\ny\n", bText: "",
			want: "--- old\n+++ new\n@@ -1,2 +0,0 @@\n-x\n-y\n",
		},
		{
			name:  "replace a middle line",
			aName: "old", bName: "new",
			aText: "a\nb\nc\n", bText: "a\nB\nc\n",
			want: "--- old\n+++ new\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n",
		},
		{
			name:  "two far-apart changes make two hunks",
			aName: "old", bName: "new",
			aText: "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\n",
			bText: "L1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nL11\n",
			want: "--- old\n+++ new\n" +
				"@@ -1,4 +1,4 @@\n-l1\n+L1\n l2\n l3\n l4\n" +
				"@@ -8,4 +8,4 @@\n l8\n l9\n l10\n-l11\n+L11\n",
		},
		{
			name:  "nearby changes merge into one hunk",
			aName: "old", bName: "new",
			aText: "a\nb\nc\nd\ne\n",
			bText: "a\nB\nc\nD\ne\n",
			want:  "--- old\n+++ new\n@@ -1,5 +1,5 @@\n a\n-b\n+B\n c\n-d\n+D\n e\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnifiedDiff(tc.aName, tc.bName, tc.aText, tc.bText)
			if got != tc.want {
				t.Errorf("UnifiedDiff mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
		})
	}
}

// TestUnifiedDiffNoTrailingNewline covers splitDiffLines' trailing-newline
// trimming: inputs with and without a final newline diff the same content.
func TestUnifiedDiffNoTrailingNewline(t *testing.T) {
	withNL := UnifiedDiff("a", "b", "one\ntwo\n", "one\ntwo\nthree\n")
	noNL := UnifiedDiff("a", "b", "one\ntwo", "one\ntwo\nthree")
	if withNL != noNL {
		t.Errorf("trailing newline changed the diff:\nwithNL=%q\nnoNL=%q", withNL, noNL)
	}
	if !strings.Contains(withNL, "+three") {
		t.Errorf("expected inserted line in diff, got:\n%s", withNL)
	}
}

// TestUnifiedDiffHeaderLabels confirms the aName/bName labels land on the
// ---/+++ header lines.
func TestUnifiedDiffHeaderLabels(t *testing.T) {
	got := UnifiedDiff("path/before.txt", "path/after.txt", "x\n", "y\n")
	if !strings.HasPrefix(got, "--- path/before.txt\n+++ path/after.txt\n") {
		t.Errorf("header labels missing/wrong:\n%s", got)
	}
}

// TestUnifiedDiffRoundTripReconstruction is a semantic check independent of
// hunk-header arithmetic: for a single-hunk diff, the context+deletions must
// reconstruct the source and context+insertions the target.
func TestUnifiedDiffRoundTripReconstruction(t *testing.T) {
	a := "keep1\nold\nkeep2\n"
	b := "keep1\nnew1\nnew2\nkeep2\n"
	diff := UnifiedDiff("a", "b", a, b)

	var fromA, fromB []string
	for _, line := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"),
			strings.HasPrefix(line, "@@"):
			continue
		case strings.HasPrefix(line, "-"):
			fromA = append(fromA, line[1:])
		case strings.HasPrefix(line, "+"):
			fromB = append(fromB, line[1:])
		case strings.HasPrefix(line, " "):
			fromA = append(fromA, line[1:])
			fromB = append(fromB, line[1:])
		}
	}
	if got := strings.Join(fromA, "\n") + "\n"; got != a {
		t.Errorf("deletions+context did not reconstruct source:\ngot  %q\nwant %q", got, a)
	}
	if got := strings.Join(fromB, "\n") + "\n"; got != b {
		t.Errorf("insertions+context did not reconstruct target:\ngot  %q\nwant %q", got, b)
	}
}
