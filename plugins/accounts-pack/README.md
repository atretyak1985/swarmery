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
swarmery account which [--path <dir>]              the effective account, the rung that decided it
                                                   (own pin, an ancestor's pin, or the default),
                                                   the estate and its root, and any shadowed pin
swarmery account use <key> [--path <dir>]          bind a directory (lists shadowing pins first;
      [--clear-pins | --clear-pins=all | --keep-pins]  see "Estates" below)
swarmery account clear [--path <dir>]              unbind it (an estate declared there stays)
swarmery account env [--path <dir>]                zero or one line: CLAUDE_CONFIG_DIR=<dir>
swarmery account exec [--path <dir>] -- <cmd ...>  run a command under the project's account and estate
swarmery account estate use <key> [--path <dir>]   declare <dir> as the root of estate <key>
swarmery account estate show [--path <dir>]        the estate a path resolves to, and its root
swarmery account estate clear [--path <dir>]       remove the declaration at <dir>
swarmery account switch <key> [--estate <root>]    move a whole declared estate to <key>; refuses a path
      [--force] [--clear-pins] [--dry-run]           under no estate and an account with unknown headroom
swarmery account move-session <uuid> --to <key>    copy a session into <key>'s config dir, re-point its
      [--from <key>] [--cwd <path>] [--dry-run]      row, print the resume command
```

`env` prints to your terminal, so it prints the config dir and nothing else.
`exec` hands the environment to the child without printing it, so it is the one
that also carries the MCP secrets (see below). Both terminal surfaces in this
pack use `exec` for exactly that reason.

`which`, `use`, `clear`, `env`, `exec`, `estate`, `switch` and `move-session`
never contact the daemon (`switch` and `move-session` read its database without
migrating it).
Your terminal has to keep working with swarmery stopped.

The pack degrades honestly rather than silently: without the CLI on `PATH`,
`claude-account.sh` prints one warning and runs the default account, and the
shell function falls through to plain `claude`.

## The terminal surfaces

Pick one (the shim and the function also coexist). They do the same thing and
differ only in how much they take over.

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

### 3. The PATH shim — `bin/install-shell-function.sh --shim`

```bash
bin/install-shell-function.sh --shim                  # write ~/.swarmery/bin/claude
bin/install-shell-function.sh --shim --real-bin /abs/path/to/claude
bin/install-shell-function.sh --shim-uninstall
```

zsh caches command locations: after `--shim`, run `rehash` (or open a new shell) so an already-open zsh finds the shim.

A function only exists in shells that sourced the profile after it was
installed. The shim is a file — `~/.swarmery/bin/claude` (or
`$SWARMERY_BIN_DIR/claude`), mode `0755` — so every shell that has
`~/.swarmery/bin` first on `PATH` routes `claude` through
`swarmery account exec`, function or no function. What it guarantees:

- it records the REAL binary's absolute path at install time, resolved with the
  shim dir stripped from `PATH` and then the same probe list the daemon uses;
  it **refuses** to install when that answer is the shim itself;
- it hands `account exec` that absolute path, never the bare word `claude` —
  that path is the only loop guard, so a nested `claude` after a `cd` re-resolves
  against the new project. No environment variable is read as a guard;
- fail-open: no `swarmery` on `PATH` execs the real binary directly with the
  same argv and exit code; a recorded binary that moved is re-probed on `PATH`
  minus the shim dir;
- it adds `export PATH="$HOME/.swarmery/bin:$PATH"` inside the marker block
  only when the profile does not already export that dir, and is idempotent.

It is still `PATH`-bound: a launchd job, a cron line or an IDE task with its own
`PATH` misses it — the SessionStart preflight below is the answer there.
swarmery's own resolvers skip the shim dir, and `swarmery install --claude-bin`
bakes the real binary into the daemon's plist.

## `/account`

`commands/account.md` — list the accounts, show the effective one, switch it,
install or remove the shell function. Its body is `swarmery account …` calls; it
holds no logic of its own.

## Hooks

`hooks/hooks.json` wires `SessionStart` → `hooks/preflight-account.sh`, a
credential **coverage** preflight. It runs
`swarmery account doctor --fast --json --path <cwd>` (the hook's own `cwd`,
falling back to `$CLAUDE_PROJECT_DIR`) under a 2-second watchdog and reads the
report's camelCase fields: `varsExpected` — the `${VAR}` names the project's
enabled plugins reference in their MCP configs; `varsPresent` — which of those
are set in this session; `varsMissing` — the rest.

It measures *which variables*, not *which account*: the failure it exists for is
a session under the right account with none of its credentials, which an
account-equality test can never see.

**The only escalation is a non-empty `varsMissing`.** Zero credentials, an
estate with no store file, no estate at all, or an empty `varsExpected` are all
healthy and silent — a project may enable a dozen plugins that reference no
`${VAR}`. On a gap it prints exactly one line of SessionStart hook JSON —
`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"…"}}`
— naming the missing variables (names only, never a value) and the cause:

- the report's `daemon` is true (a swarmery daemon worktree) → the gap is in the
  daemon's spawn seam;
- `launchedViaSwarmery` is false (the session did not come through
  `swarmery account exec`, which sets `SWARMERY_LAUNCH_PATH`) → exit and open a
  new shell: MCP env is read once at process start and cannot be repaired
  in-session;
- otherwise → nothing supplies those names; add them to the estate's store.

It stays silent (no stdout, exit 0) when `SWARMERY_SKIP_PREFLIGHT=1`, `jq` or
`swarmery` is missing, the doctor fails, hangs or prints something that is not a
report, or an account/estate key fails a character gate deliberately stricter
than `swarmery`'s own (a hand-edited file can't smuggle text into the model's
context). Every variable name must match `^[A-Za-z_][A-Za-z0-9_]*$` to reach it.
Like every hook here it is fail-open.

It also writes `~/.swarmery/run/preflight/<session_id>.env` (dir `0700`, file
`0600`) — `account=`, `estate=`, `varsExpected=`, `varsPresent=` (list
lengths) and `launch=` — which the core statusline reads to append `⚠p/n` to
the account chip. No names or values go there.

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
- **An account-keyed store is per account.** A project bound to no account, or
  to the default one, gets nothing from `<account>.env`, even when a store
  exists on the machine. (An estate store — below — is keyed by the project
  tree instead.)
- **It never reaches stdout.** `swarmery account env` prints the config-dir line
  and nothing else; the store travels through `account exec` and through the
  daemon's spawns.
- **It cannot re-point the account.** A `CLAUDE_CONFIG_DIR=` line in the store is
  ignored with a log line: the binding owns that variable, and the store is keyed
  by the account the binding named.
- **swarmery never writes it.** Put the file there yourself, `chmod 600` it, and
  keep it out of every repo.
- **It can name the trees it serves.** A line `# swarmery-root: <absolute path>`
  (a comment to every older loader) anchors the store: it is then released only
  to a project inside one of the trees its root lines name. See "Trust: two
  locks" below.

