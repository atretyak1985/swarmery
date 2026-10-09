// Package github is the GitHub repoprovider.Provider, driven through the `gh`
// CLI (and `git` for the push).
//
// Imports: the parent repoprovider (types, Exec, Classify) and its credstore
// leaf (Redact) — never the other way round (see the repoprovider package doc).
//
// Every call that needs credentials runs with the env delta returned by the
// injected env func (production: credstore.Env — the daemon's own token and an
// isolated GH_CONFIG_DIR). The one deliberate exception is AuthStatus's CLI
// fallback, which asks the OPERATOR's own gh login (nil env) whether there is a
// token worth importing.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
)

const (
	ghBinary  = "gh"
	gitBinary = "git"
	// publicHost is the host whose repos gh addresses as bare OWNER/REPO;
	// any other host (GitHub Enterprise) needs HOST/OWNER/REPO.
	publicHost = "github.com"
	// statusFields is what Status asks `gh pr view --json` for.
	statusFields = "state,isDraft,mergedAt,reviewDecision,statusCheckRollup,url"
)

// ErrNoURL is returned when `gh pr create` exits 0 but prints no PR URL.
var ErrNoURL = errors.New("gh pr create printed no pull-request URL")

// ErrInvalidRef is returned for a branch or remote name that could change the
// meaning of the git command line (a leading "-" or "+", a refspec ":").
var ErrInvalidRef = errors.New("invalid branch or remote name")

// Provider is the GitHub implementation of repoprovider.Provider.
type Provider struct {
	exec repoprovider.Exec
	env  func(host string) []string
	now  func() time.Time
}

var _ repoprovider.Provider = (*Provider)(nil)

// New builds a Provider over ex. env yields the per-host env delta for every
// credentialed call (production: credstore.Env); nil means "no delta".
func New(ex repoprovider.Exec, env func(host string) []string) *Provider {
	if env == nil {
		env = func(string) []string { return nil }
	}
	return &Provider{exec: ex, env: env, now: time.Now}
}

// Kind implements repoprovider.Provider.
func (p *Provider) Kind() repoprovider.Kind { return repoprovider.KindGitHub }

// Terms implements repoprovider.Provider.
func (p *Provider) Terms() repoprovider.Terms { return repoprovider.TermsFor(repoprovider.KindGitHub) }

// netCtx gives a network-bound call NetTimeout unless the caller set a deadline.
func netCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, repoprovider.NetTimeout)
}

func hasToken(env []string) bool {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, credstore.GitHubTokenKey+"="); ok && v != "" {
			return true
		}
	}
	return false
}

// AuthStatus implements repoprovider.Provider.
//
//   - gh not on PATH                          ⇒ unknown / none
//   - store token present, `gh api user` ok   ⇒ ok / store (with login)
//   - store token present, 401                ⇒ expired / store
//   - no store, operator's `gh auth token` ok ⇒ ok / cli (an import candidate),
//     or expired / cli when that token is rejected
//   - no store, no CLI login                  ⇒ missing / none
//
// The error is non-nil only for an unclassified failure (status unknown); it
// is redacted.
func (p *Provider) AuthStatus(ctx context.Context, host string) (repoprovider.AuthStatus, error) {
	if err := p.exec.Look(ghBinary); err != nil {
		return repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: repoprovider.SourceNone}, nil
	}
	ctx, cancel := netCtx(ctx)
	defer cancel()

	env := p.env(host)
	source := repoprovider.SourceStore
	if !hasToken(env) {
		// No daemon-owned token: does the operator's own gh login have one?
		if _, _, err := p.exec.Run(ctx, "", nil, ghBinary, "auth", "token", "--hostname", host); err != nil {
			return repoprovider.AuthStatus{Status: repoprovider.AuthMissing, Source: repoprovider.SourceNone}, nil
		}
		env, source = nil, repoprovider.SourceCLI
	}
	stdout, stderr, err := p.exec.Run(ctx, "", env, ghBinary, "api", "user", "--hostname", host)
	if err != nil {
		cerr := repoprovider.Classify(stderr, err)
		if errors.Is(cerr, repoprovider.ErrNotAuthenticated) {
			return repoprovider.AuthStatus{Status: repoprovider.AuthExpired, Source: source}, nil
		}
		return repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: source}, cerr
	}
	var user struct {
		Login string `json:"login"`
	}
	_ = json.Unmarshal([]byte(stdout), &user) // a missing login is not an auth failure
	return repoprovider.AuthStatus{Status: repoprovider.AuthOK, Login: user.Login, Source: source}, nil
}

