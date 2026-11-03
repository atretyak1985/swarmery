package probe

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadSuiteKeepsTargetCasesInOrder(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"evals/agents/demo.yaml": demoSuite})
	cases, reason, err := loadSuite(root, target)
	if err != nil || reason != "" {
		t.Fatal(err, reason)
	}
	var ids []string
	for _, c := range cases {
		ids = append(ids, c.ID)
		if !bytes.HasPrefix(c.YAML, []byte("- ")) || !bytes.Contains(c.YAML, []byte(target)) {
			t.Errorf("case %s YAML is not a one-element list: %s", c.ID, c.YAML)
		}
	}
	if got := strings.Join(ids, ","); got != "demo answers in one line,demo reports the exit code,demo stays short" {
		t.Errorf("ids = %s", got)
	}
	if cases[2].Index != 3 {
		t.Errorf("index keeps the suite position: %d", cases[2].Index)
	}
}

func TestLoadSuiteErrorsAndFallbacks(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"evals/agents/demo.yaml": "- vars:\n    system: file://../../" + target + "\n"})
	cases, _, err := loadSuite(root, target)
	if err != nil || len(cases) != 1 || cases[0].ID != "case-1" {
		t.Errorf("undescribed case: %+v %v", cases, err)
	}
	writeTree(t, root, map[string]string{"evals/agents/demo.yaml": "description: not a list\n"})
	if _, _, err := loadSuite(root, target); err == nil {
		t.Error("a mapping suite must be an error")
	}
	writeTree(t, root, map[string]string{"evals/agents/demo.yaml": "- [unclosed\n"})
	if _, _, err := loadSuite(root, target); err == nil {
		t.Error("bad YAML must be an error")
	}
}

// One case on two providers: an opus row whose regex failed and a haiku row
// that passed, both with a failing llm-rubric. The regex decides; the rubric is
// counted, not decisive.
const twoRowResults = `{"evalId":"e","results":{"results":[
 {"success":false,"cost":0.10,"vars":{"task":"run it","system":"SYSTEM TEXT"},
  "response":{"output":"opus says hi"},
  "gradingResult":{"pass":false,"componentResults":[
    {"pass":false,"assertion":{"type":"regex","value":"exit"}},
    {"pass":false,"assertion":{"type":"llm-rubric","value":"x"}}]}},
 {"success":false,"cost":0.02,"testCase":{"vars":{"task":"run it"}},
  "response":{"output":{"text":"structured"}},
  "gradingResult":{"pass":false,"componentResults":[
    {"pass":true,"assertion":{"type":"contains","value":"hi"}},
    {"pass":false,"assertion":{"type":"llm-rubric","value":"y"}}]}}
]}}`

func TestParseResultsHardAssertionsDecide(t *testing.T) {
	run, err := parseResults([]byte(twoRowResults))
	if err != nil {
		t.Fatal(err)
	}
	if run.HardPass {
		t.Error("the failing regex must fail the case")
	}
	if run.SoftTotal != 2 || run.SoftPassed != 0 {
		t.Errorf("rubric counts %d/%d", run.SoftPassed, run.SoftTotal)
	}
	if run.USD < 0.12-1e-9 || run.USD > 0.12+1e-9 {
		t.Errorf("usd %v", run.USD)
	}
	if run.Task != "run it" || !strings.Contains(run.Output, "opus says hi") || !strings.Contains(run.Output, `"structured"`) {
		t.Errorf("task/output: %+v", run)
	}
	if strings.Contains(run.Output, "SYSTEM TEXT") {
		t.Error("the system prompt must never reach the output the judge reads")
	}

	// Same shape, regex passing: promptfoo says success=false (the rubric
	// failed) but the deterministic result is a pass.
	passing := strings.Replace(twoRowResults, `{"pass":false,"assertion":{"type":"regex"`, `{"pass":true,"assertion":{"type":"regex"`, 1)
	run, err = parseResults([]byte(passing))
	if err != nil || !run.HardPass {
		t.Errorf("rubric failure decided the case: %+v %v", run, err)
	}
}

func TestParseResultsEdgeShapes(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		pass bool
		err  bool
	}{
		{`{"results":{"results":[{"success":true,"response":{"output":"ok"}}]}}`, true, false},
		{`{"results":{"results":[{"success":false,"response":{"output":"ok"}}]}}`, false, false},
		{`{"results":{"results":[{"success":false,"error":"timeout"}]}}`, false, false},
		{`{"results":{"results":[]}}`, false, true},
		{`nope`, false, true},
	} {
		run, err := parseResults([]byte(tc.raw))
		if (err != nil) != tc.err || (err == nil && run.HardPass != tc.pass) {
			t.Errorf("%s → %+v %v", tc.raw, run, err)
		}
	}
	if !isModelGraded("not-llm-rubric") || !isModelGraded("model-graded-closedqa") || isModelGraded("regex") || isModelGraded("not-contains") {
		t.Error("model-graded classification")
	}
}

