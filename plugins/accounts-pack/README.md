# accounts-pack

Bind a project to one of several Claude Code accounts and run every session in
it under that account — from the dashboard, from `/account`, and from your own
terminal. Opt-in: most people have one account and need none of this.

## What an "account" is here

One Claude Code config dir. The CLI keeps everything about a login — including
the credential — under `CLAUDE_CONFIG_DIR`, so pointing a process at another
config dir *is* switching the account. The default account is the env-LESS one:
it lives in `~/.claude`, which is where the CLI looks when `CLAUDE_CONFIG_DIR`
is unset, so a project bound to it produces an empty environment delta and
behaves exactly as it did before this pack existed.

swarmery never writes credential material. It creates directories and points
processes at them; the `claude` CLI performs each account's own login.

## From zero to a bound project

The whole flow, in order. Steps 1–2 happen in the swarmery dashboard; step 3 is
this pack; step 4 is nothing at all.

**1. Register the second account** — dashboard → Settings → *accounts* →
*+ add account*. Pick a key (say `work`); the modal reserves the config dir and
shows a login command. It deliberately does **not** run it — copy it into your
own terminal:

```bash
CLAUDE_CONFIG_DIR="$HOME/.claude-work" claude
# inside the session: /login → authorize the SECOND account → /exit
```

Use a private browser window if your browser is already logged into the first
account — otherwise you will re-login the same account under a new name. When
the login completes, the account's row shows a green *connected* dot.

**2. Bind it to a project** — project → Settings → *account* card → pick
`work` → save. The binding is written to the project's
`.claude/settings.local.json` (machine-local, gitignored — your choice never
reaches the repo or your teammates). From here on, every process the daemon
spawns for this project — dispatched tasks, verification, planning, the
terminal dock — runs under `work`. The account is resolved at spawn time: a
run already in flight keeps its old account until it finishes.

**3. Cover your own terminal** — enable this pack for the project and either
run `/account setup-shell` once (plain `claude` then follows the binding) or
use the explicit `claude-account.sh` wrapper. The SessionStart hook warns you
whenever a session starts under a different account than the bound one, and
the statusline shows an account chip whenever you are not on the default.

**4. Projects without a binding need nothing.** No binding → no
`CLAUDE_CONFIG_DIR` → byte-for-byte the pre-multi-account behaviour. The card
on the project settings page does not even render until the machine has a
second account.

## Enable per project

```jsonc
"enabledPlugins": { "accounts-pack@swarmery": true }
```

Enabling the pack **does not touch your shell profile**. The only thing that
edits it is running `bin/install-shell-function.sh` yourself.

## Requires the swarmery CLI

Every decision lives in `swarmery account`, so the shell surfaces, the hooks and
the daemon cannot disagree about which account a project uses:

```
swarmery account list                              key, config dir, default?, connected?, plan
swarmery account which [--path <dir>]              the effective account, and whether it came
                                                   from a binding or from the default
swarmery account use <key> [--path <dir>]          bind a project
swarmery account clear [--path <dir>]              unbind it
swarmery account env [--path <dir>]                zero or one line: CLAUDE_CONFIG_DIR=<dir>
swarmery account exec [--path <dir>] -- <cmd ...>  run a command under the project's account
```

`env` prints to your terminal, so it prints the config dir and nothing else.
`exec` hands the environment to the child without printing it, so it is the one
that also carries the account's secrets (see below). Both terminal surfaces in
this pack use `exec` for exactly that reason.

`which`, `use`, `clear`, `env` and `exec` never contact the daemon. Your
terminal has to keep working with swarmery stopped.

The pack degrades honestly rather than silently: without the CLI on `PATH`,
`claude-account.sh` prints one warning and runs the default account, and the
shell function falls through to plain `claude`.

## The two terminal surfaces

Pick one. They do the same thing and differ only in how much they take over.

### 1. The explicit wrapper — `bin/claude-account.sh`

```bash
claude-account.sh --resume
```

One line of shell, nothing installed, nothing shadowed: it execs
`swarmery account exec --path "${CLAUDE_PROJECT_DIR:-$PWD}" -- claude "$@"`.
The exit code is the child's. Symlink it onto your `PATH` under whatever name
you like.

### 2. The shell function — `bin/install-shell-function.sh`

```bash
bin/install-shell-function.sh              # install into ~/.zshrc or ~/.bashrc
bin/install-shell-function.sh --status
bin/install-shell-function.sh --uninstall
bin/install-shell-function.sh --profile ~/.config/shell/rc   # explicit target
```

Installs a `claude` function so that typing plain `claude` follows the binding
of the directory you are standing in. What it guarantees:

- the profile is edited **only** when you run this script — never on enable;
- the original is copied to `<profile>.bak` before the first write;
- it is idempotent — running it twice produces no second block and no diff;
- `--uninstall` removes exactly its own marker block and nothing else;
- unbalanced markers (a hand-edited profile) **abort** the run instead of
  guessing;
- it delegates to `swarmery account exec`, so the session gets the project's
  whole environment delta — the config dir *and* the account's MCP credentials,
  if it has any — and `exec`'s exit code, signals and terminal are the child's;