// validRef refuses a name git would read as an option ("-…"), a forced
// refspec ("+…") or a src:dst refspec — the ways a branch name could turn a
// plain push into a forced or redirected one.
func validRef(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "+") {
		return false
	}
	return !strings.ContainsAny(name, ": \t\n")
}

// Push implements repoprovider.Provider: `git push -u <remote> <branch>`.
// Never --force, and a branch name that would smuggle a force or a refspec in
// is refused before git runs.
func (p *Provider) Push(ctx context.Context, t repoprovider.Target, branch string) error {
	remote := t.RemoteName
	if remote == "" {
		remote = repoprovider.DefaultRemote
	}
	if !validRef(branch) || !validRef(remote) {
		return fmt.Errorf("%w: %q %q", ErrInvalidRef, remote, branch)
	}
	ctx, cancel := netCtx(ctx)
	defer cancel()
	_, stderr, err := p.exec.Run(ctx, t.RepoDir, p.env(t.Remote.Host), gitBinary, "push", "-u", remote, branch)
	return repoprovider.Classify(stderr, err)
}

// repoArg is the --repo value: OWNER/REPO on github.com, HOST/OWNER/REPO on an
// Enterprise host; "" when the remote was not parsed.
func repoArg(r repoprovider.Remote) string {
	if r.Owner == "" || r.Repo == "" {
		return ""
	}
	if r.Host == "" || r.Host == publicHost {
		return r.Slug()
	}
	return r.Host + "/" + r.Slug()
}

// OpenChangeRequest implements repoprovider.Provider: `gh pr create --head …
// --title … --body … [--base …] [--repo …] [--draft]`. The argument order keeps
// the "pr create --head <branch>" prefix the board land tests match on.
func (p *Provider) OpenChangeRequest(ctx context.Context, t repoprovider.Target, req repoprovider.ChangeRequest) (repoprovider.ChangeRef, error) {
	if err := p.exec.Look(ghBinary); err != nil {
		return repoprovider.ChangeRef{}, repoprovider.Classify("", err)
	}
	if !validRef(req.Head) || (req.Base != "" && !validRef(req.Base)) {
		return repoprovider.ChangeRef{}, fmt.Errorf("%w: head %q base %q", ErrInvalidRef, req.Head, req.Base)
	}
	args := []string{"pr", "create", "--head", req.Head, "--title", req.Title, "--body", req.Body}
	if req.Base != "" {
		args = append(args, "--base", req.Base)
	}
	if r := repoArg(t.Remote); r != "" {
		args = append(args, "--repo", r)
	}
	if req.Draft {
		args = append(args, "--draft")
	}
	ctx, cancel := netCtx(ctx)
	defer cancel()
	stdout, stderr, err := p.exec.Run(ctx, t.RepoDir, p.env(t.Remote.Host), ghBinary, args...)
	if err != nil {
		return repoprovider.ChangeRef{}, repoprovider.Classify(stderr, err)
	}
	// gh narrates on stderr and prints the URL on stdout; which stream carries
	// what has changed across versions, so both are scanned.
	url := repoprovider.FirstURL(stdout)
	if url == "" {
		url = repoprovider.FirstURL(stderr)
	}
	if url == "" {
		return repoprovider.ChangeRef{}, fmt.Errorf("%w: %s", ErrNoURL,
			credstore.Redact(repoprovider.Tail(stdout+"\n"+stderr, nil)))
	}
	return repoprovider.ChangeRef{URL: url, Number: prNumber(url), Provider: repoprovider.KindGitHub}, nil
}

