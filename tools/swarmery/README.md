# Swarmery — control plane

Local control plane for Claude Code agent systems: a single Go daemon + embedded
React SPA. It indexes session transcripts from `~/.claude/projects/` into local
SQLite and serves a live dashboard at `http://localhost:7777`. Fully local — no
cloud, no account, no telemetry.

**Dashboard** — ten places in one sidebar, each for all projects or one
(`/p/<slug>/…`). Walkthrough: [docs/guides/guide-dashboard.md](docs/guides/guide-dashboard.md);
screenshots: [docs/TOUR.md](../../docs/TOUR.md).

- **Today** — the home: this week's loop (Plan → Run → Measure → Learn → Change), what waits on you, what runs now, and the day in detail (wait time, per-project lanes, notable sessions).
- **Inbox** — every decision waiting on the operator: permission requests and `AskUserQuestion`, lessons, advisor recommendations, agent-change proposals, classifier checks. Rules and history at `/approvals/manage`.
- **Sessions** — every session across all projects, day-grouped and live; each opens to **Chat · Timeline · Diffs**. Guide: [docs/guides/guide-sessions.md](docs/guides/guide-sessions.md).
- **Plans** — New plan (Planning Mode) · Plans · Board · Playbooks. A board card you run is dispatched to a headless agent in its own worktree, with a one-phase micro-plan in the project's workspace. Guide: [docs/guides/guide-plans.md](docs/guides/guide-plans.md).
- **Health** — Overview · Agents · Friction · Estimates · Advisor · Cost & tokens over one date range: per-agent scorecards, the friction board, a deterministic advisor with a tracked recommendation lifecycle (`proposed → accepted → adopted → verified`), and cost analytics. Reference: [docs/retro.md](docs/retro.md).
- **Learning** — Lessons · The classifier · Forecast honesty · Proof. Classifier setup: [docs/guides/guide-decisions.md](docs/guides/guide-decisions.md).
- **Knowledge** — a project's Memory, Architecture map, and the Serena / Graphify dashboards.
- **Docs** — the guides and reference docs, rendered in-app.
- **System** — the full Claude config graph across global, project and plugin-cache scopes: Agents · Skills · Plugins · Hooks · Routines · Insights, with lint badges and version history.
- **Settings** — Appearance · Accounts · Notifications · Projects. Only onboarded projects are listed by default; untick *onboarded only* for the rest. A project's own settings (packs, plugin config, account) are at `/p/<slug>/settings`. Guide: [docs/guides/guide-getting-started.md](docs/guides/guide-getting-started.md).
- **Usage** — the header's `◔` chip and its modal: the operator's live Claude subscription quota (5-hour session, weekly, per-model weekly) read from their own local `claude` login, with a pace marker against elapsed window time. Reference: [docs/usage.md](docs/usage.md).

Design reference: [swarmery-design.md](swarmery-design.md) ·
[UI mockup](docs/design/swarmery-ui-mockup.html).

## Build & run

```bash
make build          # snapshot docs → vite bundle → go:embed → single ./swarmery binary
./swarmery serve    # listens on :7777 (override with SWARMERY_PORT)
# or: make install  # copy into ~/.swarmery/bin + restart the installed service (see below)
(cd web && npm test) # the web app's unit/component tests (vitest + jsdom); swarmery-ci runs them too
```

Under launchd the daemon starts with `PATH=/usr/bin:/bin:/usr/sbin:/sbin`. `serve`
widens its own PATH once at boot with the tool dirs that exist on the machine
(`~/.local/bin`, `~/.npm-global/bin`, `~/bin`, bun/cargo/volta/fnm, the newest nvm
node, `~/.claude/local`, Homebrew, `/usr/local/bin`) so the `claude` it spawns can
start `npx`/`uvx`-based MCP servers. `SWARMERY_SPAWN_PATH=/dir1:/dir2` (bake it
into the plist like any other knob) prepends extra dirs verbatim.

## Install as a service, and upgrading it

`swarmery install` copies **the binary it is run from** to `~/.swarmery/bin/swarmery`,
writes the service definition — a launchd plist on macOS
(`~/Library/LaunchAgents/com.swarmery.daemon.plist`), a `systemd --user` unit on
Linux (`~/.config/systemd/user/swarmery.service`) — with its start command pointing
at that copy, and starts or restarts the service. The hook entries that
`swarmery hooks install` and `swarmery onboard` write into a project's
`.claude/settings.local.json` invoke the same `~/.swarmery/bin/swarmery hook …`
path, never whichever binary happened to run the install. Variables already baked
into the definition survive a re-run (`swarmery install -h` lists the flags that
set them).

