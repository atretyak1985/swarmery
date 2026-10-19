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
// file under internal/ and cmd/ that spawns git has to opt out, and a new spawn
// site added without it fails this test.
//
// git is spawned in two shapes, and the census catches both:
//
//   - Direct: exec.Command / exec.CommandContext whose program is "git" (a
//     literal, or a package const whose value is "git"). That file must carry
//     the marker.
//   - Through a wrapper: a generic runner calls exec.Command(Context) with the
//     program taken from a variable (a `name string` parameter), and a caller
//     hands it "git" — `ex.Run(ctx, dir, nil, "git", "remote", …)`. The scanner
//     has no type information, so it works per package: a package where any
//     call passes "git" as an argument drives git through a wrapper, and EVERY
//     by-name spawner file in it must carry the marker, unless
//     spawnsOnlyOtherBinaries names it with the binary it runs instead.
//     mustWrapGit pins the wrappers known today, so a heuristic that stops
//     seeing one fails loudly instead of passing over it.

// optionalLocksMarkers are the two spellings git accepts.
var optionalLocksMarkers = []string{"GIT_OPTIONAL_LOCKS=0", "--no-optional-locks"}

// mustSpawnGit are files the scanner has to keep finding as DIRECT git spawns.
// Without this, a heuristic that stopped matching (a renamed import, a helper
// wrapping exec.Command) would let the whole guard pass while covering nothing.
var mustSpawnGit = []string{
	"internal/worktree/git.go",
	"internal/archmap/git.go",
	"internal/wtjanitor/git.go",
	"internal/memlint/memlint.go",
	"internal/claudeacct/gittracked.go",
	"internal/handoff/anchor.go",
}

// mustWrapGit are the by-name exec wrappers known to be handed "git"; the
// scanner has to keep classifying each as a git wrapper.
var mustWrapGit = []string{
	"internal/repoprovider/exec.go", // repoprovider.OSExec: Detect, Push, the land path
	"internal/improve/apply.go",     // improve.OSExec: the improve apply/resolver pipeline
	"internal/api/tasks_diff.go",    // execReview: the board diff reads (reviewGitBinary)
}

// spawnsOnlyOtherBinaries are by-name spawners that sit in a package which
// drives git but never run git themselves. Each entry says what it runs; an
// entry the scanner no longer needs fails the test, so the list cannot rot.
var spawnsOnlyOtherBinaries = map[string]string{
	"internal/api/resume.go":     "claude --resume (bin from claudebin)",
	"internal/improve/runner.go": "claude -p (the rewriter)",
}

// censusFile is what the scanner learns about one non-test .go file.
type censusFile struct {
	key       string // module-relative, slash-separated
	f         *ast.File
	direct    bool // exec.Command(Context) with program "git"
	byName    bool // exec.Command(Context) with the program taken from a variable
	passesGit bool // some other call passes "git" as an argument
	hasMarker bool // a string literal carries an optional-locks spelling
}

func TestGitSpawnsSetOptionalLocks(t *testing.T) {
	root := moduleRootDir(t)

	// Pass 1: parse every non-test file, grouped by package directory.
	pkgs := map[string][]*censusFile{}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
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
			rel, _ := filepath.Rel(root, path)
			dir := filepath.Dir(path)
			pkgs[dir] = append(pkgs[dir], &censusFile{key: filepath.ToSlash(rel), f: f})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Pass 2: classify each file with its package's "git" consts in scope,
	// then require the marker on direct spawns and on by-name spawners in a
	// package that drives git.
	var missing []string
	direct := map[string]bool{}
	wrappers := map[string]bool{}
	exempted := map[string]bool{}
	for _, files := range pkgs {
		gitConsts := map[string]bool{}
		for _, cf := range files {
			collectGitConsts(cf.f, gitConsts)
		}
		pkgPassesGit := false
		for _, cf := range files {
			classify(cf, gitConsts)
			pkgPassesGit = pkgPassesGit || cf.passesGit
		}
		for _, cf := range files {
			switch {
			case cf.direct:
				direct[cf.key] = true
			case cf.byName && pkgPassesGit:
				if _, ok := spawnsOnlyOtherBinaries[cf.key]; ok {
					exempted[cf.key] = true
					continue
				}
				wrappers[cf.key] = true
			default:
				continue
			}
			if !cf.hasMarker {
				missing = append(missing, cf.key)
			}
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf(`git spawned without optional locks off:

  %s

A read-only git (status, diff, log) takes index.lock to refresh the index, and a
killed one strands that lock in the operator's checkout. Set
cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0") on the command (in a
by-name wrapper: when the program is git), or pass --no-optional-locks as git's
first argument. A by-name spawner that never runs git goes in
spawnsOnlyOtherBinaries with the binary it runs.`, strings.Join(missing, "\n  "))
	}

	for _, key := range mustSpawnGit {
		if !direct[key] {
			t.Errorf("scanner no longer detects a direct git spawn in %q — the heuristic drifted, fix it before trusting this guard", key)
		}
	}
	for _, key := range mustWrapGit {
		if !wrappers[key] {
			t.Errorf("scanner no longer classifies %q as a git exec wrapper — the heuristic drifted, fix it before trusting this guard", key)
		}
	}
	for key := range spawnsOnlyOtherBinaries {
		if !exempted[key] {
			t.Errorf("spawnsOnlyOtherBinaries entry %q no longer applies — drop it", key)
		}
	}
}

// collectGitConsts adds to out every const declared in f whose value is the
// literal "git" (e.g. `reviewGitBinary = "git"`).
func collectGitConsts(f *ast.File, out map[string]bool) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i < len(vs.Values) && isGitLiteral(vs.Values[i]) {
					out[name.Name] = true
				}
			}
		}
	}
}

// classify fills cf's spawn flags and marker from its AST. Markers count only
// in string literals: a comment naming the variable is not a command that
// sets it.
func classify(cf *censusFile, gitConsts map[string]bool) {
	isGit := func(e ast.Expr) bool {
		if isGitLiteral(e) {
			return true
		}
		id, ok := e.(*ast.Ident)
		return ok && gitConsts[id.Name]
	}
	ast.Inspect(cf.f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			for _, m := range optionalLocksMarkers {
				if strings.Contains(s, m) {
					cf.hasMarker = true
				}
			}
		case *ast.CallExpr:
			if nameArg, isExec := execProgramArg(n); isExec {
				if nameArg < 0 || len(n.Args) <= nameArg {
					return true // exec.LookPath and friends spawn nothing
				}
				prog := n.Args[nameArg]
				switch {
				case isGit(prog):
					cf.direct = true
				case !isStringLiteral(prog):
					cf.byName = true
				}
				return true
			}
			for _, a := range n.Args {
				if isGit(a) {
					cf.passesGit = true
				}
			}
		}
		return true
	})
}

// execProgramArg reports whether call is an `exec.X(...)` call and, for
// exec.Command / exec.CommandContext, the index of the program argument
// (-1 for any other exec function).
func execProgramArg(call *ast.CallExpr) (int, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return -1, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" {
		return -1, false
	}
	switch sel.Sel.Name {
	case "Command":
		return 0, true
	case "CommandContext":
		return 1, true
	}
	return -1, true
}

func isStringLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING
}

func isGitLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s == "git"
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
