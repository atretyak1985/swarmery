package plainsum

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// claudeTimeout bounds one headless retelling run.
const claudeTimeout = 5 * time.Minute

// DefaultModel pins retelling runs that carry no override. Retelling a doc in
// plain words is summarisation over material the prompt already carries — the
// same shape of work as a handoff note — so it takes the handoff's model.
// Full ID, not an alias: aliases re-resolve over time.
const DefaultModel = "claude-sonnet-5"

// DefaultEffort pins how hard a retelling run thinks; see handoff.DefaultEffort.
const DefaultEffort = "low"

// modelEnv and effortEnv are this spawn site's knobs.
const (
	modelEnv  = "SWARMERY_PLAIN_MODEL"
	effortEnv = "SWARMERY_PLAIN_EFFORT"
)

// ResolveModel is the model a run will use: SWARMERY_PLAIN_MODEL, else
// DefaultModel.
func ResolveModel() string {
	if m := os.Getenv(modelEnv); m != "" {
		return m
	}
	return DefaultModel
}

// ClaudeRunner runs `claude -p --model <id> --effort <e> --output-format text`
// with the prompt on stdin. Twin of internal/handoff.ClaudeRunner — keep the
// flag order in lockstep with it.
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it).
	Timeout time.Duration
	// Model overrides ResolveModel() when non-empty.
	Model string
	// Effort overrides the resolved default when non-empty.
	Effort string
}

func (r ClaudeRunner) Run(ctx context.Context, prompt string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = claudeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	model := r.Model
	if model == "" {
		model = ResolveModel()
	}
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", err
	}
	args := []string{"-p", "--model", model}
	args = append(args, claudeflags.EffortArgsWith(r.Effort, effortEnv, DefaultEffort)...)
	args = append(args, "--output-format", "text", "--setting-sources", "project,local")
	cmd := exec.CommandContext(ctx, bin, args...)
	systemspawn.Attach(cmd)
	return systemspawn.Run(ctx, cmd, prompt)
}
