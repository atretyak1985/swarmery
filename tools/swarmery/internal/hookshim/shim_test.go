package hookshim

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const stdinFixture = `{"session_id":"sid-1","cwd":"/tmp/p","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`

// daemonStub answers /api/hooks/* with a scripted status/body and records
// what the shim sent.
func daemonStub(t *testing.T, status int, body string) (*httptest.Server, *[]byte) {
	t.Helper()
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		if r.Header.Get("Origin") != "" {
			t.Error("the shim must send no Origin header")
		}
		w.WriteHeader(status)
		if body != "" {
			w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func runShim(t *testing.T, event, baseURL string, stdin string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = Run(event, strings.NewReader(stdin), Config{
		BaseURL: baseURL,
		Stdout:  &out,
		Stderr:  &errBuf,
	})
	return out.String(), errBuf.String(), code
}

// TestAllowDecision: 200 {"decision":"allow"} → EXACT hookSpecificOutput
// stdout (verified decision contract, spike E2).
func TestAllowDecision(t *testing.T) {
	srv, got := daemonStub(t, 200, `{"decision":"allow"}`)
	stdout, _, code := runShim(t, EventPermissionRequest, srv.URL, stdinFixture)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if string(*got) != stdinFixture {
		t.Errorf("daemon got %q, want the stdin verbatim", *got)
	}
}

// TestDenyDecision: deny carries the human's reason via decision.message (E3).
func TestDenyDecision(t *testing.T) {
	srv, _ := daemonStub(t, 200, `{"decision":"deny","message":"not on prod"}`)
	stdout, _, code := runShim(t, EventPermissionRequest, srv.URL, stdinFixture)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"not on prod"}}}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestNoDecision204: 204 → exit 0 with NO stdout (fail-open, E5).
func TestNoDecision204(t *testing.T) {
	srv, _ := daemonStub(t, 204, "")
	stdout, _, code := runShim(t, EventPermissionRequest, srv.URL, stdinFixture)
	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q, want 0 and empty", code, stdout)
	}
}

// TestNonContractResponses: 4xx/5xx/garbage bodies are all silent fail-open.
func TestNonContractResponses(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{429, `{"error":"too many"}`},
		{500, `{"error":"boom"}`},
		{200, `garbage not json`},
		{200, `{"decision":"maybe"}`},
	}
	for _, c := range cases {
		srv, _ := daemonStub(t, c.status, c.body)
		stdout, _, code := runShim(t, EventPermissionRequest, srv.URL, stdinFixture)
		if code != 0 || stdout != "" {
			t.Errorf("status=%d body=%q: exit=%d stdout=%q, want silent exit 0",
				c.status, c.body, code, stdout)
		}
	}
}

// TestDaemonDownFastFailOpen: connection refused → silent exit 0 well under
// the ≤1s D3 budget.
func TestDaemonDownFastFailOpen(t *testing.T) {
	// Grab a port nobody listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	start := time.Now()
	stdout, _, code := runShim(t, EventPermissionRequest, "http://"+addr, stdinFixture)
	elapsed := time.Since(start)
	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q, want silent exit 0", code, stdout)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("fail-open took %s, want < 1.5s", elapsed)
	}
}

// TestStopEvent: fires the POST, never writes stdout regardless of outcome.
func TestStopEvent(t *testing.T) {
	srv, got := daemonStub(t, 202, "")
	stopStdin := `{"session_id":"sid-1","hook_event_name":"Stop","stop_hook_active":false}`
	stdout, _, code := runShim(t, EventStop, srv.URL, stopStdin)
	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q, want silent exit 0", code, stdout)
	}
	if string(*got) != stopStdin {
		t.Errorf("daemon got %q, want the stdin verbatim", *got)
	}
}

// TestUnknownEventIsSilent: never break a session on a bad argument.
func TestUnknownEventIsSilent(t *testing.T) {
	stdout, _, code := runShim(t, "nonsense", "http://127.0.0.1:1", "{}")
	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q, want silent exit 0", code, stdout)
	}
}

