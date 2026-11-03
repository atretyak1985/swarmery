package probe

// The bench step of a probe (harness² phase 6). The promptfoo step checks an
// agent's output contract in one turn; some proposals only show in a whole
// agentic session. This step runs the frozen bench (evals/bench, graded by each
// task's deterministic check.sh) once per tagged task on each side, with the
// session running AS the target agent from a temp copy of its plugin:
//
//	base:    <ws>/base/plugins/<pack>     loaded with --plugin-dir, unpatched
//	variant: <ws>/variant/plugins/<pack>  the same copy with the diff applied
//
// Both sides load the plugin the same way, so the only difference between the
// two sessions is the proposal. evals/bench/run.sh keeps its isolation flags
// (user settings, MCP servers and session persistence all off), which the
// phase-6 spike showed leaves the copy as the only non-builtin plugin.
//
// One sample per task: a single flip is noise. The cap is checked between
// tasks (a bench task cannot be stopped half-way without wasting what it
// spent), and like the promptfoo step this one never touches the proposal.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// SourceBench is proposal_probes.source for a frozen-bench probe.
const SourceBench = "bench"

// The bench step's own knobs. A bench task is a multi-turn session, so its cap
// is separate from (and larger than) the promptfoo step's. Model and effort
// default to the promptfoo step's opus tier, so the two steps grade the same
// configuration.
const (
	BenchMaxUSDEnv     = "SWARMERY_PROBE_BENCH_MAX_USD"
	DefaultBenchMaxUSD = 5.00
	BenchModelEnv      = "SWARMERY_PROBE_BENCH_MODEL"
	BenchEffortEnv     = "SWARMERY_PROBE_BENCH_EFFORT"
)

// BenchMaxUSD resolves the bench step's cost cap from the environment.
func BenchMaxUSD() float64 { return envUSD(BenchMaxUSDEnv, DefaultBenchMaxUSD) }

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// BenchTask is one task of the frozen bench.
type BenchTask struct {
	ID     string // the task's directory name, e.g. b01-bugfix
	Prompt string // prompt.md — the judge's "task"
}

// BenchSide is how one side's session loads the target.
type BenchSide struct {
	PluginDir string // the plugin copy, loaded for the session only
	Agent     string // <plugin>:<agent>, the agent the session runs as
}

// BenchRun is one task on one side, as evals/bench/run.sh recorded it.
type BenchRun struct {
	Pass      bool
	Error     bool // the run itself broke (run.sh's error:true): not a model result
	TimedOut  bool
	USD       float64
	Reply     string // the session's final reply
	DiffStat  string // git diff --stat of the scratch repo against its fixture
	CheckTail string // the last lines of check.sh, or the error's reason
}

// BenchRunner runs one bench task for one side inside a prepared tree.
// Production is ScriptBenchRunner; tests fake it.
type BenchRunner interface {
	RunTask(ctx context.Context, tree string, side BenchSide, t BenchTask) (BenchRun, error)
}

// benchMeta is evals/bench/tasks/<id>/meta.json. No file, or an empty agents
// list, means the task applies to every agent.
type benchMeta struct {
	Agents []string `json:"agents"`
}

// loadBenchTasks lists the bench tasks tagged for agent (the target file's
// stem), in id order. None is an empty list plus the no_suite reason.
func loadBenchTasks(tree, agent string) ([]BenchTask, string, error) {
	dir := filepath.Join(tree, "evals", "bench", "tasks")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "no frozen bench at evals/bench/tasks", nil
	}
	if err != nil {
		return nil, "", err
	}
	var tasks []BenchTask
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		prompt, err := os.ReadFile(filepath.Join(dir, e.Name(), "prompt.md"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		ok, err := taggedFor(filepath.Join(dir, e.Name(), "meta.json"), agent)
		if err != nil {
			return nil, "", fmt.Errorf("evals/bench/tasks/%s: %w", e.Name(), err)
		}
		if ok {
			tasks = append(tasks, BenchTask{ID: e.Name(), Prompt: string(prompt)})
		}
	}
	sort.Slice(tasks, func(i, k int) bool { return tasks[i].ID < tasks[k].ID })
	if len(tasks) == 0 {
		return nil, fmt.Sprintf("no bench task in evals/bench/tasks is tagged for %s", agent), nil
	}
	return tasks, "", nil
}

