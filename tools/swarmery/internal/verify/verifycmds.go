package verify

import (
	"path/filepath"
	"regexp"
	"strings"
)

// The verifier's least-privilege allow-list comes from the target's own
// contract: a phase doc's Verify block names every command a check needs
// (plan-format.md: "Verification commands are file-backed" — a project script by
// absolute path, or one tool invocation with its arguments). The verifier may run
// those, read anything, and read git history, and nothing else.
//
// Measured on CLI 2.1.308: dontAsk + `Bash(./run-checks.sh*)` runs exactly that
// command while a commit, a remote write, a curl and a token read are denied.
// Not measured: compound or prefixed shapes (`cd x && go test`, `FOO=1 make`,
// pipes) against a prefix rule — so the prompt tells the verifier to run each
// command verbatim, one per Bash call, and that a denied check is INCONCLUSIVE.

// verifyHeading matches the line that opens a Verify block: `Verify:`,
// `Verify (each is one command):`, `Verification commands:` — a line that ENDS
// at its colon. The header row `**Verify:** normal` does not match (it starts
// with `**` and carries a value after the colon).
var verifyHeading = regexp.MustCompile(`(?i)^(?:#+\s*)?verif(?:y|ication)\b[^:` + "`" + `*]*:\s*$`)

// bulletLine is one list item: `- cmd`, `* cmd` or `1. cmd`.
var bulletLine = regexp.MustCompile(`^(?:[-*]|\d+[.)])\s+(.+)$`)

// ParseVerifyCommands returns the commands of the first Verify block in doc,
// in order, each stripped of a wrapping pair of backticks. ok is false when the
// doc declares no block, the block lists nothing, or any command could not be
// carried by a prefix rule: a comma or a parenthesis would break the
// comma-joined --allowedTools value, and a heredoc or a multi-line command is
// not one invocation. One unusable command voids the whole block on purpose —
// a list that silently missed a check would turn that check into a denial.
func ParseVerifyCommands(doc string) (cmds []string, ok bool) {
	lines := strings.Split(doc, "\n")
	for i, raw := range lines {
		if !verifyHeading.MatchString(strings.TrimSpace(raw)) {
			continue
		}
		for _, next := range lines[i+1:] {
			line := strings.TrimSpace(next)
			if line == "" {
				if len(cmds) == 0 {
					continue // a blank line between the heading and its first item
				}
				break
			}
			m := bulletLine.FindStringSubmatch(line)
			if m == nil {
				break
			}
			cmd := strings.TrimSpace(m[1])
			if len(cmd) >= 2 && strings.HasPrefix(cmd, "`") && strings.HasSuffix(cmd, "`") {
				cmd = strings.TrimSpace(cmd[1 : len(cmd)-1])
			}
			if !carriable(cmd) {
				return nil, false
			}
			cmds = append(cmds, cmd)
		}
		return cmds, len(cmds) > 0
	}
	return nil, false
}

// envPrefix matches a leading `NAME=value` assignment (`FOO=1 make test`).
var envPrefix = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// carriable reports whether cmd can travel as one `Bash(<cmd>*)` rule. Beyond
// what breaks the comma-joined flag (a comma, parentheses, a backtick, a
// newline), it refuses every COMPOUND or prefixed shape: the CLI splits a
// compound command and matches each subcommand against the allow rules on its
// own, so `cd tools/x && go test ./...` is not admitted by a rule spelling the
// whole line — the `cd` half matches nothing and the check is denied. A leading
// `cd ` or `NAME=` prefix, `&&`, `||`, `;`, `|`, `&`, `$(`, `>` and `<` (heredocs
// included) all void the block, so it falls back to bypass + the deny list.
//
// Also refused: a trailing `\` (a continuation — the real command is longer
// than the line), and the wrapper prefixes (`env `, `time `, `sudo `, `nohup `,
// `xargs `, `exec `, `nice `, `command `), which put a different program in
// front of the declared check.
func carriable(cmd string) bool {
	if cmd == "" || strings.ContainsAny(cmd, ",()`\n;|&<>") || strings.Contains(cmd, "$(") ||
		strings.HasSuffix(cmd, `\`) {
		return false
	}
	for _, p := range wrapperPrefixes {
		if strings.HasPrefix(cmd, p) {
			return false
		}
	}
	return !envPrefix.MatchString(cmd)
}

// wrapperPrefixes are the leading words that make a line not one plain
// invocation of the declared check (`cd ` included: a compound in disguise).
var wrapperPrefixes = []string{"cd ", "env ", "time ", "sudo ", "nohup ", "xargs ", "exec ", "nice ", "command "}

// readTools are what every least-privilege grader may use.
var readTools = []string{"Read", "Grep", "Glob"}

// ReadOnlyTools returns a fresh copy of the read tools (the reviewers' whole
// allow-list).
func ReadOnlyTools() []string { return append([]string(nil), readTools...) }

// gitReadRules are the read-only git commands the verifier's prompt asks for
// ("diff vs <base>"). `git diff*` without a space also admits a bare `git diff`.
var gitReadRules = []string{"Bash(git diff*)", "Bash(git log*)", "Bash(git show*)", "Bash(git status*)"}

// VerifierAllowList is the verifier's --allowedTools for declared commands: the
// read tools, read-only git, and one `Bash(<cmd>*)` per command. A command that
// names the PROJECT path (a phase doc written against the primary checkout) is
// also allowed with the WORKTREE path in its place, because the verifier runs in
// the worktree and is right to substitute it.
func VerifierAllowList(cmds []string, projectPath, worktreePath string) []string {
	out := append(ReadOnlyTools(), gitReadRules...)
	proj := cleanDir(projectPath)
	wt := cleanDir(worktreePath)
	for _, c := range cmds {
		out = append(out, "Bash("+c+"*)")
		if proj != "" && wt != "" && proj != wt && strings.Contains(c, proj) {
			out = append(out, "Bash("+strings.ReplaceAll(c, proj, wt)+"*)")
		}
	}
	return out
}

func cleanDir(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// runScopeHeading labels the daemon's notice inside the contract text, so the
// verifier can tell it from the criteria it grades.
const runScopeHeading = "RUN SCOPE (set by the daemon, not part of the contract above):"

// WithRunScope appends the least-privilege notice to a verification contract:
// which commands Bash is limited to, how to run them, and that a denied check is
// INCONCLUSIVE. No commands returns the contract unchanged (the fallback run).
func WithRunScope(contract string, cmds []string, projectPath, worktreePath string) string {
	if len(cmds) == 0 {
		return contract
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(contract, "\n"))
	b.WriteString("\n\n")
	b.WriteString(runScopeHeading)
	b.WriteString("\n- In this run Bash may run ONLY the Verify commands declared above, plus read-only git (git diff / log / show / status). Every other command is denied.\n")
	b.WriteString("- Run each declared command exactly as written, one per Bash call: no `cd … &&` prefix, no env-var prefix, no pipe or redirect.")
	if p, w := cleanDir(projectPath), cleanDir(worktreePath); p != "" && w != "" && p != w {
		b.WriteString(" A path under " + p + " may be replaced by the same path under " + w + " (your cwd).")
	}
	b.WriteString("\n- The declared commands:\n")
	for _, c := range cmds {
		b.WriteString("  - " + c + "\n")
	}
	b.WriteString("- If a check you need is denied or cannot run, the verdict is INCONCLUSIVE — name the denied command. A denial is never evidence that the work is wrong, so it is never FAIL.")
	return b.String()
}
