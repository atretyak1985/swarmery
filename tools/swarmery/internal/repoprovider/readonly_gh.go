package repoprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
)

// ErrNoStoredToken is returned by NewReadOnlyGH when the daemon holds no GitHub
// token for the host. A read-only reader never falls back to the operator's
// own `gh auth login`: a routine that runs every two hours must not quietly
// spend whichever account the operator's shell happens to be logged in as.
var ErrNoStoredToken = errors.New("no GitHub token stored for this host")

// ErrWriteRefused is returned, before gh runs, for any call ReadOnlyGH does
// not recognise as a read.
var ErrWriteRefused = errors.New("read-only gh: refused")

// ReadOnlyGH runs `gh api` GET requests against one host with the daemon's
// stored token and isolated GH_CONFIG_DIR (credstore.Env), and refuses
// everything else before a process starts.
//
// The fence is an allowlist, not a denylist of write verbs: the only gh
// subcommand is `api`; the method may only be GET or HEAD; the field and
// input flags are refused outright, because `gh api` silently switches to
// POST the moment a -f/-F/--input appears; `graphql` is refused, since a
// mutation travels as a POST to it; and a header may only be Accept or
// X-GitHub-Api-Version, so no X-HTTP-Method-Override rides in.
type ReadOnlyGH struct {
	exec Exec
	host string
	env  []string
}

// NewReadOnlyGH builds the reader for host over ex, with env(host) as the
// credential env delta (production: credstore.Env — see StoredReadOnlyGH).
// ErrNoStoredToken when that delta carries no GitHub token.
func NewReadOnlyGH(ex Exec, host string, env func(host string) []string) (*ReadOnlyGH, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "github.com"
	}
	var delta []string
	if env != nil {
		delta = env(host)
	}
	if !hasGitHubToken(delta) {
		return nil, fmt.Errorf("%w: %s", ErrNoStoredToken, host)
	}
	return &ReadOnlyGH{exec: ex, host: host, env: delta}, nil
}

// StoredReadOnlyGH is NewReadOnlyGH over the daemon's credential store.
func StoredReadOnlyGH(ex Exec, host string) (*ReadOnlyGH, error) {
	return NewReadOnlyGH(ex, host, credstore.Env)
}

// Host is the host the reader is bound to.
func (g *ReadOnlyGH) Host() string { return g.host }

// Run executes `gh <args…>` once CheckReadOnly accepts args, with
// --hostname pinned to the reader's host when the call names none. It returns
// stdout; a failure is Classify'd (redacted, bounded).
func (g *ReadOnlyGH) Run(ctx context.Context, args ...string) (string, error) {
	if err := CheckReadOnly(args); err != nil {
		return "", err
	}
	if !hasHostnameFlag(args) && g.host != "github.com" {
		args = append(append([]string(nil), args...), "--hostname", g.host)
	}
	ctx, cancel := NetCtx(ctx)
	defer cancel()
	stdout, stderr, err := g.exec.Run(ctx, "", g.env, "gh", args...)
	if err != nil {
		return "", Classify(stderr, err)
	}
	return stdout, nil
}

// API is Run("api", endpoint, flags…).
func (g *ReadOnlyGH) API(ctx context.Context, endpoint string, flags ...string) (string, error) {
	return g.Run(ctx, append([]string{"api", endpoint}, flags...)...)
}

// readFlags are the `gh api` flags a read may carry, mapped to whether the
// flag takes a value.
var readFlags = map[string]bool{
	"--paginate": false,
	"--slurp":    false,
	"--include":  false,
	"-i":         false,
	"--silent":   false,
	"--verbose":  false,
	"--jq":       true,
	"-q":         true,
	"--template": true,
	"-t":         true,
	"--hostname": true,
	"--cache":    true,
	"--method":   true,
	"-X":         true,
	"--header":   true,
	"-H":         true,
}

// readHeaders are the request headers a read may set (lowercase names).
var readHeaders = map[string]bool{
	"accept":               true,
	"x-github-api-version": true,
}

// CheckReadOnly reports whether a gh argv is a read ReadOnlyGH may run; the
// error wraps ErrWriteRefused and names the offending argument. Pure.
func CheckReadOnly(args []string) error {
	if len(args) == 0 || args[0] != "api" {
		first := ""
		if len(args) > 0 {
			first = args[0]
		}
		return fmt.Errorf("%w: only `gh api` may run, not %q", ErrWriteRefused, first)
	}
	endpoints := 0
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			endpoints++
			ep := strings.TrimPrefix(a, "/")
			if ep == "graphql" || strings.HasPrefix(ep, "graphql?") {
				return fmt.Errorf("%w: graphql (a mutation is a POST to it)", ErrWriteRefused)
			}
			continue
		}
		name, value, inline := splitFlag(a)
		takesValue, known := readFlags[name]
		if !known {
			return fmt.Errorf("%w: flag %s", ErrWriteRefused, name)
		}
		if takesValue && !inline {
			if i+1 >= len(args) {
				return fmt.Errorf("%w: flag %s needs a value", ErrWriteRefused, name)
			}
			i++
			value = args[i]
		}
		switch name {
		case "--method", "-X":
			if m := strings.ToUpper(strings.TrimSpace(value)); m != "GET" && m != "HEAD" {
				return fmt.Errorf("%w: method %s", ErrWriteRefused, value)
			}
		case "--header", "-H":
			hname, _, _ := strings.Cut(value, ":")
			if !readHeaders[strings.ToLower(strings.TrimSpace(hname))] {
				return fmt.Errorf("%w: header %q", ErrWriteRefused, strings.TrimSpace(hname))
			}
		}
	}
	if endpoints != 1 {
		return fmt.Errorf("%w: want exactly one endpoint, got %d", ErrWriteRefused, endpoints)
	}
	return nil
}

// splitFlag splits "--name=value" and the attached short form "-XPOST" into
// name and value; inline reports whether the value was attached.
func splitFlag(a string) (name, value string, inline bool) {
	if strings.HasPrefix(a, "--") {
		if n, v, ok := strings.Cut(a, "="); ok {
			return n, v, true
		}
		return a, "", false
	}
	if len(a) > 2 {
		return a[:2], strings.TrimPrefix(a[2:], "="), true
	}
	return a, "", false
}

func hasHostnameFlag(args []string) bool {
	for _, a := range args {
		if a == "--hostname" || strings.HasPrefix(a, "--hostname=") {
			return true
		}
	}
	return false
}

// hasGitHubToken reports whether an env delta carries a non-empty GitHub token.
func hasGitHubToken(env []string) bool {
	for _, kv := range env {
		for _, k := range []string{credstore.GitHubTokenKey, credstore.GitHubEnterpriseTokenKey} {
			if v, ok := strings.CutPrefix(kv, k+"="); ok && strings.TrimSpace(v) != "" {
				return true
			}
		}
	}
	return false
}