That one path is what makes an upgrade a two-step on a machine that runs the
service. Installing a release — `scripts/install.sh`, the one-liner in the repo-root
README — only replaces `~/.local/bin/swarmery` and starts nothing; the service and
the hooks keep running the previous copy under `~/.swarmery/bin` until you re-run
the installer **from the new binary**:

```bash
curl -fsSL https://raw.githubusercontent.com/atretyak1985/swarmery/main/scripts/install.sh | bash
swarmery install        # re-point the service (and with it the hooks) at the new binary
swarmery version        # the build you just installed …
swarmery status         # … and, first line, the build the daemon now runs (GET /api/health "build")
```

`scripts/install.sh` prints that reminder itself when it finds a service definition
on the machine.

From a source checkout, `make install` does the copy-then-rename into
`~/.swarmery/bin` and the restart itself (`launchctl kickstart -k` on macOS,
`systemctl --user restart` on Linux) and prints the build the service came back up
on. It leaves the service definition alone — change baked variables with
`swarmery install --<flag> …`, not with `make install`. With no service loaded yet
it drops the binary in place and names the setup command.

## Security model

The daemon has no authentication: it binds loopback (`--bind 127.0.0.1`) and
trusts whoever can reach it. A browser lends that reach to every page it
renders, so two fences stand in front of the API (`internal/api/origin.go`).
Both derive "this daemon" from the address it actually listens on, and both
are extended only by `SWARMERY_TRUSTED_ORIGINS` (comma-separated
`scheme://host[:port]`; `swarmery install --trusted-origins …` bakes it into
the service definition):

- **Origin.** Every write, the `/api/ws` live stream and the terminal upgrade
  refuse a browser `Origin` other than the daemon's own — scheme, host **and
  port**. A page on `http://localhost:5173` (another dev server, an SSH
  port-forward) is as foreign as one on the internet; opt it in if it must
  drive the daemon (`make dev` does so for the vite dev server). The terminal
  also refuses a request with no `Origin` at all. Elsewhere a request without
  an `Origin` (the hook shim, `curl`, `swarmery console`, the notch companion)
  passes: a non-browser client cannot be driven by a web page.
- **Host.** Every route — reads included — refuses a `Host` header that does
  not name the daemon: a loopback name or address on the bound port, the
  listener's own address (`--bind 192.168.1.5` is reached as
  `192.168.1.5:7777`), or the host of a trusted origin. This closes DNS
  rebinding, where a page re-points its own name at `127.0.0.1` and reads the
  API as same-origin. Reaching the daemon by any other name or port — a
  hosts-file alias, a reverse proxy that forwards the original `Host`, a
  tunnel on another port — needs that origin opted in, for every client.
  That includes a routine's webhook sender (`POST /api/hooks/routine/…` is
  token-gated and exempt from the Origin fence, not from this one).

Both fences answer `403 {"error": …}` before any handler runs.

### Agent spawn environment