func taggedFor(metaPath, agent string) (bool, error) {
	raw, err := os.ReadFile(metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var m benchMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return false, fmt.Errorf("parse meta.json: %w", err)
	}
	if len(m.Agents) == 0 {
		return true, nil
	}
	for _, a := range m.Agents {
		if strings.TrimSpace(a) == agent {
			return true, nil
		}
	}
	return false, nil
}

// benchSide resolves the plugin copy and the agent reference for target in
// tree: the plugin's manifest name (else the pack dir) and the agent's
// frontmatter name (else the file stem), read from THIS tree so a proposal
// that renames its agent still runs as itself.
func benchSide(tree, target string) (BenchSide, error) {
	pack := strings.Split(target, "/")[1]
	dir := filepath.Join(tree, "plugins", pack)
	plugin := pack
	raw, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil {
		return BenchSide{}, fmt.Errorf("plugin copy %s has no .claude-plugin/plugin.json: %w", "plugins/"+pack, err)
	}
	var m struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &m) == nil && strings.TrimSpace(m.Name) != "" {
		plugin = strings.TrimSpace(m.Name)
	}
	agent := strings.TrimSuffix(filepath.Base(target), ".md")
	if body, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(target))); err == nil {
		if n := frontmatterName(string(body)); n != "" {
			agent = n
		}
	}
	return BenchSide{PluginDir: dir, Agent: plugin + ":" + agent}, nil
}

