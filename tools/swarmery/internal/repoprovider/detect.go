package repoprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultRemote is the git remote Detect reads.
const DefaultRemote = "origin"

// ProbeTimeout bounds the GitLab API probe of an unknown host.
const ProbeTimeout = 3 * time.Second

// Detection sources.
const (
	SourceConfig  = "config"
	SourceHost    = "host"
	SourceProbe   = "probe"
	SourceUnknown = "unknown"
)

// Detection is which provider a repo's remote belongs to, and why.
type Detection struct {
	Kind   Kind   `json:"kind"`
	Remote Remote `json:"remote"`
	Terms  Terms  `json:"terms"`
	Source string `json:"source"` // config | host | probe | unknown
}

// ErrBadRemote is returned by ParseRemote for a URL it cannot read as
// host + owner + repo.
var ErrBadRemote = errors.New("unrecognised remote URL")

// ParseRemote parses the three remote shapes git accepts for a hosted repo:
//
//	https://host[:port]/owner/repo[.git][/]
//	[user@]host:owner/repo[.git]         (scp-like ssh)
//	ssh://[user@]host[:port]/owner/repo[.git]
//
// Owner keeps every path segment but the last, so a GitLab subgroup path
// ("group/sub") survives. An ssh port is the SSH port, not the web one, and is
// dropped from Host; an https port is kept.
func ParseRemote(raw string) (Remote, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Remote{}, fmt.Errorf("%w: empty", ErrBadRemote)
	}
	var host, path, proto string
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return Remote{}, fmt.Errorf("%w: %q", ErrBadRemote, s)
		}
		switch u.Scheme {
		case "https", "http":
			proto, host = "https", u.Host
		case "ssh", "git+ssh":
			proto, host = "ssh", u.Hostname()
		default:
			return Remote{}, fmt.Errorf("%w: scheme %q", ErrBadRemote, u.Scheme)
		}
		path = u.Path
		// A remote URL may embed credentials (https://<token>@host/…,
		// user:password@…); they never leave this function. An ssh user name
		// ("git@") is not a credential and is kept.
		_, hasPassword := u.User.Password()
		if u.User != nil && (proto == "https" || hasPassword) {
			u.User = nil
			s = u.String()
		}
	} else {
		// scp-like: [user@]host:path — the colon must come before any slash.
		hostPart, p, ok := strings.Cut(s, ":")
		if !ok || strings.Contains(hostPart, "/") {
			return Remote{}, fmt.Errorf("%w: %q", ErrBadRemote, s)
		}
		if i := strings.LastIndex(hostPart, "@"); i >= 0 {
			hostPart = hostPart[i+1:]
		}
		proto, host, path = "ssh", hostPart, p
	}
	host = strings.ToLower(host)
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segs := strings.Split(path, "/")
	if host == "" || len(segs) < 2 {
		return Remote{}, fmt.Errorf("%w: %q", ErrBadRemote, s)
	}
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return Remote{}, fmt.Errorf("%w: %q", ErrBadRemote, s)
		}
	}
	return Remote{
		URL:      s,
		Host:     host,
		Owner:    strings.Join(segs[:len(segs)-1], "/"),
		Repo:     segs[len(segs)-1],
		Protocol: proto,
	}, nil
}

// Prober answers whether an unknown host runs GitLab.
type Prober interface {
	IsGitLab(ctx context.Context, host string) bool
}

// HTTPProber probes GET <scheme>://<host>/api/v4/version. A 200 whose JSON body
// carries "version", or a 401 with a JSON body (an anonymous request to a
// GitLab that requires auth), reads as GitLab. Anything else — an HTML page, a
// redirect to a login form, a timeout — reads as "not GitLab".
type HTTPProber struct {
	// Client defaults to an http.Client with ProbeTimeout.
	Client *http.Client
	// Scheme defaults to "https"; tests point it at an httptest server.
	Scheme string
}

// IsGitLab implements Prober.
func (p HTTPProber) IsGitLab(ctx context.Context, host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, "/?#@ ") {
		return false
	}
	if _, _, err := net.SplitHostPort(host); err != nil && strings.Contains(host, ":") {
		return false
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: ProbeTimeout}
	}
	scheme := p.Scheme
	if scheme == "" {
		scheme = "https"
	}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+host+"/api/v4/version", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusOK:
		_, ok := obj["version"]
		return ok
	case http.StatusUnauthorized:
		return true
	default:
		return false
	}
}

// Detect reads the repo's DefaultRemote URL and decides its provider:
// an explicit cfg.Provider wins; then the public hosts (github.com,
// gitlab.com); then the GitLab API probe for any other host (a GitHub
// Enterprise host must be declared explicitly); else unknown. A repo without
// the remote is ErrNoRemote whatever the config says — there is nothing to push
// to.
func Detect(ctx context.Context, ex Exec, repoDir string, cfg Config, probe Prober) (Detection, error) {
	stdout, stderr, err := ex.Run(ctx, repoDir, nil, "git", "remote", "get-url", DefaultRemote)
	if err != nil {
		c := classify(stderr, err)
		if c.Sentinel != ErrBinaryMissing {
			c.Sentinel = ErrNoRemote
		}
		return Detection{}, c
	}
	remote, err := ParseRemote(stdout)
	if err != nil {
		c := classify("", err)
		c.Sentinel = ErrNoRemote
		return Detection{}, c
	}
	return classifyHost(ctx, remote, cfg, probe), nil
}

// classifyHost is Detect's decision on an already-parsed remote.
func classifyHost(ctx context.Context, remote Remote, cfg Config, probe Prober) Detection {
	d := Detection{Remote: remote}
	switch {
	case cfg.ExplicitKind() != "":
		d.Kind, d.Source = cfg.ExplicitKind(), SourceConfig
	case remote.Host == "github.com":
		d.Kind, d.Source = KindGitHub, SourceHost
	case remote.Host == "gitlab.com":
		d.Kind, d.Source = KindGitLab, SourceHost
	case probe != nil && probe.IsGitLab(ctx, remote.Host):
		d.Kind, d.Source = KindGitLab, SourceProbe
	default:
		d.Kind, d.Source = KindUnknown, SourceUnknown
	}
	d.Terms = TermsFor(d.Kind)
	return d
}