| Env | Values | Default | What it does |
|---|---|---|---|
| `SWARMERY_AGENT_SCRUB_VCS_TOKENS` | `1` \| unset | unset (off) | When `1`, agents spawned through `internal/runcore` (board dispatch, verify, planning, plan runs and phase runs) lose the **environment-carried** code-host tokens `GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, `GLAB_TOKEN`, `GITLAB_TOKEN` and the config-dir overrides `GH_CONFIG_DIR`, `GLAB_CONFIG_DIR`, whether they came from the daemon's own env or an account/estate secret store. Any other value leaves the environment untouched. See the notes below the table. |

Notes on `SWARMERY_AGENT_SCRUB_VCS_TOKENS`:

- **It is not a full credential boundary.** Dropping `GH_CONFIG_DIR`/`GLAB_CONFIG_DIR` sends `gh`/`glab` back to their default config (`~/.config/gh`, `~/.config/glab-cli`) and the system keyring, where `gh auth login` keeps the operator's token, and agents inherit `HOME`. An operator logged in that way is still reachable from an agent.
- **Skills lose access only if the token came from the environment.** Skills that call `gh`/`glab` themselves (`commit-push-pr`, `jira-delivery`) stop working only when their access came from an environment-carried token. A `git push` over SSH is unaffected.
- **Not every spawn goes through `runcore`, so not every spawn is scrubbed.** These paths compose their own env with `claudeacct.SpawnEnvResolved` and are unaffected by the flag:
  - `internal/routines/runner.go` (routines)
  - `internal/api/resume.go` (session resume)
  - `internal/systemspawn/systemspawn.go` (the one-shot system spawns: decide, triage judge, lessons, extract, handoff, improve, retro analysis, trajectory judge, reply extractor, account probe)
  - `internal/provision/runner.go` (provisioning)

## Backup & restore

The daemon's operational database (`~/.swarmery/swarmery.db` by default — sessions,
approvals, cost-tracking, the config-registry graph) is the one piece of local
state with no external source of truth. Snapshot it with:

```bash
swarmery backup                       # → ~/.swarmery/backups/swarmery-<timestamp>.db
swarmery backup --out /path/to/snap.db
```

`backup` uses SQLite `VACUUM INTO`, so it is **safe to run while the daemon is
serving** (brief read lock, no downtime) and yields a single self-contained file
with no `-wal`/`-shm` sidecars. Schedule it from cron/launchd for a rolling
history.

**A pre-migration snapshot must not come from `swarmery backup` of a newer
binary.** `backup` opens the source through `store.Open`, which applies any
pending migration as a side effect — so it would snapshot the already-migrated
shape. Before installing a binary that adds a migration, take the snapshot with
`sqlite3 ~/.swarmery/swarmery.db "VACUUM INTO '<path>'"` (or with the OLD binary's
`swarmery backup`).

## Switching accounts

Two terminal commands move work between Claude Code accounts. Neither contacts
the daemon; both read the database through a no-migrate handle.

```bash
swarmery account switch <key> [--estate <root>] [--force] [--clear-pins] [--dry-run]
swarmery account move-session <uuid> --to <key> [--from <key>] [--cwd <path>] \
    [--project-dir <name>] [--force] [--overwrite] [--dry-run]