- the fallback calls `command claude`, never `claude`, so it cannot recurse;
- the fallback is silent — no CLI on `PATH` falls through to plain `claude`
  with no output. This runs on every invocation, so a warning here would be
  noise forever. A swarmery that is present and fails is not retried: its exit
  status is the command's.

Already-open shells keep the old definition until they are restarted
(`source ~/.zshrc`, or `unset -f claude` after uninstalling).

## `/account`

`commands/account.md` — list the accounts, show the effective one, switch it,
install or remove the shell function. Its body is `swarmery account …` calls; it
holds no logic of its own.

## Hooks

`hooks/hooks.json` wires `SessionStart` → `hooks/warn-wrong-account.sh`, which
warns when a session is running under an account other than the project's
binding. The actual account comes from `$CLAUDE_CONFIG_DIR` (falling back to
`$HOME/.claude`) — the same env Claude Code itself uses to pick a login, read
straight from the hook's process environment, never from `transcript_path`
(the SessionStart payload doesn't carry one). The binding comes from the
hook's own `cwd` (falling back to `$CLAUDE_PROJECT_DIR`) resolving
`<project>/.claude/settings.local.json` → `.swarmery.claudeAccount`.

It stays silent (no stdout, exit 0) when: there is no binding; the binding
matches the actual account; the settings file is missing, unparseable, or
`jq` is unavailable; or the stored binding fails the same validity check
`swarmery` itself uses for a key (a hand-edited file can't smuggle arbitrary
text into the model's context this way). On an actual mismatch it prints
exactly one line of SessionStart hook JSON —
`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"…"}}`
— so Claude Code sees the warning from the first message of the session.
Like every hook here it is fail-open — a hook that can fail is a hook that
can block a session.

## Per-account secrets

Some plugins ship an `.mcp.json` whose servers reference credentials as
`${SOME_TOKEN}`. Those expand from the **process environment** of the session,
and on a multi-account machine they belong to one account, not to the box: an
operator's work tokens have no business in a personal project's sessions.

swarmery keeps them in a per-account store and injects them wherever it starts
a `claude` for a bound project — every daemon engine (dispatch, verify, plan and
phase runs, planning, routines, provisioning, and the background runners that
work out of the daemon's own home: improve, retro analysis, trajectory judging,
handoff generation, extraction), the dashboard's resume and terminal dock, and
`swarmery account exec`:

```
<SWARMERY_SECRETS_DIR or ~/.swarmery/secrets>/<account>.env    dir 0700, file 0600
```

`KEY=value` per line; blank lines and `#` comments are skipped, a leading
`export ` is tolerated, and one matched pair of surrounding quotes is stripped.
Properties worth knowing:

- **The mode is enforced.** A store file readable by group or other is refused
  outright — swarmery logs the path and the mode and loads *nothing*.
- **It is per account.** A project bound to no account, or to the default one,
  gets no variables at all, even when a store exists on the machine.
- **It never reaches stdout.** `swarmery account env` prints the config-dir line
  and nothing else; the store travels through `account exec` and through the
  daemon's spawns.
- **It cannot re-point the account.** A `CLAUDE_CONFIG_DIR=` line in the store is
  ignored with a log line: the binding owns that variable, and the store is keyed
  by the account the binding named.
- **swarmery never writes it.** Put the file there yourself, `chmod 600` it, and
  keep it out of every repo.

A project bound **explicitly** to `default` runs under `~/.claude` even when the
daemon's plist carries a `CLAUDE_CONFIG_DIR` (`swarmery install
--claude-config-dir`): the spawn removes the inherited variable. A project with
**no** binding inherits it — that is what the install flag is for.

## Known edges

- **The binding is read from the project root only.** It lives in
  `<project>/.claude/settings.local.json` and is not searched for in parent
  directories, so both terminal surfaces follow the binding when your shell sits
  at the project root, and give you the default account from a subdirectory.
  `claude-account.sh` does not avoid this: it prefers `CLAUDE_PROJECT_DIR`, and
  Claude Code exports that variable to **hook processes**, not to your login
  shell — from your own terminal the wrapper always falls back to `$PWD`. Run
  either surface from the project root, or pass the root explicitly with
  `swarmery account exec --path <root> -- claude`.
- **A running session keeps its account.** A binding decides what the *next*
  session starts under; nothing re-homes a live one.
- **An empty delta inherits.** A project bound to the default account adds no
  variable, so a `CLAUDE_CONFIG_DIR` you exported by hand in that shell still
  applies — the same rule every other swarmery spawner follows.
- **The binding file is machine-local** (`settings.local.json`, gitignored):
  two people on one repo can legitimately use different accounts.
- **A binding that git tracks is ignored.** If a repository commits
  `.claude/settings.local.json`, `swarmery` (every spawn, `account which|env|exec`,
  the `claude` shell function and the dashboard) ignores it. The project runs under
  the default account with no secret-store variables. The same happens when the
  file's origin can't be established: git is missing from `PATH`, the check times
  out after 2 s, or git reports an error. Every symlink on the way to the file is
  checked too. swarmery logs one warning per path with the cause, and the
  dashboard's account card shows it. To fix it, run `git rm --cached` on the file
  and gitignore it.
  - **The SessionStart hook doesn't know about this rule.** It reads the file
    directly, so for a tracked binding it can warn that the session runs under the
    "wrong" account, even though swarmery deliberately ignored that binding.
