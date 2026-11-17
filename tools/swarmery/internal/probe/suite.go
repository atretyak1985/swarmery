package probe

// The agent's promptfoo suite, cut into one-case runs so the cost cap can stop
// the job between cases, and the runner that executes one case through the
// repo's own evals/providers/claude-cli.js (a Claude subscription, no API key).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// suitePath is evals/agents/<agent>.yaml for plugins/<pack>/agents/<agent>.md.
func suitePath(target string) string {
	return filepath.Join("evals", "agents", strings.TrimSuffix(filepath.Base(target), ".md")+".yaml")
}

// loadSuite reads the target's suite from the base tree and keeps the cases
// that exercise the target file (a suite can also reference other agents).
// No suite, or no case naming the target, is an empty list plus the reason the
// no_suite row records — not an error.
func loadSuite(base, target string) ([]Case, string, error) {
	rel := suitePath(target)
	raw, err := os.ReadFile(filepath.Join(base, rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "no promptfoo suite at " + filepath.ToSlash(rel), nil
	}
	if err != nil {
		return nil, "", err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", rel, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode {
		return nil, "", fmt.Errorf("%s: expected a YAML list of test cases", rel)
	}
	var cases []Case
	for i, item := range doc.Content[0].Content {
		one := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{item}}
		b, err := yaml.Marshal(one)
		if err != nil {
			return nil, "", fmt.Errorf("%s case %d: %w", rel, i+1, err)
		}
		if !bytes.Contains(b, []byte(target)) {
			continue
		}
		id := caseDescription(item)
		if id == "" {
			id = fmt.Sprintf("case-%d", i+1)
		}
		cases = append(cases, Case{Index: i, ID: id, YAML: b})
	}
	if len(cases) == 0 {
		return nil, fmt.Sprintf("%s has no case that loads %s", filepath.ToSlash(rel), target), nil
	}
	return cases, "", nil
}

func caseDescription(n *yaml.Node) string {
	if n.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "description" {
			return strings.TrimSpace(n.Content[i+1].Value)
		}
	}
	return ""
}

// Tier models the generated config maps the suite's provider labels to. They
// mirror evals/promptfooconfig.yaml (opus-tier, haiku-tier, sonnet judge for
// llm-rubric) on the claude-cli provider, like evals/sweep/run.sh. The opus
// tier runs at medium: the effort the step-7.2 sweep settled most suited agents
// on, and what keeps one case inside the default cap.
const (
	opusTierModel    = "claude-opus-5-5"
	opusTierEffort   = "medium"
	haikuTierModel   = "claude-haiku-4-5-20251001"
	rubricJudgeModel = "claude-sonnet-5"
)

// PromptfooBinEnv points at a promptfoo executable; unset → `npx --yes
// promptfoo@<the version pinned in evals/package.json>`.
const PromptfooBinEnv = "SWARMERY_PROBE_PROMPTFOO_BIN"

// caseTimeout bounds one promptfoo run of one case (the provider's own
// per-call timeout is 10 minutes; a case can carry two providers plus rubrics).
const caseTimeout = 25 * time.Minute

// PromptfooRunner runs one case with `promptfoo eval` inside a prepared tree.
type PromptfooRunner struct {
	// Timeout overrides caseTimeout when > 0.
	Timeout time.Duration
}

