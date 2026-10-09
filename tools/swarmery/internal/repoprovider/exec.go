package repoprovider

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Timeouts, split by what a command waits on (the same split as the board
// review loop in internal/api/tasks_diff.go): a local git read that takes
// longer than LocalTimeout is a wedged lock; a push or a `gh pr create` waits on
// a network round-trip and a remote-side hook.
const (
	LocalTimeout = 10 * time.Second
	NetTimeout   = 90 * time.Second
	// OutputTail bounds the tool output an error carries.
	OutputTail = 2048
)

// Exec is the process boundary of the package. stdout and stderr are returned
// SEPARATELY: a PR URL is parsed out of stdout, and git's progress chatter on
// stderr must not land inside it.
type Exec interface {
	// Run executes name+args in dir. env is a DELTA appended to the daemon's
	// own environment (last value wins). When ctx carries no deadline the
	// implementation applies LocalTimeout.
	Run(ctx context.Context, dir string, env []string, name string, args ...string) (stdout, stderr string, err error)
	// Look reports whether the binary resolves on PATH.
	Look(name string) error
}

// OSExec is the production Exec: it shells out to the real binaries.
type OSExec struct{}

// Run implements Exec.
func (OSExec) Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, LocalTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%s %s timed out: %w", name, firstArg(args), ctx.Err())
	}
	return stdout.String(), stderr.String(), err
}

// Look implements Exec.
func (OSExec) Look(name string) error {
	_, err := exec.LookPath(name)
	return err
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// FakeExec is a scripted Exec for tests (this package's and its callers').
// Commands are keyed by binary + first argument ("git push", "gh pr") — the
// same keying as the board review loop's fakeReview, so its land tests port
// onto it unchanged.
//
// Resolution order for a Run: Fn (when set and it reports handled), then
// Missing (the binary is absent: an *exec.Error wrapping exec.ErrNotFound),
// then Errs (stderr; the call fails with "exit status 1"), then Out (stdout on
// success; "" when unscripted).
type FakeExec struct {
	mu sync.Mutex

	Out     map[string]string // key → stdout on success
	Errs    map[string]string // key → stderr; the call also fails
	Missing map[string]bool   // binaries reported absent by Look and Run
	// Fn, when set, is consulted first; handled=false falls through to the maps.
	Fn func(dir string, env []string, name string, args []string) (stdout, stderr string, err error, handled bool)

	Calls []string   // "name arg1 arg2 …" per Run
	Dirs  []string   // dir per Run
	Envs  [][]string // env delta per Run
}

// FakeKey is the script key for name+args.
func FakeKey(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + args[0]
}

// Run implements Exec.
func (f *FakeExec) Run(_ context.Context, dir string, env []string, name string, args ...string) (string, string, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, strings.Join(append([]string{name}, args...), " "))
	f.Dirs = append(f.Dirs, dir)
	f.Envs = append(f.Envs, append([]string(nil), env...))
	fn := f.Fn
	f.mu.Unlock()
	if fn != nil {
		if out, errOut, err, ok := fn(dir, env, name, args); ok {
			return out, errOut, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Missing[name] {
		return "", "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	k := FakeKey(name, args)
	if msg, bad := f.Errs[k]; bad {
		return "", msg, fmt.Errorf("exit status 1")
	}
	return f.Out[k], "", nil
}

// Look implements Exec.
func (f *FakeExec) Look(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Missing[name] {
		return &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	return nil
}

// Ran reports whether any recorded call contains substr.
func (f *FakeExec) Ran(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// FirstURL returns the first http(s) URL in s, trimmed of trailing punctuation.
// `gh pr create` prints the PR URL on its own line but also prints progress
// text, and which stream each lands on has changed across gh versions —
// scanning for the URL keeps this working either way. Pure.
func FirstURL(s string) string {
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://") {
			return strings.TrimRight(field, ".,);\"'")
		}
	}
	return ""
}
