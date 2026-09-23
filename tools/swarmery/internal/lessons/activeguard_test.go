package lessons

// The acceptance criterion this file enforces: "No code path sets
// status = 'active' except the operator action." A lesson that turns active on
// its own reaches future runs as instructions nobody reviewed, and a regression
// here would be silent — nothing fails, the queue just empties itself.
//
// So the guard reads the SOURCE, not behaviour: every SQL string literal in the
// module that writes a lessons table (surprise_lessons, retro_lessons) is
// inspected, and
//
//  1. only internal/lessons/review.go:Accept may write 'active';
//  2. no write may set status from a parameter (status = ?), which would let a
//     caller smuggle 'active' past rule 1;
//  3. every INSERT into surprise_lessons must insert the literal 'candidate'.
//
// The scan must keep finding Accept's own write, or it passes vacuously.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	writeRe     = regexp.MustCompile(`(?i)\b(UPDATE|INSERT\s+INTO)\s+(surprise_lessons|retro_lessons)\b`)
	whereRe     = regexp.MustCompile(`(?i)\bWHERE\b|\bON\s+CONFLICT\b`)
	activeRe    = regexp.MustCompile(`'active'`)
	paramStatus = regexp.MustCompile(`(?i)\bstatus\s*=\s*\?`)
	insertRe    = regexp.MustCompile(`(?i)INSERT\s+INTO\s+surprise_lessons\b`)
)

const acceptSite = "internal/lessons/review.go:Accept"

func TestOnlyTheOperatorActionSetsActive(t *testing.T) {
	root := moduleRoot(t)
	foundAccept := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "web", "testdata", "node_modules", ".git":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			site := rel + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || !writeRe.MatchString(s) {
					return true
				}
				head := s
				if loc := whereRe.FindStringIndex(s); loc != nil {
					head = s[:loc[0]]
				}
				if activeRe.MatchString(head) {
					if site != acceptSite {
						t.Errorf("%s writes status 'active' to a lessons table — only the operator's Accept may", site)
					} else {
						foundAccept = true
					}
				}
				if paramStatus.MatchString(head) {
					t.Errorf("%s sets a lesson status from a parameter; statuses must be SQL literals so this guard can see them", site)
				}
				if insertRe.MatchString(s) && !strings.Contains(head, "'candidate'") {
					t.Errorf("%s inserts into surprise_lessons without the literal 'candidate' status", site)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !foundAccept {
		t.Fatalf("the scan no longer finds %s writing 'active' — the guard drifted and would pass vacuously", acceptSite)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("go.mod not found above %s", dir)
	return ""
}