// RunCase implements SuiteRunner.
func (r PromptfooRunner) RunCase(ctx context.Context, tree string, c Case) (CaseRun, error) {
	dir := filepath.Join(tree, "evals", "probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return CaseRun{}, err
	}
	stem := fmt.Sprintf("case-%02d", c.Index+1)
	caseFile := filepath.Join(dir, stem+".yaml")
	if err := os.WriteFile(caseFile, c.YAML, 0o644); err != nil {
		return CaseRun{}, err
	}
	// The suites spell their vars as file://../../plugins/…, resolved against
	// the CONFIG's directory, so the config lives two levels below the tree
	// root — evals/probe/ — exactly like evals/sweep/*.gen.yaml.
	cfg := filepath.Join(dir, stem+".config.yaml")
	if err := os.WriteFile(cfg, []byte(probeConfig(tree, caseFile)), 0o644); err != nil {
		return CaseRun{}, err
	}
	out := filepath.Join(dir, stem+".json")

	argv, err := promptfooArgv(tree)
	if err != nil {
		return CaseRun{}, err
	}
	argv = append(argv, "eval", "-c", cfg, "-o", out, "--no-cache", "-j", "1", "--no-progress-bar")

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = caseTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = filepath.Join(tree, "evals")
	cmd.Env = promptfooEnv(tree)
	var combined bytes.Buffer
	cmd.Stdout, cmd.Stderr = &combined, &combined
	runErr := cmd.Run()

	// promptfoo exits non-zero when a case FAILS — that is a result, not an
	// error. Only a missing or unreadable results file is.
	raw, rerr := os.ReadFile(out)
	if rerr != nil {
		tail := combined.String()
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		return CaseRun{}, fmt.Errorf("promptfoo produced no results (%v): %s", runErr, strings.TrimSpace(tail))
	}
	return parseResults(raw)
}

// probeConfig is the generated promptfoo config for one case in one tree.
func probeConfig(tree, caseFile string) string {
	evals := filepath.Join(tree, "evals")
	provider := "file://" + filepath.Join(evals, "providers", "claude-cli.js")
	var b strings.Builder
	fmt.Fprintf(&b, "description: swarmery proposal probe\n")
	fmt.Fprintf(&b, "prompts:\n  - file://%s\n", filepath.Join(evals, "prompts", "agent-chat.json"))
	fmt.Fprintf(&b, "providers:\n")
	fmt.Fprintf(&b, "  - id: %s\n    label: opus-tier\n    config: { model: %s, effort: %s }\n", provider, opusTierModel, opusTierEffort)
	fmt.Fprintf(&b, "  - id: %s\n    label: haiku-tier\n    config: { model: %s }\n", provider, haikuTierModel)
	fmt.Fprintf(&b, "defaultTest:\n  options:\n    provider:\n      id: %s\n      config: { model: %s }\n", provider, rubricJudgeModel)
	fmt.Fprintf(&b, "tests:\n  - file://%s\n", caseFile)
	return b.String()
}

// promptfooArgv resolves the promptfoo command: the env override, else npx at
// the version evals/package.json pins (so a probe runs the same promptfoo the
// suite was written against).
func promptfooArgv(tree string) ([]string, error) {
	if bin := strings.TrimSpace(os.Getenv(PromptfooBinEnv)); bin != "" {
		return []string{bin}, nil
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		return nil, fmt.Errorf("npx not found on PATH (set %s to a promptfoo binary): %w", PromptfooBinEnv, err)
	}
	spec := "promptfoo"
	if v := pinnedPromptfoo(filepath.Join(tree, "evals", "package.json")); v != "" {
		spec += "@" + v
	}
	return []string{npx, "--yes", spec}, nil
}

func pinnedPromptfoo(pkg string) string {
	raw, err := os.ReadFile(pkg)
	if err != nil {
		return ""
	}
	var p struct {
		DevDependencies map[string]string `json:"devDependencies"`
		Dependencies    map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	v := p.DevDependencies["promptfoo"]
	if v == "" {
		v = p.Dependencies["promptfoo"]
	}
	return strings.TrimLeft(v, "^~=")
}

// promptfooEnv is the environment of the promptfoo process and so of every
// `claude -p` the provider spawns: the System project's account binding (the
// same one every other daemon utility run uses), the real claude ahead of the
// accounts-pack shim on PATH, and promptfoo's own state kept inside the temp
// tree instead of ~/.promptfoo.
func promptfooEnv(tree string) []string {
	env := os.Environ()
	if dir, ok := systemspawn.Dir(); ok {
		env = claudeacct.SpawnEnvFor(env, dir)
	}
	if bin, err := claudebin.Resolve(); err == nil {
		env = prependPath(env, filepath.Dir(bin))
	}
	return append(env,
		"PROMPTFOO_CONFIG_DIR="+filepath.Join(tree, ".promptfoo"),
		"PROMPTFOO_DISABLE_TELEMETRY=1",
		"PROMPTFOO_DISABLE_UPDATE=1",
	)
}

func prependPath(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			kv = "PATH=" + dir + string(os.PathListSeparator) + v
			found = true
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "PATH="+dir)
	}
	return out
}

