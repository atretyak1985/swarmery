// Package gitlab is the GitLab repoprovider.Provider (GitLab.com and
// self-hosted), driven through the `glab` CLI (and `git` for the push, via the
// shared repoprovider.GitPush).
//
// Imports: the parent repoprovider (types, Exec, Classify, GitPush) and its
// credstore leaf (token key names) — never the other way round (see the
// repoprovider package doc).
//
// Credentials follow the GitHub provider's rule: every call runs with the env
// delta returned by the injected env func (production: credstore.Env — the
// daemon's own GITLAB_TOKEN and an isolated GLAB_CONFIG_DIR) when that delta
// carries a GitLab token, and with no credential delta otherwise, so an
// operator logged in only through `glab auth login` keeps working.
//
// glab picks its host from GITLAB_HOST or from -R HOST/OWNER/REPO, depending
// on the version and the subcommand; every repo-scoped call passes both.
//
// Merge policy stays with the operator on the MR page: this package never
// passes --remove-source-branch or --squash.
package gitlab

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
	glabBinary = "glab"
	// hostEnv is the variable glab reads its default host from.
	hostEnv = "GITLAB_HOST"
)

// ErrNoURL is returned when `glab mr create` exits 0 but prints no MR URL.
var ErrNoURL = errors.New("glab mr create printed no merge-request URL")

// Provider is the GitLab implementation of repoprovider.Provider.
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
func (p *Provider) Kind() repoprovider.Kind { return repoprovider.KindGitLab }

// Terms implements repoprovider.Provider.
func (p *Provider) Terms() repoprovider.Terms { return repoprovider.TermsFor(repoprovider.KindGitLab) }

func hasToken(env []string) bool {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, credstore.GitLabTokenKey+"="); ok && v != "" {
			return true
		}
	}
	return false
}

// callEnv is the credential delta for a call to host: the injected env when it
// carries a GitLab token, else nil — the operator's own glab login. An isolated
// GLAB_CONFIG_DIR without a token would only turn a working CLI login into a
// not-authenticated failure.
func (p *Provider) callEnv(host string) []string {
	if env := p.env(host); hasToken(env) {
		return env
	}
	return nil
}

// glabEnv is callEnv plus GITLAB_HOST=<host> (when host is known), for the
// repo-scoped glab calls.
func (p *Provider) glabEnv(host string) []string {
	env := p.callEnv(host)
	if host == "" {
		return env
	}
	return append(append([]string(nil), env...), hostEnv+"="+host)
}

// AuthStatus implements repoprovider.Provider.
//
//   - glab not on PATH                                ⇒ unknown / none
//   - store token present, `glab api user` ok         ⇒ ok / store (with username)
//   - store token present, 401                        ⇒ expired / store
//   - no store, operator's `glab auth status` ok      ⇒ ok / cli (an import
//     candidate), or expired / cli when `glab api user` is then rejected
//   - no store, no (valid) CLI login                  ⇒ missing / none
//
// The error is non-nil only for an unclassified failure (status unknown); it
// is redacted.
func (p *Provider) AuthStatus(ctx context.Context, host string) (repoprovider.AuthStatus, error) {
	if err := p.exec.Look(glabBinary); err != nil {
		return repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: repoprovider.SourceNone}, nil
	}
	ctx, cancel := repoprovider.NetCtx(ctx)
	defer cancel()

	env := p.callEnv(host)
	source := repoprovider.SourceStore
	if env == nil {
		// No daemon-owned token: is the operator's own glab logged in to host?
		// (Without --show-token: the token never enters this process.)
		if _, _, err := p.exec.Run(ctx, "", nil, glabBinary, "auth", "status", "--hostname", host); err != nil {
			return repoprovider.AuthStatus{Status: repoprovider.AuthMissing, Source: repoprovider.SourceNone}, nil
		}
		source = repoprovider.SourceCLI
	}
	stdout, stderr, err := p.exec.Run(ctx, "", env, glabBinary, "api", "user", "--hostname", host)
	if err != nil {
		cerr := repoprovider.Classify(stderr, err)
		if errors.Is(cerr, repoprovider.ErrNotAuthenticated) {
			return repoprovider.AuthStatus{Status: repoprovider.AuthExpired, Source: source}, nil
		}
		return repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: source}, cerr
	}
	var user struct {
		Username string `json:"username"`
	}
	_ = json.Unmarshal([]byte(stdout), &user) // a missing username is not an auth failure
	return repoprovider.AuthStatus{Status: repoprovider.AuthOK, Login: user.Username, Source: source}, nil
}

