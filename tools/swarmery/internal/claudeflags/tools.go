package claudeflags

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Tool-list flags. Both --allowedTools and --disallowedTools take ONE
// comma-joined value here: `claude --help` documents a comma- or space-separated
// list, a second flag of the same name would leave it to the CLI whether the
// lists merge or the last one wins, and a space inside a rule's parentheses
// (`Bash(git log*)`, `Bash(curl *127.0.0.1:7777*)`) survives the comma join —
// measured on 2.1.308 with exactly that shape.

// AllowedToolsArgs is the `--allowedTools <list>` pair for tools (blanks and
// duplicates dropped, order kept), or nil when the list is empty.
func AllowedToolsArgs(tools []string) []string {
	if joined := JoinRules(tools); joined != "" {
		return []string{"--allowedTools", joined}
	}
	return nil
}

// JoinRules comma-joins rules, dropping blanks and duplicates, order kept.
func JoinRules(rules ...[]string) string {
	seen := map[string]bool{}
	var out []string
	for _, list := range rules {
		for _, r := range list {
			r = strings.TrimSpace(r)
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	return strings.Join(out, ",")
}

// EditUnder is the allow rule that lets the Write/Edit tools touch files under
// dir and nowhere else: `Edit(//<abs dir>/**)`, the leading `//` marking an
// absolute path. Measured on 2.1.308: the Edit spelling also admits the Write
// tool, while `Write(//abs/**)` granted nothing. A relative dir, or the
// filesystem root, yields "" — the first would be read against cwd, the second
// is not a confinement.
func EditUnder(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	dir = filepath.Clean(dir)
	if dir == string(filepath.Separator) {
		return ""
	}
	return "Edit(/" + filepath.ToSlash(dir) + "/**)"
}

// DefaultDaemonPort is the daemon's own default HTTP port (cmd/swarmery's
// --port default).
const DefaultDaemonPort = 7777

// daemonPort is the port THIS daemon listens on, set once at startup
// (SetDaemonPort). The executor sites build their deny list from it without
// every runner struct having to carry the port.
var daemonPort atomic.Int64

// SetDaemonPort records the port this daemon's HTTP API listens on. cmd/swarmery
// calls it once, before any spawn; a value <= 0 resets to DefaultDaemonPort.
func SetDaemonPort(port int) { daemonPort.Store(int64(port)) }

// DaemonPort is the port SetDaemonPort recorded, or DefaultDaemonPort.
func DaemonPort() int {
	if p := int(daemonPort.Load()); p > 0 {
		return p
	}
	return DefaultDaemonPort
}

// daemonClients and daemonHosts span DaemonDenyRules: the HTTP clients a model
// reaches for, against the three spellings of the loopback address the daemon
// binds by default. In a Bash rule only `*` is special, so the brackets of
// `[::1]` are literal.
var (
	daemonClients = []string{"curl", "wget", "http"}
	daemonHosts   = []string{"127.0.0.1", "localhost", "[::1]"}
)

// daemonSecretReads deny the Read tool — and the file commands Claude Code
// recognizes in Bash (cat, head, tail, sed, redirections; permissions reference,
// "Read and Edit") — on the daemon's credentials: the link token that signs a
// request to its API, and the account credential and secret stores. Only
// those paths: an executor's own worktree lives under ~/.swarmery/worktrees
// and a System run's cwd is ~/.swarmery, so `Read(~/.swarmery/**)` would deny
// a run its own files. The `~/` form is the home-relative anchor; it is
// resolved against the spawned CLI's home, which is the daemon's (no spawn
// sets HOME), and a deny rule also applies to a symlink's target, so no
// `//<abs>` duplicate is needed.
//
// The daemon's database (and its -wal / -shm files) holds every stored
// transcript and the approvals history, and the CLI's plaintext login lives in
// <config dir>/.credentials.json: the default ~/.claude, an account's
// ~/.claude-<key> (internal/claudeacct), and ~/.config/claude. No executor
// flow reads any of them: a run's credentials reach the CLI process itself,
// never a tool call, and no shipped skill opens the database.
var daemonSecretReads = []string{
	"Read(~/.swarmery/link-token)",
	"Read(~/.swarmery/credentials/**)",
	"Read(~/.swarmery/secrets/**)",
	"Read(~/.swarmery/swarmery.db*)",
	"Read(~/.claude/.credentials.json)",
	"Read(~/.claude-*/.credentials.json)",
	"Read(~/.config/claude/.credentials.json)",
}

// DaemonDenyRules are the deny rules that keep a run off this daemon's own HTTP
// API on port: one Bash rule per client and host, `Bash(curl *127.0.0.1:7777*)`,
// the same by installed path, `Bash(*/curl *127.0.0.1:7777*)`, the `swarmery
// api` client, and reads of the daemon's credentials (daemonSecretReads). A
// port <= 0 is DaemonPort(). That API stamps verdicts, ticks phases, starts
// runs and answers approvals — nothing a spawned agent may do on its own.
//
// Syntax (Claude Code permissions reference — "Wildcard patterns", "Compound
// commands", "What a Bash rule doesn't match"; checked 2026-10-10):
//   - `*` may stand anywhere in a Bash rule and matches any text, spaces included,
//     so `curl *127.0.0.1:7777*` matches `curl -s http://127.0.0.1:7777/api/x`.
//   - the `:*` suffix is only recognised at the END of a rule. Mid-rule the colon is
//     literal, so `Bash(curl:*127.0.0.1:7777*)` would match no curl command at all.
//   - a deny rule applies when ANY subcommand of a compound command matches, and
//     past a leading env assignment; deny rules block in every mode, including
//     bypassPermissions (measured again 2026-10-22).
//   - it does not match the program by path (`/usr/bin/curl …`) or inside `sh -c`,
//     hence the `*/<client>` rules for the path form. Before matching, the CLI
//     strips wrappers such as `command`, `timeout` and `nice` ("Wrappers").
//   - it does not match inside `sh -c '…'`, through a variable, or a client
//     the list does not name: this guards the commands a model writes, it is
//     not a network boundary.
func DaemonDenyRules(port int) []string {
	if port <= 0 {
		port = DaemonPort()
	}
	out := make([]string, 0, 2*len(daemonClients)*len(daemonHosts)+7+len(daemonSecretReads))
	for _, prefix := range []string{"", "*/"} {
		for _, c := range daemonClients {
			for _, h := range daemonHosts {
				out = append(out, fmt.Sprintf("Bash(%s%s *%s:%d*)", prefix, c, h, port))
			}
		}
	}
	// `swarmery api` signs its request with the link token, so it reaches the
	// daemon's write routes without curl. Deny it by name, through an
	// installed-binary path, and behind `command` (stripped by current CLIs;
	// the explicit rule covers one that does not).
	out = append(out, "Bash(swarmery api*)", "Bash(*/swarmery api*)", "Bash(command swarmery*)")
	// `swarmery link token` prints the link token, and `--rotate` replaces it
	// under the running mod.
	out = append(out, "Bash(swarmery link*)", "Bash(*/swarmery link*)")
	// `swarmery open` reads the link token and prints a one-use dashboard
	// unlock ticket.
	out = append(out, "Bash(swarmery open*)", "Bash(*/swarmery open*)")
	return append(out, daemonSecretReads...)
}

// AgentDenyEnv opts the executor sites out of AgentDenyArgs: "0", "off", "false"
// or "no" drops the deny list (the pre-least-privilege argv). Anything else,
// unset included, keeps it.
const AgentDenyEnv = "SWARMERY_AGENT_DENY_RULES"

// remoteWriteDeny is what no daemon-spawned agent may do on its own, whatever its
// prompt says: send commits to a remote, or change a PR/MR. The daemon lands work
// itself (internal/repoprovider), from its own process, so none of these is ever
// an agent's job. `git push*` (no space) also catches a bare `git push`;
// `git -* push*` catches a push behind git's global options (`git -C <dir>
// push`, `git -c k=v push`, `git --no-pager push`), which the first rule does
// not match. It is anchored on a leading `-` rather than written `git * push*`
// because that form also matches every `git commit -m "… push …"` an executor
// writes; the residue is a flagged git command whose text holds " push"
// (`git -C <dir> log --grep push`). Only the
// MUTATING PR/MR subcommands are listed: `gh pr view/list/diff/checks` and
// `glab mr view/list/diff` are reads a routine or an executor may need.
//
// The API escape hatch is covered too: `gh api` / `glab api` with an explicit
// write method, or with fields (gh switches to POST as soon as -f/-F/--field/
// --raw-field/--input is given). A plain `gh api repos/o/r/pulls` GET stays a
// read. Documented limit: a GLOBAL flag before the subcommand (`gh -R o/r pr
// merge`, `glab -R o/r mr merge`) slips a subcommand list — the rules match
// the command text as written, they are a guard, not a sandbox.
var remoteWriteDeny = []string{
	"Bash(git push*)", "Bash(git -* push*)",
	"Bash(gh pr create*)", "Bash(gh pr merge*)", "Bash(gh pr close*)", "Bash(gh pr reopen*)",
	"Bash(gh pr edit*)", "Bash(gh pr ready*)", "Bash(gh pr review*)", "Bash(gh pr comment*)",
	"Bash(gh pr update-branch*)", "Bash(gh pr lock*)", "Bash(gh pr unlock*)",
	"Bash(glab mr create*)", "Bash(glab mr merge*)", "Bash(glab mr close*)", "Bash(glab mr reopen*)",
	"Bash(glab mr update*)", "Bash(glab mr approve*)", "Bash(glab mr revoke*)", "Bash(glab mr note*)",
	"Bash(glab mr rebase*)", "Bash(glab mr subscribe*)", "Bash(glab mr unsubscribe*)",
	"Bash(glab mr delete*)", "Bash(glab mr del *)",
	"Bash(gh api *-X POST*)", "Bash(gh api *-X PATCH*)", "Bash(gh api *-X PUT*)", "Bash(gh api *-X DELETE*)",
	"Bash(gh api *-XPOST*)", "Bash(gh api *-XPATCH*)", "Bash(gh api *-XPUT*)", "Bash(gh api *-XDELETE*)",
	"Bash(gh api *--method*)", "Bash(gh api *-f *)", "Bash(gh api *-F *)",
	"Bash(gh api *--field*)", "Bash(gh api *--raw-field*)", "Bash(gh api *--input*)",
	"Bash(glab api *-X*)", "Bash(glab api *--method*)", "Bash(glab api *-f *)", "Bash(glab api *-F *)",
	"Bash(glab api *--field*)", "Bash(glab api *--raw-field*)", "Bash(glab api *--input*)",
}

// AgentDenyRules is the EXECUTOR deny set (dispatch, phaserun, planrun,
// provision) for a daemon on port (<= 0 is DaemonPort()): remoteWriteDeny plus
// DaemonDenyRules. The daemon API is denied whole on purpose: no core skill an
// executor runs reads it — area-lessons, the one core skill that curls
// /api/lessons, is disable-model-invocation (operator-invoked only), and a
// headless run already receives its lessons in the prompt. jira-pack's
// swarmery-board-card, if a dispatched card ever runs it, degrades to its
// documented "daemon unavailable" fallback.
func AgentDenyRules(port int) []string {
	return append(append([]string(nil), remoteWriteDeny...), DaemonDenyRules(port)...)
}

// AgentDenyArgs is the `--disallowedTools <AgentDenyRules>` pair every
// bypassPermissions executor carries, or nil when AgentDenyEnv opts out. Deny
// rules are the one kind of rule bypassPermissions still honours (measured).
func AgentDenyArgs(port int) []string {
	return denyArgs(AgentDenyRules(port))
}

// RoutineDenyArgs is the routines' deny list: remoteWriteDeny only. An ai-prompt
// routine is operator authored and may legitimately read or drive the daemon's
// own API (the shipped ci-and-pr-watch template is installed through it), so the
// daemon rules are not applied there.
func RoutineDenyArgs() []string {
	return denyArgs(remoteWriteDeny)
}

func denyArgs(rules []string) []string {
	if OptedOut(os.Getenv(AgentDenyEnv)) {
		return nil
	}
	return []string{"--disallowedTools", JoinRules(rules)}
}

// OptedOut reports whether a default-on knob's value turns it off: "0", "off",
// "false" or "no", case and surrounding space ignored.
func OptedOut(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}
