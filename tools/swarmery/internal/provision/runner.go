// Package provision owns the "enable pack → install + generate" pipeline: a
// mocked Runner (the only seam touching the real claude binary), a pack→action
// policy map, and a Service that enqueues single-flight jobs with durable status.
package provision

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
)

// Runner executes the claude binary. It is the ONLY seam that touches a real
// process — every test injects a stub. Mirrors internal/improve.Runner.
type Runner interface {
	// Claude runs `claude <args...>` with cwd=dir (dir=="" inherits the daemon
	// cwd), feeding stdin (""=none), and returns trimmed stdout. A non-nil error
	// carries a stderr tail. The daemon's launchd PATH must contain `claude`
	// (already ensured for the serena/graphify tool dashboards).
	Claude(ctx context.Context, dir, stdin string, args ...string) (string, error)
}

// stderrTailBytes caps how much captured stderr lands in the error (and thus in
// provision_jobs.error).
const stderrTailBytes = 4096

// defaultModel pins headless generator runs: without --model the CLI inherits
// the account default (Fable-5 here — 2× the Opus price). Full ID, not an
// alias — aliases re-resolve over time.
const defaultModel = "claude-opus-5"

// permEnv is this spawn site's --permission-mode knob (internal/claudeflags owns
// the resolution and the "off" escape hatch). Used by the generate step in
// service.go, whose product is files on disk.
const permEnv = "SWARMERY_PROVISION_PERMISSION_MODE"

// ClaudeRunner is the production Runner: it resolves the claude binary through
// internal/claudebin, the same pattern internal/improve and every other
// daemon-launched spawn use — a bare PATH lookup is not enough under launchd.
type ClaudeRunner struct{}

func (ClaudeRunner) Claude(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	// launchd hands the daemon a minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin) that
	// omits every usual install dir, so a bare exec of "claude" fails with ENOENT
	// under the service while working in the operator's shell. Resolve explicitly.
	bin, err := claudebin.Resolve()
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	// Resolving the account from `dir` is correct HERE — and only here and in
	// planning. A provision run's dir IS the project path, so it carries the
	// project's .claude/settings.local.json. dispatch and verify look the same but
	// are not: their cwd is a worktree with no settings file, which is why they
	// take the key from the caller instead (plan A3).
	//
	// dir=="" means "inherit the daemon cwd" and names no project at all —
	// resolving a binding for "" would probe a RELATIVE .claude/settings.local.json
	// and could bind the run to whatever project the daemon happens to sit in, so
	// SpawnEnvFor hands os.Environ() back untouched for it. A bound project gets
	// its config dir AND its secret store — the same composition every other
	// swarmery spawn uses.
	if dir != "" {
		cmd.Dir = dir
	}
	// An unbound project ⇒ a byte-identical copy of os.Environ().
	cmd.Env = claudeacct.SpawnEnvFor(os.Environ(), dir)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	joined := "claude " + strings.Join(args, " ")
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s timed out; stderr: %s", joined, tail(errb.String(), stderrTailBytes))
		}
		return "", fmt.Errorf("%s: %w; stderr: %s", joined, err, tail(errb.String(), stderrTailBytes))
	}
	return strings.TrimSpace(out.String()), nil
}

// tail returns the last ≤ n bytes of s, trimmed.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
