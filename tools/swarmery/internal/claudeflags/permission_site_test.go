package claudeflags

import (
	"regexp"
	"strings"
	"testing"
)

// The per-site defaults (phase 9 of the least-privilege work). The measurement
// behind them is the package doc's table: only the executor sites need
// bypassPermissions; a read-only grader and the planner get dontAsk plus an
// allow-list; a session swarmery did not spawn gets no flag at all.
func TestModeFor_SiteDefaults(t *testing.T) {
	t.Setenv(ModeEnv, "")
	t.Setenv(siteEnv, "")
	for site, want := range map[Site]string{
		SiteExecutor: "bypassPermissions",
		SiteReadOnly: "dontAsk",
		SitePlanner:  "dontAsk",
		SiteForeign:  "",
	} {
		if got := ModeFor(site, siteEnv); got != want {
			t.Errorf("ModeFor(%s) = %q, want %q", site, got, want)
		}
	}
}

// The knobs keep their precedence over every site default: the site's own knob,
// then the cross-site knob. Restoring the old behaviour stays one env var away.
func TestModeFor_KnobsBeatTheSiteDefault(t *testing.T) {
	t.Setenv(siteEnv, "")
	t.Setenv(ModeEnv, "bypassPermissions")
	if got := ModeFor(SiteReadOnly, siteEnv); got != "bypassPermissions" {
		t.Errorf("global knob: ModeFor(read-only) = %q, want bypassPermissions", got)
	}
	t.Setenv(siteEnv, "acceptEdits")
	if got := ModeFor(SitePlanner, siteEnv); got != "acceptEdits" {
		t.Errorf("site knob: ModeFor(planner) = %q, want acceptEdits", got)
	}
	t.Setenv(siteEnv, "off")
	if got := ModeFor(SiteExecutor, siteEnv); got != "" {
		t.Errorf("off: ModeFor(executor) = %q, want omitted", got)
	}
}

// A typo falls back to the SITE's default, not to bypassPermissions: a mistyped
// knob on a read-only site must not widen it.
func TestModeFor_InvalidFallsBackToTheSiteDefault(t *testing.T) {
	t.Setenv(ModeEnv, "")
	t.Setenv(siteEnv, "dontAks")
	if got := ModeFor(SiteReadOnly, siteEnv); got != "dontAsk" {
		t.Errorf("ModeFor(read-only, typo) = %q, want the site default dontAsk", got)
	}
	if got := ModeFor(SiteForeign, siteEnv); got != "" {
		t.Errorf("ModeFor(foreign, typo) = %q, want the site default (omitted)", got)
	}
}

// Mode is the executor resolution, unchanged: every existing caller keeps its
// bypassPermissions default.
func TestMode_IsTheExecutorSite(t *testing.T) {
	t.Setenv(ModeEnv, "")
	t.Setenv(siteEnv, "")
	if got := Mode(siteEnv); got != ModeFor(SiteExecutor, siteEnv) || got != DefaultMode {
		t.Errorf("Mode() = %q, want the executor default %q", got, DefaultMode)
	}
}

func TestAllowedToolsArgs(t *testing.T) {
	if got := AllowedToolsArgs(nil); got != nil {
		t.Errorf("AllowedToolsArgs(nil) = %q, want nil (no flag)", got)
	}
	got := AllowedToolsArgs([]string{"Read", " Grep ", "", "Read", "Bash(git log*)"})
	if want := []string{"--allowedTools", "Read,Grep,Bash(git log*)"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("AllowedToolsArgs = %q, want %q", got, want)
	}
}

// The `//` spelling is the measured one: `Write(//abs/**)` granted nothing on
// CLI 2.1.308 while `Edit(//abs/**)` let the Write tool through.
func TestEditUnder(t *testing.T) {
	if got := EditUnder("/Volumes/ws/"); got != "Edit(//Volumes/ws/**)" {
		t.Errorf("EditUnder = %q", got)
	}
	if got := EditUnder("relative/dir"); got != "" {
		t.Errorf("EditUnder(relative) = %q, want \"\" (a relative rule would be read against cwd)", got)
	}
	if got := EditUnder("/"); got != "" {
		t.Errorf("EditUnder(/) = %q, want \"\" (a rule for the whole disk is not a confinement)", got)
	}
}

func TestDaemonDenyRules(t *testing.T) {
	want := []string{
		"Bash(curl *127.0.0.1:7777*)", "Bash(curl *localhost:7777*)", "Bash(curl *[::1]:7777*)",
		"Bash(wget *127.0.0.1:7777*)", "Bash(wget *localhost:7777*)", "Bash(wget *[::1]:7777*)",
		"Bash(http *127.0.0.1:7777*)", "Bash(http *localhost:7777*)", "Bash(http *[::1]:7777*)",
		"Bash(*/curl *127.0.0.1:7777*)", "Bash(*/curl *localhost:7777*)", "Bash(*/curl *[::1]:7777*)",
		"Bash(*/wget *127.0.0.1:7777*)", "Bash(*/wget *localhost:7777*)", "Bash(*/wget *[::1]:7777*)",
		"Bash(*/http *127.0.0.1:7777*)", "Bash(*/http *localhost:7777*)", "Bash(*/http *[::1]:7777*)",
		"Bash(swarmery api*)", "Bash(*/swarmery api*)", "Bash(command swarmery*)",
		"Read(~/.swarmery/link-token)", "Read(~/.swarmery/credentials/**)", "Read(~/.swarmery/secrets/**)",
	}
	if got := DaemonDenyRules(0); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("DaemonDenyRules(0) =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := DaemonDenyRules(7794); got[1] != "Bash(curl *localhost:7794*)" || got[11] != "Bash(*/curl *[::1]:7794*)" {
		t.Errorf("DaemonDenyRules(7794) = %q", got)
	}
}

