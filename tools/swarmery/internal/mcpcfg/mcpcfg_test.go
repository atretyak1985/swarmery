package mcpcfg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// listFixture mirrors real `claude mcp list` output captured on 2026-07-25:
// spaces + colons in names, all four health markers, an http detail.
const listFixture = `Checking MCP server health…

claude.ai Atlassian Rovo: https://mcp.atlassian.com/v1/mcp - ✔ Connected
claude.ai Linear: https://mcp.linear.app/mcp - ! Needs authentication
plugin:context7:context7: npx -y @upstash/context7-mcp - ✔ Connected
plugin:figma:figma: https://mcp.figma.com/mcp (HTTP) - ! Needs authentication
auggie: auggie --mcp --mcp-auto-workspace - ✘ Failed to connect
playwright: npx -y @playwright/mcp@latest - ✔ Connected
somesse: https://example.com/x (SSE) - ⏸ Pending approval
`

func TestParseList(t *testing.T) {
	got := parseList([]byte(listFixture))
	if len(got) != 7 {
		t.Fatalf("want 7 servers, got %d: %+v", len(got), got)
	}

	by := map[string]Server{}
	for _, s := range got {
		by[s.Name] = s
	}

	cases := []struct {
		name   string
		status Status
		tr     Transport
		detail string
	}{
		{"claude.ai Atlassian Rovo", StatusConnected, TransportHTTP, "https://mcp.atlassian.com/v1/mcp"},
		{"claude.ai Linear", StatusNeedsAuth, TransportHTTP, "https://mcp.linear.app/mcp"},
		{"plugin:context7:context7", StatusConnected, TransportStdio, "npx -y @upstash/context7-mcp"},
		{"plugin:figma:figma", StatusNeedsAuth, TransportHTTP, "https://mcp.figma.com/mcp (HTTP)"},
		{"auggie", StatusFailed, TransportStdio, "auggie --mcp --mcp-auto-workspace"},
		{"playwright", StatusConnected, TransportStdio, "npx -y @playwright/mcp@latest"},
		{"somesse", StatusPending, TransportSSE, "https://example.com/x (SSE)"},
	}
	for _, c := range cases {
		s, ok := by[c.name]
		if !ok {
			t.Errorf("missing server %q", c.name)
			continue
		}
		if s.Status != c.status {
			t.Errorf("%q status = %q, want %q", c.name, s.Status, c.status)
		}
		if s.Transport != c.tr {
			t.Errorf("%q transport = %q, want %q", c.name, s.Transport, c.tr)
		}
		if s.Detail != c.detail {
			t.Errorf("%q detail = %q, want %q", c.name, s.Detail, c.detail)
		}
		if s.Source != "cli-list" {
			t.Errorf("%q source = %q, want cli-list", c.name, s.Source)
		}
	}
}

func TestParseListEmptyAndGarbage(t *testing.T) {
	if got := parseList(nil); len(got) != 0 {
		t.Errorf("nil → want 0, got %d", len(got))
	}
	// A header-only run and a garbage line both yield no servers, no panic.
	if got := parseList([]byte("Checking MCP server health…\n\nnot a server row\n")); len(got) != 0 {
		t.Errorf("garbage → want 0, got %d: %+v", len(got), got)
	}
}