A project bound **explicitly** to `default` runs under `~/.claude` even when the
daemon's plist carries a `CLAUDE_CONFIG_DIR` (`swarmery install
--claude-config-dir`): the spawn removes the inherited variable. A project with
**no** binding inherits it — that is what the install flag is for.

### Estates: credentials keyed by the project tree, not by the payer

Which subscription pays for tokens has nothing to do with the credentials a
tree's MCP servers need. So the binding file carries a second, optional field:

```jsonc
// <estate-root>/.claude/settings.local.json
{ "swarmery": { "claudeAccount": "<payer>", "estate": "<store>" } }
```

Write it with `swarmery account estate use <store> --path <estate-root>` — never
by hand. It does three independent things:

1. it makes `<estate-root>` the **estate root**: every directory below it
   resolves to it;
2. it selects `<SWARMERY_SECRETS_DIR or ~/.swarmery/secrets>/<store>.env` as the
   credential store, **when that file exists and one of its `# swarmery-root:`
   lines names a tree containing `<estate-root>`**;
3. it selects `<estate-root>/.claude/settings.json` as the estate's settings
   file, when that file exists — again only for an estate its store admits.

**An estate whose store is missing, or carries no root line, is _unanchored_**:
it supplies zero credentials and no estate settings, and that is never an error.
`swarmery account estate use` says so in one line and prints the exact root
line to add; `swarmery account which` prints `estate <key> unanchored`.

Resolution walks up from the project: the project's own binding, then each
ancestor, stopping before your home directory. The **account** is taken from
the first rung that pins one; the **estate** from the first rung that declares
one — independently. A sub-repo pinned to a different payer therefore still
gets its estate's credentials. `swarmery account which` prints the rung, the
estate and root, and any ancestor pin the winning rung shadows.