// The daemon rules catch a client by installed path, on the IPv6 loopback and
// through `command`; none of them touches the executor's own worktree, which
// lives under ~/.swarmery/worktrees.
func TestDaemonDenyRules_Forms(t *testing.T) {
	rules := DaemonDenyRules(7794)
	hit := func(cmd string) string {
		for _, r := range rules {
			if matchesRule(r, cmd) {
				return r
			}
		}
		return ""
	}
	for _, cmd := range []string{
		"curl -s http://127.0.0.1:7794/api/x", "/usr/bin/curl -s http://127.0.0.1:7794/api/x",
		"/opt/homebrew/bin/wget -qO- localhost:7794/api", "curl -g http://[::1]:7794/api/x",
		"http POST [::1]:7794/api/x", "command swarmery api POST /api/x", "/Users/u/.swarmery/bin/swarmery api GET /x",
	} {
		if hit(cmd) == "" {
			t.Errorf("daemon deny list misses %q", cmd)
		}
	}
	for _, cmd := range []string{"curl -s https://example.com", "/usr/bin/curl http://127.0.0.1:8080/", "swarmery version"} {
		if r := hit(cmd); r != "" {
			t.Errorf("daemon deny list blocks %q (rule %s)", cmd, r)
		}
	}
	for _, r := range rules {
		if strings.HasPrefix(r, "Read(") && (strings.Contains(r, "worktrees") || r == "Read(~/.swarmery/**)") {
			t.Errorf("rule %s would deny the executor its own worktree (~/.swarmery/worktrees)", r)
		}
	}
}

// Read-only PR/MR subcommands stay allowed (a routine may watch PRs), and the
// routines' list leaves the daemon API reachable (operator-authored prompts may
// drive it). Review minors.
func TestDenyLists_ReadsStayAllowed(t *testing.T) {
	t.Setenv(AgentDenyEnv, "")
	exec := AgentDenyArgs(7794)[1]
	for _, read := range []string{"Bash(gh pr *)", "Bash(gh pr view", "Bash(gh pr list", "Bash(glab mr *)", "Bash(glab mr view"} {
		if strings.Contains(exec, read) {
			t.Errorf("executor deny list %q blocks a read: %q", exec, read)
		}
	}
	routine := RoutineDenyArgs()
	if len(routine) != 2 || !strings.Contains(routine[1], "Bash(git push*)") || strings.Contains(routine[1], "7794") || strings.Contains(routine[1], "curl") {
		t.Errorf("RoutineDenyArgs = %q, want remote-write rules only, no daemon-API rules", routine)
	}
}

