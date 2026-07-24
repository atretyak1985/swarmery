package dispatch

import (
	"strings"
	"text/template"
)

// contractTemplate is the EXECUTION CONTRACT appended verbatim to every
// dispatched task prompt (phase-3 spec — normative). It injects the honest-exit
// sentinel vocabulary (DESIGN.md §4.1), the Swarm-Task-Id commit trailer
// (§4.3), the declared file scope, and the "work only in this worktree" fence.
// text/template so {branch}/{taskID}/{fileScope} interpolate without any risk
// of prompt-side format bugs; the literal wording below is fixed.
var contractTemplate = template.Must(template.New("contract").Parse(
	`--- EXECUTION CONTRACT (swarmery dispatcher) ---
You are running unattended inside a dedicated git worktree on branch {{.Branch}}. Work ONLY here.
- Commit your work in logical increments. Every commit message MUST end with the trailer line:
  Swarm-Task-Id: {{.TaskID}}
- Stay within this file scope if declared: {{.FileScope}}. If a required change falls outside it, stop and end with: BLOCKED: <what and why>.
- If you discover the requested work is already done on HEAD, do NOT redo it. End your reply with: PREMISE STALE: <evidence>.
- If no changes are needed, end with: NO-OP: <reason>. If this duplicates other tracked work, end with: DUPLICATE: <task-id>.
- If you are genuinely blocked, end with: BLOCKED: <reason>. Never fake completion by skipping the remaining work.
- Do not push, do not create PRs, do not switch branches.
--- END CONTRACT ---`))

// contractData is the template payload.
type contractData struct {
	Branch    string
	TaskID    string
	FileScope string
}

// scopeText renders a file-scope list for the contract line. Empty ⇒ the
// literal "(none declared — the whole worktree)" so the model gets an explicit
// signal rather than a bare blank.
func scopeText(scope []string) string {
	clean := cleanScope(scope)
	if len(clean) == 0 {
		return "(none declared — the whole worktree)"
	}
	return strings.Join(clean, ", ")
}

// BuildPrompt assembles the full dispatched prompt: the task's own prompt
// (the workspace step-doc / board card body) followed by a blank line and the
// execution contract block. taskID is the external card id (T-xxxxxx); branch
// is the worktree branch (swarm/<id>).
func BuildPrompt(taskPrompt, branch, taskID string, fileScope []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(taskPrompt, "\n"))
	b.WriteString("\n\n")
	// template execution on a fixed template with string data cannot fail; the
	// error is ignored deliberately (belt-and-braces: a failure would leave the
	// contract absent, which the caller would still spawn — acceptable, and
	// unreachable).
	_ = contractTemplate.Execute(&b, contractData{
		Branch:    branch,
		TaskID:    taskID,
		FileScope: scopeText(fileScope),
	})
	return b.String()
}

// Sentinel classification of a dispatched session's final assistant text.
// Order matters only in that the terminal-done sentinels are distinct from
// BLOCKED. Prefixes are matched case-insensitively on the trimmed first
// non-empty line's leading token (Fusion parses from the reply tail; our
// contract instructs the model to END with the sentinel, so the last assistant
// turn's leading sentinel is authoritative).

// Sentinel is the parsed outcome of a dispatched run's final assistant text.
type Sentinel struct {
	Kind string // "" (none) | "done" | "blocked"
	Line string // the full sentinel line, for result_note / dispatch_error
}

// doneSentinels move a task straight to done with the line as result_note.
var doneSentinels = []string{"PREMISE STALE:", "NO-OP:", "NOOP:", "DUPLICATE:", "REDUNDANT:"}

// blockedSentinel routes to todo + paused with the line as dispatch_error.
const blockedSentinel = "BLOCKED:"

// ClassifySentinel scans the final assistant text for a leading sentinel. It
// checks every non-empty line (not just the first) because a model may append
// the sentinel as the last line after prose; the LAST matching sentinel wins
// (closest to the reply's end, matching the contract's "end with" instruction).
func ClassifySentinel(text string) Sentinel {
	var found Sentinel
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if hasSentinelPrefix(upper, blockedSentinel) {
			found = Sentinel{Kind: "blocked", Line: line}
			continue
		}
		for _, s := range doneSentinels {
			if hasSentinelPrefix(upper, s) {
				found = Sentinel{Kind: "done", Line: line}
				break
			}
		}
	}
	return found
}

// hasSentinelPrefix reports whether an upper-cased line begins with a sentinel
// token. It tolerates common markdown emphasis wrappers the model might add
// (leading '*', '-', '#', '>' and backticks) before the token.
func hasSentinelPrefix(upperLine, token string) bool {
	trimmed := strings.TrimLeft(upperLine, "*-#> `")
	return strings.HasPrefix(trimmed, token)
}
