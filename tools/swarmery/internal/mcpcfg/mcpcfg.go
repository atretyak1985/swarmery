// Package mcpcfg reads and mutates Claude Code's MCP-server configuration by
// shelling out to the `claude mcp …` CLI. The source of truth is Claude Code's
// own config — this package never reinvents it; it parses the stable text
// output of `claude mcp list`/`get` and constructs `claude mcp add`/`remove`
// invocations.
//
// The subprocess launcher is injectable (NewWithRunner) exactly like
// toolproc.Config.Command, so unit tests exercise the parser and argv builder
// against captured fixture strings without the real CLI on PATH. Every command
// is executed with the argument vector passed as a []string to the runner —
// never a shell string — so a connector name or argument can never be
// interpreted by a shell (injection-proof by construction).
package mcpcfg

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"unicode"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
)

// Transport is the MCP wire transport of a server.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"
	// TransportUnknown — the CLI did not report a type (e.g. claude.ai-config
	// servers omit it) and it could not be inferred from the detail.
	TransportUnknown Transport = ""
)

// Status is the live connection health of a server, normalized from the CLI's
// glyph+phrase markers.
type Status string

const (
	StatusConnected Status = "connected"
	StatusFailed    Status = "failed"
	StatusNeedsAuth Status = "needs_auth"
	StatusPending   Status = "pending"
	StatusDisabled  Status = "disabled"
	StatusUnknown   Status = "unknown"
)

// Scope is the configuration scope a server lives in.
type Scope string

const (
	ScopeLocal    Scope = "local"
	ScopeUser     Scope = "user"
	ScopeProject  Scope = "project"
	ScopeClaudeAI Scope = "claudeai"
	ScopeUnknown  Scope = "unknown"
)

// Server is one configured MCP server as surfaced to the UI.
type Server struct {
	Name      string    `json:"name"`
	Transport Transport `json:"transport"`
	Scope     Scope     `json:"scope"`
	Status    Status    `json:"status"`
	// Detail is the command+args (stdio) or URL (http/sse) as printed by the
	// CLI — display-only, never re-executed.
	Detail string `json:"detail"`
	// Source labels where the row came from ("cli-list").
	Source string `json:"source"`
}

// AddSpec is a validated request to add a server. Command/Args apply to stdio;
// URL applies to http/sse.
type AddSpec struct {
	Name      string
	Transport Transport
	Command   string
	Args      []string
	URL       string
	Scope     Scope
}

// runner launches `claude` with the given args and returns combined stdout.
// Injectable for tests.
type runner func(ctx context.Context, args ...string) ([]byte, error)

// Reader shells out to the claude CLI. The zero value is not usable; use New or
// NewWithRunner.
type Reader struct {
	run runner
}

// New returns a Reader that executes the real `claude` binary from PATH.
func New() *Reader {
	return &Reader{run: execRunner}
}

// NewWithRunner returns a Reader driven by a caller-supplied runner (tests).
func NewWithRunner(run func(ctx context.Context, args ...string) ([]byte, error)) *Reader {
	return &Reader{run: run}
}

// ErrClaudeNotFound reports that the CLI this package shells to could not be
// located. The API maps it to 503 (the host cannot serve connectors) rather
// than 500 (the daemon is broken) — a missing CLI is not a server fault.
var ErrClaudeNotFound = errors.New("claude CLI not found")

// execRunner is the production launcher: `claude <args…>`, capturing stdout.
// The binary is resolved through claudebin rather than relying on PATH, because
// launchd starts the daemon with a minimal PATH that omits every install dir
// claude actually lives in. CombinedOutput folds stderr in so a non-zero exit
// still yields the CLI's human-readable reason for the error message.
//
// cmd.Dir is deliberately unset: the daemon's own working directory is what
// `claude mcp list` reports from, and changing it would silently change which
// servers are visible.
func execRunner(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := claudebin.Resolve()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrClaudeNotFound, err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("claude %s: %w: %s",
			strings.Join(args, " "), redactBin(err, bin), redactPath(strings.TrimSpace(string(out)), bin))
	}
	return out, nil
}

