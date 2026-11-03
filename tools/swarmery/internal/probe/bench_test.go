package probe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// benchFiles is a tree with three bench tasks: one for demo, one untagged
// (every agent), one for another agent only.
func benchFiles() map[string]string {
	return map[string]string{
		"plugins/core/.claude-plugin/plugin.json":      `{"name":"core","version":"0.0.1"}`,
		"evals/bench/tasks/b01-fix/prompt.md":          "Fix the off-by-one.",
		"evals/bench/tasks/b01-fix/meta.json":          `{"agents":["demo","debugger"]}`,
		"evals/bench/tasks/b02-stale/prompt.md":        "Fix a bug that is already fixed.",
		"evals/bench/tasks/b03-other/prompt.md":        "Only for another agent.",
		"evals/bench/tasks/b03-other/meta.json":        `{"agents":["other"]}`,
		"evals/bench/tasks/not-a-task/README.txt":      "no prompt.md here",
		"evals/bench/run.sh":                           "#!/usr/bin/env bash\n",
		"evals/bench/tasks/b01-fix/repo/total.go":      "package orders\n",
		"evals/bench/tasks/b02-stale/repo/discount.go": "package orders\n",
	}
}

type benchCall struct {
	side, task, agent string
}

// fakeBench answers per (side, task) and proves each side loads its own copy:
// the variant plugin dir carries the patch, the base one does not.
type fakeBench struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []benchCall
	pass   map[string]bool // "side|task" → pass; missing → true
	broken map[string]bool // "side|task" → run.sh error:true
	failOn string          // "side|task" → RunTask returns an error
	usd    float64
}

func (f *fakeBench) RunTask(_ context.Context, tree string, side BenchSide, t BenchTask) (BenchRun, error) {
	name := filepath.Base(tree)
	f.mu.Lock()
	f.calls = append(f.calls, benchCall{name, t.ID, side.Agent})
	f.mu.Unlock()
	if side.PluginDir != filepath.Join(tree, "plugins", "core") {
		f.t.Fatalf("%s: plugin dir %s is not this tree's copy", name, side.PluginDir)
	}
	body, err := os.ReadFile(filepath.Join(side.PluginDir, "agents", "demo.md"))
	if err != nil {
		f.t.Fatalf("%s: plugin copy has no agent: %v", name, err)
	}
	if patched := strings.Contains(string(body), variantAdd); patched != (name == "variant") {
		f.t.Fatalf("%s copy patched=%v", name, patched)
	}
	key := name + "|" + t.ID
	if key == f.failOn {
		return BenchRun{USD: f.usd}, errors.New("run.sh: jq is required")
	}
	pass, ok := f.pass[key]
	if !ok {
		pass = true
	}
	tailText := ""
	if !pass {
		tailText = "go test ./... failed\nmore"
	}
	if f.broken[key] {
		return BenchRun{Error: true, USD: f.usd, CheckTail: "session error: subtype=error_during_execution"}, nil
	}
	return BenchRun{Pass: pass, USD: f.usd, Reply: name + "-reply", DiffStat: " total.go | 2 +-", CheckTail: tailText}, nil
}

func newBenchService(t *testing.T) (*Service, *fakeBench, *fakeJudge, int64) {
	t.Helper()
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	bench := &fakeBench{t: t, pass: map[string]bool{}, broken: map[string]bool{}, usd: 0.25}
	judge := &fakeJudge{answer: "A left the repo cleaner.\nCHOICE: A", usd: 0.02}
	s := &Service{DB: db, Source: &fakeSource{files: benchFiles()}, Suite: &fakeSuite{t: t}, Judge: judge,
		Bench: bench, BenchMaxUSD: 100, MaxUSD: 0.01, Flip: func() bool { return true }}
	return s, bench, judge, id
}