// Push implements repoprovider.Provider through the shared
// repoprovider.GitPush: `git push -u <remote> <branch>`, never forced, a
// smuggled refspec refused before git runs.
func (p *Provider) Push(ctx context.Context, t repoprovider.Target, branch string) error {
	return repoprovider.GitPush(ctx, p.exec, p.callEnv(t.Remote.Host), t, branch)
}

// repoArg is the -R value: HOST/OWNER/REPO (OWNER may carry subgroups), or
// OWNER/REPO when the host is unknown; "" when the remote was not parsed.
func repoArg(r repoprovider.Remote) string {
	if r.Owner == "" || r.Repo == "" {
		return ""
	}
	if r.Host == "" {
		return r.Slug()
	}
	return r.Host + "/" + r.Slug()
}

// OpenChangeRequest implements repoprovider.Provider: `glab mr create
// --source-branch <head> --target-branch <base> --title … --description …
// [--draft] --yes [-R host/owner/repo]` with GITLAB_HOST=<host>. An empty
// req.Base resolves to the project's default branch (`glab repo view`); when
// that lookup fails, --target-branch is left out and glab applies the same
// default itself.
func (p *Provider) OpenChangeRequest(ctx context.Context, t repoprovider.Target, req repoprovider.ChangeRequest) (repoprovider.ChangeRef, error) {
	if err := p.exec.Look(glabBinary); err != nil {
		return repoprovider.ChangeRef{}, repoprovider.Classify("", err)
	}
	if !repoprovider.ValidRef(req.Head) || (req.Base != "" && !repoprovider.ValidRef(req.Base)) {
		return repoprovider.ChangeRef{}, fmt.Errorf("%w: head %q base %q", repoprovider.ErrInvalidRef, req.Head, req.Base)
	}
	ctx, cancel := repoprovider.NetCtx(ctx)
	defer cancel()

	base := req.Base
	if base == "" {
		base = p.defaultBranch(ctx, t)
	}
	args := []string{"mr", "create", "--source-branch", req.Head}
	if base != "" {
		args = append(args, "--target-branch", base)
	}
	args = append(args, "--title", req.Title, "--description", req.Body)
	if req.Draft {
		args = append(args, "--draft")
	}
	args = append(args, "--yes")
	if r := repoArg(t.Remote); r != "" {
		args = append(args, "-R", r)
	}
	stdout, stderr, err := p.exec.Run(ctx, t.RepoDir, p.glabEnv(t.Remote.Host), glabBinary, args...)
	if err != nil {
		return repoprovider.ChangeRef{}, repoprovider.Classify(stderr, err)
	}
	// glab prints progress and the URL; which stream carries which has changed
	// across versions, so both are scanned.
	url := repoprovider.FirstURL(stdout)
	if url == "" {
		url = repoprovider.FirstURL(stderr)
	}
	if url == "" {
		return repoprovider.ChangeRef{}, fmt.Errorf("%w: %s", ErrNoURL,
			repoprovider.RedactedTail(stdout+"\n"+stderr, nil))
	}
	return repoprovider.ChangeRef{URL: url, Number: mrIID(url), Provider: repoprovider.KindGitLab}, nil
}

// defaultBranch is the project's default branch from `glab repo view
// <host/owner/repo> --output json`, or "" when it cannot be read.
func (p *Provider) defaultBranch(ctx context.Context, t repoprovider.Target) string {
	r := repoArg(t.Remote)
	if r == "" {
		return ""
	}
	stdout, _, err := p.exec.Run(ctx, t.RepoDir, p.glabEnv(t.Remote.Host), glabBinary, "repo", "view", r, "--output", "json")
	if err != nil {
		return ""
	}
	var v struct {
		DefaultBranch string `json:"default_branch"`
	}
	if json.Unmarshal([]byte(stdout), &v) != nil || !repoprovider.ValidRef(v.DefaultBranch) {
		return ""
	}
	return v.DefaultBranch
}

