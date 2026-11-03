package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// Judge answers one blind comparison prompt and reports what it cost.
// Production is ClaudeJudge; tests fake it.
type Judge interface {
	Judge(ctx context.Context, prompt string) (raw string, usd float64, err error)
}

// JudgeModelEnv names the blind judge's model; DefaultJudgeModel is the same
// pin as trajjudge.DefaultModel — the fleet's judge tier. Full ID, not an
// alias: aliases re-resolve over time.
const (
	JudgeModelEnv     = "SWARMERY_PROBE_JUDGE_MODEL"
	DefaultJudgeModel = "claude-sonnet-5"
)

// JudgeModel resolves the judge model from the environment.
func JudgeModel() string {
	if v := strings.TrimSpace(os.Getenv(JudgeModelEnv)); v != "" {
		return v
	}
	return DefaultJudgeModel
}

// DefaultJudgeEffort pins the judge's reasoning depth: it reads two short
// answers against a fixed five-step order and writes six lines — bounded
// reading, like trajjudge. judgeEffortEnv is the site knob (claudeflags owns
// the resolution and the "off" escape hatch). SWARMERY_PROBE_EFFORT is taken by
// the project-config probe, hence the longer name.
const (
	DefaultJudgeEffort = "low"
	judgeEffortEnv     = "SWARMERY_PROBE_JUDGE_EFFORT"
)

const judgeTimeout = 3 * time.Minute

// ClaudeJudge runs `claude -p --output-format json` with the prompt on stdin,
// from the System project (systemspawn), like every other daemon utility run.
// JSON output, unlike its text-mode twins, because a probe has a cost cap and
// the CLI reports total_cost_usd only there.
type ClaudeJudge struct {
	// Model overrides JudgeModel() when non-empty.
	Model string
}

// Judge implements Judge.
func (j ClaudeJudge) Judge(ctx context.Context, prompt string) (string, float64, error) {
	ctx, cancel := context.WithTimeout(ctx, judgeTimeout)
	defer cancel()
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", 0, err
	}
	model := j.Model
	if model == "" {
		model = JudgeModel()
	}
	args := []string{"-p", "--model", model}
	args = append(args, claudeflags.EffortArgs(judgeEffortEnv, DefaultJudgeEffort)...)
	args = append(args, "--output-format", "json", "--setting-sources", "project,local")
	cmd := exec.CommandContext(ctx, bin, args...)
	systemspawn.Attach(cmd)
	out, err := systemspawn.Run(ctx, cmd, prompt)
	if err != nil {
		return "", 0, err
	}
	return parseCLIJSON(out)
}

// parseCLIJSON reads `claude -p --output-format json`: the answer and its cost.
func parseCLIJSON(out string) (string, float64, error) {
	var d struct {
		Result       string  `json:"result"`
		IsError      bool    `json:"is_error"`
		Subtype      string  `json:"subtype"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &d); err != nil {
		return "", 0, fmt.Errorf("unparseable claude -p output: %s", clip(out, 300))
	}
	if d.IsError {
		return "", d.TotalCostUSD, fmt.Errorf("claude -p error: %s", clip(firstNonEmpty(d.Result, d.Subtype), 300))
	}
	return d.Result, d.TotalCostUSD, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