// TestWaitingNotice: the operator sees the stderr hint while polling (Q-A).
func TestWaitingNotice(t *testing.T) {
	srv, _ := daemonStub(t, 204, "")
	_, stderr, _ := runShim(t, EventPermissionRequest, srv.URL, stdinFixture)
	if !strings.Contains(stderr, "waiting for remote approval") {
		t.Errorf("stderr = %q, want the waiting notice", stderr)
	}
}

// ── updatedInput passthrough (hooks-protocol amendment 1, spike E12) ─────────

// TestAllowWithUpdatedInput: the 200 body's updatedInput rides VERBATIM as
// hookSpecificOutput.decision.updatedInput (E12a — answers injected, no
// terminal dialog).
func TestAllowWithUpdatedInput(t *testing.T) {
	updated := `{"questions":[{"question":"Pick a color","options":[{"label":"Red"}],"multiSelect":false}],"answers":{"Pick a color":"Red"}}`
	srv, _ := daemonStub(t, 200, `{"decision":"allow","updatedInput":`+updated+`}`)
	stdout, _, code := runShim(t, EventPermissionRequest, srv.URL, stdinFixture)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":` + updated + `}}}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestAllowUpdatedInputAbsent: no updatedInput (or a literal null) keeps the
// stdout byte-identical to the frozen gate-2.2 allow form — old daemons and
// new shims stay compatible in both directions.
func TestAllowUpdatedInputAbsent(t *testing.T) {
	want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` + "\n"
	for _, body := range []string{`{"decision":"allow"}`, `{"decision":"allow","updatedInput":null}`} {
		srv, _ := daemonStub(t, 200, body)
		stdout, _, code := runShim(t, EventPermissionRequest, srv.URL, stdinFixture)
		if code != 0 || stdout != want {
			t.Errorf("body %s: exit=%d stdout=%q, want the frozen allow form", body, code, stdout)
		}
	}
}

// TestDenyIgnoresUpdatedInput: updatedInput accompanies allow only — a deny
// carrying one (non-contract) must not forward it.
func TestDenyIgnoresUpdatedInput(t *testing.T) {
	srv, _ := daemonStub(t, 200, `{"decision":"deny","message":"m","updatedInput":{"x":1}}`)
	stdout, _, code := runShim(t, EventPermissionRequest, srv.URL, stdinFixture)
	want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"m"}}}` + "\n"
	if code != 0 || stdout != want {
		t.Errorf("exit=%d stdout=%q, want deny without updatedInput", code, stdout)
	}
}

// ── SessionStart additionalContext injection (plugin drift) ──────────────────

const sessionStartStdin = `{"session_id":"sid-1","cwd":"/Volumes/Work/swarmery","hook_event_name":"SessionStart"}`

// A 200 carrying additionalContext becomes exactly one line of SessionStart
// hook JSON on stdout — that line is what Claude Code prepends to the session.
func TestSessionStartInjectsAdditionalContext(t *testing.T) {
	srv, got := daemonStub(t, 200, `{"additionalContext":"core@swarmery is NOT loaded"}`)
	stdout, _, code := runShim(t, EventSessionStart, srv.URL, sessionStartStdin)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := `{"hookSpecificOutput":{"additionalContext":"core@swarmery is NOT loaded","hookEventName":"SessionStart"}}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Errorf("stdout must be exactly one line, got %q", stdout)
	}
	if !strings.Contains(string(*got), `"pid"`) {
		t.Errorf("the shim must still inject the PPID, sent %q", string(*got))
	}
}

// 204 is the normal answer for a healthy project: print nothing at all.
func TestSessionStart204PrintsNothing(t *testing.T) {
	srv, _ := daemonStub(t, 204, "")
	stdout, _, code := runShim(t, EventSessionStart, srv.URL, sessionStartStdin)
	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q, want 0 and silence", code, stdout)
	}
}

// A 200 with an empty context is silence too — never an empty injected block.
func TestSessionStartEmptyContextPrintsNothing(t *testing.T) {
	srv, _ := daemonStub(t, 200, `{"additionalContext":""}`)
	stdout, _, code := runShim(t, EventSessionStart, srv.URL, sessionStartStdin)
	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q, want 0 and silence", code, stdout)
	}
}

func TestSessionStartBadBodyPrintsNothing(t *testing.T) {
	srv, _ := daemonStub(t, 200, `not json`)
	stdout, _, code := runShim(t, EventSessionStart, srv.URL, sessionStartStdin)
	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q, want 0 and silence", code, stdout)
	}
}

// Fail-open is the law: a dead daemon must not break every session start.
func TestSessionStartDaemonDownPrintsNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing is listening there now

	stdout, _, code := runShim(t, EventSessionStart, "http://"+addr, sessionStartStdin)
	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q, want 0 and silence", code, stdout)
	}
}

// ── SessionStart terminal identity (notch companion, phase 1) ───────────────

// sentTerminal decodes the "terminal" object the shim POSTed to the daemon.
func sentTerminal(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var payload struct {
		Terminal map[string]string `json:"terminal"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode daemon body %q: %v", body, err)
	}
	return payload.Terminal
}