func TestParseStatus(t *testing.T) {
	cases := map[string]Status{
		"✔ Connected":            StatusConnected,
		"✘ Failed to connect":    StatusFailed,
		"! Needs authentication": StatusNeedsAuth,
		"⏸ Pending approval":     StatusPending,
		"disabled":               StatusDisabled,
		"something novel":        StatusUnknown,
		"":                       StatusUnknown,
	}
	for in, want := range cases {
		if got := parseStatus(in); got != want {
			t.Errorf("parseStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddArgsStdio(t *testing.T) {
	got, err := addArgs(AddSpec{
		Name:      "my-server",
		Transport: TransportStdio,
		Command:   "npx",
		Args:      []string{"-y", "my-mcp"},
		Scope:     ScopeUser,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"mcp", "add", "-s", "user", "my-server", "npx", "-y", "my-mcp"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stdio argv = %v, want %v", got, want)
	}
}

func TestAddArgsHTTPAndSSE(t *testing.T) {
	http, err := addArgs(AddSpec{Name: "s", Transport: TransportHTTP, URL: "https://ex.com/mcp", Scope: ScopeLocal})
	if err != nil {
		t.Fatalf("http err: %v", err)
	}
	if want := []string{"mcp", "add", "--transport", "http", "-s", "local", "s", "https://ex.com/mcp"}; !reflect.DeepEqual(http, want) {
		t.Errorf("http argv = %v, want %v", http, want)
	}
	sse, err := addArgs(AddSpec{Name: "s", Transport: TransportSSE, URL: "https://ex.com/sse", Scope: ScopeLocal})
	if err != nil {
		t.Fatalf("sse err: %v", err)
	}
	if sse[3] != "sse" {
		t.Errorf("sse transport token = %q, want sse", sse[3])
	}
}

func TestAddArgsValidation(t *testing.T) {
	bad := []AddSpec{
		{Name: "", Transport: TransportStdio, Command: "x", Scope: ScopeLocal},                       // empty name
		{Name: "n", Transport: TransportStdio, Command: "", Scope: ScopeLocal},                       // stdio no command
		{Name: "n", Transport: TransportHTTP, URL: "", Scope: ScopeLocal},                            // http no url
		{Name: "n", Transport: TransportHTTP, URL: "ftp://x", Scope: ScopeLocal},                     // bad scheme
		{Name: "n", Transport: TransportHTTP, URL: "https://", Scope: ScopeLocal},                    // no host
		{Name: "n", Transport: Transport("carrier-pigeon"), Command: "x", Scope: ScopeLocal},         // bad transport
		{Name: "n", Transport: TransportStdio, Command: "x", Scope: Scope("root")},                   // bad scope
		{Name: "bad\nname", Transport: TransportStdio, Command: "x", Scope: ScopeLocal},              // control char
		{Name: strings.Repeat("x", 201), Transport: TransportStdio, Command: "x", Scope: ScopeLocal}, // too long
	}
	for i, spec := range bad {
		if _, err := addArgs(spec); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("case %d: want ErrInvalidInput, got %v", i, err)
		}
	}
}

// TestAddArgsInjectionIsLiteral is the security-gate assertion: a shell
// metacharacter payload in the name survives as a SINGLE argv element and is
// never split, quoted, or interpreted. This is what makes argv-slice exec
// injection-proof.
func TestAddArgsInjectionIsLiteral(t *testing.T) {
	payload := "evil; rm -rf / #"
	got, err := addArgs(AddSpec{Name: payload, Transport: TransportStdio, Command: "echo", Scope: ScopeLocal})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// The name must appear verbatim as exactly one element.
	found := 0
	for _, a := range got {
		if a == payload {
			found++
		}
	}
	if found != 1 {
		t.Errorf("payload should be exactly one literal argv element, found %d in %v", found, got)
	}
}

// fakeRunner records the argv it was called with and returns a canned reply.
type fakeRunner struct {
	reply    []byte
	err      error
	lastArgs []string
	calls    int
}

func (f *fakeRunner) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls++
	f.lastArgs = append([]string(nil), args...)
	return f.reply, f.err
}

func TestReaderList(t *testing.T) {
	f := &fakeRunner{reply: []byte(listFixture)}
	r := NewWithRunner(f.run)
	servers, err := r.List(context.Background())
	if err != nil {
		t.Fatalf("List err: %v", err)
	}
	if len(servers) != 7 {
		t.Fatalf("want 7, got %d", len(servers))
	}
	if want := []string{"mcp", "list"}; !reflect.DeepEqual(f.lastArgs, want) {
		t.Errorf("List argv = %v, want %v", f.lastArgs, want)
	}
}

func TestReaderListError(t *testing.T) {
	f := &fakeRunner{err: errors.New("boom")}
	if _, err := NewWithRunner(f.run).List(context.Background()); err == nil {
		t.Error("want error from List when runner fails")
	}
}

func TestReaderAdd(t *testing.T) {
	f := &fakeRunner{}
	r := NewWithRunner(f.run)
	err := r.Add(context.Background(), AddSpec{Name: "s", Transport: TransportHTTP, URL: "https://ex.com", Scope: ScopeLocal})
	if err != nil {
		t.Fatalf("Add err: %v", err)
	}
	if f.calls != 1 {
		t.Fatalf("want 1 call, got %d", f.calls)
	}
	if f.lastArgs[0] != "mcp" || f.lastArgs[1] != "add" {
		t.Errorf("Add argv prefix = %v", f.lastArgs[:2])
	}
}

func TestReaderAddValidationDoesNotExec(t *testing.T) {
	f := &fakeRunner{}
	// Invalid spec (bad transport) must fail BEFORE the runner is touched.
	err := NewWithRunner(f.run).Add(context.Background(), AddSpec{Name: "s", Transport: "nope", Scope: ScopeLocal})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
	if f.calls != 0 {
		t.Errorf("runner must not be called on invalid input, got %d calls", f.calls)
	}
}

func TestReaderRemove(t *testing.T) {
	f := &fakeRunner{}
	r := NewWithRunner(f.run)
	if err := r.Remove(context.Background(), "claude.ai Linear", ScopeUser); err != nil {
		t.Fatalf("Remove err: %v", err)
	}
	want := []string{"mcp", "remove", "claude.ai Linear", "-s", "user"}
	if !reflect.DeepEqual(f.lastArgs, want) {
		t.Errorf("Remove argv = %v, want %v", f.lastArgs, want)
	}
}

func TestReaderRemoveNoScope(t *testing.T) {
	f := &fakeRunner{}
	if err := NewWithRunner(f.run).Remove(context.Background(), "x", ""); err != nil {
		t.Fatalf("Remove err: %v", err)
	}
	want := []string{"mcp", "remove", "x"}
	if !reflect.DeepEqual(f.lastArgs, want) {
		t.Errorf("Remove (no scope) argv = %v, want %v", f.lastArgs, want)
	}
}

func TestReaderRemoveValidation(t *testing.T) {
	f := &fakeRunner{}
	if err := NewWithRunner(f.run).Remove(context.Background(), "", ScopeLocal); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty name → want ErrInvalidInput, got %v", err)
	}
	if err := NewWithRunner(f.run).Remove(context.Background(), "x", Scope("root")); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad scope → want ErrInvalidInput, got %v", err)
	}
	if f.calls != 0 {
		t.Errorf("runner must not run on invalid Remove, got %d", f.calls)
	}
}