// frontmatterName is the `name:` of a leading --- block, or "".
func frontmatterName(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			return ""
		}
		if v, ok := strings.CutPrefix(l, "name:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// RunBench probes one proposal on the frozen bench and returns what it spent.
// Like Run, it writes proposal_probes rows (source=bench) and nothing else.
func (s *Service) RunBench(ctx context.Context, jobID string, p Proposal) (float64, error) {
	target := p.target()
	if p.TargetKind == "skill" || !isAgentFile(target) {
		return 0, s.insert(jobID, p.ID, Row{
			Source: SourceBench, BlindChoice: ChoiceNoSuite,
			Reason: fmt.Sprintf("no bench: a bench session runs as an agent, and %s is not a marketplace agent definition (plugins/<pack>/agents/<name>.md)",
				orDash(target)),
		})
	}
	ws, err := prepare(ctx, s.Source, p)
	if err != nil {
		return 0, err
	}
	defer ws.cleanup()

	tasks, reason, err := loadBenchTasks(ws.base, strings.TrimSuffix(filepath.Base(target), ".md"))
	if err != nil {
		return 0, err
	}
	if len(tasks) == 0 {
		return 0, s.insert(jobID, p.ID, Row{Source: SourceBench, BlindChoice: ChoiceNoSuite, Reason: reason})
	}
	base, err := benchSide(ws.base, target)
	if err != nil {
		return 0, err
	}
	variant, err := benchSide(ws.variant, target)
	if err != nil {
		return 0, err
	}

	maxUSD := s.CapUSD(SourceBench)
	spent := 0.0
	for i, t := range tasks {
		if spent >= maxUSD {
			for _, rest := range tasks[i:] {
				if err := s.insert(jobID, p.ID, Row{
					Source: SourceBench, TaskID: rest.ID, BlindChoice: ChoiceCapped,
					Reason: fmt.Sprintf("cost cap reached: $%.2f spent of $%.2f (%s)", spent, maxUSD, BenchMaxUSDEnv),
				}); err != nil {
					return spent, err
				}
			}
			return spent, nil
		}
		row, usd, err := s.runBenchTask(ctx, ws, base, variant, t, spent, maxUSD)
		spent += usd
		if ierr := s.insert(jobID, p.ID, row); ierr != nil {
			return spent, ierr
		}
		if err != nil {
			// run.sh itself could not run (bash, jq, git or claude missing): it
			// would fail the same way on every remaining task.
			return spent, err
		}
	}
	return spent, nil
}

// runBenchTask runs one task on both sides and, when check.sh agrees on both,
// asks the blind judge. The row is always insertable; a non-nil error means the
// job should stop.
func (s *Service) runBenchTask(ctx context.Context, ws workspace, base, variant BenchSide, t BenchTask, spent, maxUSD float64) (Row, float64, error) {
	row := Row{Source: SourceBench, TaskID: t.ID}
	b, err := s.Bench.RunTask(ctx, ws.base, base, t)
	if err != nil {
		row.BlindChoice, row.Reason, row.USD = ChoiceSkipped, "base run failed: "+clip(err.Error(), 400), b.USD
		return row, b.USD, err
	}
	v, err := s.Bench.RunTask(ctx, ws.variant, variant, t)
	usd := b.USD + v.USD
	row.USD = usd
	if err != nil {
		row.BlindChoice, row.Reason = ChoiceSkipped, "variant run failed: "+clip(err.Error(), 400)
		return row, usd, err
	}
	if b.Error || v.Error {
		// A broken session (an API error, a non-success result) is not a model
		// result on either side; re-run rather than read anything into it.
		row.BlindChoice = ChoiceSkipped
		row.Reason = "session error, not a model result — re-run: " + clip(sessionErrors(b, v), 400)
		return row, usd, nil
	}
	row.BasePass, row.VariantPass = &b.Pass, &v.Pass

	switch {
	case b.Pass != v.Pass:
		row.BlindChoice = ChoiceSkipped
		row.Reason = "check.sh disagrees, so the deterministic result decides; no judge"
	case spent+usd >= maxUSD:
		row.BlindChoice = ChoiceCapped
		row.Reason = fmt.Sprintf("cost cap reached before the blind judge: $%.2f of $%.2f", spent+usd, maxUSD)
	default:
		choice, reason, jusd := s.judge(ctx,
			CaseRun{Task: t.Prompt, Output: benchAnswer(b)},
			CaseRun{Task: t.Prompt, Output: benchAnswer(v)})
		usd += jusd
		row.BlindChoice, row.Reason = choice, reason
	}
	if note := checkNotes(b, v); note != "" {
		row.Reason = strings.TrimSpace(row.Reason + "\n" + note)
	}
	row.USD = usd
	return row, usd, nil
}

// benchAnswer is what the judge reads for one side: the final reply plus the
// shape of the change it left in the scratch repo.
func benchAnswer(r BenchRun) string {
	stat := strings.TrimSpace(r.DiffStat)
	if stat == "" {
		stat = "(no file changed)"
	}
	reply := strings.TrimSpace(r.Reply)
	if reply == "" {
		reply = "(no final reply)"
	}
	return reply + "\n\nFiles changed (git diff --stat):\n" + stat
}

func sessionErrors(b, v BenchRun) string {
	var parts []string
	if b.Error {
		parts = append(parts, "base: "+oneLine(b.CheckTail))
	}
	if v.Error {
		parts = append(parts, "variant: "+oneLine(v.CheckTail))
	}
	return strings.Join(parts, " · ")
}

// checkNotes records why a side failed (timeout, or check.sh's first failed
// assertion), so a card reader sees the cause without the result files.
func checkNotes(b, v BenchRun) string {
	note := func(side string, r BenchRun) string {
		switch {
		case r.TimedOut:
			return side + ": timed out"
		case !r.Pass && strings.TrimSpace(r.CheckTail) != "":
			return side + ": " + oneLine(r.CheckTail)
		}
		return ""
	}
	var parts []string
	for _, n := range []string{note("base", b), note("variant", v)} {
		if n != "" {
			parts = append(parts, n)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "check.sh — " + strings.Join(parts, " · ")
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clip(s, 200)
}

// benchTimeout bounds one run.sh invocation: run.sh's own per-task limit
// (BENCH_TIMEOUT, 900 s by default) plus room for the check and teardown.
const benchTimeout = 20 * time.Minute

// ScriptBenchRunner runs one task with the tree's own evals/bench/run.sh.
type ScriptBenchRunner struct {
	// Claude overrides the resolved claude binary (tests pass a stub).
	Claude string
	// Timeout overrides benchTimeout when > 0.
	Timeout time.Duration
}

// RunTask implements BenchRunner.
func (r ScriptBenchRunner) RunTask(ctx context.Context, tree string, side BenchSide, t BenchTask) (BenchRun, error) {
	work, err := os.MkdirTemp(filepath.Dir(tree), "bench-"+filepath.Base(tree)+"-*")
	if err != nil {
		return BenchRun{}, err
	}
	defer os.RemoveAll(work)
	tmp := filepath.Join(work, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return BenchRun{}, err
	}
	claude := r.Claude
	if claude == "" {
		if claude, err = claudebin.Resolve(); err != nil {
			return BenchRun{}, err
		}
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = benchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := filepath.Join(work, "result.json")
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(tree, "evals", "bench", "run.sh"), out,
		envOr(BenchModelEnv, opusTierModel), envOr(BenchEffortEnv, opusTierEffort), t.ID)
	cmd.Dir = tree
	// INT lets run.sh's trap take the session's whole process group down; the
	// wait delay is its one-second grace plus margin.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = benchEnv(claude, side, tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	raw, rerr := os.ReadFile(out)
	if rerr != nil {
		return BenchRun{}, fmt.Errorf("bench run.sh produced no result (%v): %s", runErr, tail(stderr.String(), 600))
	}
	var f struct {
		Tasks []struct {
			ID        string   `json:"id"`
			Pass      bool     `json:"pass"`
			Error     bool     `json:"error"`
			TimedOut  bool     `json:"timedOut"`
			CostUSD   *float64 `json:"costUsd"`
			CheckTail string   `json:"checkTail"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return BenchRun{}, fmt.Errorf("parse bench result: %w", err)
	}
	if len(f.Tasks) != 1 || f.Tasks[0].ID != t.ID {
		return BenchRun{}, fmt.Errorf("bench result holds %d tasks, want only %s", len(f.Tasks), t.ID)
	}
	res := f.Tasks[0]
	run := BenchRun{Pass: res.Pass, Error: res.Error, TimedOut: res.TimedOut, CheckTail: res.CheckTail}
	if res.CostUSD != nil {
		run.USD = *res.CostUSD
	}
	// BENCH_KEEP=1 left the scratch repo at <tmp>/bench.*/<id>.
	if dirs, _ := filepath.Glob(filepath.Join(tmp, "bench.*", t.ID)); len(dirs) == 1 {
		if b, err := os.ReadFile(filepath.Join(dirs[0], ".bench-reply.txt")); err == nil {
			run.Reply = string(b)
		}
		run.DiffStat = diffStat(ctx, dirs[0])
	}
	return run, nil
}

// benchEnv is run.sh's environment: the System project's account binding (as
// for every daemon utility run), the claude binary, the side's plugin and
// agent, and a private TMPDIR so the kept scratch repo is findable. It drops
// AGENT_PROJECT so the copy's hooks cannot attribute a bench session to a
// workspace project.
func benchEnv(claude string, side BenchSide, tmp string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "AGENT_PROJECT", "TMPDIR", "BENCH_CLAUDE", "BENCH_PLUGIN_DIR", "BENCH_AGENT", "BENCH_KEEP":
			continue
		}
		env = append(env, kv)
	}
	if dir, ok := systemspawn.Dir(); ok {
		env = claudeacct.SpawnEnvFor(env, dir)
	}
	return append(env,
		"BENCH_CLAUDE="+claude,
		"BENCH_PLUGIN_DIR="+side.PluginDir,
		"BENCH_AGENT="+side.Agent,
		"BENCH_KEEP=1",
		"TMPDIR="+tmp,
	)
}

// diffStat is `git diff --stat` of everything the session changed in the
// scratch repo (new files included) against the fixture commit. The scratch
// repo is throwaway, so staging into its index is fine.
func diffStat(ctx context.Context, dir string) string {
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	root, err := git("rev-list", "--max-parents=0", "HEAD")
	if err != nil || root == "" {
		return "(diff unavailable)"
	}
	if _, err := git("add", "-A"); err != nil {
		return "(diff unavailable)"
	}
	stat, err := git("diff", "--cached", "--stat", strings.Fields(root)[0])
	if err != nil {
		return "(diff unavailable)"
	}
	return clip(stat, 1500)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
