package repoprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidRef is returned for a branch or remote name that could change the
// meaning of a git or CLI command line (a leading "-" or "+", a refspec ":").
var ErrInvalidRef = errors.New("invalid branch or remote name")

// ValidRef refuses a name git would read as an option ("-…"), a forced
// refspec ("+…") or a src:dst refspec — the ways a branch name could turn a
// plain push into a forced or redirected one. Whitespace is refused too.
func ValidRef(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "+") {
		return false
	}
	return !strings.ContainsAny(name, ": \t\n")
}

// NetCtx gives a network-bound call NetTimeout unless the caller already set a
// deadline.
func NetCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, NetTimeout)
}

// GitPush is the push every provider shares: `git push -u <remote> <branch>`
// in t.RepoDir, with env as the env delta (the provider's credentialed env, or
// nil for the operator's own git credentials). An empty t.RemoteName means
// DefaultRemote.
//
// Never --force, and a branch or remote name that would smuggle a force, an
// option or a refspec in is refused with ErrInvalidRef before git runs. A
// failure comes back classified and redacted (see Classify).
func GitPush(ctx context.Context, ex Exec, env []string, t Target, branch string) error {
	remote := t.RemoteName
	if remote == "" {
		remote = DefaultRemote
	}
	if !ValidRef(branch) || !ValidRef(remote) {
		return fmt.Errorf("%w: %q %q", ErrInvalidRef, remote, branch)
	}
	ctx, cancel := NetCtx(ctx)
	defer cancel()
	_, stderr, err := ex.Run(ctx, t.RepoDir, env, "git", "push", "-u", remote, branch)
	return Classify(stderr, err)
}