// mrIID is the N of …/-/merge_requests/N (or the legacy …/merge_requests/N),
// or 0.
func mrIID(url string) int {
	_, rest, ok := strings.Cut(url, "/merge_requests/")
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

// pipelineRef is the part of a pipeline object Status reads.
type pipelineRef struct {
	Status string `json:"status"`
}

// mrView is the `glab mr view --output json` shape (the GitLab MR API object).
// Field names drift across glab versions: the pipeline is `head_pipeline` in
// some and `pipeline` in others, the draft flag `draft` or the older
// `work_in_progress`; both of each are read.
type mrView struct {
	State               string       `json:"state"`
	Draft               bool         `json:"draft"`
	WorkInProgress      bool         `json:"work_in_progress"`
	DetailedMergeStatus string       `json:"detailed_merge_status"`
	HeadPipeline        *pipelineRef `json:"head_pipeline"`
	Pipeline            *pipelineRef `json:"pipeline"`
	Approved            *bool        `json:"approved"`
	WebURL              string       `json:"web_url"`
}

// Status implements repoprovider.Provider: `glab mr view <iid> -R
// <host/owner/repo> --output json`.
func (p *Provider) Status(ctx context.Context, t repoprovider.Target, ref repoprovider.ChangeRef) (repoprovider.ChangeStatus, error) {
	if err := p.exec.Look(glabBinary); err != nil {
		return repoprovider.ChangeStatus{}, repoprovider.Classify("", err)
	}
	sel := ref.URL
	if ref.Number > 0 {
		sel = strconv.Itoa(ref.Number)
	} else if n := mrIID(ref.URL); n > 0 {
		sel = strconv.Itoa(n)
	}
	if sel == "" {
		return repoprovider.ChangeStatus{}, errors.New("gitlab: change reference has neither an iid nor a URL")
	}
	args := []string{"mr", "view", sel}
	if r := repoArg(t.Remote); r != "" {
		args = append(args, "-R", r)
	}
	args = append(args, "--output", "json")
	ctx, cancel := repoprovider.NetCtx(ctx)
	defer cancel()
	stdout, stderr, err := p.exec.Run(ctx, t.RepoDir, p.glabEnv(t.Remote.Host), glabBinary, args...)
	if err != nil {
		return repoprovider.ChangeStatus{}, repoprovider.Classify(stderr, err)
	}
	var v mrView
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		return repoprovider.ChangeStatus{}, fmt.Errorf("gitlab: unreadable glab mr view output: %s",
			repoprovider.RedactedTail(stdout, err))
	}
	return repoprovider.ChangeStatus{
		State:     mrState(v.State),
		Draft:     v.Draft || v.WorkInProgress,
		CI:        ciSummary(v),
		Review:    reviewSummary(v),
		CheckedAt: p.now().UTC(),
	}, nil
}

// mrState maps GitLab's opened|merged|closed (and the transient locked) onto
// the neutral states.
func mrState(s string) string {
	switch strings.ToLower(s) {
	case "merged":
		return repoprovider.StateMerged
	case "closed":
		return repoprovider.StateClosed
	default: // opened, locked
		return repoprovider.StateOpen
	}
}

// ciSummary reads the MR's pipeline status — head_pipeline first, pipeline as
// the fallback field name — onto the neutral CI values:
//
//	success                          ⇒ success
//	skipped                          ⇒ success  (nothing ran that could fail; GitHub's SKIPPED reads the same)
//	failed, canceled                 ⇒ failure  (a cancelled pipeline never went green, so the MR is not mergeable on CI)
//	running, pending, created,
//	preparing, scheduled,
//	waiting_for_resource             ⇒ pending
//	manual                           ⇒ pending  (blocked on a person pressing play — not finished, not failed)
//	no pipeline / null / ""          ⇒ none
//
// Any status GitLab adds later reads as pending: it is neither a known green
// nor a known red.
func ciSummary(v mrView) string {
	var status string
	switch {
	case v.HeadPipeline != nil && v.HeadPipeline.Status != "":
		status = v.HeadPipeline.Status
	case v.Pipeline != nil:
		status = v.Pipeline.Status
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return repoprovider.CINone
	case "success", "skipped":
		return repoprovider.CISuccess
	case "failed", "canceled", "cancelled":
		return repoprovider.CIFailure
	default: // running, pending, created, preparing, scheduled, manual, waiting_for_resource, …
		return repoprovider.CIPending
	}
}

// requestedChanges is the detailed_merge_status a reviewer's "Request changes"
// sets (GitLab 17+); notApproved is the one a missing required approval sets.
const (
	requestedChanges = "requested_changes"
	notApproved      = "not_approved"
)

// reviewSummary maps the MR's review state onto the neutral review values:
//
//	detailed_merge_status requested_changes ⇒ changes_requested (wins over approvals)
//	approved: true                          ⇒ approved
//	approved: false                         ⇒ review_required
//	approved absent, not_approved           ⇒ review_required
//	approved absent otherwise               ⇒ none
func reviewSummary(v mrView) string {
	dms := strings.ToLower(strings.TrimSpace(v.DetailedMergeStatus))
	switch {
	case dms == requestedChanges:
		return repoprovider.ReviewChangesRequested
	case v.Approved != nil && *v.Approved:
		return repoprovider.ReviewApproved
	case v.Approved != nil, dms == notApproved:
		return repoprovider.ReviewRequired
	default:
		return repoprovider.ReviewNone
	}
}
