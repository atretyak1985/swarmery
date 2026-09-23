package runcore

// G7 census: exactly which spawn seams pass --setting-sources.
//
// Eight seams run `claude` with --setting-sources project,local, which switches
// the `user` settings tier off for that child: its pluginConfigs and its env
// block stop reaching the run. Work that reasons per seam — which ones need
// project config delivered by --settings, which ones keep the account's user
// tier live — is planned against exactly these eight (fact G7 in the
// account-switch estate plan's ground-truth table). A ninth seam landing
// silently would make that per-seam table wrong with nothing going red, so this
// test pins the set and names the file that joined or left it.
//
// It pins TWO sets, not one, because "files containing the flag" is measurably
// the wrong census — it counts eleven files, three of which are not seams:
//
//	A  files carrying the flag as a Go STRING LITERAL, outside internal/runcore
//	   (where Args emits it from Spec.SettingSources): the six raw-argv runners.
//	B  files assigning Spec.SettingSources: dispatch and verify.
//
// A ∪ B is G7's eight. A third assertion pins every file that MENTIONS the flag
// at all — A, B, and the three that only document or emit it — so a new seam
// that spells the flag some other way (a shared constant, a comment-described
// helper) still names itself here instead of slipping past both sets.
//
// The --settings emitter set is deliberately not censused. It is one file today
// (runcore/spawner.go, fed by planrun and phaserun), but the plan that relies on
// this census grows it on purpose, to ten code sites, to deliver project config
// on every seam. An always-on assertion over it would go red on that work's
// second emitter and break its own runcore and `make test` criteria. Do not
// "complete" this census by adding it.

import (
	"bytes"
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

const censusFlag = "--setting-sources"

// censusRawArgv is set A: the runners that append the flag to a raw argv.
var censusRawArgv = []string{
	"internal/extract/runner.go",
	"internal/handoff/runner.go",
	"internal/improve/runner.go",
	"internal/retroanalysis/runner.go",
	"internal/routines/runner.go",
	"internal/trajjudge/trajjudge.go",
}

// censusSpecField is set B: the engines that set Spec.SettingSources.
var censusSpecField = []string{
	"internal/dispatch/runner.go",
	"internal/verify/runner.go",
}

// censusMentionOnly mention the flag without being a seam: the emitter, the
// field's declaration, and the secret store's rationale.
var censusMentionOnly = []string{
	"internal/claudeacct/secrets.go",
	"internal/runcore/spawner.go",
	"internal/runcore/spec.go",
}

func TestSettingSourceSeamCensus(t *testing.T) {
	root := censusModuleRoot(t)
	var literal, specField, mentions []string

	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "node_modules", ".git":
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if bytes.Contains(src, []byte(censusFlag)) {
				mentions = append(mentions, rel)
			}
			hasLit, setsField, perr := censusScan(path, src)
			if perr != nil {
				return perr
			}
			if hasLit && !strings.HasPrefix(rel, "internal/runcore/") {
				literal = append(literal, rel)
			}
			if setsField {
				specField = append(specField, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	censusAssert(t, "A (files carrying a "+strconv.Quote(censusFlag)+" string literal, outside internal/runcore)",
		literal, censusRawArgv)
	censusAssert(t, "B (files assigning Spec.SettingSources)", specField, censusSpecField)
	var all []string
	all = append(all, censusRawArgv...)
	all = append(all, censusSpecField...)
	all = append(all, censusMentionOnly...)
	censusAssert(t, "of files mentioning "+censusFlag+" at all", mentions, all)

	if n := len(censusRawArgv) + len(censusSpecField); n != 8 {
		t.Fatalf("G7 is eight seams; the census lists %d", n)
	}
}

// censusScan reports whether src carries the flag inside a string literal, and
// whether it sets a SettingSources field (composite-literal key or assignment).
func censusScan(path string, src []byte) (hasLit, setsField bool, err error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		return false, false, err
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if s, uerr := strconv.Unquote(v.Value); uerr == nil && strings.Contains(s, censusFlag) {
					hasLit = true
				}
			}
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok && id.Name == "SettingSources" {
				setsField = true
			}
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "SettingSources" {
					setsField = true
				}
			}
		}
		return true
	})
	return hasLit, setsField, nil
}

// censusAssert names every file that joined or left a set.
func censusAssert(t *testing.T, set string, got, want []string) {
	t.Helper()
	have := map[string]bool{}
	for _, f := range got {
		have[f] = true
	}
	expected := map[string]bool{}
	for _, f := range want {
		expected[f] = true
	}
	var joined, left []string
	for f := range have {
		if !expected[f] {
			joined = append(joined, f)
		}
	}
	for f := range expected {
		if !have[f] {
			left = append(left, f)
		}
	}
	sort.Strings(joined)
	sort.Strings(left)
	for _, f := range joined {
		t.Errorf("%s JOINED set %s — update the plan's G7 census before changing this", f, set)
	}
	for _, f := range left {
		t.Errorf("%s LEFT set %s — update the plan's G7 census before changing this", f, set)
	}
}

// censusModuleRoot walks up from this package to the directory holding go.mod.
func censusModuleRoot(t *testing.T) string {
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
