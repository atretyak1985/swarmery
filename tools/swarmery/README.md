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
