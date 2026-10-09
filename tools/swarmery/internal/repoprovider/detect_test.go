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
