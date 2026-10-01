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
# or: make install  # deploy + launchd auto-start (see repo-root README)
```

Under launchd the daemon starts with `PATH=/usr/bin:/bin:/usr/sbin:/sbin`. `serve`
widens its own PATH once at boot with the tool dirs that exist on the machine
(`~/.local/bin`, `~/.npm-global/bin`, `~/bin`, bun/cargo/volta/fnm, the newest nvm
node, `~/.claude/local`, Homebrew, `/usr/local/bin`) so the `claude` it spawns can
start `npx`/`uvx`-based MCP servers. `SWARMERY_SPAWN_PATH=/dir1:/dir2` (bake it
into the plist like any other knob) prepends extra dirs verbatim.

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

Back up the database first (above) if the version you are rolling away from ran a
newer schema migration — migrations are forward-only, so an older binary may
refuse a database it does not recognize.

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
others.

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
