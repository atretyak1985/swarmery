package worktree_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This guard exists because the failure it prevents leaves no trace of its
// cause. A read-only git command such as `status` opportunistically takes
// index.lock to write back its refreshed stat cache. When the daemon kills that
// git at a deadline (or the daemon itself is stopped), the 0-byte lock stays
// behind with no holder, and the operator's next `git commit` in the PRIMARY
// checkout fails with "index.lock exists". Seen twice during the 2026-10-19
// close-out.
//
// GIT_OPTIONAL_LOCKS=0 (or --no-optional-locks) turns off only that
// opportunistic write; commands that must write the index still lock. So every
// file under internal/ that spawns git directly has to opt out, and a new spawn
// site added without it fails this test.

// optionalLocksMarkers are the two spellings git accepts.
var optionalLocksMarkers = []string{"GIT_OPTIONAL_LOCKS=0", "--no-optional-locks"}

// mustSpawnGit are files the scanner has to keep finding. Without this, a
// heuristic that stopped matching (a renamed import, a helper wrapping
// exec.Command) would let the whole guard pass while covering nothing.
var mustSpawnGit = []string{
	"internal/worktree/git.go",
	"internal/archmap/git.go",
	"internal/wtjanitor/git.go",
	"internal/memlint/memlint.go",
	"internal/claudeacct/gittracked.go",
	"internal/handoff/anchor.go",
}

func TestGitSpawnsSetOptionalLocks(t *testing.T) {
	root := moduleRootDir(t)
	internal := filepath.Join(root, "internal")
	var missing []string
	seen := map[string]bool{}

	err := filepath.WalkDir(internal, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			// memlinttest is a test-fixture helper that builds throwaway repos;
			// it never runs against an operator's checkout.
			case "testdata", "node_modules", "memlinttest":
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
		if !spawnsGit(f) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		key := filepath.ToSlash(rel)
		seen[key] = true
		if !hasOptionalLocksMarker(f) {
			missing = append(missing, key)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf(`git spawned without optional locks off:

  %s

A read-only git (status, diff, log) takes index.lock to refresh the index, and a
killed one strands that lock in the operator's checkout. Set
cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0") on the command, or pass
--no-optional-locks as git's first argument.`, strings.Join(missing, "\n  "))
	}

	for _, key := range mustSpawnGit {
		if !seen[key] {
			t.Errorf("scanner no longer detects a git spawn in %q — the heuristic drifted, fix it before trusting this guard", key)
		}
	}
}

// spawnsGit reports whether f calls exec.Command or exec.CommandContext with the
// literal "git" as the program name.
func spawnsGit(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "exec" {
			return true
		}
		nameArg := -1
		switch sel.Sel.Name {
		case "Command":
			nameArg = 0
		case "CommandContext":
			nameArg = 1
		}
		if nameArg < 0 || len(call.Args) <= nameArg {
			return true
		}
		if lit, ok := call.Args[nameArg].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil && s == "git" {
				found = true
			}
		}
		return true
	})
	return found
}

// hasOptionalLocksMarker reports whether a string literal in f carries one of
// the optional-locks spellings. Literals only: a comment naming the variable
// is not a command that sets it.
func hasOptionalLocksMarker(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, m := range optionalLocksMarkers {
			if strings.Contains(s, m) {
				found = true
			}
		}
		return true
	})
	return found
}

// moduleRootDir walks up from this package to the directory holding go.mod.
func moduleRootDir(t *testing.T) string {
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
