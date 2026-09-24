// Package gitstat parses `git diff --numstat` output — the one grammar two
// daemon features measure a change set with:
//
//   - internal/improve's apply gate, which must refuse a diff that touches any
//     path outside its single target file and caps the change at 120 lines;
//   - internal/actuals, which records what a finished phase run actually changed
//     (files, lines, size band) for the learning loop.
//
// Both need the SAME answer about the same bytes, which is why the parser lives
// here once rather than as two copies that would drift on the edge cases that
// matter: binary rows, paths containing spaces, CRLF endings.
//
// Callers are expected to run numstat with `--no-renames` (so every row has a
// single path column rather than an `old => new` token) and with
// `-c core.quotepath=false` (so non-ASCII paths arrive verbatim, not as quoted
// octal escapes). The parser does not second-guess either: it reports the path
// column exactly as git printed it.
package gitstat

import (
	"fmt"
	"strconv"
	"strings"
)

// FileStat is one numstat row.
type FileStat struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	// Binary is true for a "-\t-\tpath" row: git could not count lines, so Added
	// and Removed are 0 by construction rather than by measurement.
	Binary bool `json:"binary,omitempty"`
}

// ParseNumstat parses `git diff --numstat` output into one FileStat per row, in
// the order git printed them. It is fail-CLOSED: any non-blank row with fewer
// than three TAB-separated columns, or with an empty path column, is a hard
// error rather than a silent skip — a gate that skipped a row it could not read
// would wave the unread path through.
//
// The row is split on TABs, never on whitespace: git does not quote spaces in
// numstat paths, so a whitespace split would truncate `a/x.md y.md` to its last
// token and two different files could parse to the same path. Everything after
// the second TAB is the path VERBATIM (spaces included); only a trailing CR is
// stripped, because trimming spaces would corrupt a path that legitimately ends
// in one.
//
// Pure; unit-tested.
func ParseNumstat(out string) ([]FileStat, error) {
	var files []FileStat
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 || fields[2] == "" {
			return nil, fmt.Errorf("malformed numstat line %q", line)
		}
		f := FileStat{Path: fields[2]}
		if fields[0] == "-" && fields[1] == "-" {
			f.Binary = true
		} else {
			f.Added = parseCount(fields[0])
			f.Removed = parseCount(fields[1])
		}
		files = append(files, f)
	}
	return files, nil
}

// Totals sums the added and removed line counts. Binary rows contribute 0.
func Totals(files []FileStat) (added, removed int) {
	for _, f := range files {
		added += f.Added
		removed += f.Removed
	}
	return added, removed
}

// Paths returns the path column of every row, in order.
func Paths(files []FileStat) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// parseCount reads one numstat count. "-" (binary) and anything unreadable are
// 0: a count git could not produce is not a count this package should invent.
func parseCount(s string) int {
	if s == "-" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