```

- `switch` re-binds a whole **declared estate** (a root whose
  `.claude/settings.local.json` carries `swarmery.estate`) to `<key>`. It refuses
  a path under no estate (use `swarmery account use <key> --path <dir>` for one
  directory), and refuses an account whose quota headroom is unknown — no fresh
  `account_quota` row — unless `--force`. Descendant pins are listed as
  *redundant* (same account as the estate) or *disagreeing* (a deliberate
  override, never cleared); `--clear-pins` sweeps redundant pins only on a run
  that keeps the payer (`switch <current-key> --clear-pins`). The estate's
  credential store is reported as a count, never a name or a value.
- `move-session` finds `<uuid>.jsonl` under the source account's
  `projects/*/`, copies it, its `<uuid>/` directory and the project `memory/`
  into the same-named directory of `<key>`'s config dir (never moving),
  re-points the session's `sessions.account`, and prints the resume command.
  Identical destination files (size + SHA-256) are left alone; a destination
  file that DIFFERS refuses the whole move, listing every such path, unless
  `--overwrite`, which first renames each to `<name>.pre-move-<UTC timestamp>`.
  Files land via temp file + fsync + rename; symlinks are never copied (listed
  as refused/skipped), and a symlinked destination directory refuses the move.
  Re-running a finished move is a one-line no-op. The directory name is located
  on disk, never computed from the session's cwd. A live session is refused
  unless `--force`.

The headroom `switch` gates on is recorded by the daemon's **quota poller**:
every `SWARMERY_QUOTA_INTERVAL` (default `10m`; `0` or `off` disables it, with
one log line and no requests) it reads each account's usage windows into
`account_quota`. A failed read writes nothing, so absence stays "unknown".
Usage-limit hits are recorded in `account_limit_hits` from transcript error
records as they are ingested.

## Retention (prune)

Old sessions' raw rows (turns/events/file_changes) can be rolled up into
`daily_rollups` and deleted — session headers stay browsable (`pruned=1`) and
analytics keeps counting the pruned days from the rollups:

```bash
swarmery prune --older-than 90d --dry-run   # count ONE pass per table (recommended first)
swarmery prune --older-than 90d             # roll up + delete + VACUUM, one bounded pass
```

`--older-than <Nd>` is required; prefer stopping the daemon first (prune
deletes rows and VACUUMs the same WAL).

**One invocation is one bounded pass.** Both the real run and `--dry-run` are
capped at **300 sessions** (`internal/prune.MaxSessionsPerPass`): the store runs
a *single* SQLite connection, so an unbounded pass over a real backlog is a
multi-minute all-or-nothing transaction that every HTTP handler and every ingest
write queues behind — and killing it rolls the whole thing back, so a restart
loop makes no forward progress at all. When more sessions are waiting than one
pass can take, the CLI says so and you run it again:

```
  capped at 300 sessions this pass — more remain; run again to continue the backlog
  note: each pass ends with its own VACUUM (a full rewrite of the database file), so a multi-pass drain pays that rewrite once per run
```

Two consequences worth planning around:

- **`--dry-run` reports the bounded slice, not your backlog.** `sessions marked:
  300` alongside the capped line means "at least 300", never "exactly 300".
  There is no flag that counts the whole backlog; ask SQLite directly instead.
  This is a plain `SELECT` — it changes no data, and it is safe to run while the
  daemon is serving (WAL readers do not block the writer):

  ```bash
  cutoff=$(date -u -v-90d +%Y-%m-%dT%H:%M:%S.000Z)   # GNU: date -u -d '90 days ago' +…
  sqlite3 ~/.swarmery/swarmery.db \
    "SELECT COUNT(*) FROM sessions
      WHERE pruned = 0 AND ended_at IS NOT NULL AND ended_at < '$cutoff';"
  ```

  Divide by 300 to get the number of passes still owed. Use the same window here
  and in `--older-than`, or the two numbers describe different backlogs.

  Two things *not* to reach for:

  - `swarmery backup`, to get a scratch copy to count on. It opens the source
    through `store.Open`, which applies pending **migrations** to your live
    database as a side effect. It is the right tool for snapshots (see
    [Backup & restore](#backup--restore)), not for a read-only question.
  - A `?mode=ro` URI, to make the read explicitly read-only. The database is in
    WAL mode, and SQLite cannot create the `-shm` file a WAL connection needs
    when it is opened read-only — so on a cleanly-closed store (no `-wal`/`-shm`
    sidecars present) it fails with `unable to open database file (14)` rather
    than answering.
- **Each pass VACUUMs.** VACUUM rewrites the *entire* database file, so draining
  a 5,000-session backlog is 17 invocations and 17 full-file rewrites — not one
  delete plus one VACUUM. On a large store, schedule the drain accordingly, or
  let the daemon's daily tick do it: the tick never VACUUMs (see below), so it
  costs no rewrites, it just does not reclaim the space either.

### Scheduled prune (the daemon)

The CLI above is the manual, one-off path. While `swarmery serve` is running it
also prunes **once a day**, on the same maintenance tick as revision retention,
so telemetry no longer grows without bound if nobody remembers to run the CLI:

```bash
SWARMERY_RETENTION_DAYS=60   # default; 0 disables scheduled pruning entirely
```

Values `1..13` are raised to `14` with a warning: `/api/projects/health` reads
live tables only (never `daily_rollups`) for its week-over-week comparison, so a
shorter window would make `costPrevWeekUsd` silently go null.

Three differences from the CLI, all deliberate:

- **The first pass is delayed 5 minutes after startup** (`prune.FirstTickDelay`),
  then it repeats every 24h. Startup is exactly when ingest is replaying
  transcripts over the store's single SQLite connection, and a prune fired
  inline there contends with that replay — on a large store the operator gets an
  unresponsive dashboard and no log line for minutes. So **"I restarted the
  daemon and nothing pruned" is correct behaviour for the first five minutes**,
  and a restart cycle shorter than five minutes never prunes at all: each restart
  resets the timer. If you are waiting on retention, either wait out the delay
  (the `retention prune:` line below is the confirmation) or run the CLI, which
  has no such delay. Startup-time messages — `retention prune: disabled` and the
  `SWARMERY_RETENTION_DAYS` clamp warning — are *not* delayed: the window is
  resolved immediately, only the work is deferred.
- The daemon **never VACUUMs**. VACUUM rewrites the whole database file and
  blocks every writer; the daemon is a live writer. Space is reclaimed on the
  next manual `swarmery prune`, not by the daily tick.
- It logs one line per pass even when nothing expired, because at one line a day
  that line is the only evidence retention is alive:

```
retention prune: sessions=191 turns=34228 events=43446 file_changes=4559 rollups=180 (cutoff 2026-07-20T09:35:34.849Z)
retention prune: capped at 300 sessions/pass — backlog remains, continuing next pass
retention prune: disabled
```

The tick is bounded by the same 300-session cap as the CLI, so on a large
backlog it drains ~300 sessions **per day** and logs the `capped` line until it
catches up. That line means candidates genuinely remain beyond this pass; its
absence means the daemon is caught up. To drain a big backlog faster, run the
CLI repeatedly (accepting one VACUUM per pass) rather than waiting on the tick.

**Restore** is stop-copy-start (SQLite has no live in-place restore):

```bash
swarmery uninstall          # or: launchctl stop … / kill the `swarmery serve` process
cp ~/.swarmery/backups/swarmery-<timestamp>.db ~/.swarmery/swarmery.db
rm -f ~/.swarmery/swarmery.db-wal ~/.swarmery/swarmery.db-shm   # drop stale WAL sidecars
swarmery install            # or restart `swarmery serve`
```

## Rollback

Releases are cut by pushing a `swarmery-v*` tag (see
[`.github/workflows/swarmery-release.yml`](../../.github/workflows/swarmery-release.yml)),
which publishes versioned binaries + `SHA256SUMS` on a GitHub Release. To roll a
local build back to a known-good version:

```bash
git checkout <last-good-tag>   # e.g. swarmery-v0.1.0  (git tag -l 'swarmery-v*')
make build
```

Back up the database **before** upgrading (above) whenever the new version adds a
schema migration, because there is no way back through the schema itself.
Migrations are forward-only and an older binary does **not** refuse a newer
database: `store.Open` only checks that the migrations it embeds are recorded in
`schema_migrations`, ignores the newer rows, and starts on whatever schema is
there. A migration that merely added tables or columns goes unnoticed; one that
renamed, dropped or constrained something the older code touches surfaces later as
runtime SQL errors (`no such column`, `NOT NULL constraint failed`) on the affected
pages and ingest paths, not as a refusal at startup. To roll back cleanly, restore
the pre-upgrade snapshot (stop-copy-start, above) rather than running the older
binary on the migrated database.

## Excluding throwaway projects

Spike/e2e runs under `/tmp` would otherwise pollute the dashboards. The
`--exclude-projects` flag (env `SWARMERY_EXCLUDE`, default
`/tmp/*,/private/tmp/*`) takes comma-separated path globs; a cwd is excluded
when a glob matches it or any ancestor directory. Both tracking channels
honor it:

- the **JSONL scanner** skips matching project dirs on backfill, rescan, and
  fsnotify tail — deleted data cannot rescan itself back in;
- the **hooks channel** still serves permission requests from excluded cwds
  (the fail-open decision flow is untouched: the daemon answers 204 and the
  shim falls back to the native dialog), but persists no session/project rows.

Exclusion gates row *creation* only — rows that already exist are never
deleted by code; remove them with a one-off SQL cleanup. Set
`SWARMERY_EXCLUDE=''` to disable.

## Landing a phase

A finished plan phase (or a reviewed board card) lands from the dashboard: a
push sends its run branch to `origin`, and the change-request action also opens
a pull request on GitHub or a merge request on GitLab (GitLab.com and
self-hosted). Every label the UI shows — "Open PR" / "Open MR", "PR #12" /
"MR #12" — comes from the provider the daemon detected (the API's `terms`),
never from a guess in the browser.

The operator guide, [docs/landing.md](docs/landing.md), covers the lifecycle
(none → ready → pushed → pr_open → merged, or returned), the three Review
buttons, every 409/422 code with its remedy, and what is deferred. Signing the
daemon in is covered in [docs/vcs-login.md](docs/vcs-login.md). To keep agents
from seeing environment-carried VCS tokens, see `SWARMERY_AGENT_SCRUB_VCS_TOKENS`
under [Agent spawn environment](#agent-spawn-environment).

**Requirements.** `git` on the daemon's PATH, plus the host's CLI: `gh` ≥ 2.40
for GitHub, `glab` ≥ 1.40 for GitLab. The daemon uses the CLI's own login
(`gh auth login --hostname <host>` / `glab auth login --hostname <host>`); the
project banner names the exact command when it is missing or expired.

**Signing in from the dashboard.** Instead of the CLI login, the banner's
*Sign in* gives the daemon a token of its own — an OAuth device code entered
in any browser, or a pasted personal access token — stored in
`~/.swarmery/secrets/vcs-<host>.env` (0600). The device code needs an OAuth
application registered once per host and its client id; see
[docs/vcs-login.md](docs/vcs-login.md) for the registration steps, where the
token lives and how to revoke it.

| Variable | Default | Effect |
|---|---|---|
| `SWARMERY_GITHUB_CLIENT_ID` | unset | Client id of a GitHub OAuth App with Device Flow enabled; enables the device-code sign-in for GitHub hosts. Overridden per host by `swarmery.vcs.clientIds.<host>` in `.claude/settings.local.json`. |
| `SWARMERY_GITLAB_CLIENT_ID` | unset | Application id of a non-confidential GitLab application with scope `api`; enables the device-code sign-in for GitLab hosts. Same per-host override. |

**How the provider is detected** — first match wins, read from `origin`:

1. `vcs.provider` in the project's config (below) — `github` or `gitlab`;
2. the host itself: `github.com` → GitHub, `gitlab.com` → GitLab;
3. an SSH remote's alias resolved through `ssh -G <alias>` (your
   `~/.ssh/config`), and the providers' port-443 endpoints
   (`ssh.github.com`, `altssh.gitlab.com`) mapped to their canonical hosts;
4. a probe of an unknown HTTPS host's `GET /api/v4/version` — an answer means
   self-hosted GitLab (the probe never guesses GitHub: GitHub Enterprise hosts
   are configured explicitly with `vcs.provider: "github"`);
5. otherwise the banner asks once — *"Which service hosts `<host>`?"* — and
   stores the answer as `swarmery.vcs.provider` in
   `.claude/settings.local.json` (`PUT /api/projects/{id}/vcs/provider`).

**Configuration.** Shared defaults live in the `vcs` block of
`.claude/project.json`; `.claude/settings.local.json` overrides any key per
machine under `swarmery.vcs`:

```jsonc
// .claude/project.json (committed)
{ "vcs": { "provider": "gitlab", "baseBranch": "main" } }

// .claude/settings.local.json (local, wins per key)
{ "swarmery": { "vcs": {
  "provider": "gitlab",       // github | gitlab
  "baseBranch": "develop",    // change-request target; empty = the host's default branch
  "allowPushToBase": false    // let a run branch that IS the base branch be pushed
} } }
```

| Key | Default | Effect |
|---|---|---|
| `swarmery.vcs.provider` / `vcs.provider` | auto-detect | Pins the provider (`github` \| `gitlab`). |
| `swarmery.vcs.baseBranch` / `vcs.baseBranch` | host's default branch | Target branch of the PR/MR. |
| `swarmery.vcs.allowPushToBase` / `vcs.allowPushToBase` | `false` | Without it, landing a run branch that is the base branch is refused (409). |
| `swarmery.vcs.clientIds.<host>` | unset | OAuth client id for the device-code sign-in on that host; wins over `SWARMERY_GITHUB_CLIENT_ID` / `SWARMERY_GITLAB_CLIENT_ID`. |

`vcs.forkRemote` is reserved: the fork workflow is not supported yet, and a
project that sets it is refused (409) rather than pushed somewhere surprising.

**What the daemon never does.** A push is never forced and never retried (a
diverged remote branch is a 422 that tells you how to rebase), and merge policy
stays with you on the PR/MR page: `--force`, `--squash` and
`--remove-source-branch` are never passed. Every failure answers 422 with the
exact command that finishes the job by hand; tool output is passed through
redaction, so a token a CLI echoes (`ghp_…`, `glpat-…`) never reaches the
browser.

**Status and merge.** Once a phase's PR/MR is open, the daemon reads its
status every 10 minutes (first pass 30 s after boot; up to 20 change requests
per tick, the longest-unread first) with `gh pr view` / `glab mr view`, and the
Review tab shows it as chips — CI, review, state — with a **Refresh** button
(`POST /api/epics/{taskId}/phases/{phaseId}/landing/refresh`) for an
immediate read. When the change request merges, the phase moves to `merged` and
its landed date is the merge time. A change request closed without merging keeps
the phase at `pr_open`, with a `closed` chip: what that means for the phase is your call. The
daemon never deletes a branch and never ticks a criterion on merge. A
rejected credential marks the project's sign-in expired, so the banner asks for
a fresh login.

| Env | Values | Default | What it does |
|---|---|---|---|
| `SWARMERY_LANDPOLL_INTERVAL` | Go duration (`5m`, `1h`) \| `0` | `10m` | How often the daemon reads open PR/MR status. `0` disables the poller (the Refresh button still works); an invalid value falls back to `10m` with a boot warning. The boot log says `landpoll: every 10m0s` or `landpoll: disabled`. |

## Notifications (webhook)

`--notify-url` (env `SWARMERY_NOTIFY_URL`) turns on an outbound webhook;
`--notify-template` picks the body shape (`generic`, `ntfy`, `telegram`).
`--notify-events` (env `SWARMERY_NOTIFY_EVENTS`) chooses what is sent — any
comma-separated mix of `approval_requested`, `approval_expired`,
`session_completed`, `session_error`, `plugin_drift`, `phase_surprise`,
`run_needs_operator`. The default is `approval_requested,run_needs_operator`:
the two moments a human is blocking work (a pending tool approval, a headless
run that stopped to ask the operator). An explicit value **replaces** the
default rather than extending it, so keep both names in the list when adding
others. `approval_expired` also fires for approvals a daemon restart orphaned
(the hook died with the old process, so the request can no longer be answered
from the dashboard) — the body says so, and the answer belongs in the terminal.

## Complexity routing

Before a board card or a plan phase spawns, the daemon scores how complex it is
(prompt size, dependencies, risky paths, the planner's forecast, project
history), maps the score to a tier `S`/`M`/`L`/`XL`, and picks a **model**,
**effort** and — for cards — **playbook** from a per-tier policy
(`internal/route`). Every pick is written to `route_decisions` with its signals,
score, reasons, what actually ran and which rung won; outcomes, verify verdicts
and cost are joined in lazily. Learning → **Forecast honesty** → **routing** (`/learning?tab=honesty`) shows the
per-tier and per-model outcome and cost; a group stays hidden until it has 20
runs.

| Env | Values | Default | What it does |
|---|---|---|---|
| `SWARMERY_ROUTE_DISPATCH` | `off` \| `shadow` \| `active` | `shadow` | Board-card dispatch. `shadow` records the pick and changes nothing; `active` applies it where nobody chose explicitly; `off` skips the router entirely. An invalid value logs a warning and means `shadow`. |
| `SWARMERY_ROUTE_PHASERUN` | `off` \| `shadow` \| `active` | `shadow` | Plan phase runs, same semantics. |
| `SWARMERY_ROUTE_POLICY` | path to a JSON file | unset (in-code `DefaultPolicy`) | Overrides weights, tier cut-offs and per-tier picks. A set path that cannot be read or parsed is an error: the router records nothing and spawns keep their `off` values. |

In `active` the route pick takes the slot just above the env/default rung and
never beats an explicit choice:

- dispatch model: card → playbook `model:` → **route** → default;
  effort: **route** → `SWARMERY_DISPATCH_EFFORT` → default;
  playbook: card → **route** (else the old length/deps heuristic; `review-heavy`
  is never picked automatically);
- phase model / effort: request → doc `**Model:**` / `**Effort:**` → **route**
  → env → default.

`applied=1` on a row means *any* route rung won (model, effort or playbook);
`won_rung` names the rung that won the **model** ladder only. Because route
effort is the top dispatch effort rung, every `active` dispatch row is
`applied=1`. `haiku` resolves to its full model ID inside `internal/route`; it
is deliberately not offered in the planner's model picker.

A card's cost sums every playbook stage's session inside the run's window
(explicitly linked sessions only). Known limit: the composer's resume takes its
effort from `SWARMERY_RESUME_EFFORT`, not from the routed effort.

### Shadow → active runbook

1. Leave both surfaces on `shadow` (the default) for at least two weeks.
2. Open Learning → Forecast honesty → routing (`/learning?tab=honesty`). Flip a surface only when every
   visible tier has n ≥ 20 and tier `S`'s failure rate is not worse than the
   model it replaces (the "picked vs ran" table is the evidence).
3. Set `SWARMERY_ROUTE_DISPATCH=active` (and/or `SWARMERY_ROUTE_PHASERUN=active`)
   in the daemon env, then `make install`.
4. Roll back by setting the surface to `off` — identical to pre-feature
   behaviour, pinned argv-for-argv by `TestRouteOffGolden` — or back to
   `shadow` to keep recording.

## Nightly inbox triage

`config/routines/inbox-triage.json` ships a routine, `inbox-triage-nightly`,
that labels the classifier queue every night at 03:30 without the operator. Its
one `command` step runs
`swarmery triage run --kinds classifier --cap 60 --trigger schedule --wait`:
a triage run on the running daemon limited to **the classifier kind**, and to
**at most 60 sessions a night**. It writes classifier labels and records its
verdicts; it does not accept lessons, dismiss recommendations or decide anything
else in the inbox. A machine
asleep at 03:30 runs it once on wake (`catchUp: run_one`).

The daemon never seeds it by itself. Seed it once, from `tools/swarmery/`
(idempotent by name — seeding again updates the same routine):

```bash
swarmery routine seed --from config/routines/inbox-triage.json
```

Two ways to stop it:

- **Disable the routine** on the Routines page (`enabled=false`): the scheduler
  never fires it again; every other routine keeps running.
- **`SWARMERY_ROUTINES=0`** in the daemon env stops the scheduler for every
  routine. A manual **Run now** on the Routines page still works with the
  switch off — on purpose: the switch stops what fires on its own, not what the
  operator asks for.

The two subcommands behind it:

| Command | What it does | Exit codes |
|---|---|---|
| `swarmery triage run [--kinds <a,b>] [--project <slug>] [--cap <n>] [--trigger operator\|schedule] [--wait] [--wait-timeout <dur>] [--port <n>] [--url <base>]` | Starts a run on the running daemon over HTTP (never opens the database). `--wait` polls it every 5 s until it ends and prints `triage run <id>: ok · applied N · suggested N · skipped N · failed N · $X.XX`; `--wait-timeout` (default 50m) stops waiting, not the run. | `0` the run ended ok, **or another run was already active** (the work is being done) · `1` the run ended failed, it no longer exists (404 on the poll), the wait timed out, or the command was cancelled (SIGINT/SIGTERM) · `2` usage, or the daemon is unreachable |
| `swarmery triage check [--run <id>] [--strict-leftovers] [--db <path>]` | Read-only audit (opens the database without migrating, `query_only`): the run's status is `ok` and its counters match its verdict rows — both always strict. The classifier sessions still queued in the run's **own scope** (its project, or the whole fleet) are split into four buckets: **covered by this run** (a `sample`/`skipped`/`failed`/`rejected`/`suggested`/`undone`/`undoing` verdict from it on a queued decision — `applied` does not count, an applied label takes the question out of the queue; `undone` does, the operator took the label back), **held by an earlier run** (the engine's own blocking rule, applied to each queued decision over the runs with a lower id: a `sample`/`suggested`/`undone`/`undoing` verdict, a `skipped` one from the 7 days before this run started, or three `failed`/`rejected` ones in those 7 days — so this run skipped the session and wrote no row), **arrived after the run started**, and **unexplained** (listed by session). Unexplained sessions fail the check only with `--strict-leftovers`; without it they are informational, since a run that stopped at its cap leaves them too. Queue decisions the classifier cannot reach — no session, or a session not ingested — are counted for information and never fail the check. A run whose kinds exclude `classifier` skips the queue checks. Default run: the newest fleet-wide run the operator started. | `0` everything matches · `1` a mismatch, or no such run · `2` usage or database error |

## Weekly memory review

`config/routines/memory-review.json` ships a second routine, `memory-review-weekly`,
that turns "read your memory every Friday" into a dated record in `routine_runs`.
Every Friday at 09:00 it runs two `command` steps over **every non-archived
project**, both read-only:

1. `swarmery memory lint --all` — the stale-fact report: auto-memory lines that
   still call a PR open when the project's git history already carries its merge.
   A report, not a gate (exit 0 with findings); `continueOnFailure: true` so a
   crash in the lint does not hide the next step.
2. `swarmery memory consolidate --all` — a consolidation **dry-run** per project:
   which closed entries would leave the always-loaded `MEMORY.md`, and which are
   held back. `--all` refuses `--yes` by design — the routine can only plan; moving
   files stays a per-project operator call (`--project <path> --yes`).

A project with no auto-memory prints `(no auto-memory at <path>)` in both steps.
A machine asleep on Friday morning runs it once on wake (`catchUp: run_one`); the
run's step output keeps a bounded tail, enough for a weekly glance on the
Routines page.

The daemon never seeds it by itself. Seed it once, from `tools/swarmery/`
(idempotent by name — seeding again updates the same routine):

```bash
swarmery routine seed --from config/routines/memory-review.json
```

Disabling and the `SWARMERY_ROUTINES=0` switch work exactly as for
`inbox-triage-nightly` above.
