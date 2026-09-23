package claudeflags_test

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

// This guard exists because the failure it catches is invisible. A headless
// `claude -p` run WITHOUT --permission-mode cannot write a single file — not
// even inside its own cwd, and not inside a directory the session lists as
// allowed — yet the process still exits 0 with a plausible reply. The daemon
// then reports success over work that never landed: the planning wizard's whole
// product is a plan directory, and for one release it existed only in prose
// because the resume spawn omitted the flag.
//
// So every spawn site must make the decision EXPLICITLY: pass a permission mode,
// or be listed below with the reason it needs no write access. Adding a new
// headless spawn without either fails this test.

// argvMarkers identify a claude CLI argv. `-p`/`-r` alone would also match
// `ps -p` and `git commit-tree -p`, so a site counts only when it also carries a
// flag no other binary we spawn accepts.
var argvMarkers = []string{"--output-format", "--session-id", "--setting-sources"}

// readOnlySites are the headless spawns that deliberately carry no permission
// mode: their entire contract is stdout, and the daemon — not the model — writes
// whatever lands on disk. Key is "<path>:<func>", value is why it is safe.
var readOnlySites = map[string]string{
	"internal/improve/runner.go:Run":                "the model returns a unified diff on stdout; internal/improve/apply.go applies it",
	"internal/retroanalysis/runner.go:Run":          "the model returns the analysis on stdout; internal/retroanalysis persists the row",
	"internal/handoff/runner.go:Run":                "the model returns handoff prose on stdout; internal/handoff/handoff.go does the os.WriteFile",
	"internal/extract/runner.go:Run":                "classification pass — stdout JSON only, the caller persists the rows",
	"internal/trajjudge/trajjudge.go:Run":           "advisory judge — stdout verdict only, persisted by the daemon",
	"internal/api/project_config_probe.go:runProbe": "documented non-writing probe: it returns config suggestions and nothing else",
}

// mustDetect are sites the scanner has to keep finding. Without this a heuristic
// that stopped matching — a renamed flag, an argv moved behind a helper — would
// make the whole guard pass vacuously while covering nothing.
var mustDetect = []string{
	"internal/runcore/spawner.go:Args",
	"internal/api/resume.go:resumeArgs",
	"internal/routines/runner.go:Run",
	"internal/provision/service.go:run",
}

func TestHeadlessSpawnSitesDecidePermissionMode(t *testing.T) {
	root := moduleRoot(t)
	var missing, missingEffort []string
	seen := map[string]bool{}

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
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			lits, calls := scanFunc(fn)
			if !isHeadlessClaudeArgv(lits) {
				continue
			}
			key := filepath.ToSlash(rel) + ":" + fn.Name.Name
			seen[key] = true
			// The second decision, and the reason this guard grew a twin: an
			// omitted --effort is NOT the cheap default, it is the CLI's xhigh.
			// A site that forgets it does not fail — it quietly runs every turn
			// at maximum reasoning depth, which is the same shape of invisible
			// loss the permission-mode guard was written for. Unlike that one,
			// there is no read-only exemption: stdout-only sites think too, and
			// a classification pass is exactly where xhigh is pure waste.
			if !decidesEffort(calls) && !lits["--effort"] {
				missingEffort = append(missingEffort, key)
			}
			if decidesPermissionMode(calls) || lits["--permission-mode"] {
				continue
			}
			if _, allowed := readOnlySites[key]; allowed {
				continue
			}
			missing = append(missing, key)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf(`headless claude spawn without a permission-mode decision:

  %s

A headless run without --permission-mode cannot write ANY file and still exits 0.
Either pass claudeflags.PermissionModeArgs("SWARMERY_<SITE>_PERMISSION_MODE") in
the argv, or — if the run's contract is stdout only — add the site to
readOnlySites in this file with the reason.`, strings.Join(missing, "\n  "))
	}

	if len(missingEffort) > 0 {
		sort.Strings(missingEffort)
		t.Errorf(`headless claude spawn without an effort decision:

  %s

A headless run without --effort does not get a cheap default — it gets the CLI's
own, which is xhigh, the DEEPEST setting. Every turn of that run then pays
maximum reasoning tokens whether or not the work needed them, silently.

Pass claudeflags.EffortArgs("SWARMERY_<SITE>_EFFORT", <SITE>.DefaultEffort) in a
flat argv, or claudeflags.Effort(...) into runcore.Spec.Effort — and add the
site's default to spawndefaults_test.go so the value is pinned, not incidental.`,
			strings.Join(missingEffort, "\n  "))
	}

	for _, key := range mustDetect {
		if !seen[key] {
			t.Errorf("scanner no longer detects %q — the heuristic drifted, fix it before trusting this guard", key)
		}
	}

	// A stale entry is as dangerous as a missing one: it would silently absolve a
	// future site that reuses the name.
	for key := range readOnlySites {
		if !seen[key] {
			t.Errorf("readOnlySites entry %q no longer matches a headless spawn site — remove or re-key it", key)
		}
	}
}

// scanFunc returns the string literals in fn and which claudeflags helpers it
// calls.
//
// It matches the SELECTOR, not just the package: `claudeflags.<anything>` used
// to count as "this function decided its permission mode", which was harmless
// while --permission-mode was the only thing this package resolved. It stopped
// being harmless the moment sites began calling claudeflags.Effort — a site that
// pinned its effort and forgot its permission mode would have been absolved by
// the effort call alone, and the guard would have passed while the run could not
// write a file. Two decisions, two separate proofs.
func scanFunc(fn *ast.FuncDecl) (lits map[string]bool, calls map[string]bool) {
	lits, calls = map[string]bool{}, map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if s, err := strconv.Unquote(v.Value); err == nil {
					lits[s] = true
				}
			}
		case *ast.SelectorExpr:
			if id, ok := v.X.(*ast.Ident); ok && id.Name == "claudeflags" {
				calls[v.Sel.Name] = true
			}
		}
		return true
	})
	return lits, calls
}

// decidesPermissionMode: the function asks claudeflags for its mode, in either
// spelling (PermissionModeArgs for flat argv, Mode for a runcore.Spec value).
func decidesPermissionMode(calls map[string]bool) bool {
	return calls["PermissionModeArgs"] || calls["Mode"]
}

// decidesEffort: the function asks claudeflags for its reasoning depth, in any
// of its spellings (EffortArgs for flat argv, EffortArgsWith when the site also
// honours an explicitly-set per-run override, Effort for a runcore.Spec value).
func decidesEffort(calls map[string]bool) bool {
	return calls["EffortArgs"] || calls["EffortArgsWith"] || calls["Effort"] || calls["NormalizeEffort"]
}

// isHeadlessClaudeArgv reports whether these literals build a headless claude
// argv: a prompt/resume flag plus a marker only the claude CLI takes.
func isHeadlessClaudeArgv(lits map[string]bool) bool {
	if !lits["-p"] && !lits["-r"] {
		return false
	}
	for _, m := range argvMarkers {
		if lits[m] {
			return true
		}
	}
	return false
}

// moduleRoot walks up from this package to the directory holding go.mod.
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