// Every source variable maps to its documented field (E-fixture: a fully
// populated Warp environment, iTerm's session id takes precedence over
// TERM_SESSION_ID when both happen to be set).
func TestSessionStartTerminalIdentityMapsEveryVariable(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "WarpTerminal")
	t.Setenv("WARP_FOCUS_URL", "warp://focus/abc")
	t.Setenv("__CFBundleIdentifier", "dev.warp.Warp-Stable")
	t.Setenv("ITERM_SESSION_ID", "w0t1p2:iterm-uuid")
	t.Setenv("TERM_SESSION_ID", "should-lose-to-iterm")

	srv, got := daemonStub(t, 204, "")
	_, _, code := runShim(t, EventSessionStart, srv.URL, sessionStartStdin)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	terminal := sentTerminal(t, *got)
	want := map[string]string{
		"program":   "WarpTerminal",
		"focusUrl":  "warp://focus/abc",
		"bundleId":  "dev.warp.Warp-Stable",
		"sessionId": "w0t1p2:iterm-uuid",
	}
	for field, wantVal := range want {
		if terminal[field] != wantVal {
			t.Errorf("terminal[%q] = %q, want %q", field, terminal[field], wantVal)
		}
	}
}

// TERM_SESSION_ID is the fallback when ITERM_SESSION_ID is absent (vscode's
// integrated terminal and other TERM_SESSION_ID-only emulators).
func TestSessionStartTerminalIdentityFallsBackToTermSessionID(t *testing.T) {
	t.Setenv("ITERM_SESSION_ID", "")
	t.Setenv("TERM_SESSION_ID", "vscode-term-1")

	srv, got := daemonStub(t, 204, "")
	runShim(t, EventSessionStart, srv.URL, sessionStartStdin)

	terminal := sentTerminal(t, *got)
	if terminal["sessionId"] != "vscode-term-1" {
		t.Errorf("terminal[sessionId] = %q, want the TERM_SESSION_ID fallback", terminal["sessionId"])
	}
}

// Absent variables are omitted from the JSON object entirely, never sent as
// empty strings — the daemon tells "unset" from "explicitly blank" this way.
func TestSessionStartTerminalIdentityOmitsAbsentVars(t *testing.T) {
	for _, v := range []string{"TERM_PROGRAM", "WARP_FOCUS_URL", "__CFBundleIdentifier", "ITERM_SESSION_ID", "TERM_SESSION_ID"} {
		t.Setenv(v, "")
	}

	srv, got := daemonStub(t, 204, "")
	runShim(t, EventSessionStart, srv.URL, sessionStartStdin)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(*got, &raw); err != nil {
		t.Fatalf("decode daemon body %q: %v", *got, err)
	}
	var terminal map[string]json.RawMessage
	if err := json.Unmarshal(raw["terminal"], &terminal); err != nil {
		t.Fatalf("decode terminal object %q: %v", raw["terminal"], err)
	}
	if len(terminal) != 0 {
		t.Errorf("terminal = %v, want every field omitted when its variable is absent", terminal)
	}
}
