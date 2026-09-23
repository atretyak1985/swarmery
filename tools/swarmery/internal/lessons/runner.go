package lessons

// The headless runner behind candidate generation. Shaped after
// internal/retroanalysis/runner.go: same binary resolution, same flag order,
// same account/cwd attachment, same stderr+stdout error contract.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// Runner executes one generation prompt and returns the model's raw stdout.
// Mocked in every test — no real claude invocation outside production.
type Runner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// claudeTimeout bounds one generation run. The input is small and the answer
// is at most two sentences of JSON; anything slower is stuck.
const claudeTimeout = 3 * time.Minute

// ClaudeRunner runs `claude -p --output-format text` with the prompt on stdin.
//
// It passes NO --permission-mode, deliberately: the contract is stdout JSON and
// internal/lessons writes the rows. internal/claudeflags' spawn-site scanner
// records that decision in readOnlySites.
type ClaudeRunner struct {
	// Timeout overrides claudeTimeout when > 0 (tests shrink it).
	Timeout time.Duration
	// Model overrides EnvModel / DefaultModel when non-empty.
	Model string
	// Effort overrides EnvEffort / DefaultEffort when non-empty.
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
		model = strings.TrimSpace(os.Getenv(EnvModel))
	}
	if model == "" {
		model = DefaultModel
	}
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", err
	}
	args := []string{"-p", "--model", model}
	args = append(args, claudeflags.EffortArgsWith(r.Effort, EnvEffort, DefaultEffort)...)
	args = append(args, "--output-format", "text", "--setting-sources", "project,local")
	cmd := exec.CommandContext(ctx, bin, args...)
	systemspawn.Attach(cmd)
	return systemspawn.Run(ctx, cmd, prompt)
}