// redactPath rewrites every occurrence of the resolved executable path — an
// absolute path, usually under the operator's home — to the literal "claude".
func redactPath(s, bin string) string {
	if bin == "" {
		return s
	}
	return strings.ReplaceAll(s, bin, "claude")
}

// redactBin applies redactPath to an error's message so nothing surfaced
// through the API carries filesystem detail. exec.Error and fs.PathError both
// embed the resolved path in their message — that is exactly the leak this
// closes. The original error stays reachable through Unwrap, so errors.Is/As
// over the chain (e.g. *exec.ExitError) behave as they did before redaction.
func redactBin(err error, bin string) error {
	if err == nil || bin == "" {
		return err
	}
	msg := redactPath(err.Error(), bin)
	if msg == err.Error() {
		return err
	}
	return &redactedErr{msg: msg, err: err}
}

// redactedErr carries a sanitized message over an intact error chain.
type redactedErr struct {
	msg string
	err error
}

func (e *redactedErr) Error() string { return e.msg }
func (e *redactedErr) Unwrap() error { return e.err }

// List returns every configured MCP server via `claude mcp list`.
func (r *Reader) List(ctx context.Context) ([]Server, error) {
	out, err := r.run(ctx, "mcp", "list")
	if err != nil {
		return nil, err
	}
	return parseList(out), nil
}

// Add validates the spec, builds an injection-safe argv, and runs
// `claude mcp add …`.
func (r *Reader) Add(ctx context.Context, spec AddSpec) error {
	args, err := addArgs(spec)
	if err != nil {
		return err
	}
	_, err = r.run(ctx, args...)
	return err
}

// Remove runs `claude mcp remove <name> [-s <scope>]`. An empty scope lets the
// CLI remove the server from whichever scope holds it.
func (r *Reader) Remove(ctx context.Context, name string, scope Scope) error {
	if err := validateName(name); err != nil {
		return err
	}
	args := []string{"mcp", "remove", name}
	if scope != "" {
		if err := validateScope(scope); err != nil {
			return err
		}
		args = append(args, "-s", string(scope))
	}
	_, err := r.run(ctx, args...)
	return err
}

// ── validation ───────────────────────────────────────────────────────────────

// ErrInvalidInput wraps every validation failure so the API layer can map it to
// a 400 rather than a 500.
var ErrInvalidInput = errors.New("invalid connector input")

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if len(name) > 200 {
		return fmt.Errorf("%w: name too long", ErrInvalidInput)
	}
	// `claude mcp add|remove` parse their argv with an option parser: a name
	// that begins with a dash would be read as one of their flags.
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("%w: name must not begin with '-'", ErrInvalidInput)
	}
	for _, r := range name {
		// Control characters (incl. NUL, newline) have no place in a server
		// name and are the kind of thing an injection attempt would smuggle.
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: name contains control characters", ErrInvalidInput)
		}
	}
	return nil
}

func validateScope(scope Scope) error {
	switch scope {
	case ScopeLocal, ScopeUser, ScopeProject:
		return nil
	default:
		return fmt.Errorf("%w: unknown scope %q", ErrInvalidInput, scope)
	}
}

// addArgs turns a spec into the exact argv for `claude mcp add`, validating
// every field. The returned slice is safe to hand to exec.Command: each element
// is a discrete argument, so no shell parsing of user input ever occurs.
func addArgs(spec AddSpec) ([]string, error) {
	if err := validateName(spec.Name); err != nil {
		return nil, err
	}
	// The page offers only local|user; project is accepted for completeness.
	if err := validateScope(spec.Scope); err != nil {
		return nil, err
	}

	args := []string{"mcp", "add"}

	switch spec.Transport {
	case TransportStdio:
		if strings.TrimSpace(spec.Command) == "" {
			return nil, fmt.Errorf("%w: stdio transport requires a command", ErrInvalidInput)
		}
		// A command is an executable, never a flag; one beginning with a dash
		// is refused rather than passed on.
		if strings.HasPrefix(spec.Command, "-") {
			return nil, fmt.Errorf("%w: command must not begin with '-'", ErrInvalidInput)
		}
		if err := validateArgTokens(spec.Args); err != nil {
			return nil, err
		}
		// `--` ends the CLI's own options before the command: everything after
		// it is the child argv, taken positionally. A child argument that begins
		// with a dash (`npx -y pkg`, `-e K=V`) is then the child's, not an option
		// of `mcp add` — without the marker `-e K=V` became an environment entry
		// of the server. Each token is still passed discretely.
		args = append(args, "-s", string(spec.Scope), spec.Name, "--", spec.Command)
		args = append(args, spec.Args...)
	case TransportHTTP, TransportSSE:
		if err := validateURL(spec.URL); err != nil {
			return nil, err
		}
		args = append(args, "--transport", string(spec.Transport), "-s", string(spec.Scope), spec.Name, spec.URL)
	default:
		return nil, fmt.Errorf("%w: unknown transport %q", ErrInvalidInput, spec.Transport)
	}

	return args, nil
}