// ── promptfoo results.json (best-effort shape, like internal/evals) ─────────

type pfAssertion struct {
	Type string `json:"type"`
}

type pfComponent struct {
	Pass      bool         `json:"pass"`
	Assertion *pfAssertion `json:"assertion"`
}

type pfGrading struct {
	Pass             bool          `json:"pass"`
	ComponentResults []pfComponent `json:"componentResults"`
}

type pfRow struct {
	Success  bool           `json:"success"`
	Error    string         `json:"error"`
	Cost     float64        `json:"cost"`
	Vars     map[string]any `json:"vars"`
	TestCase struct {
		Vars map[string]any `json:"vars"`
	} `json:"testCase"`
	Response *struct {
		Output any `json:"output"`
	} `json:"response"`
	GradingResult *pfGrading `json:"gradingResult"`
}

type pfOut struct {
	Results struct {
		Results []pfRow `json:"results"`
	} `json:"results"`
}

// modelGraded are the assertion types an LLM grades. They are recorded and
// never decide a case (D9): only the deterministic ones do.
var modelGraded = map[string]bool{
	"llm-rubric": true, "g-eval": true, "factuality": true, "model-graded-closedqa": true,
	"model-graded-factuality": true, "answer-relevance": true, "context-faithfulness": true,
	"context-recall": true, "context-relevance": true, "select-best": true, "search-rubric": true,
	"closed-qa": true,
}

func isModelGraded(t string) bool {
	t = strings.TrimPrefix(t, "not-")
	return modelGraded[t] || strings.HasPrefix(t, "model-graded")
}

// parseResults folds one case's result rows (one per provider the case runs
// on) into a CaseRun: hard pass = every row answered and every non-model-graded
// assertion passed.
func parseResults(raw []byte) (CaseRun, error) {
	var f pfOut
	if err := json.Unmarshal(raw, &f); err != nil {
		return CaseRun{}, fmt.Errorf("parse promptfoo results: %w", err)
	}
	rows := f.Results.Results
	if len(rows) == 0 {
		return CaseRun{}, errors.New("promptfoo results hold no case")
	}
	run := CaseRun{HardPass: true}
	var outputs []string
	for _, r := range rows {
		run.USD += r.Cost
		if run.Task == "" {
			run.Task = varString(r.Vars, "task")
			if run.Task == "" {
				run.Task = varString(r.TestCase.Vars, "task")
			}
		}
		if r.Response != nil {
			outputs = append(outputs, outputText(r.Response.Output))
		}
		if r.Error != "" && (r.Response == nil || outputText(r.Response.Output) == "") {
			run.HardPass = false
			continue
		}
		if r.GradingResult == nil || len(r.GradingResult.ComponentResults) == 0 {
			run.HardPass = run.HardPass && r.Success
			continue
		}
		for _, c := range r.GradingResult.ComponentResults {
			if c.Assertion != nil && isModelGraded(c.Assertion.Type) {
				run.SoftTotal++
				if c.Pass {
					run.SoftPassed++
				}
				continue
			}
			run.HardPass = run.HardPass && c.Pass
		}
	}
	run.Output = strings.Join(outputs, "\n\n---\n\n")
	return run, nil
}

func varString(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

func outputText(v any) string {
	switch o := v.(type) {
	case nil:
		return ""
	case string:
		return o
	default:
		b, _ := json.Marshal(o)
		return string(b)
	}
}
