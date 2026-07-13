// Package textdiff is the daemon's ONE canonical unified diff — born in
// internal/api (phase 4 step-05), moved here in step-08 so the Stage 2 write
// base (internal/sysedit 409 conflicts, rollback previews) can reuse it
// without importing the HTTP layer.
//
// Own Myers implementation by design: go.mod already carries 4 direct
// dependencies (dependency budget, plan step-05 task 5) — keep it exported
// and free of HTTP concerns.
package textdiff

import (
	"fmt"
	"strings"
)

// diffCtx is the unified-diff context width (the classic default).
const diffCtx = 3

// diffOp is one line of an edit script: ' ' keep, '-' delete, '+' insert.
type diffOp struct {
	kind byte
	line string
}

// UnifiedDiff renders a unified diff (3 lines of context) turning aText into
// bText. Returns "" when the texts are identical. aName/bName become the
// ---/+++ header labels.
func UnifiedDiff(aName, bName, aText, bText string) string {
	if aText == bText {
		return ""
	}
	a, b := splitDiffLines(aText), splitDiffLines(bText)
	ops := myersOps(a, b)

	// aPos[i]/bPos[i]: lines of a/b consumed before op i (0-based).
	aPos := make([]int, len(ops)+1)
	bPos := make([]int, len(ops)+1)
	for i, op := range ops {
		aPos[i+1], bPos[i+1] = aPos[i], bPos[i]
		if op.kind != '+' {
			aPos[i+1]++
		}
		if op.kind != '-' {
			bPos[i+1]++
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", aName, bName)
	i := 0
	for i < len(ops) {
		for i < len(ops) && ops[i].kind == ' ' {
			i++
		}
		if i == len(ops) {
			break
		}
		// Hunk start: up to diffCtx context lines before the change.
		start := i - diffCtx
		if start < 0 {
			start = 0
		}
		// Extend over subsequent changes separated by <= 2*diffCtx context.
		end := i + 1
		for j := end; j < len(ops); {
			if ops[j].kind != ' ' {
				end = j + 1
				j++
				continue
			}
			k := j
			for k < len(ops) && ops[k].kind == ' ' {
				k++
			}
			if k < len(ops) && k-j <= 2*diffCtx {
				j = k
				continue
			}
			break
		}
		stop := end + diffCtx
		if stop > len(ops) {
			stop = len(ops)
		}

		aCount, bCount := aPos[stop]-aPos[start], bPos[stop]-bPos[start]
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n",
			hunkStart(aPos[start], aCount), aCount,
			hunkStart(bPos[start], bCount), bCount)
		for _, op := range ops[start:stop] {
			out.WriteByte(op.kind)
			out.WriteString(op.line)
			out.WriteByte('\n')
		}
		i = stop
	}
	return out.String()
}

// hunkStart renders the unified-format start line: 1-based, except an empty
// range points at the line BEFORE the insertion point (e.g. `@@ -0,0 +1,3 @@`).
func hunkStart(pos, count int) int {
	if count == 0 {
		return pos
	}
	return pos + 1
}

// splitDiffLines splits text into lines without a trailing-newline artifact.
func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// myersOps computes a shortest edit script (Myers, O((N+M)·D)) between two
// line slices.
func myersOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	max := n + m
	offset := max
	v := make([]int, 2*max+2)
	var trace [][]int // trace[d] = v snapshot BEFORE round d (state after d-1)
	dEnd := 0
outer:
	for d := 0; d <= max; d++ {
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1] // step down: insert b line
			} else {
				x = v[offset+k-1] + 1 // step right: delete a line
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				dEnd = d
				break outer
			}
		}
	}

	// Backtrack from (n, m) to (0, 0).
	ops := []diffOp{}
	x, y := n, m
	for d := dEnd; d > 0; d-- {
		vprev := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vprev[offset+k-1] < vprev[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vprev[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY { // trailing snake of this round
			ops = append(ops, diffOp{' ', a[x-1]})
			x--
			y--
		}
		if x == prevX { // came via k+1: an insertion
			ops = append(ops, diffOp{'+', b[y-1]})
			y--
		} else { // came via k-1: a deletion
			ops = append(ops, diffOp{'-', a[x-1]})
			x--
		}
	}
	for x > 0 && y > 0 { // leading snake (round 0)
		ops = append(ops, diffOp{' ', a[x-1]})
		x--
		y--
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}