func validateArgTokens(tokens []string) error {
	for _, t := range tokens {
		for _, r := range t {
			if r == 0 {
				return fmt.Errorf("%w: argument contains a NUL byte", ErrInvalidInput)
			}
		}
	}
	return nil
}

func validateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%w: http/sse transport requires a url", ErrInvalidInput)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: malformed url", ErrInvalidInput)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: url must be http(s)", ErrInvalidInput)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: url must have a host", ErrInvalidInput)
	}
	return nil
}

// ── parsing ──────────────────────────────────────────────────────────────────

// parseList turns `claude mcp list` stdout into servers. The format is one line
// per server: "<name>: <detail> - <glyph> <phrase>". The status marker (last
// " - <glyph…>") is the parse anchor; the remainder before it is "name: detail",
// split on the first ": ". Header/blank/indented lines are skipped. The parser
// is total — an unrecognized line is dropped, never fatal.
func parseList(stdout []byte) []Server {
	servers := []Server{}
	for _, raw := range strings.Split(string(stdout), "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		// Skip the "Checking MCP server health…" header and any indented
		// continuation (details are always inline for list, but be defensive).
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		if strings.HasPrefix(line, "Checking ") {
			continue
		}
		// Must look like "name: …" to be a server row.
		colon := strings.Index(line, ": ")
		if colon < 0 {
			continue
		}
		name := line[:colon]
		rest := line[colon+2:]

		// Split the trailing status off the detail. The marker is " - " before
		// a glyph/phrase; use the LAST occurrence so a " - " inside a detail
		// (rare) doesn't fool us.
		detail := rest
		status := StatusUnknown
		if idx := strings.LastIndex(rest, " - "); idx >= 0 {
			detail = strings.TrimSpace(rest[:idx])
			status = parseStatus(rest[idx+3:])
		}

		servers = append(servers, Server{
			Name:      name,
			Transport: inferTransport(detail),
			Scope:     ScopeUnknown, // list output does not carry scope
			Status:    status,
			Detail:    detail,
			Source:    "cli-list",
		})
	}
	return servers
}

// parseStatus normalizes a glyph+phrase marker to a Status. Matching is on
// keywords/glyphs (not exact strings) so cosmetic CLI changes don't break it.
func parseStatus(marker string) Status {
	m := strings.ToLower(strings.TrimSpace(marker))
	switch {
	case strings.Contains(m, "✔") || strings.Contains(m, "connected"):
		return StatusConnected
	case strings.Contains(m, "✘") || strings.Contains(m, "failed"):
		return StatusFailed
	case strings.Contains(m, "!") || strings.Contains(m, "needs authentication") || strings.Contains(m, "authenticat"):
		return StatusNeedsAuth
	case strings.Contains(m, "⏸") || strings.Contains(m, "pending"):
		return StatusPending
	case strings.Contains(m, "disabled"):
		return StatusDisabled
	default:
		return StatusUnknown
	}
}

// inferTransport guesses the transport from a list detail: a URL detail is
// http/sse (a "(SSE)" suffix disambiguates; default http), otherwise stdio.
func inferTransport(detail string) Transport {
	d := strings.ToLower(detail)
	if strings.Contains(d, "(sse)") {
		return TransportSSE
	}
	if strings.HasPrefix(d, "http://") || strings.HasPrefix(d, "https://") {
		return TransportHTTP
	}
	return TransportStdio
}