// matchesRule reports whether a Bash command is matched by one `Bash(<pattern>)`
// rule, with the CLI's documented wildcard semantics: `*` matches any text,
// spaces included, and the pattern covers the whole command.
func matchesRule(rule, cmd string) bool {
	if !strings.HasPrefix(rule, "Bash(") || !strings.HasSuffix(rule, ")") {
		return false
	}
	parts := strings.Split(rule[len("Bash("):len(rule)-1], "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(cmd)
}

// Every remote-write form is matched by some deny rule (review I3), and the
// reads are matched by none — for the executors and the routines alike.
func TestDenyLists_WriteFormsMatchedReadsNot(t *testing.T) {
	t.Setenv(AgentDenyEnv, "")
	lists := map[string][]string{
		"executor": strings.Split(AgentDenyArgs(7794)[1], ","),
		"routine":  strings.Split(RoutineDenyArgs()[1], ","),
	}
	writes := []string{
		"git push", "git push origin swarm/x",
		"git -C /wt push origin swarm/x", "git -c push.default=current push", "git --no-pager push -u origin x",
		"git --git-dir=/r/.git push",
		"gh pr create --fill", "gh pr merge 12 --squash", "gh pr close 12", "gh pr reopen 12",
		"gh pr edit 12 --title x", "gh pr ready 12", "gh pr review 12 --approve", "gh pr comment 12 -b x",
		"gh pr update-branch 12", "gh pr lock 12", "gh pr unlock 12",
		"glab mr create", "glab mr merge 3", "glab mr close 3", "glab mr reopen 3", "glab mr update 3 --draft",
		"glab mr approve 3", "glab mr revoke 3", "glab mr note 3 -m x", "glab mr rebase 3",
		"glab mr subscribe 3", "glab mr unsubscribe 3", "glab mr delete 3", "glab mr del 3",
		"glab api projects/1/merge_requests --input body.json",
		"gh api repos/o/r/pulls -X POST -f title=x", "gh api -X PATCH repos/o/r/pulls/1", "gh api repos/o/r/x -X PUT",
		"gh api repos/o/r/git/refs/heads/x -X DELETE", "gh api repos/o/r/pulls -XPOST", "gh api --method POST repos/o/r/pulls",
		"gh api repos/o/r/issues -f title=x", "gh api repos/o/r/issues -F n=1", "gh api repos/o/r/issues --field title=x",
		"gh api repos/o/r/issues --raw-field body=x", "gh api repos/o/r/issues --input body.json",
		"glab api projects/1/merge_requests -X POST", "glab api --method PUT projects/1", "glab api projects/1/notes -f body=x",
	}
	reads := []string{
		"gh pr view 12", "gh pr list", "gh pr diff 12", "gh pr checks 12", "gh pr status",
		"gh api repos/o/r/pulls", "gh api repos/o/r/pulls --jq .[].number", "gh api repos/o/r/pulls --paginate",
		"glab mr view 3", "glab mr list", "glab mr diff 3", "glab api projects/1/merge_requests",
		"git status", "git log --oneline",
		// A commit or a search that merely mentions the word is not a push.
		`git commit -m "feat: push notifications"`, "git log --grep push", "git -C /wt status",
	}
	for name, rules := range lists {
		hit := func(cmd string) string {
			for _, r := range rules {
				if matchesRule(r, cmd) {
					return r
				}
			}
			return ""
		}
		for _, w := range writes {
			if hit(w) == "" {
				t.Errorf("%s deny list misses the write %q", name, w)
			}
		}
		for _, r := range reads {
			if rule := hit(r); rule != "" {
				t.Errorf("%s deny list blocks the read %q (rule %s)", name, r, rule)
			}
		}
	}
}

// A resume names the ORIGINAL site's knob first, then its own; the global after.
func TestModeForChain_OrderAndForeignClamp(t *testing.T) {
	const origin, resume = "SWARMERY_TESTORIGIN_PERMISSION_MODE", "SWARMERY_TESTRESUME_PERMISSION_MODE"
	t.Setenv(ModeEnv, "")
	t.Setenv(origin, "acceptEdits")
	t.Setenv(resume, "plan")
	if got := ModeForChain(SitePlanner, origin, resume); got != "acceptEdits" {
		t.Errorf("chain = %q, want the origin knob's acceptEdits", got)
	}
	t.Setenv(origin, "")
	if got := ModeForChain(SitePlanner, origin, resume); got != "plan" {
		t.Errorf("chain = %q, want the resume knob's plan", got)
	}
	// No knob may widen a foreign session: bypass, auto and acceptEdits all
	// collapse to "no flag"; dontAsk and plan pass.
	for _, wide := range []string{"bypassPermissions", "auto", "acceptEdits"} {
		t.Setenv(resume, "")
		t.Setenv(ModeEnv, wide)
		if got := ModeForChain(SiteForeign, resume); got != "" {
			t.Errorf("foreign with %s=%s = %q, want no flag", ModeEnv, wide, got)
		}
	}
	t.Setenv(ModeEnv, "")
	t.Setenv(resume, "dontAsk")
	if got := ModeForChain(SiteForeign, resume); got != "dontAsk" {
		t.Errorf("foreign with dontAsk = %q, want dontAsk", got)
	}
}

// The executor deny set: no push, no PR/MR, no daemon API. It rides on every
// bypassPermissions site, because deny rules are the only rules bypass honours.
func TestAgentDenyArgs(t *testing.T) {
	t.Setenv(AgentDenyEnv, "")
	args := AgentDenyArgs(7794)
	if len(args) != 2 || args[0] != "--disallowedTools" {
		t.Fatalf("AgentDenyArgs = %q, want one --disallowedTools flag", args)
	}
	for _, want := range []string{"Bash(git push*)", "Bash(gh pr create*)", "Bash(gh pr merge*)", "Bash(glab mr merge*)", "Bash(curl *127.0.0.1:7794*)"} {
		if !strings.Contains(args[1], want) {
			t.Errorf("deny list %q lacks %q", args[1], want)
		}
	}
	// `swarmery api` signs with the link token and reaches the daemon's write
	// routes without curl; it must be denied by name and by installed path.
	rules := strings.Split(args[1], ",")
	for _, cmd := range []string{"swarmery api POST /api/routines", "/usr/local/bin/swarmery api PATCH /api/projects/1"} {
		hit := false
		for _, r := range rules {
			if matchesRule(r, cmd) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("executor deny list misses %q", cmd)
		}
	}
	for _, off := range []string{"0", "off", "false"} {
		t.Setenv(AgentDenyEnv, off)
		if got := AgentDenyArgs(7794); got != nil {
			t.Errorf("%s=%s: AgentDenyArgs = %q, want nil", AgentDenyEnv, off, got)
		}
	}
}
