package decide

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// ClaudeTimeout bounds one headless classifier call.
const ClaudeTimeout = 60 * time.Second

// ClaudeModel / ClaudeEffort pin the claude backend to the cheapest tier. The
// backend exists for the case where the local server is unreachable; it is OFF
// unless SWARMERY_DECIDE_CLAUDE is set, and even then only for questions that
// set AllowRemote — it is the one path by which evidence leaves the machine.
const (
	ClaudeModel  = "claude-haiku-4-5"
	ClaudeEffort = "low"
)

// Claude is the headless `claude -p` backend. It has no logprobs, so its
// answers are always uncalibrated: the confidence is the model's self-report,
// logged for the dashboard but never enough to clear an active threshold.
type Claude struct {
	// Run executes the prompt and returns stdout. nil ⇒ spawn the CLI.
	Run     func(ctx context.Context, prompt string) (string, error)
	Timeout time.Duration // 0 ⇒ ClaudeTimeout
}

func (c *Claude) Name() string { return BackendClaude }

func (c *Claude) Ask(ctx context.Context, q Question) (Answer, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = ClaudeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := c.Run
	if run == nil {
		run = spawnClaude
	}
	prompt := systemPrompt + ` Add "confidence": a number 0..1.` + "\n\n" + userPrompt(q)
	out, err := run(ctx, prompt)
	if err != nil {
		return Answer{}, err
	}
	value, err := parseAnswer(q, out)
	if err != nil {
		return Answer{}, err
	}
	a := Answer{Value: value}
	if start, end := strings.IndexByte(out, '{'), strings.LastIndexByte(out, '}'); start >= 0 && end > start {
		var obj struct {
			Confidence float64 `json:"confidence"`
		}
		if json.Unmarshal([]byte(out[start:end+1]), &obj) == nil && obj.Confidence > 0 && obj.Confidence <= 1 {
			a.Confidence = obj.Confidence
			a.Probs = map[string]float64{value: obj.Confidence}
		}
	}
	return a, nil
}

// spawnClaude runs the classifier prompt through the CLI, with the same
// launch-context rules every other headless runner in the daemon uses
// (claudebin for PATH, systemspawn for cwd/account and error shaping).
func spawnClaude(ctx context.Context, prompt string) (string, error) {
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, "-p", "--model", ClaudeModel, "--effort", ClaudeEffort,
		"--output-format", "text", "--setting-sources", "project,local")
	systemspawn.Attach(cmd)
	return systemspawn.Run(ctx, cmd, prompt)
}
