// Package hookshim implements `swarmery hook <event>` — the tiny runtime
// process Claude Code invokes as a PermissionRequest / Stop hook. It forwards
// the hook stdin to the daemon and maps the daemon's answer onto the hook
// stdout contract (docs/hooks-protocol.md, docs/hooks-format.md E2/E3).
//
// Fail-open is the law here (D3, verified E5/E6/E7): on ANY failure — daemon
// down, connect timeout, non-contract status, malformed body, poll expiry —
// the shim exits 0 with NO stdout, so Claude Code falls back to its native
// terminal permission dialog. The shim never returns a non-zero exit and
// never blocks a session.
package hookshim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Hook event names (the `swarmery hook <event>` argument = URL path suffix).
const (
	EventPermissionRequest = "permission-request"
	EventStop              = "stop"
)

// Timing knobs, frozen by docs/hooks-protocol.md §Timing (Q-A/E6).
const (
	DefaultConnectTimeout = 500 * time.Millisecond
	DefaultPollTimeout    = 120 * time.Second
	stopTimeout           = 5 * time.Second
	maxStdin              = 4 << 20
)

// Config wires the shim; zero values fall back to the frozen defaults.
type Config struct {
	BaseURL        string        // daemon origin, e.g. http://127.0.0.1:7777
	ConnectTimeout time.Duration // dead-daemon budget (default 500ms)
	PollTimeout    time.Duration // long-poll wall clock (default 120s)
	Stdout         io.Writer     // hook stdout (the decision channel to Claude)
	Stderr         io.Writer     // operator notices (spinner hint)
	LogPath        string        // one-line-per-call audit log; "" disables
}

// hookSpecificOutput is the verified decision contract (E2/E3): stdout shape
// Claude Code parses from a PermissionRequest hook.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName string       `json:"hookEventName"`
		Decision      hookDecision `json:"decision"`
	} `json:"hookSpecificOutput"`
}

type hookDecision struct {
	Behavior string  `json:"behavior"`
	Message  *string `json:"message,omitempty"`
	// UpdatedInput carries dashboard-entered AskUserQuestion answers
	// ({questions, answers}) verbatim from the daemon's 200 body to Claude
	// (hooks-protocol amendment 1, spike E12a). Empty → key omitted, stdout
	// byte-identical to the frozen gate-2.2 form.
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
}

// Run executes one hook invocation and returns the process exit code —
// always 0 by contract (a crashing shim would still fail open per E7, but
// exit 0 + silence is the designed path).
func Run(event string, stdin io.Reader, cfg Config) int {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = DefaultPollTimeout
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}

	body, err := io.ReadAll(io.LimitReader(stdin, maxStdin))
	if err != nil {
		logLine(cfg.LogPath, event, "", "stdin-error")
		return 0
	}
	tool := toolNameOf(body)

	switch event {
	case EventPermissionRequest:
		outcome := runPermissionRequest(body, cfg)
		logLine(cfg.LogPath, event, tool, outcome)
	case EventStop:
		outcome := post(cfg, EventStop, body, stopTimeout, nil)
		logLine(cfg.LogPath, event, tool, outcome)
	default:
		fmt.Fprintf(cfg.Stderr, "swarmery hook: unknown event %q\n", event)
		logLine(cfg.LogPath, event, tool, "unknown-event")
	}
	return 0
}

// runPermissionRequest long-polls the daemon and prints the decision JSON on
// 200; everything else is silent fail-open. Returns the outcome for the log.
func runPermissionRequest(body []byte, cfg Config) string {
	fmt.Fprintln(cfg.Stderr, "swarmery: waiting for remote approval… (Esc cancels; native dialog on timeout)")

	var decided *hookDecisionBody
	outcome := post(cfg, EventPermissionRequest, body, cfg.PollTimeout, func(resp *http.Response) string {
		if resp.StatusCode != http.StatusOK {
			return fmt.Sprintf("http-%d", resp.StatusCode) // incl. 204 → fail-open
		}
		var d hookDecisionBody
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
			return "bad-body"
		}
		if d.Decision != "allow" && d.Decision != "deny" {
			return "bad-decision"
		}
		decided = &d
		return d.Decision
	})

	if decided == nil {
		return outcome // no stdout → Claude shows the native dialog (E5)
	}
	var out hookOutput
	out.HookSpecificOutput.HookEventName = "PermissionRequest"
	out.HookSpecificOutput.Decision.Behavior = decided.Decision
	if decided.Decision == "deny" {
		// message reaches Claude verbatim as the tool result (E3) — include
		// the key even when the human left the reason empty.
		msg := decided.Message
		out.HookSpecificOutput.Decision.Message = &msg
	}
	if decided.Decision == "allow" && len(decided.UpdatedInput) > 0 && string(decided.UpdatedInput) != "null" {
		// updatedInput accompanies allow only (hooks-protocol amendment 1):
		// forwarded verbatim, never inspected — the daemon builds and
		// validates it (E12a/b: answers injected, no terminal dialog).
		out.HookSpecificOutput.Decision.UpdatedInput = decided.UpdatedInput
	}
	enc, err := json.Marshal(out)
	if err != nil {
		return "marshal-error"
	}
	fmt.Fprintln(cfg.Stdout, string(enc))
	return outcome
}

// hookDecisionBody is the daemon's 200 body (docs/hooks-protocol.md,
// updatedInput per amendment 1 — absent everywhere except answered
// AskUserQuestion requests).
type hookDecisionBody struct {
	Decision     string          `json:"decision"`
	Message      string          `json:"message"`
	UpdatedInput json.RawMessage `json:"updatedInput"`
}

// post sends the hook stdin to the daemon. onResp (may be nil) classifies a
// received response; transport failures return their own outcome strings.
func post(cfg Config, event string, body []byte, timeout time.Duration, onResp func(*http.Response) string) string {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:       (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext,
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/api/hooks/"+event, bytes.NewReader(body))
	if err != nil {
		return "request-error"
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "unreachable" // connect refused / timeout / poll deadline
	}
	defer resp.Body.Close()
	if onResp == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return fmt.Sprintf("http-%d", resp.StatusCode)
	}
	return onResp(resp)
}

func toolNameOf(body []byte) string {
	var p struct {
		ToolName string `json:"tool_name"`
	}
	_ = json.Unmarshal(body, &p)
	return p.ToolName
}

// logLine appends one audit line (ts, event, tool, outcome) — best-effort,
// never fatal.
func logLine(path, event, tool, outcome string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s event=%s tool=%s outcome=%s\n",
		time.Now().UTC().Format(time.RFC3339), event, tool, outcome)
}