func TestLoadBenchTasksFollowsTags(t *testing.T) {
	tree := t.TempDir()
	if err := (&fakeSource{files: benchFiles()}).Export(context.Background(), tree, nil); err != nil {
		t.Fatal(err)
	}
	ids := func(agent string) []string {
		tasks, _, err := loadBenchTasks(tree, agent)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range tasks {
			out = append(out, x.ID)
		}
		return out
	}
	if got := strings.Join(ids("demo"), ","); got != "b01-fix,b02-stale" {
		t.Errorf("demo tasks = %s (tagged + untagged, never another agent's)", got)
	}
	if got := strings.Join(ids("other"), ","); got != "b02-stale,b03-other" {
		t.Errorf("other tasks = %s", got)
	}
	if got := strings.Join(ids("planner"), ","); got != "b02-stale" {
		t.Errorf("an untagged task applies to every agent: %s", got)
	}

	if err := os.WriteFile(filepath.Join(tree, "evals/bench/tasks/b02-stale/meta.json"), []byte(`{"agents":["x"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if tasks, reason, err := loadBenchTasks(tree, "planner"); err != nil || len(tasks) != 0 || !strings.Contains(reason, "tagged for planner") {
		t.Errorf("no tagged task: %v %q %v", tasks, reason, err)
	}
	if err := os.WriteFile(filepath.Join(tree, "evals/bench/tasks/b02-stale/meta.json"), []byte(`{agents`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBenchTasks(tree, "demo"); err == nil || !strings.Contains(err.Error(), "b02-stale") {
		t.Errorf("malformed meta.json must fail naming the task: %v", err)
	}
	if tasks, reason, err := loadBenchTasks(t.TempDir(), "demo"); err != nil || len(tasks) != 0 || reason == "" {
		t.Errorf("no bench dir: %v %q %v", tasks, reason, err)
	}
}

func TestRunBenchStoresBenchRowsPerTask(t *testing.T) {
	s, bench, judge, id := newBenchService(t)
	bench.pass["variant|b01-fix"] = false
	p, _ := s.LoadProposal(id)
	usd, err := s.RunBench(context.Background(), "job-b", p)
	if err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, s.DB, id)
	if len(rows) != 2 {
		t.Fatalf("want one row per tagged task (b01, b02), got %+v", rows)
	}
	for _, r := range rows {
		if r.Source != SourceBench || r.JobID != "job-b" {
			t.Errorf("row %+v: source/job", r)
		}
	}
	split, tie := rows[0], rows[1]
	if split.TaskID != "b01-fix" || !*split.BasePass || *split.VariantPass || split.BlindChoice != ChoiceSkipped {
		t.Errorf("split: %+v", split)
	}
	if !strings.Contains(split.Reason, "variant: go test ./... failed") {
		t.Errorf("a failing side's first check line belongs in the reason: %q", split.Reason)
	}
	if tie.TaskID != "b02-stale" || tie.BlindChoice != ChoiceBase || !strings.HasPrefix(tie.Reason, "[A=base · B=variant]") {
		t.Errorf("tie (base at A, judge picks A): %+v", tie)
	}
	if len(judge.prompts) != 1 {
		t.Fatalf("judge runs only on the tie: %d", len(judge.prompts))
	}
	pr := judge.prompts[0]
	for _, want := range []string{"Fix a bug that is already fixed.", "base-reply", "variant-reply", "git diff --stat", "total.go | 2 +-"} {
		if !strings.Contains(pr, want) {
			t.Errorf("judge prompt lacks %q", want)
		}
	}
	for _, c := range bench.calls {
		if c.agent != "core:demo" {
			t.Errorf("session must run as core:demo, got %q", c.agent)
		}
		if c.task == "b03-other" {
			t.Error("ran a task tagged for another agent")
		}
	}
	if len(bench.calls) != 4 {
		t.Errorf("want base+variant per task = 4 runs, got %d", len(bench.calls))
	}
	if want := 4*0.25 + 0.02; usd < want-1e-9 || usd > want+1e-9 {
		t.Errorf("usd = %v, want %v", usd, want)
	}
}

func TestBenchCapIsCheckedBetweenTasks(t *testing.T) {
	s, bench, judge, id := newBenchService(t)
	s.BenchMaxUSD = 0.4 // one task = 0.5 > cap
	p, _ := s.LoadProposal(id)
	usd, err := s.RunBench(context.Background(), "j", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(bench.calls) != 2 || usd != 0.5 {
		t.Errorf("the cap stops after the first task: %d runs, $%v", len(bench.calls), usd)
	}
	if len(judge.prompts) != 0 {
		t.Error("judge ran past the cap")
	}
	rows := mustRows(t, s.DB, id)
	if len(rows) != 2 || rows[0].BlindChoice != ChoiceCapped || rows[0].BasePass == nil ||
		rows[1].BlindChoice != ChoiceCapped || rows[1].BasePass != nil {
		t.Fatalf("rows: %+v", rows)
	}
	if !strings.Contains(rows[1].Reason, BenchMaxUSDEnv) {
		t.Errorf("capped reason names the bench knob: %q", rows[1].Reason)
	}
	// The promptfoo cap (MaxUSD 0.01 here) is not the bench step's.
	if s.CapUSD(SourceBench) != 0.4 || s.CapUSD(SourcePromptfoo) != 0.01 {
		t.Errorf("caps: bench %v promptfoo %v", s.CapUSD(SourceBench), s.CapUSD(SourcePromptfoo))
	}
}

func TestBenchSessionErrorIsSkippedAndInfraErrorStops(t *testing.T) {
	s, bench, judge, id := newBenchService(t)
	bench.broken["base|b01-fix"] = true
	p, _ := s.LoadProposal(id)
	if _, err := s.RunBench(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, s.DB, id)
	if len(rows) != 2 || rows[0].BlindChoice != ChoiceSkipped || rows[0].BasePass != nil ||
		!strings.Contains(rows[0].Reason, "re-run") || !strings.Contains(rows[0].Reason, "base: session error") {
		t.Errorf("session error row: %+v", rows[0])
	}
	if rows[1].BlindChoice != ChoiceBase || len(judge.prompts) != 1 {
		t.Errorf("the next task still runs: %+v", rows[1])
	}

	s2, bench2, _, id2 := newBenchService(t)
	bench2.failOn = "variant|b01-fix"
	p2, _ := s2.LoadProposal(id2)
	if _, err := s2.RunBench(context.Background(), "j", p2); err == nil {
		t.Fatal("an infrastructure failure must fail the job")
	}
	rows2 := mustRows(t, s2.DB, id2)
	if len(rows2) != 1 || rows2[0].BlindChoice != ChoiceSkipped || !strings.Contains(rows2[0].Reason, "variant run failed: run.sh: jq") {
		t.Errorf("rows: %+v", rows2)
	}
}

func TestRunBenchNoSuitePaths(t *testing.T) {
	s, bench, _, _ := newBenchService(t)
	skill := insertProposal(t, s.DB, "approved", "skill", "", "plugins/core/skills/testing/SKILL.md", demoDiff)
	p, _ := s.LoadProposal(skill)
	if _, err := s.RunBench(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	if rows := mustRows(t, s.DB, skill); len(rows) != 1 || rows[0].BlindChoice != ChoiceNoSuite || rows[0].Source != SourceBench {
		t.Errorf("skill: %+v", rows)
	}

	// An agent no task is tagged for (and no untagged task) gets one no_suite row.
	files := benchFiles()
	files["evals/bench/tasks/b02-stale/meta.json"] = `{"agents":["other"]}`
	files["evals/bench/tasks/b01-fix/meta.json"] = `{"agents":["other"]}`
	s.Source = &fakeSource{files: files}
	id := insertProposal(t, s.DB, "proposed", "agent", target, "", demoDiff)
	p, _ = s.LoadProposal(id)
	if _, err := s.RunBench(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, s.DB, id)
	if len(rows) != 1 || rows[0].BlindChoice != ChoiceNoSuite || !strings.Contains(rows[0].Reason, "tagged for demo") {
		t.Errorf("untagged agent: %+v", rows)
	}
	if len(bench.calls) != 0 {
		t.Error("no_suite paths must not run a session")
	}
}

func TestStartSourceBenchIsAdvisory(t *testing.T) {
	s, _, _, id := newBenchService(t)
	s.Go = func(fn func()) { fn() }
	s.BenchMaxUSD = 7
	before := proposalSnapshot(t, s.DB, id)
	job, err := s.StartSource(id, SourceBench)
	if err != nil {
		t.Fatal(err)
	}
	if job.Source != SourceBench || job.MaxUSD != 7 {
		t.Errorf("job %+v: source and the bench cap", job)
	}
	if after := proposalSnapshot(t, s.DB, id); after != before {
		t.Errorf("a bench probe changed the proposal: %s → %s", before, after)
	}
	if jobs := s.Jobs(id); len(jobs) != 1 || jobs[0].Status != JobDone {
		t.Errorf("jobs %+v", jobs)
	}

	if _, err := s.StartSource(id, "vibes"); !errors.Is(err, ErrUnknownSource) {
		t.Errorf("unknown source: %v", err)
	}
	applied := insertProposal(t, s.DB, "applied", "agent", target, "", demoDiff)
	if _, err := s.StartSource(applied, SourceBench); !errors.Is(err, ErrNotProbeable) {
		t.Errorf("applied: %v", err)
	}
	s.Bench = nil
	if _, err := s.StartSource(id, SourceBench); !errors.Is(err, ErrBenchUnattached) {
		t.Errorf("no runner: %v", err)
	}
	if job, err := s.Start(id); err != nil || job.Source != SourcePromptfoo {
		t.Errorf("Start stays the promptfoo step: %+v %v", job, err)
	}
}

func TestBenchSideReadsManifestAndFrontmatter(t *testing.T) {
	tree := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(tree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins/web-pack/.claude-plugin/plugin.json", `{"name":"web"}`)
	write("plugins/web-pack/agents/ui.md", "---\nname: \"ui-dev\"\ndescription: x\n---\nbody\n")
	side, err := benchSide(tree, "plugins/web-pack/agents/ui.md")
	if err != nil || side.Agent != "web:ui-dev" || side.PluginDir != filepath.Join(tree, "plugins", "web-pack") {
		t.Errorf("side %+v %v", side, err)
	}
	write("plugins/web-pack/agents/plain.md", "# no frontmatter\nname: not-this\n")
	if side, _ := benchSide(tree, "plugins/web-pack/agents/plain.md"); side.Agent != "web:plain" {
		t.Errorf("stem fallback: %q", side.Agent)
	}
	if _, err := benchSide(tree, "plugins/none/agents/x.md"); err == nil {
		t.Error("a pack without a manifest cannot be loaded as a plugin")
	}
}

func TestCopyTreeKeepsTheExecutableBit(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(filepath.Join(src, "hook.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "doc.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"hook.sh": 0o755, "doc.md": 0o644} {
		fi, err := os.Stat(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o111 != want&0o111 {
			t.Errorf("%s mode %v, want exec bit as in %v", name, fi.Mode().Perm(), want)
		}
	}
}

// TestScriptBenchRunnerDrivesTheRealRunScript runs the repo's own
// evals/bench/run.sh against a one-task tree with a stub claude, end to end:
// flags passed through, result parsed, reply and diff stat collected.
func TestScriptBenchRunnerDrivesTheRealRunScript(t *testing.T) {
	for _, bin := range []string{"bash", "jq", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	runSh, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "evals", "bench", "run.sh"))
	if err != nil {
		t.Skipf("repo run.sh not reachable: %v", err)
	}
	tree := filepath.Join(t.TempDir(), "variant")
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(tree, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("evals/bench/run.sh", string(runSh), 0o755)
	write("evals/bench/tasks/t01/prompt.md", "Make a.txt say fixed.", 0o644)
	write("evals/bench/tasks/t01/repo/a.txt", "broken\n", 0o644)
	write("evals/bench/tasks/t01/check.sh", "#!/usr/bin/env bash\ngrep -qx fixed \"$1/a.txt\" || { echo 'a.txt is not fixed'; exit 1; }\n", 0o755)
	write("plugins/core/.claude-plugin/plugin.json", `{"name":"core"}`, 0o644)

	argsLog := filepath.Join(t.TempDir(), "args.txt")
	stub := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(stub, []byte(`#!/usr/bin/env bash
case " $* " in *" --version "*) echo "stub 0.0.0"; exit 0 ;; esac
printf '%s\n' "$@" >"$BENCH_STUB_ARGS"
echo fixed >a.txt
echo new >b.txt
jq -nc '{type:"result",subtype:"success",is_error:false,num_turns:2,duration_ms:5,total_cost_usd:0.125,result:"Fixed a.txt."}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BENCH_STUB_ARGS", argsLog)
	t.Setenv("AGENT_PROJECT", "must-not-leak")

	side := BenchSide{PluginDir: filepath.Join(tree, "plugins", "core"), Agent: "core:demo"}
	run, err := ScriptBenchRunner{Claude: stub}.RunTask(context.Background(), tree, side, BenchTask{ID: "t01"})
	if err != nil {
		t.Fatal(err)
	}
	if !run.Pass || run.Error || run.TimedOut || run.USD != 0.125 {
		t.Errorf("run = %+v", run)
	}
	if strings.TrimSpace(run.Reply) != "Fixed a.txt." {
		t.Errorf("reply = %q", run.Reply)
	}
	if !strings.Contains(run.DiffStat, "a.txt") || !strings.Contains(run.DiffStat, "b.txt") {
		t.Errorf("diff stat must list the edit and the new file: %q", run.DiffStat)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	argv := "\n" + string(args)
	for _, want := range []string{"\n--plugin-dir\n" + side.PluginDir + "\n", "\n--agent\ncore:demo\n", "\n--strict-mcp-config\n"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q:\n%s", want, args)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(tree), "bench-variant-*")); len(left) != 0 {
		t.Errorf("runner left its scratch behind: %v", left)
	}

	// A failing check is a model result, not an error.
	if err := os.WriteFile(filepath.Join(tree, "evals/bench/tasks/t01/check.sh"),
		[]byte("#!/usr/bin/env bash\necho 'a.txt is not fixed'; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run, err = ScriptBenchRunner{Claude: stub}.RunTask(context.Background(), tree, side, BenchTask{ID: "t01"})
	if err != nil || run.Pass || run.Error || !strings.Contains(run.CheckTail, "not fixed") {
		t.Errorf("failing check: %+v %v", run, err)
	}

	// No claude at all: run.sh refuses before any task, which is an error.
	if _, err := (ScriptBenchRunner{Claude: filepath.Join(t.TempDir(), "nope")}).RunTask(context.Background(), tree, side, BenchTask{ID: "t01"}); err == nil {
		t.Error("a missing claude must be an infrastructure error")
	}
}

func TestBenchEnvDropsProjectAttribution(t *testing.T) {
	t.Setenv("AGENT_PROJECT", "p")
	t.Setenv("BENCH_KEEP", "0")
	env := strings.Join(benchEnv("/bin/claude", BenchSide{PluginDir: "/x", Agent: "core:a"}, "/tmp/t"), "\n") + "\n"
	if strings.Contains(env, "AGENT_PROJECT=") || strings.Contains(env, "BENCH_KEEP=0") {
		t.Error("AGENT_PROJECT and stale BENCH_ vars must not reach run.sh")
	}
	for _, want := range []string{"BENCH_CLAUDE=/bin/claude\n", "BENCH_PLUGIN_DIR=/x\n", "BENCH_AGENT=core:a\n", "BENCH_KEEP=1\n", "TMPDIR=/tmp/t\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
}

func TestBenchMaxUSDEnv(t *testing.T) {
	t.Setenv(BenchMaxUSDEnv, "")
	if BenchMaxUSD() != DefaultBenchMaxUSD {
		t.Errorf("default = %v", BenchMaxUSD())
	}
	t.Setenv(BenchMaxUSDEnv, "1.5")
	if BenchMaxUSD() != 1.5 {
		t.Errorf("env = %v", BenchMaxUSD())
	}
	t.Setenv(BenchMaxUSDEnv, "-3")
	if BenchMaxUSD() != DefaultBenchMaxUSD {
		t.Errorf("negative falls back: %v", BenchMaxUSD())
	}
}