func TestProbeConfigPointsAtTheTreeAndTiers(t *testing.T) {
	cfg := probeConfig("/tmp/x/base", "/tmp/x/base/evals/probe/case-01.yaml")
	for _, want := range []string{
		"file:///tmp/x/base/evals/prompts/agent-chat.json",
		"id: file:///tmp/x/base/evals/providers/claude-cli.js\n    label: opus-tier\n    config: { model: claude-opus-5-5, effort: medium }",
		"label: haiku-tier",
		"config: { model: claude-sonnet-5 }",
		"tests:\n  - file:///tmp/x/base/evals/probe/case-01.yaml",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
}

func TestPromptfooArgv(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"evals/package.json": `{"devDependencies":{"promptfoo":"^0.125.1"}}`})
	if v := pinnedPromptfoo(filepath.Join(root, "evals", "package.json")); v != "0.125.1" {
		t.Errorf("pinned = %q", v)
	}
	if v := pinnedPromptfoo(filepath.Join(root, "missing.json")); v != "" {
		t.Errorf("missing = %q", v)
	}
	t.Setenv(PromptfooBinEnv, "/opt/pf")
	if argv, err := promptfooArgv(root); err != nil || len(argv) != 1 || argv[0] != "/opt/pf" {
		t.Errorf("override: %v %v", argv, err)
	}
	t.Setenv(PromptfooBinEnv, "")
	bin := t.TempDir()
	writeTree(t, bin, map[string]string{"npx": "#!/bin/sh\n"})
	if err := os.Chmod(filepath.Join(bin, "npx"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	argv, err := promptfooArgv(root)
	if err != nil || len(argv) != 3 || argv[1] != "--yes" || argv[2] != "promptfoo@0.125.1" {
		t.Errorf("npx: %v %v", argv, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := promptfooArgv(root); err == nil || !strings.Contains(err.Error(), PromptfooBinEnv) {
		t.Errorf("no npx: %v", err)
	}
}

func TestPrependPath(t *testing.T) {
	env := prependPath([]string{"A=1", "PATH=/usr/bin"}, "/x")
	if env[1] != "PATH=/x:/usr/bin" {
		t.Errorf("%v", env)
	}
	if env := prependPath([]string{"A=1"}, "/x"); env[1] != "PATH=/x" {
		t.Errorf("%v", env)
	}
}

// A stand-in promptfoo: writes a results file to the -o path and checks the
// config it was handed points at its own tree.
const fakePromptfoo = `#!/bin/sh
cfg=""; out=""
while [ $# -gt 0 ]; do
  case "$1" in
    -c) cfg="$2"; shift ;;
    -o) out="$2"; shift ;;
  esac
  shift
done
grep -q "label: opus-tier" "$cfg" || exit 3
[ -n "$PROMPTFOO_CONFIG_DIR" ] || exit 4
if [ "$FAKE_PF_NO_OUTPUT" = 1 ]; then echo "boom" >&2; exit 1; fi
printf '{"results":{"results":[{"success":true,"cost":0.05,"vars":{"task":"t"},"response":{"output":"done"}}]}}' > "$out"
exit 100
`

func TestPromptfooRunnerRunsOneCase(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "promptfoo")
	if err := os.WriteFile(bin, []byte(fakePromptfoo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PromptfooBinEnv, bin)
	tree := t.TempDir()
	writeTree(t, tree, map[string]string{"evals/package.json": "{}"})
	c := Case{Index: 0, ID: "x", YAML: []byte("- description: x\n")}
	run, err := PromptfooRunner{}.RunCase(context.Background(), tree, c)
	if err != nil {
		t.Fatal(err)
	}
	if !run.HardPass || run.USD != 0.05 || run.Output != "done" {
		t.Errorf("run %+v", run)
	}
	if _, err := os.Stat(filepath.Join(tree, "evals", "probe", "case-01.yaml")); err != nil {
		t.Error("case file not written under evals/probe")
	}

	t.Setenv("FAKE_PF_NO_OUTPUT", "1")
	c.Index = 1
	if _, err := (PromptfooRunner{}).RunCase(context.Background(), tree, c); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("missing results must be an error quoting the output: %v", err)
	}
}

func TestGitSourceExportsFromTheObjectStore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	writeTree(t, repo, map[string]string{
		target:                   baseAgent,
		"evals/agents/demo.yaml": demoSuite,
		"scripts/other.sh":       "echo\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "x")
	// An uncommitted edit must NOT reach the export: it reads the commit.
	writeTree(t, repo, map[string]string{target: "dirty\n"})

	dst := t.TempDir()
	if err := (GitSource{Repo: repo, Ref: "HEAD"}).Export(context.Background(), dst, []string{"evals", "plugins/core"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, target)); err != nil || string(b) != baseAgent {
		t.Errorf("target: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "scripts")); err == nil {
		t.Error("exported a path that was not asked for")
	}
	if err := (GitSource{Repo: repo, Ref: "no-such-ref"}).Export(context.Background(), dst, []string{"evals"}); err == nil {
		t.Error("a bad ref must fail")
	}
	if err := (GitSource{}).Export(context.Background(), dst, nil); err == nil {
		t.Error("no repo must fail")
	}
}

func TestUntarSkipsEscapesAndLinks(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(&tar.Header{Name: "ok/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "ok/a.txt", Typeflag: tar.TypeReg, Mode: 0o644}, "a")
	add(&tar.Header{Name: "../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644}, "x")
	add(&tar.Header{Name: "ok/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	dst := filepath.Join(parent, "dst")
	if err := untar(&buf, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "ok", "a.txt")); string(b) != "a" {
		t.Error("regular file missing")
	}
	if _, err := os.Stat(filepath.Join(parent, "evil.txt")); err == nil {
		t.Error("path traversal escaped dst")
	}
	if _, err := os.Lstat(filepath.Join(dst, "ok", "link")); err == nil {
		t.Error("symlink extracted")
	}
}