// TestNewUsesRealExec is a smoke check that New() wires a runner that shells to
// the real binary — gated so it never requires `claude` on the test host.
func TestNewUsesRealExec(t *testing.T) {
	// Opt-in: `claude mcp list` health-checks every configured MCP server, so
	// on a developer machine each `go test ./...` spawned the whole MCP fleet.
	if os.Getenv("SWARMERY_E2E") == "" {
		t.Skip("set SWARMERY_E2E=1 to run the real-CLI integration smoke")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH — skipping real-CLI integration smoke")
	}
	r := New()
	if _, err := r.List(context.Background()); err != nil {
		t.Fatalf("real List failed: %v", err)
	}
}

// TestRedactBin is the path-leak guard: the resolved binary is an absolute path
// under the operator's home, and exec/fs errors embed it in their message. No
// surfaced error may carry it, because these errors reach an API response body.
func TestRedactBin(t *testing.T) {
	for _, bin := range []string{
		"/Users/tester/.local/bin/claude",
		"/home/tester/.npm-global/bin/claude",
	} {
		raw := fmt.Errorf("fork/exec %s: permission denied", bin)
		got := redactBin(raw, bin)
		msg := got.Error()

		if want := "fork/exec claude: permission denied"; msg != want {
			t.Errorf("redactBin(%q) = %q, want %q", bin, msg, want)
		}
		for _, leak := range []string{bin, "/Users/", "/home/"} {
			if strings.Contains(msg, leak) {
				t.Errorf("redacted error %q still leaks %q", msg, leak)
			}
		}
		if !errors.Is(got, raw) {
			t.Errorf("redactBin must preserve the error chain; errors.Is failed for %q", bin)
		}
	}

	// The real home of whoever runs this must never appear either.
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		bin := filepath.Join(home, ".local", "bin", "claude")
		msg := redactBin(fmt.Errorf("exec %s: no such file or directory", bin), bin).Error()
		if strings.Contains(msg, home) {
			t.Errorf("redacted error %q leaks the real home dir", msg)
		}
	}

	// Pass-throughs: nothing to redact, nothing changed.
	if redactBin(nil, "/x/claude") != nil {
		t.Error("a nil error must stay nil")
	}
	unrelated := errors.New("claude mcp list: context deadline exceeded")
	if got := redactBin(unrelated, "/x/claude"); got != unrelated {
		t.Errorf("an error without the path must pass through unchanged, got %v", got)
	}
}

// TestExecRunnerRedactsResolvedPath drives the production runner end to end
// against a resolved-but-missing binary (SWARMERY_CLAUDE_BIN is honoured
// verbatim, no stat), proving the redaction is wired into execRunner itself and
// not just available as a helper. Hermetic: no real claude involved.
func TestExecRunnerRedactsResolvedPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	t.Setenv("SWARMERY_CLAUDE_BIN", bin)

	_, err := execRunner(context.Background(), "mcp", "list")
	if err == nil {
		t.Fatal("execRunner against a missing binary must fail")
	}
	msg := err.Error()
	if strings.Contains(msg, bin) || strings.Contains(msg, dir) {
		t.Errorf("execRunner leaked the resolved path: %q", msg)
	}
	if !strings.HasPrefix(msg, "claude mcp list: ") {
		t.Errorf("execRunner error = %q, want it to keep the `claude <args>: …` shape", msg)
	}
}
