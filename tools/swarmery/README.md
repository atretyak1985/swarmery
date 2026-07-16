# Swarmery — control plane

Local control plane for Claude Code agent systems: a single Go daemon + embedded
React SPA. It indexes session transcripts from `~/.claude/projects/` into local
SQLite and serves a live dashboard at `http://localhost:7777`. Fully local — no
cloud, no account, no telemetry.

**Dashboard** (see the repo-root [README](../../README.md#control-plane) for screenshots):

- **Command deck** — triage view: what's working vs. waiting, availability/cost/quality headlines, today's activity feed, and an approvals rail.
- **Sessions** — every session across all projects, filterable by project and status; each opens to **Chat · Timeline · Diffs**.
- **Analytics** — cost/tokens/runs over time by project or model, with a per-project breakdown and an agent × project cross-tab.
- **Approvals** — pending `AskUserQuestion` and permission requests with inline approve/deny and expiry timers.
- **System** — the full Claude config graph (agents · skills · hooks · commands · overlays) across global and project scopes, with lint badges and version history.
- **Docs** — the framework docs (onboarding · extending · neutrality) rendered in-app.

Design reference: [swarmery-design.md](swarmery-design.md) ·
[UI mockup](docs/design/swarmery-ui-mockup.html).

## Build & run

```bash
make build          # snapshot docs → vite bundle → go:embed → single ./swarmery binary
./swarmery serve    # listens on :7777 (override with SWARMERY_PORT)
# or: make install  # deploy + launchd auto-start (see repo-root README)
```

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
swarmery prune --older-than 90d --dry-run   # count what would be pruned per table (recommended first)
swarmery prune --older-than 90d             # roll up + delete + VACUUM
```

`--older-than <Nd>` is required; prefer stopping the daemon first (prune
deletes rows and VACUUMs the same WAL).

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
