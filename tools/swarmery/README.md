# Swarmery — control plane

Local control plane for Claude Code agent systems: a single Go daemon + embedded
React SPA. It indexes session transcripts from `~/.claude/projects/` into local
SQLite and serves a live dashboard at `http://localhost:7777`. Fully local — no
cloud, no account, no telemetry.

**Dashboard** (see the repo-root [README](../../README.md#the-dashboard) for screenshots):

- **Command deck** — triage view: what's working vs. waiting, availability/cost/quality headlines, today's activity feed, and an approvals rail.
- **Sessions** — every session across all projects, filterable by project and status; each opens to **Chat · Timeline · Diffs**.
- **Analytics** — cost/tokens/runs over time by project or model, with a per-project breakdown and an agent × project cross-tab.
- **Approvals** — pending `AskUserQuestion` and permission requests with inline approve/deny and expiry timers.
- **System** — the full Claude config graph (agents · skills · hooks · commands · overlays) across global and project scopes, with lint badges and version history.
- **Retro** — agent-system retrospectives: per-agent health scorecards, a friction board, a lessons feed parsed from workspace retrospectives, and a heuristics-only advisor with a tracked recommendation lifecycle (`proposed → accepted → adopted → verified`). Full guide: [docs/retro.md](docs/retro.md).
- **Usage** — the header's `◔` chip and its modal: the operator's live Claude subscription quota (5-hour session, weekly, per-model weekly) read from their own local `claude` login, with a pace marker against elapsed window time. Full guide: [docs/usage.md](docs/usage.md).
- **Docs** — the framework docs (onboarding · extending · neutrality · retro) rendered in-app.

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