Precedence: the account-keyed `<account>.env` is read first (back-compat), the
estate's `<store>.env` second, and the **estate wins** any name both define —
the composed environment holds exactly one entry per name.

Nested estates **subtract, they do not accumulate**: an `estate` declared deeper
replaces the ancestor's store and settings file wholesale; nothing is merged.

A daemon worktree (`~/.swarmery/worktrees/…`) resolves through its source
checkout, read from the worktree's own `.git` file.

`swarmery account use <key> --path <estate-root>` lists every descendant pin
that would shadow the write, before writing. `--clear-pins` clears only the pins
that already equal `<key>` (redundant ones); a pin that disagrees is left in
place and named as a deliberate divergence. `--clear-pins=all` clears every
listed pin. `--keep-pins` clears nothing. With no flag a terminal is asked
about the redundant set; anything else only lists.

### Trust: two locks

Two things decide what a directory is actually given, and neither can be
widened by a file someone else wrote:

- **Lock 1 — provenance.** A binding file that git **tracks** (or whose status
  git cannot establish) declares nothing — for every field of the `swarmery`
  object and every key, `default` included — at every rung of the walk. It
  arrived with a clone, a pull or a teammate's commit, so it may not choose the
  payer or the credentials. The rung reads as "nothing declared here", the walk
  continues, and `swarmery account which` prints an `ignored:` line with the
  reason and the fix (`git rm --cached`). Every writer (`use`, `clear`, `estate
  use|clear`, `switch`) refuses such a file on set and on clear and leaves it
  byte-identical. A binding file that is itself a **symlink** is never read; a
  symlinked `.claude` **directory** is read, and every link on the way is checked
  the same way.
- **Lock 2 — the store anchor.** A store carrying `# swarmery-root:` lines is
  released only to a directory inside one of those trees (compared as resolved
  files, never as string prefixes). An **estate** store must be anchored, or it
  releases nothing (the unanchored state above). An **account** store without
  root lines keeps working as before and logs one `store-rootless` warning per
  process. `swarmery account use` still writes a payer whose store does not
  admit the path — the payer is gated by Lock 1 only — and prints the root line
  that would release the credentials.

`swarmery account which` prints one `admission:` line per store: `<key>.env
admitted by root <root>`, `not admitted by <key>.env roots`, `<key>.env
rootless`, or `estate <key> unanchored`.

Two consequences worth knowing:

- **An extracted archive or a copied tree outside every root** can still switch
  the *payer* with an untracked binding (Lock 1 cannot tell it from yours), but
  it receives no name from an anchored store and no estate settings.
- **The dashboard's account terminal** holds only an account key and no project
  path, so an anchored account store is not released there. A project-scoped
  terminal resolves (and is admitted) from its project.

## Known edges

- **The shim and the function coexist; the function wins in shells that have
  it.** A shell function shadows every `PATH` entry, so a shell that sourced the
  function runs it; it calls `swarmery account exec -- claude`, whose resolver
  skips the shim dir and finds the real binary — one resolution, not two. A
  shell without the function (opened before it was installed, or one that
  sources no rc file but reads the profile's `PATH`) gets the shim instead.
- **The preflight walks like the CLI.** It asks `swarmery account doctor`, which
  resolves the path with the same ancestor walk as every other `account`
  subcommand, so a subdirectory inherits its ancestor's binding and estate.
- **A running session keeps its account.** A binding decides what the *next*
  session starts under; nothing re-homes a live one.
- **An empty delta inherits.** A project bound to the default account adds no
  variable, so a `CLAUDE_CONFIG_DIR` you exported by hand in that shell still
  applies — the same rule every other swarmery spawner follows.
- **The binding file is machine-local** (`settings.local.json`, gitignored):
  two people on one repo can legitimately use different accounts.
- **A binding that git tracks is ignored** (Lock 1, above). If a repository
  commits `.claude/settings.local.json`, `swarmery` (every spawn, every rung of
  the walk, `account which|env|exec|doctor`, the `claude` shell function, the
  preflight hook and the dashboard) ignores it. The same happens when the file's
  origin can't be established: git is missing from `PATH`, the check times out
  after 2 s, or git reports an error. swarmery logs one warning per path with the
  cause and a shell-quoted remedy, and the dashboard's account card shows it. To
  fix it, run `git rm --cached` on the file and gitignore it.