// prNumber is the N of …/pull/N, or 0.
func prNumber(url string) int {
	_, rest, ok := strings.Cut(url, "/pull/")
	if !ok {
		return 0
	}
	rest, _, _ = strings.Cut(rest, "/")
	rest, _, _ = strings.Cut(rest, "#")
	rest, _, _ = strings.Cut(rest, "?")
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// prView is the `gh pr view --json statusFields` shape.
type prView struct {
	State             string    `json:"state"`
	IsDraft           bool      `json:"isDraft"`
	MergedAt          string    `json:"mergedAt"`
	ReviewDecision    string    `json:"reviewDecision"`
	StatusCheckRollup []ghCheck `json:"statusCheckRollup"`
	URL               string    `json:"url"`
}

// ghCheck is one rollup entry: a CheckRun (status + conclusion) or a commit
// StatusContext (state).
type ghCheck struct {
	Typename   string `json:"__typename"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

// Status implements repoprovider.Provider: `gh pr view <n> --repo … --json …`.
func (p *Provider) Status(ctx context.Context, t repoprovider.Target, ref repoprovider.ChangeRef) (repoprovider.ChangeStatus, error) {
	if err := p.exec.Look(ghBinary); err != nil {
		return repoprovider.ChangeStatus{}, repoprovider.Classify("", err)
	}
	sel := ref.URL
	if ref.Number > 0 {
		sel = strconv.Itoa(ref.Number)
	}
	if sel == "" {
		return repoprovider.ChangeStatus{}, errors.New("github: change reference has neither a number nor a URL")
	}
	args := []string{"pr", "view", sel}
	if r := repoArg(t.Remote); r != "" {
		args = append(args, "--repo", r)
	}
	args = append(args, "--json", statusFields)
	ctx, cancel := netCtx(ctx)
	defer cancel()
	stdout, stderr, err := p.exec.Run(ctx, t.RepoDir, p.env(t.Remote.Host), ghBinary, args...)
	if err != nil {
		return repoprovider.ChangeStatus{}, repoprovider.Classify(stderr, err)
	}
	var v prView
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		return repoprovider.ChangeStatus{}, fmt.Errorf("github: unreadable gh pr view output: %s",
			credstore.Redact(repoprovider.Tail(stdout, err)))
	}
	return repoprovider.ChangeStatus{
		State:     prState(v),
		Draft:     v.IsDraft,
		CI:        ciSummary(v.StatusCheckRollup),
		Review:    reviewSummary(v.ReviewDecision),
		CheckedAt: p.now().UTC(),
	}, nil
}

func prState(v prView) string {
	if v.MergedAt != "" {
		return repoprovider.StateMerged
	}
	switch strings.ToUpper(v.State) {
	case "MERGED":
		return repoprovider.StateMerged
	case "CLOSED":
		return repoprovider.StateClosed
	default:
		return repoprovider.StateOpen
	}
}

func reviewSummary(decision string) string {
	switch strings.ToUpper(decision) {
	case "APPROVED":
		return repoprovider.ReviewApproved
	case "CHANGES_REQUESTED":
		return repoprovider.ReviewChangesRequested
	case "REVIEW_REQUIRED":
		return repoprovider.ReviewRequired
	default:
		return repoprovider.ReviewNone
	}
}

// ciSummary folds the rollup: any failure ⇒ failing; else any unfinished ⇒
// pending; else passing; an empty rollup ⇒ none.
func ciSummary(checks []ghCheck) string {
	if len(checks) == 0 {
		return repoprovider.CINone
	}
	failing, pending := false, false
	for _, c := range checks {
		switch checkOutcome(c) {
		case repoprovider.CIFailing:
			failing = true
		case repoprovider.CIPending:
			pending = true
		}
	}
	switch {
	case failing:
		return repoprovider.CIFailing
	case pending:
		return repoprovider.CIPending
	default:
		return repoprovider.CIPassing
	}
}

func checkOutcome(c ghCheck) string {
	if c.Typename == "StatusContext" || (c.Status == "" && c.State != "") {
		switch strings.ToUpper(c.State) {
		case "SUCCESS":
			return repoprovider.CIPassing
		case "FAILURE", "ERROR":
			return repoprovider.CIFailing
		default: // PENDING, EXPECTED
			return repoprovider.CIPending
		}
	}
	if strings.ToUpper(c.Status) != "COMPLETED" {
		return repoprovider.CIPending
	}
	switch strings.ToUpper(c.Conclusion) {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return repoprovider.CIPassing
	case "":
		return repoprovider.CIPending
	default: // FAILURE, CANCELLED, TIMED_OUT, ACTION_REQUIRED, STARTUP_FAILURE, STALE
		return repoprovider.CIFailing
	}
}
