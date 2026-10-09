package repoprovider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in    string
		want  Remote
		isErr bool
	}{
		{in: "https://github.com/acme/widgets.git", want: Remote{Host: "github.com", Owner: "acme", Repo: "widgets", Protocol: "https"}},
		{in: "https://github.com/acme/widgets", want: Remote{Host: "github.com", Owner: "acme", Repo: "widgets", Protocol: "https"}},
		{in: "https://github.com/acme/widgets/", want: Remote{Host: "github.com", Owner: "acme", Repo: "widgets", Protocol: "https"}},
		{in: "https://user:pw@GitLab.Corp:8443/group/sub/app.git\n", want: Remote{URL: "https://GitLab.Corp:8443/group/sub/app.git", Host: "gitlab.corp:8443", Owner: "group/sub", Repo: "app", Protocol: "https"}},
		{in: "http://git.local/a/b", want: Remote{Host: "git.local", Owner: "a", Repo: "b", Protocol: "https"}},
		{in: "git@github.com:acme/widgets.git", want: Remote{Host: "github.com", Owner: "acme", Repo: "widgets", Protocol: "ssh"}},
		{in: "git@gitlab.com:group/sub/app", want: Remote{Host: "gitlab.com", Owner: "group/sub", Repo: "app", Protocol: "ssh"}},
		{in: "gitlab.corp:team/app.git", want: Remote{Host: "gitlab.corp", Owner: "team", Repo: "app", Protocol: "ssh"}},
		{in: "ssh://git@github.com/acme/widgets.git", want: Remote{Host: "github.com", Owner: "acme", Repo: "widgets", Protocol: "ssh"}},
		{in: "ssh://git@git.corp:2222/acme/widgets/", want: Remote{Host: "git.corp", Owner: "acme", Repo: "widgets", Protocol: "ssh"}},
		{in: "ssh://git:pw@git.corp/a/b", want: Remote{URL: "ssh://git.corp/a/b", Host: "git.corp", Owner: "a", Repo: "b", Protocol: "ssh"}},
		{in: "https://ghp_tokentokentoken@github.com/a/b.git", want: Remote{URL: "https://github.com/a/b.git", Host: "github.com", Owner: "a", Repo: "b", Protocol: "https"}},
		{in: "", isErr: true},
		{in: "https://github.com/acme", isErr: true},
		{in: "https://github.com//widgets", isErr: true},
		{in: "https://github.com/acme/../widgets", isErr: true},
		{in: "ftp://host/a/b", isErr: true},
		{in: "/local/path/repo", isErr: true},
		{in: "./a/b:c", isErr: true},
		{in: "https://%zz/a/b", isErr: true},
	}
	for _, c := range cases {
		got, err := ParseRemote(c.in)
		if c.isErr {
			if !errors.Is(err, ErrBadRemote) {
				t.Errorf("ParseRemote(%q) err = %v, want ErrBadRemote", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRemote(%q): %v", c.in, err)
			continue
		}
		if c.want.URL == "" {
			c.want.URL = strings.TrimSpace(c.in)
		}
		if got != c.want {
			t.Errorf("ParseRemote(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// Review fix 5: a malformed remote's error never carries its userinfo or a
// token, whether url.Parse succeeded (too few segments) or failed.
func TestParseRemoteErrorHidesCredentials(t *testing.T) {
	tok := "ghp_" + strings.Repeat("u", 36)
	for _, in := range []string{
		"https://user:s3cret@host/o",       // parses, one segment
		"https://user:s3cret@%zz/o/r",      // url.Parse fails
		"https://user:s3cret@host/o/../r",  // dot segment
		"ssh://git:s3cret@host/o",          // ssh with a password
		"https://" + tok + "@github.com/o", // token as the user name
		"ftp://user:s3cret@host/o/r",       // bad scheme
		"https://user:s3cret@host?x=@y/o",  // "@" past the authority
	} {
		_, err := ParseRemote(in)
		if !errors.Is(err, ErrBadRemote) {
			t.Fatalf("ParseRemote(%q) = %v, want ErrBadRemote", in, err)
		}
		if msg := err.Error(); strings.Contains(msg, "s3cret") || strings.Contains(msg, tok) || strings.Contains(msg, "user:") {
			t.Errorf("ParseRemote(%q) error leaks credentials: %q", in, msg)
		}
	}
	// Detect wraps the same text: still clean.
	_, err := Detect(context.Background(), remoteFake("https://user:s3cret@host/o"), "/repo", Config{}, nil)
	if !errors.Is(err, ErrNoRemote) || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("Detect error = %v", err)
	}
	if got := stripUserinfo("git@github.com:a/b"); got != "git@github.com:a/b" {
		t.Fatalf("scp form changed: %q", got)
	}
	if got := stripUserinfo("https://u:p@h"); got != "https://h" {
		t.Fatalf("authority-only: %q", got)
	}
}

type stubProber struct {
	gitlab bool
	asked  []string
}

func (s *stubProber) IsGitLab(_ context.Context, host string) bool {
	s.asked = append(s.asked, host)
	return s.gitlab
}

func remoteFake(url string) *FakeExec {
	return &FakeExec{Out: map[string]string{"git remote": url + "\n"}}
}

func TestDetect(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		url        string
		cfg        Config
		probe      *stubProber
		wantKind   Kind
		wantSource string
		wantShort  string
		wantProbed bool
	}{
		{"github.com", "git@github.com:acme/widgets.git", Config{}, &stubProber{}, KindGitHub, SourceHost, "PR", false},
		{"gitlab.com", "https://gitlab.com/g/p.git", Config{}, &stubProber{}, KindGitLab, SourceHost, "MR", false},
		{"unknown probe true", "https://git.corp/team/app.git", Config{}, &stubProber{gitlab: true}, KindGitLab, SourceProbe, "MR", true},
		{"unknown probe false", "https://git.corp/team/app.git", Config{}, &stubProber{}, KindUnknown, SourceUnknown, "CR", true},
		{"explicit github on enterprise", "https://ghe.corp/team/app.git", Config{Provider: "github"}, &stubProber{gitlab: true}, KindGitHub, SourceConfig, "PR", false},
		{"explicit gitlab overrides host", "git@github.com:acme/w.git", Config{Provider: " GitLab "}, &stubProber{}, KindGitLab, SourceConfig, "MR", false},
		{"auto is not explicit", "git@github.com:acme/w.git", Config{Provider: "auto"}, &stubProber{}, KindGitHub, SourceHost, "PR", false},
	}
	for _, c := range cases {
		f := remoteFake(c.url)
		d, err := Detect(ctx, f, "/repo", c.cfg, c.probe)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if d.Kind != c.wantKind || d.Source != c.wantSource || d.Terms.ChangeShort != c.wantShort {
			t.Errorf("%s: got kind=%s source=%s terms=%+v", c.name, d.Kind, d.Source, d.Terms)
		}
		if (len(c.probe.asked) > 0) != c.wantProbed {
			t.Errorf("%s: probed=%v, want %v", c.name, c.probe.asked, c.wantProbed)
		}
		if f.Calls[0] != "git remote get-url origin" || f.Dirs[0] != "/repo" {
			t.Errorf("%s: call=%q dir=%q", c.name, f.Calls[0], f.Dirs[0])
		}
		if d.Terms.Provider == "" || d.Terms.Change == "" {
			t.Errorf("%s: empty terms %+v", c.name, d.Terms)
		}
	}
	// A nil prober on an unknown host is unknown, not a panic.
	d, err := Detect(ctx, remoteFake("https://git.corp/a/b"), "/repo", Config{}, nil)
	if err != nil || d.Kind != KindUnknown {
		t.Fatalf("nil prober: %+v %v", d, err)
	}
}

// sshAliasFake scripts a remote URL and, when sshOut is non-empty, the
// `ssh -G` answer; sshErr makes `ssh -G` fail instead.
func sshAliasFake(url, sshOut, sshErr string) *FakeExec {
	f := remoteFake(url)
	if sshOut != "" {
		f.Out["ssh -G"] = sshOut
	}
	if sshErr != "" {
		f.Errs = map[string]string{"ssh -G": sshErr}
	}
	return f
}

// TestDetectResolvesSSHHost: an ssh_config alias and a provider's port-443 SSH
// endpoint are classified by the host they really reach, and that host is what
// Remote.Host carries (the URL stays as configured, for git to resolve itself).
func TestDetectResolvesSSHHost(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		f         *FakeExec
		probe     *stubProber
		wantKind  Kind
		wantHost  string
		wantSSH   bool // an `ssh -G` call is expected
		wantProbe []string
	}{
		{"alias to github.com",
			sshAliasFake("git@github-work:acme/widgets.git", "user git\nhostname github.com\nport 22\n", ""),
			&stubProber{}, KindGitHub, "github.com", true, nil},
		{"alias to ssh.github.com is canonicalised",
			sshAliasFake("git@gh443:acme/widgets.git", "hostname ssh.github.com\nport 443\n", ""),
			&stubProber{}, KindGitHub, "github.com", true, nil},
		{"alias to a self-hosted GitLab probes the REAL host",
			sshAliasFake("git@work-gl:team/app.git", "HostName GitLab.Corp.Example\n", ""),
			&stubProber{gitlab: true}, KindGitLab, "gitlab.corp.example", true, []string{"gitlab.corp.example"}},
		{"github port-443 endpoint needs no lookup",
			sshAliasFake("ssh://git@ssh.github.com:443/acme/widgets.git", "", ""),
			&stubProber{}, KindGitHub, "github.com", false, nil},
		{"gitlab altssh endpoint needs no lookup",
			sshAliasFake("ssh://git@altssh.gitlab.com:443/g/p.git", "", ""),
			&stubProber{}, KindGitLab, "gitlab.com", false, nil},
		{"known host needs no lookup",
			sshAliasFake("git@github.com:acme/widgets.git", "", ""),
			&stubProber{}, KindGitHub, "github.com", false, nil},
		{"https is never looked up",
			sshAliasFake("https://git.corp/acme/widgets.git", "", ""),
			&stubProber{}, KindUnknown, "git.corp", false, []string{"git.corp"}},
		{"ssh -G failing keeps the alias",
			sshAliasFake("git@github-work:acme/widgets.git", "", "ssh: command failed"),
			&stubProber{}, KindUnknown, "github-work", true, []string{"github-work"}},
		{"ssh -G without a hostname line keeps the alias",
			sshAliasFake("git@github-work:acme/widgets.git", "user git\n", ""),
			&stubProber{}, KindUnknown, "github-work", true, []string{"github-work"}},
		{"ssh missing keeps the alias",
			func() *FakeExec {
				f := remoteFake("git@github-work:acme/widgets.git")
				f.Missing = map[string]bool{"ssh": true}
				return f
			}(),
			&stubProber{}, KindUnknown, "github-work", true, []string{"github-work"}},
	}
	for _, c := range cases {
		d, err := Detect(ctx, c.f, "/repo", Config{}, c.probe)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if d.Kind != c.wantKind || d.Remote.Host != c.wantHost {
			t.Errorf("%s: kind=%s host=%q, want %s %q", c.name, d.Kind, d.Remote.Host, c.wantKind, c.wantHost)
		}
		if got := c.f.Ran("ssh -G"); got != c.wantSSH {
			t.Errorf("%s: ssh -G ran=%v, want %v (calls %v)", c.name, got, c.wantSSH, c.f.Calls)
		}
		if fmt.Sprint(c.probe.asked) != fmt.Sprint(c.wantProbe) {
			t.Errorf("%s: probed %v, want %v", c.name, c.probe.asked, c.wantProbe)
		}
	}
	// An explicit provider still gets the resolved host (it feeds --repo).
	f := sshAliasFake("git@github-work:acme/widgets.git", "hostname github.com\n", "")
	d, err := Detect(ctx, f, "/repo", Config{Provider: "github"}, nil)
	if err != nil || d.Remote.Host != "github.com" || d.Source != SourceConfig {
		t.Fatalf("explicit github + alias: %+v %v", d, err)
	}
	if d.Remote.URL != "git@github-work:acme/widgets.git" {
		t.Errorf("URL rewritten to %q; git must keep resolving the alias itself", d.Remote.URL)
	}
}

func TestSSHConfigHostname(t *testing.T) {
	cases := map[string]string{
		"user git\nhostname github.com\n": "github.com",
		"HostName Example.COM":            "example.com",
		"  hostname   spaced.example  ":   "spaced.example",
		"hostnamex nope\n":                "",
		"hostname -oProxyCommand=x\n":     "",
		"hostname a/b\n":                  "",
		"":                                "",
	}
	for in, want := range cases {
		if got := sshConfigHostname(in); got != want {
			t.Errorf("sshConfigHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveHostRefusesOptionLikeHost(t *testing.T) {
	f := &FakeExec{Out: map[string]string{"ssh -G": "hostname github.com\n"}}
	r := Remote{Host: "-oproxycommand=x", Protocol: "ssh"}
	if got := resolveHost(context.Background(), f, r); got != r.Host || len(f.Calls) != 0 {
		t.Errorf("option-like host resolved to %q, calls %v", got, f.Calls)
	}
}

func TestDetectFailures(t *testing.T) {
	ctx := context.Background()
	noRemote := &FakeExec{Errs: map[string]string{"git remote": "error: No such remote 'origin'"}}
	if _, err := Detect(ctx, noRemote, "/repo", Config{Provider: "github"}, nil); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("no remote: %v", err)
	}
	odd := &FakeExec{Errs: map[string]string{"git remote": "fatal: weird"}}
	if _, err := Detect(ctx, odd, "/repo", Config{}, nil); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("unclassified remote failure: %v", err)
	}
	noGit := &FakeExec{Missing: map[string]bool{"git": true}}
	_, err := Detect(ctx, noGit, "/repo", Config{}, nil)
	if !errors.Is(err, ErrBinaryMissing) || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing git: %v", err)
	}
	bad := remoteFake("/some/local/path")
	if _, err := Detect(ctx, bad, "/repo", Config{}, nil); !errors.Is(err, ErrNoRemote) || !errors.Is(err, ErrBadRemote) {
		t.Fatalf("unparseable remote: %v", err)
	}
}

func proberFor(srv *httptest.Server) (HTTPProber, string) {
	return HTTPProber{Client: srv.Client(), Scheme: "http"}, strings.TrimPrefix(srv.URL, "http://")
}

func TestHTTPProber(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"200 version json", 200, `{"version":"17.4.0","revision":"abc"}`, true},
		{"401 json", 401, `{"message":"401 Unauthorized"}`, true},
		{"200 json without version", 200, `{"hello":"world"}`, false},
		{"200 html", 200, `<html>login</html>`, false},
		{"401 html", 401, `<html>no</html>`, false},
		{"404 json", 404, `{"message":"404 Not Found"}`, false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v4/version" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(c.status)
			fmt.Fprint(w, c.body)
		}))
		p, host := proberFor(srv)
		if got := p.IsGitLab(context.Background(), host); got != c.want {
			t.Errorf("%s: IsGitLab = %v, want %v", c.name, got, c.want)
		}
		srv.Close()
	}
}

func TestHTTPProberRejectsAndTimesOut(t *testing.T) {
	p := HTTPProber{}
	for _, host := range []string{"", "evil.com/x", "a@b", "host:port:x", "a b"} {
		if p.IsGitLab(context.Background(), host) {
			t.Errorf("IsGitLab(%q) = true", host)
		}
	}
	// Unreachable: a closed server.
	srv := httptest.NewServer(http.NotFoundHandler())
	hp, host := proberFor(srv)
	srv.Close()
	if hp.IsGitLab(context.Background(), host) {
		t.Fatal("closed server read as GitLab")
	}
	// A slow host is abandoned at the caller's deadline.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	sp, shost := proberFor(slow)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if sp.IsGitLab(ctx, shost) {
		t.Fatal("slow host read as GitLab")
	}
	if time.Since(start) > time.Second {
		t.Fatal("probe did not honour the deadline")
	}
}
